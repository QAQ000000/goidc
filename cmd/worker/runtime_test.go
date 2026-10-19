package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
)

func TestWorkerJobContextSeparatesHistoricalAndStoredOrigins(t *testing.T) {
	parentID := correlation.NewID()
	ctx := correlation.WithID(t.Context(), parentID)
	stored := correlation.NewID()
	if got := correlation.ID(workerJobContext(ctx, stored)); got != stored {
		t.Fatalf("stored origin=%s", got)
	}
	first := correlation.ID(workerJobContext(ctx, ""))
	second := correlation.ID(workerJobContext(ctx, ""))
	if !correlation.Valid(first) || !correlation.Valid(second) || first == second || first == parentID || second == parentID {
		t.Fatal("historical jobs shared an unrelated origin")
	}
}

func TestCertificationRuntimeObserverDoesNotMaskBlockedExecution(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	done := make(chan struct{})
	events := make(chan bool, 4)
	processor := certificationProcessorFunc(func(ctx context.Context) (bool, error) { close(started); <-ctx.Done(); return false, nil })
	go func() {
		defer close(done)
		runCertifications(ctx, processor, slog.New(slog.NewJSONHandler(io.Discard, nil)), func(running bool) { events <- running })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("execution did not start")
	}
	if !<-events {
		t.Fatal("missing start")
	}
	select {
	case state := <-events:
		t.Fatalf("blocked execution reported completion/progress: %v", state)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("execution did not stop")
	}
	if <-events {
		t.Fatal("missing actual completion")
	}
}
