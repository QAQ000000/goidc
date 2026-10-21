package accesslog

import (
	"context"
	"errors"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

type testWriter struct {
	mu         sync.Mutex
	batches    [][]Record
	write      func(context.Context, []Record) error
	prune      func(context.Context, time.Time, int) (int64, error)
	closeCount atomic.Int32
	closed     chan struct{}
}

func newTestWriter() *testWriter { return &testWriter{closed: make(chan struct{})} }

func (w *testWriter) WriteBatch(ctx context.Context, batch []Record) error {
	w.mu.Lock()
	w.batches = append(w.batches, append([]Record(nil), batch...))
	w.mu.Unlock()
	if w.write != nil {
		return w.write(ctx, batch)
	}
	return nil
}

func (w *testWriter) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if w.prune != nil {
		return w.prune(ctx, before, limit)
	}
	return 0, nil
}

func (w *testWriter) Close() {
	if w.closeCount.Add(1) == 1 {
		close(w.closed)
	}
}

func (w *testWriter) snapshotBatches() [][]Record {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make([][]Record, len(w.batches))
	for i, batch := range w.batches {
		out[i] = append([]Record(nil), batch...)
	}
	return out
}

func quietOptions(capacity, batch int) Options {
	return Options{Capacity: capacity, BatchSize: batch, FlushInterval: time.Hour, CleanupInterval: time.Hour, WriteTimeout: 2 * time.Second}
}

func newTestSink(t *testing.T, writer Writer, options Options) *Sink {
	t.Helper()
	s := New(writer, options)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			t.Errorf("sink cleanup failed: %v", err)
		}
	})
	return s
}

func waitTestSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatalf("timed out waiting for %s", name)
	}
}

func stopTestSink(t *testing.T, sink *Sink) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if err := sink.Shutdown(ctx); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func validRecord(id int64) Record {
	return Record{ActorID: id, Method: "GET", Path: "/api/v1/me", StatusCode: 200}
}

