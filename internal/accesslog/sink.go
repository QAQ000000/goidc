// Package accesslog provides bounded, best-effort access diagnostics. Business
// audits must remain in their original transactions and never use this queue.
package accesslog

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/correlation"
)

type Record struct {
	ActorID        int64
	Method, Path   string
	StatusCode     int
	DurationMillis int64
	RemoteIP       net.IP
	OccurredAt     time.Time
	CorrelationID  string
}

type Writer interface {
	WriteBatch(context.Context, []Record) error
	Prune(context.Context, time.Time, int) (int64, error)
	Close()
}

type Options struct {
	Report                                                  func(Stats)
	Capacity, BatchSize, PruneBatch, PruneBatches           int
	FlushInterval, WriteTimeout, CleanupInterval, Retention time.Duration
}

func DefaultOptions() Options {
	return Options{Capacity: 1024, BatchSize: 64, PruneBatch: 1000, PruneBatches: 16, FlushInterval: 250 * time.Millisecond, WriteTimeout: time.Second, CleanupInterval: time.Minute, Retention: 30 * 24 * time.Hour}
}

type Stats struct {
	Status                 string     `json:"status"`
	Capacity               int        `json:"capacity"`
	QueueDepth             int        `json:"queue_depth"`
	InFlight               int        `json:"in_flight"`
	Accepted               uint64     `json:"accepted"`
	Written                uint64     `json:"written"`
	Dropped                uint64     `json:"dropped"`
	WriteFailed            uint64     `json:"write_failed"`
	CleanupFailed          uint64     `json:"cleanup_failed"`
	Pruned                 uint64     `json:"pruned"`
	CleanupBudgetExhausted uint64     `json:"cleanup_budget_exhausted"`
	ShutdownDiscarded      uint64     `json:"shutdown_discarded"`
	MaxQueueWaitMillis     int64      `json:"max_queue_wait_ms"`
	OldestQueuedAt         *time.Time `json:"oldest_queued_at,omitempty"`
	LastSuccessAt          *time.Time `json:"last_success_at,omitempty"`
	LastFailureAt          *time.Time `json:"last_failure_at,omitempty"`
	RetentionDays          int        `json:"retention_days"`
}

type queued struct {
	record Record
	at     time.Time
}

type Sink struct {
	mu         sync.Mutex
	items      []queued
	head, size int
	closing    bool
	stats      Stats
	options    Options
	writer     Writer
	wake, done chan struct{}
	cancel     context.CancelFunc
}

// New starts one writer. The sink owns writer and closes it after bounded drain.
func New(writer Writer, options Options) *Sink {
	defaults := DefaultOptions()
	if options.Capacity <= 0 {
		options.Capacity = defaults.Capacity
	}
	if options.BatchSize <= 0 || options.BatchSize > 1000 {
		options.BatchSize = defaults.BatchSize
	}
	if options.PruneBatch <= 0 || options.PruneBatch > 1000 {
		options.PruneBatch = defaults.PruneBatch
	}
	if options.PruneBatches <= 0 || options.PruneBatches > 100 {
		options.PruneBatches = defaults.PruneBatches
	}
	if options.FlushInterval <= 0 {
		options.FlushInterval = defaults.FlushInterval
	}
	if options.WriteTimeout <= 0 {
		options.WriteTimeout = defaults.WriteTimeout
	}
	if options.CleanupInterval <= 0 {
		options.CleanupInterval = defaults.CleanupInterval
	}
	if options.Retention <= 0 {
		options.Retention = defaults.Retention
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &Sink{items: make([]queued, options.Capacity), options: options, writer: writer, wake: make(chan struct{}, 1), done: make(chan struct{}), cancel: cancel, stats: Stats{Status: "ok", Capacity: options.Capacity, RetentionDays: int(options.Retention / (24 * time.Hour))}}
	go s.run(ctx)
	return s
}

// TryEnqueue never waits for IO or queue capacity. Only copied, non-secret value
// fields cross the request boundary; no request/context or credentials are kept.
func (s *Sink) TryEnqueue(record Record) bool {
	now := time.Now().UTC()
	if record.OccurredAt.IsZero() {
		record.OccurredAt = now
	}
	record.Path = strings.SplitN(strings.SplitN(record.Path, "?", 2)[0], "#", 2)[0]
	if len(record.Path) > 2048 {
		record.Path = record.Path[:2048]
		for !utf8.ValidString(record.Path) {
			record.Path = record.Path[:len(record.Path)-1]
		}
	}
	record.Path = strings.Clone(strings.ToValidUTF8(record.Path, ""))
	if record.Path == "" {
		record.Path = "/unavailable"
	}
	if record.DurationMillis < 0 {
		record.DurationMillis = 0
	}
	if record.DurationMillis > 2147483647 {
		record.DurationMillis = 2147483647
	}
	record.RemoteIP = append(net.IP(nil), record.RemoteIP...)
	if !correlation.Valid(record.CorrelationID) {
		record.CorrelationID = ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closing || s.size == len(s.items) || record.ActorID <= 0 || !validMethod(record.Method) || record.StatusCode < 100 || record.StatusCode > 599 {
		s.stats.Dropped++
		s.signal()
		return false
	}
	s.items[(s.head+s.size)%len(s.items)] = queued{record, now}
	s.size++
	s.stats.Accepted++
	if s.size >= s.options.BatchSize {
		s.signal()
	}
	return true
}

func (s *Sink) signal() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Sink) Snapshot() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.stats
	out.QueueDepth = s.size
	if s.size > 0 {
		at := s.items[s.head].at
		out.OldestQueuedAt = &at
	}
	// Returned timestamps never alias the counters owned by the writer.
	if out.LastSuccessAt != nil {
		at := *out.LastSuccessAt
		out.LastSuccessAt = &at
	}
	if out.LastFailureAt != nil {
		at := *out.LastFailureAt
		out.LastFailureAt = &at
	}
	if s.closing {
		out.Status = "stopping"
	}
	select {
	case <-s.done:
		out.Status = "stopped"
	default:
	}
	return out
}

