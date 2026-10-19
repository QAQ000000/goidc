package notification

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/codex/zjmfgo/internal/notify"
)

type originCenterStore struct {
	*centerFixtureStore
	activeOrigins    []string
	completedOrigins []string
}

func (s *originCenterStore) NotificationJobActive(ctx context.Context, job notify.Job) (bool, error) {
	s.activeOrigins = append(s.activeOrigins, correlation.ID(ctx))
	return s.centerFixtureStore.NotificationJobActive(ctx, job)
}
func (s *originCenterStore) CompleteNotificationJob(ctx context.Context, job notify.Job, result notify.Result) error {
	s.completedOrigins = append(s.completedOrigins, correlation.ID(ctx))
	return s.centerFixtureStore.CompleteNotificationJob(ctx, job, result)
}

func TestNotificationWorkerRestoresEachOriginAndProtectsPayload(t *testing.T) {
	fixture, _ := centerFixture(t, notify.Result{Outcome: "accepted"})
	first := *fixture.jobs[0]
	first.ID = 1
	first.CorrelationID = correlation.NewID()
	first.Body = "private-body-marker"
	first.Target = "private-target-marker"
	second := first
	second.ID = 2
	second.CorrelationID = correlation.NewID()
	historical := first
	historical.ID = 3
	historical.CorrelationID = ""
	fixture.jobs = []*notify.Job{&first, &second, &historical}
	db := &originCenterStore{centerFixtureStore: fixture}
	parentID := correlation.NewID()
	ctx := correlation.WithID(t.Context(), parentID)
	var logs bytes.Buffer
	count, err := DrainNotificationCenter(ctx, db, 10, slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil || count != 3 {
		t.Fatalf("drain count=%d error=%v", count, err)
	}
	if len(db.completedOrigins) != 3 || db.completedOrigins[0] != first.CorrelationID || db.completedOrigins[1] != second.CorrelationID {
		t.Fatalf("origins=%v", db.completedOrigins)
	}
	last := db.completedOrigins[2]
	if !correlation.Valid(last) || last == parentID || last == first.CorrelationID || last == second.CorrelationID {
		t.Fatalf("historical job inherited another origin: %s", last)
	}
	for i, id := range db.activeOrigins {
		if id != db.completedOrigins[i/2] {
			t.Fatalf("lease phase origin changed: %v", db.activeOrigins)
		}
	}
	if correlation.ID(ctx) != parentID {
		t.Fatal("job context mutated polling context")
	}
	if strings.Contains(logs.String(), "private-body-marker") || strings.Contains(logs.String(), "private-target-marker") {
		t.Fatal("worker logged private delivery data")
	}
	for _, id := range db.completedOrigins {
		if !strings.Contains(logs.String(), id) {
			t.Fatalf("missing diagnostic origin %s", id)
		}
	}
}

type blockedCenterStore struct {
	*centerFixtureStore
	started chan struct{}
}

func (s *blockedCenterStore) RecoverStaleNotificationJobs(ctx context.Context) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
}
func TestNotificationRuntimeObserverFollowsActualLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	db := &blockedCenterStore{centerFixtureStore: &centerFixtureStore{}, started: make(chan struct{})}
	events := make(chan bool, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		RunNotificationCenter(ctx, db, nil, func(running bool) { events <- running })
	}()
	select {
	case <-db.started:
	case <-time.After(time.Second):
		t.Fatal("loop did not begin")
	}
	if !<-events {
		t.Fatal("loop did not record start")
	}
	select {
	case state := <-events:
		t.Fatalf("blocked loop reported progress/completion %v", state)
	default:
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("loop did not stop")
	}
	if <-events {
		t.Fatal("loop failed to record actual completion")
	}
}