func TestAccessLogBoundedNonblockingQueue(t *testing.T) {
	writer := newTestWriter()
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	writer.write = func(ctx context.Context, _ []Record) error {
		once.Do(func() { close(entered) })
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	sink := newTestSink(t, writer, quietOptions(3, 2))
	if !sink.TryEnqueue(validRecord(1)) || !sink.TryEnqueue(validRecord(2)) {
		t.Fatal("initial batch rejected")
	}
	waitTestSignal(t, entered, "blocked writer")
	admitted := make(chan struct{})
	go func() {
		defer close(admitted)
		for id := int64(3); id <= 5; id++ {
			if !sink.TryEnqueue(validRecord(id)) {
				t.Errorf("available queue slot rejected for %d", id)
			}
		}
		if sink.TryEnqueue(validRecord(6)) {
			t.Error("accepted beyond queue capacity")
		}
	}()
	// The writer cannot finish until this test releases it. Admission completing
	// beforehand proves that it neither waits on IO nor waits for queue space.
	waitTestSignal(t, admitted, "nonblocking queue admission")
	stats := sink.Snapshot()
	if stats.QueueDepth != 3 || stats.InFlight != 2 || stats.Accepted != 5 || stats.Dropped != 1 || stats.OldestQueuedAt == nil {
		t.Fatalf("bounded snapshot: %+v", stats)
	}
	*stats.OldestQueuedAt = time.Time{}
	if sink.Snapshot().OldestQueuedAt.IsZero() {
		t.Fatal("snapshot oldest timestamp aliases internal state")
	}
	close(release)
	stopTestSink(t, sink)
	var ids []int64
	for _, batch := range writer.snapshotBatches() {
		if len(batch) > 2 {
			t.Fatalf("batch exceeded limit: %d", len(batch))
		}
		for _, record := range batch {
			ids = append(ids, record.ActorID)
		}
	}
	if !reflect.DeepEqual(ids, []int64{1, 2, 3, 4, 5}) || sink.Snapshot().Written != 5 || writer.closeCount.Load() != 1 {
		t.Fatalf("drain lost or duplicated events: ids=%v stats=%+v", ids, sink.Snapshot())
	}
}

func TestAccessLogBatchFailureNotRetried(t *testing.T) {
	writer := newTestWriter()
	var calls atomic.Int32
	writer.write = func(context.Context, []Record) error {
		if calls.Add(1) == 1 {
			return errors.New("unknown commit result")
		}
		return nil
	}
	sink := newTestSink(t, writer, quietOptions(4, 2))
	for id := int64(1); id <= 4; id++ {
		if !sink.TryEnqueue(validRecord(id)) {
			t.Fatal("unexpected admission failure")
		}
	}
	stopTestSink(t, sink)
	stats := sink.Snapshot()
	batches := writer.snapshotBatches()
	if len(batches) != 2 || len(batches[0]) != 2 || len(batches[1]) != 2 || batches[0][0].ActorID != 1 || batches[1][0].ActorID != 3 || stats.Accepted != 4 || stats.WriteFailed != 2 || stats.Written != 2 || stats.LastFailureAt == nil || stats.LastSuccessAt == nil {
		t.Fatalf("failure replayed or accounting drifted: batches=%+v stats=%+v", batches, stats)
	}
	*stats.LastFailureAt, *stats.LastSuccessAt = time.Time{}, time.Time{}
	if current := sink.Snapshot(); current.LastFailureAt.IsZero() || current.LastSuccessAt.IsZero() {
		t.Fatal("snapshot failure/success timestamps alias internal state")
	}
}

func TestAccessLogEventIsolationAndBounds(t *testing.T) {
	writer := newTestWriter()
	sink := newTestSink(t, writer, quietOptions(10, 10))
	before := time.Now().UTC()
	originalTime := before.Add(-time.Hour)
	ip := net.ParseIP("192.0.2.19")
	first := validRecord(1)
	first.RemoteIP, first.OccurredAt, first.DurationMillis = ip, originalTime, 19
	first.CorrelationID = "0123456789abcdef0123456789abcdef"
	first.Path = "/api/v1/admin/customers/7?token=secret-query#secret-fragment"
	if !sink.TryEnqueue(first) {
		t.Fatal("first event rejected")
	}
	ip[len(ip)-1] = 90
	longPath := validRecord(2)
	longPath.Path = "/" + strings.Repeat("界", 1000)
	longPath.CorrelationID = "fedcba9876543210fedcba9876543210"
	longPath.DurationMillis = 1 << 40
	if !sink.TryEnqueue(longPath) {
		t.Fatal("long path event rejected")
	}
	invalidUTF8 := validRecord(3)
	invalidUTF8.Path, invalidUTF8.CorrelationID, invalidUTF8.DurationMillis = "/a\xffb", "secret-invalid-correlation", -19
	if !sink.TryEnqueue(invalidUTF8) {
		t.Fatal("invalid utf8 event rejected")
	}
	emptyPath := validRecord(4)
	emptyPath.Path = "?secret-query"
	if !sink.TryEnqueue(emptyPath) {
		t.Fatal("empty path event rejected")
	}
	for _, invalid := range []Record{
		{ActorID: 0, Method: "GET", Path: "/", StatusCode: 200},
		{ActorID: 1, Method: "secret-method", Path: "/", StatusCode: 200},
		{ActorID: 1, Method: "GET", Path: "/", StatusCode: 999},
	} {
		if sink.TryEnqueue(invalid) {
			t.Fatal("invalid diagnostic event accepted")
		}
	}
	stopTestSink(t, sink)
	var records []Record
	for _, batch := range writer.snapshotBatches() {
		records = append(records, batch...)
	}
	if len(records) != 4 || records[0].Path != "/api/v1/admin/customers/7" || !records[0].RemoteIP.Equal(net.ParseIP("192.0.2.19")) || !records[0].OccurredAt.Equal(originalTime) || records[0].DurationMillis != 19 || records[0].CorrelationID != first.CorrelationID {
		t.Fatalf("event identity or snapshot changed: %+v", records)
	}
	if len(records[1].Path) > 2048 || !utf8.ValidString(records[1].Path) || records[1].DurationMillis != 2147483647 || records[1].CorrelationID != longPath.CorrelationID {
		t.Fatalf("path/duration/correlation boundary failed: %+v", records[1])
	}
	if records[2].Path != "/ab" || records[2].CorrelationID != "" || records[2].DurationMillis != 0 || records[3].Path != "/unavailable" {
		t.Fatalf("invalid metadata normalization: %+v", records)
	}
	for _, record := range records[1:] {
		if record.OccurredAt.Before(before) || record.OccurredAt.After(time.Now().UTC()) {
			t.Fatal("missing occurrence time was not normalized at admission")
		}
	}
	if stats := sink.Snapshot(); stats.Accepted != 4 || stats.Dropped != 3 || stats.Written != 4 {
		t.Fatalf("event validation accounting: %+v", stats)
	}
}

func TestAccessLogWriteTimeout(t *testing.T) {
	writer := newTestWriter()
	entered, expired := make(chan struct{}), make(chan struct{})
	writer.write = func(ctx context.Context, _ []Record) error {
		close(entered)
		<-ctx.Done()
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			t.Errorf("write context ended with %v", ctx.Err())
		}
		close(expired)
		return ctx.Err()
	}
	options := quietOptions(2, 1)
	options.WriteTimeout = 30 * time.Millisecond
	sink := newTestSink(t, writer, options)
	if !sink.TryEnqueue(validRecord(1)) {
		t.Fatal("enqueue rejected")
	}
	waitTestSignal(t, entered, "write admission")
	waitTestSignal(t, expired, "write timeout")
	stopTestSink(t, sink)
	if stats := sink.Snapshot(); stats.WriteFailed != 1 || stats.Written != 0 || stats.InFlight != 0 || stats.LastFailureAt == nil || len(writer.snapshotBatches()) != 1 {
		t.Fatalf("timeout accounting or retry: %+v", stats)
	}
}

