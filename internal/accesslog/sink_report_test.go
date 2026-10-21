package accesslog

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestAccessLogReportsDuringContinuousFullBatchFailures(t *testing.T) {
	writer := newTestWriter()
	paused, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	var refill atomic.Bool
	refill.Store(true)
	var calls, reported atomic.Int32
	reports := make(chan Stats, 16)
	var sink *Sink
	writer.write = func(ctx context.Context, _ []Record) error {
		n := calls.Add(1)
		// Refill before returning so the queue never drops below a full batch.
		// This forces every subsequent write through the inner drain loop.
		if refill.Load() && !sink.TryEnqueue(validRecord(int64(n+1))) {
			t.Error("continuous batch refill was rejected")
		}
		if n == 12 {
			close(paused)
			select {
			case <-release:
			case <-ctx.Done():
			}
		}
		return errors.New("persistent diagnostics failure")
	}
	options := quietOptions(1, 1)
	options.Report = func(stats Stats) {
		reported.Add(1)
		reports <- stats
	}
	sink = newTestSink(t, writer, options)
	// Release the controlled writer before newTestSink's shutdown cleanup,
	// including when an assertion fails against a broken implementation.
	t.Cleanup(func() {
		refill.Store(false)
		releaseOnce.Do(func() { close(release) })
	})
	if !sink.TryEnqueue(validRecord(1)) {
		t.Fatal("initial event rejected")
	}
	waitTestSignal(t, paused, "twelfth continuously full batch")
	// The writer is still inside the drain loop and has not shut down. Its
	// first failure must already have reached operators, even under full load.
	select {
	case stats := <-reports:
		if stats.WriteFailed != 1 || stats.QueueDepth != 1 {
			t.Fatalf("first live failure report is inaccurate: %+v", stats)
		}
	default:
		t.Fatal("continuous full-batch failures starved live diagnostic reporting")
	}
	if got := reported.Load(); got != 1 {
		t.Fatalf("minute throttle emitted %d reports for 11 rapid failures", got)
	}
	if stats := sink.Snapshot(); stats.WriteFailed != 11 || stats.QueueDepth != 1 || stats.InFlight != 1 {
		t.Fatalf("test did not sustain full-batch failures: %+v", stats)
	}
	refill.Store(false)
	releaseOnce.Do(func() { close(release) })
	stopTestSink(t, sink)
}
