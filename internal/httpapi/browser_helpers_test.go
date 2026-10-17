package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func browserFixturePaths(t *testing.T) (ready, finish string) {
	t.Helper()
	ready, finish = os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in isolated browser fixture")
	}
	if ready == finish {
		t.Fatal("browser fixture requires distinct ready and finish paths")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("browser fixture requires fresh ready and finish paths")
		}
	}
	return ready, finish
}

func writeBrowserJSON(t *testing.T, path string, value any) {
	t.Helper()
	manifest, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(path) })
	_, writeErr := file.Write(manifest)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		t.Fatal(err)
	}
}

func waitBrowserFinish(t *testing.T, ready, finish string, limit time.Duration) {
	t.Helper()
	t.Cleanup(func() { os.Remove(ready); os.Remove(finish) })
	if raw := os.Getenv("ZJMFGO_BROWSER_TIMEOUT_SECONDS"); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 60 {
			limit = time.Duration(seconds) * time.Second
		}
	}
	if deadline, ok := t.Deadline(); ok {
		limit = min(limit, time.Until(deadline)-30*time.Second)
	}
	if limit <= 0 {
		t.Fatal("insufficient time for browser acceptance")
	}
	t.Log("isolated browser fixture ready")
	deadline := time.NewTimer(limit)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-t.Context().Done():
			t.Fatal("browser fixture cancelled")
		case <-deadline.C:
			t.Fatal("browser fixture was not closed before its cleanup deadline")
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				return
			} else if !errors.Is(err, os.ErrNotExist) {
				t.Fatal(err)
			}
		}
	}
}

// stop joins the goroutine and is safe both before assertions and in cleanup.
func startBrowserWorker(t *testing.T, step func(context.Context) error) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if err := step(ctx); err != nil && !errors.Is(err, pgx.ErrNoRows) {
					if ctx.Err() == nil {
						t.Error(err)
					}
					return
				}
			}
		}
	}()
	var once sync.Once
	stop := func() { once.Do(cancel); <-done }
	t.Cleanup(stop)
	return stop
}

func startBrowserProvisionWorker(t *testing.T, db *store.Store, before func(context.Context) error) func() {
	t.Helper()
	return startBrowserWorker(t, func(ctx context.Context) error {
		if before != nil {
			if err := before(ctx); err != nil {
				return err
			}
		}
		job, err := db.ClaimProvisionJob(ctx)
		if err != nil {
			return err
		}
		return db.ProcessProvisionJob(ctx, *job)
	})
}

func TestBrowserWorkerStopJoinsInFlightStep(t *testing.T) {
	started, exited := make(chan struct{}), make(chan struct{})
	stop := startBrowserWorker(t, func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(exited)
		return ctx.Err()
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("worker did not start")
	}
	stop()
	select {
	case <-exited:
	default:
		t.Fatal("stop returned before the in-flight step exited")
	}
	// Both explicit teardown and t.Cleanup may join the same worker.
	stop()
}

func TestBrowserFinishCleansPrivateManifest(t *testing.T) {
	dir := t.TempDir()
	ready, finish := filepath.Join(dir, "ready.json"), filepath.Join(dir, "finish")
	t.Setenv("ZJMFGO_BROWSER_READY_FILE", ready)
	t.Setenv("ZJMFGO_BROWSER_FINISH_FILE", finish)
	t.Run("completed fixture", func(t *testing.T) {
		gotReady, gotFinish := browserFixturePaths(t)
		writeBrowserJSON(t, gotReady, map[string]string{"api_origin": "http://127.0.0.1:12345"})
		info, err := os.Stat(gotReady)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatal("fixture manifest must be private", err)
		}
		if err := os.WriteFile(gotFinish, nil, 0600); err != nil {
			t.Fatal(err)
		}
		waitBrowserFinish(t, gotReady, gotFinish, 3*time.Second)
	})
	for _, path := range []string{ready, finish} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fixture left private files behind", err)
		}
	}
}

func TestBrowserProvisionWorkerDeliversIsolatedJob(t *testing.T) {
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"fixture-worker","public_ip":[{"ip":"192.0.2.42"}]}}`)
	}, true)
	stop := startBrowserProvisionWorker(t, f.db, nil)
	defer stop()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("browser worker did not deliver the isolated job")
		case <-tick.C:
			var delivered bool
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT status='active' AND upstream_id='42' AND (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND status='succeeded')=1 FROM services WHERE id=$1`, f.sid).Scan(&delivered); err != nil {
				t.Fatal(err)
			}
			if delivered {
				stop()
				return
			}
		}
	}
}