func TestAccessLogScheduledFlushAndCleanup(t *testing.T) {
	writer := newTestWriter()
	flushed, pruned := make(chan struct{}), make(chan struct{})
	var flushOnce, pruneOnce sync.Once
	writer.write = func(context.Context, []Record) error {
		flushOnce.Do(func() { close(flushed) })
		return nil
	}
	writer.prune = func(context.Context, time.Time, int) (int64, error) {
		pruneOnce.Do(func() { close(pruned) })
		return 0, nil
	}
	options := quietOptions(4, 4)
	options.FlushInterval, options.CleanupInterval = time.Millisecond, 2*time.Millisecond
	sink := newTestSink(t, writer, options)
	if !sink.TryEnqueue(validRecord(1)) {
		t.Fatal("enqueue rejected")
	}
	// A partial batch has no admission wake-up. It must be persisted by the
	// periodic writer, and retention must also run without any later requests.
	waitTestSignal(t, flushed, "partial-batch periodic flush")
	waitTestSignal(t, pruned, "periodic retention")
	stopTestSink(t, sink)
	if stats := sink.Snapshot(); stats.Written != 1 || stats.WriteFailed != 0 || stats.CleanupFailed != 0 {
		t.Fatalf("scheduled writer/maintenance failed: %+v", stats)
	}
}

func TestAccessLogRetentionBatchBudget(t *testing.T) {
	for _, test := range []struct {
		name   string
		counts []int64
		failAt int
		pruned uint64
		budget uint64
		failed uint64
	}{
		{name: "stop at partial batch", counts: []int64{5, 2}, pruned: 7},
		{name: "bounded full batches", counts: []int64{5, 5, 5}, pruned: 15, budget: 1},
		{name: "stop at failure", counts: []int64{5, 0}, failAt: 2, pruned: 5, failed: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			writer := newTestWriter()
			calls := 0
			started := time.Now().UTC()
			writer.prune = func(_ context.Context, before time.Time, limit int) (int64, error) {
				calls++
				if limit != 5 || before.Before(started.Add(-48*time.Hour)) || before.After(time.Now().UTC().Add(-48*time.Hour)) {
					t.Fatalf("retention arguments drifted: before=%v limit=%d", before, limit)
				}
				if calls > len(test.counts) {
					t.Fatal("retention exceeded the declared batch budget")
				}
				if calls == test.failAt {
					return 0, errors.New("prune unavailable")
				}
				return test.counts[calls-1], nil
			}
			// Directly exercise the maintenance budget, without a competing ticker.
			sink := &Sink{writer: writer, options: Options{PruneBatch: 5, PruneBatches: 3, WriteTimeout: time.Second, Retention: 48 * time.Hour}, done: make(chan struct{})}
			sink.prune(t.Context())
			stats := sink.Snapshot()
			if calls != len(test.counts) || stats.Pruned != test.pruned || stats.CleanupBudgetExhausted != test.budget || stats.CleanupFailed != test.failed {
				t.Fatalf("retention accounting: calls=%d stats=%+v", calls, stats)
			}
			if test.failed > 0 && (stats.LastFailureAt == nil || stats.Status != "degraded") {
				t.Fatalf("retention error invisible in health: %+v", stats)
			}
		})
	}
}

func TestAccessLogRetentionSharedDeadline(t *testing.T) {
	writer := newTestWriter()
	var firstDeadline time.Time
	calls := 0
	writer.prune = func(ctx context.Context, _ time.Time, limit int) (int64, error) {
		calls++
		deadline, bounded := ctx.Deadline()
		if !bounded {
			t.Fatal("retention context has no deadline")
		}
		if calls == 1 {
			firstDeadline = deadline
			return int64(limit), nil
		}
		if calls != 2 || !deadline.Equal(firstDeadline) {
			t.Fatal("each retention batch acquired a new time budget")
		}
		<-ctx.Done()
		return 0, ctx.Err()
	}
	sink := &Sink{writer: writer, options: Options{PruneBatch: 5, PruneBatches: 10, WriteTimeout: 30 * time.Millisecond, Retention: 24 * time.Hour}, done: make(chan struct{})}
	sink.prune(t.Context())
	if stats := sink.Snapshot(); calls != 2 || stats.Pruned != 5 || stats.CleanupFailed != 1 || stats.LastFailureAt == nil {
		t.Fatalf("retention deadline did not stop maintenance: calls=%d stats=%+v", calls, stats)
	}
}