// Shutdown stops admission, drains within ctx, and cancels an in-flight write
// when the deadline expires. It is safe to call repeatedly or concurrently.
func (s *Sink) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.closing = true
	s.mu.Unlock()
	s.signal()
	select {
	case <-s.done:
		return nil
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	}
}

func (s *Sink) take() ([]Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := min(s.size, s.options.BatchSize)
	batch := make([]Record, 0, count)
	now := time.Now()
	for range count {
		item := s.items[s.head]
		s.items[s.head] = queued{}
		s.head = (s.head + 1) % len(s.items)
		s.size--
		batch = append(batch, item.record)
		s.stats.MaxQueueWaitMillis = max(s.stats.MaxQueueWaitMillis, now.Sub(item.at).Milliseconds())
	}
	s.stats.InFlight = count
	return batch, s.closing
}

func (s *Sink) run(ctx context.Context) {
	defer close(s.done)
	defer s.cancel()
	defer s.writer.Close()
	flush := time.NewTicker(s.options.FlushInterval)
	defer flush.Stop()
	var reported Stats
	var reportedAt time.Time
	report := func(force bool) {
		if s.options.Report == nil {
			return
		}
		stats := s.Snapshot()
		if stats.Dropped == reported.Dropped && stats.WriteFailed == reported.WriteFailed && stats.CleanupFailed == reported.CleanupFailed && stats.CleanupBudgetExhausted == reported.CleanupBudgetExhausted && stats.ShutdownDiscarded == reported.ShutdownDiscarded {
			return
		}
		if force || time.Since(reportedAt) >= time.Minute {
			s.options.Report(stats)
			reported = stats
			reportedAt = time.Now()
		}
	}
	defer func() { report(true) }()
	cleanup := time.NewTicker(s.options.CleanupInterval)
	defer cleanup.Stop()
	for {
		report(false)
		select {
		case <-ctx.Done():
			s.discard()
			return
		case <-cleanup.C:
			s.prune(ctx)
		case <-s.wake:
		case <-flush.C:
		}
		for {
			if ctx.Err() != nil {
				s.discard()
				return
			}
			batch, closing := s.take()
			if len(batch) == 0 {
				if closing {
					return
				}
				break
			}
			writeCtx, cancel := context.WithTimeout(ctx, s.options.WriteTimeout)
			err := s.writer.WriteBatch(writeCtx, batch)
			cancel()
			s.mu.Lock()
			s.stats.InFlight = 0
			now := time.Now().UTC()
			if err != nil {
				s.stats.WriteFailed += uint64(len(batch))
				s.stats.LastFailureAt = &now
				s.stats.Status = "degraded"
			} else {
				s.stats.Written += uint64(len(batch))
				s.stats.LastSuccessAt = &now
				s.stats.Status = "ok"
			}
			more := s.size >= s.options.BatchSize || s.closing
			s.mu.Unlock()
			// A continuously full queue may never return to the outer select.
			// Report failures here too; the same minute throttle still applies.
			report(false)
			// No retry: an unknown COMMIT could duplicate diagnostics. All failed batch
			// records are counted; stdout remains the complete request log source.
			if !more {
				break
			}
			// Retention is bounded too and cannot starve indefinitely under full load.
			select {
			case <-cleanup.C:
				s.prune(ctx)
			default:
			}
		}
	}
}

func (s *Sink) prune(ctx context.Context) {
	// At most PruneBatches batches share one wall-clock budget. Never let a
	// retention backlog monopolize the writer or promise an exact deletion time.
	pruneCtx, cancel := context.WithTimeout(ctx, s.options.WriteTimeout)
	defer cancel()
	for i := 0; i < s.options.PruneBatches; i++ {
		count, err := s.writer.Prune(pruneCtx, time.Now().UTC().Add(-s.options.Retention), s.options.PruneBatch)
		s.mu.Lock()
		if err != nil {
			s.stats.CleanupFailed++
			now := time.Now().UTC()
			s.stats.LastFailureAt = &now
			s.stats.Status = "degraded"
		} else {
			s.stats.Pruned += uint64(count)
		}
		if err == nil && count == int64(s.options.PruneBatch) && i == s.options.PruneBatches-1 {
			s.stats.CleanupBudgetExhausted++
		}
		s.mu.Unlock()
		if err != nil || count < int64(s.options.PruneBatch) {
			return
		}
	}
}

func validMethod(method string) bool {
	switch method {
	case "GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS":
		return true
	}
	return false
}

func (s *Sink) discard() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stats.ShutdownDiscarded += uint64(s.size)
	s.size = 0
	clear(s.items)
}
