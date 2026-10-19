package store

import (
	"context"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// Uses the actual worker binary, isolated PG18 schema, disabled gateways and no
// business jobs. The minute lanes must finish their real timer-driven passes.
func TestWorkerRuntimeProcessProgressAndStop(t *testing.T) {
	if os.Getenv("ZJMFGO_WORKER_RUNTIME_ACCEPTANCE") != "1" {
		t.Skip("set ZJMFGO_WORKER_RUNTIME_ACCEPTANCE=1")
	}
	raw := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if _, _, err := runtimeFixtureDSN(raw); err != nil {
		t.Fatal(err)
	}
	f, err := newReleaseRuntimeFixture(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := f.cleanup(context.Background()); err != nil {
			t.Error(err)
		}
	})
	if err = f.db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "worker")
	build := exec.CommandContext(t.Context(), "./scripts/go.sh", "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("worker build: %v %s", err, out)
	}
	logs, err := os.Create(filepath.Join(dir, "worker.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer logs.Close()
	child := exec.Command(binary)
	child.Dir = dir
	child.Env = []string{"DATABASE_URL=" + f.dsn, "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(make([]byte, 32)), "PUBLIC_ORIGIN=http://127.0.0.1:3088", "PAYMENT_MOCK_ENABLED=false"}
	child.Stdout = logs
	child.Stderr = logs
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = child.Process.Kill()
			<-done
		}
	})
	deadline := time.NewTimer(80 * time.Second)
	defer deadline.Stop()
	poll := time.NewTicker(100 * time.Millisecond)
	defer poll.Stop()
	complete := false
	var instance string
	for !complete {
		select {
		case err := <-done:
			stopped = true
			out, _ := os.ReadFile(logs.Name())
			t.Fatalf("worker exited before all lanes progressed: %v %s", err, out)
		case <-deadline.C:
			report, _ := f.db.WorkerRuntimeDiagnostics(t.Context())
			out, _ := os.ReadFile(logs.Name())
			t.Fatalf("real lanes did not complete: %+v logs=%s", report, out)
		case <-poll.C:
			var count int
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT count(*),coalesce(min(instance_id),'') FROM worker_runtime_lanes WHERE last_poll_at IS NOT NULL AND started_at IS NOT NULL AND completed_at IS NOT NULL AND completed_passes>0`).Scan(&count, &instance); err != nil {
				t.Fatal(err)
			}
			complete = count == 7
		}
	}
	report, err := f.db.WorkerRuntimeDiagnostics(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "ok" || len(report.Lanes) != 7 {
		t.Fatalf("idle worker not healthy after real passes: %+v", report)
	}
	if err = child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err = <-done:
		stopped = true
		if err != nil {
			out, _ := os.ReadFile(logs.Name())
			t.Fatalf("worker TERM: %v %s", err, out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("worker did not stop after TERM")
	}
	var allStopped bool
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT i.stopped_at IS NOT NULL AND count(l.*)=7 AND bool_and(l.state='stopped') FROM worker_runtime_instances i JOIN worker_runtime_lanes l ON l.instance_id=i.id WHERE i.id=$1 GROUP BY i.id`, instance).Scan(&allStopped); err != nil || !allStopped {
		t.Fatalf("shutdown state not durable: %v %v", allStopped, err)
	}
}