func TestAccessLogShutdownDeadlineDiscardsQueue(t *testing.T) {
	writer := newTestWriter()
	entered := make(chan struct{})
	writer.write = func(ctx context.Context, _ []Record) error {
		close(entered)
		<-ctx.Done()
		return ctx.Err()
	}
	options := quietOptions(2, 1)
	options.WriteTimeout = time.Hour
	sink := newTestSink(t, writer, options)
	if !sink.TryEnqueue(validRecord(1)) {
		t.Fatal("enqueue rejected")
	}
	waitTestSignal(t, entered, "in-flight write")
	if !sink.TryEnqueue(validRecord(2)) || !sink.TryEnqueue(validRecord(3)) {
		t.Fatal("queue admission rejected")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := sink.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown = %v, want deadline", err)
	}
	waitTestSignal(t, writer.closed, "cancelled writer close")
	stopTestSink(t, sink)
	if sink.TryEnqueue(validRecord(4)) {
		t.Fatal("accepted after shutdown")
	}
	stats := sink.Snapshot()
	if stats.Accepted != 3 || stats.Written != 0 || stats.WriteFailed != 1 || stats.ShutdownDiscarded != 2 || stats.QueueDepth != 0 || stats.InFlight != 0 || stats.Dropped != 1 || stats.Status != "stopped" || writer.closeCount.Load() != 1 || len(writer.snapshotBatches()) != 1 {
		t.Fatalf("shutdown lost accounting or replayed: %+v", stats)
	}
}

func TestAccessLogConcurrentAdmissionAndShutdown(t *testing.T) {
	writer := newTestWriter()
	sink := newTestSink(t, writer, quietOptions(16, 4))
	start := make(chan struct{})
	var workers sync.WaitGroup
	var accepted atomic.Uint64
	for producer := 0; producer < 8; producer++ {
		workers.Go(func() {
			<-start
			for event := 0; event < 100; event++ {
				if sink.TryEnqueue(validRecord(int64(producer*100 + event + 1))) {
					accepted.Add(1)
				}
				_ = sink.Snapshot()
			}
		})
	}
	for range 4 {
		workers.Go(func() {
			<-start
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			if err := sink.Shutdown(ctx); err != nil {
				t.Errorf("concurrent shutdown: %v", err)
			}
		})
	}
	close(start)
	workers.Wait()
	stopTestSink(t, sink)
	stats := sink.Snapshot()
	if stats.Accepted != accepted.Load() || stats.Accepted+stats.Dropped != 800 || stats.Written != stats.Accepted || stats.WriteFailed != 0 || stats.ShutdownDiscarded != 0 || stats.QueueDepth != 0 || stats.InFlight != 0 || stats.Status != "stopped" || writer.closeCount.Load() != 1 {
		t.Fatalf("concurrent admission/shutdown accounting: %+v accepted=%d", stats, accepted.Load())
	}
	seen := make(map[int64]bool)
	for _, batch := range writer.snapshotBatches() {
		for _, record := range batch {
			if seen[record.ActorID] {
				t.Fatalf("event %d was written twice", record.ActorID)
			}
			seen[record.ActorID] = true
		}
	}
	if uint64(len(seen)) != stats.Written {
		t.Fatalf("written metric differs from writer: %d vs %d", stats.Written, len(seen))
	}
}

func TestAccessLogFailureReportOnShutdown(t *testing.T) {
	writer := newTestWriter()
	writer.write = func(context.Context, []Record) error { return errors.New("diagnostics unavailable") }
	var mu sync.Mutex
	var reports []Stats
	options := quietOptions(1, 1)
	options.Report = func(stats Stats) {
		mu.Lock()
		reports = append(reports, stats)
		mu.Unlock()
	}
	sink := newTestSink(t, writer, options)
	if !sink.TryEnqueue(validRecord(1)) {
		t.Fatal("enqueue rejected")
	}
	stopTestSink(t, sink)
	mu.Lock()
	defer mu.Unlock()
	if len(reports) == 0 || reports[len(reports)-1].WriteFailed != 1 {
		t.Fatalf("failed diagnostics were not reported on final drain: %+v", reports)
	}
}
