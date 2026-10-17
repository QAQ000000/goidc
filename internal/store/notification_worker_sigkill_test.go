package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Opt in to real cmd/worker subprocesses, SIGKILL and disposable PG18 schemas.
// No production delivery endpoints or inherited application credentials are used.
func TestNotificationWorkerSIGKILLRecovery(t *testing.T) {
	if os.Getenv("ZJMFGO_WORKER_CRASH_ACCEPTANCE") != "1" {
		t.Skip("set ZJMFGO_WORKER_CRASH_ACCEPTANCE=1")
	}
	raw := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if _, _, err := runtimeFixtureDSN(raw); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	binary := filepath.Join(dir, "worker")
	build := exec.CommandContext(t.Context(), "./scripts/go.sh", "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("worker build: %v %s", err, out)
	}
	for _, limit := range []int{3, 1} {
		t.Run(fmt.Sprintf("attempt_limit_%d", limit), func(t *testing.T) {
			f, err := newReleaseRuntimeFixture(t.Context(), raw)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := f.cleanup(context.Background()); err != nil {
					t.Error(err)
				}
			})
			db := f.db
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err := db.CheckSchema(t.Context()); err != nil {
				t.Fatal(err)
			}
			db.ProvisionEncryptionKey = make([]byte, 32)
			var requests atomic.Int32
			received := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]string
				if r.Method != "POST" || r.Header.Get("Authorization") != "Bearer fixture-only" || json.NewDecoder(r.Body).Decode(&payload) != nil || payload["message"] != "fixture message" {
					http.Error(w, "invalid fixture request", 400)
					return
				}
				n := requests.Add(1)
				if n == 1 {
					received <- struct{}{}
					<-r.Context().Done()
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)
			sealed, err := smsCrypt(db.ProvisionEncryptionKey, []byte("fixture-only"), "token", true)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(t.Context(), `UPDATE sms_settings SET configured=true,enabled=true,provider='generic_http_json',endpoint=$1,token_encrypted=$2 WHERE id=1`, server.URL, sealed); err != nil {
				t.Fatal(err)
			}
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			id, err := db.QueueSMSWithID(t.Context(), tx, "+8613800000000", "fixture message", "test", "", time.Now().Add(time.Hour))
			if err != nil {
				tx.Rollback(t.Context())
				t.Fatal(err)
			}
			if err = tx.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			if _, err = db.Pool.Exec(t.Context(), `UPDATE sms_outbox SET max_attempts=$2 WHERE id=$1`, id, limit); err != nil {
				t.Fatal(err)
			}
			type child struct {
				cmd     *exec.Cmd
				log     *os.File
				stopped bool
			}
			start := func() *child {
				log, err := os.CreateTemp(dir, "worker-*.log")
				if err != nil {
					t.Fatal(err)
				}
				c := &child{cmd: exec.Command(binary), log: log}
				c.cmd.Dir = dir
				c.cmd.Env = []string{"DATABASE_URL=" + f.dsn, "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(db.ProvisionEncryptionKey), "PUBLIC_ORIGIN=http://127.0.0.1:3088", "SMTP_HOST=127.0.0.1", "SMTP_PORT=1", "SMTP_TLS_MODE=none", "PAYMENT_MOCK_ENABLED=false"}
				c.cmd.Stdout = log
				c.cmd.Stderr = log
				if err = c.cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Logf("worker started pid=%d schema=%s", c.cmd.Process.Pid, f.schema)
				t.Cleanup(func() {
					if !c.stopped {
						c.cmd.Process.Kill()
						c.cmd.Wait()
					}
					log.Close()
				})
				return c
			}
			stop := func(c *child, sig syscall.Signal) {
				t.Helper()
				if err := c.cmd.Process.Signal(sig); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- c.cmd.Wait() }()
				var err error
				select {
				case err = <-done:
				case <-time.After(10 * time.Second):
					c.cmd.Process.Kill()
					err = <-done
					t.Fatal("worker stop timeout", err)
				}
				c.stopped = true
				if sig == syscall.SIGKILL {
					if status, ok := c.cmd.ProcessState.Sys().(syscall.WaitStatus); !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
						t.Fatal("not killed", err)
					}
				} else if err != nil {
					data, _ := os.ReadFile(c.log.Name())
					t.Fatalf("worker stop: %v %s", err, data)
				}
			}
			wait := func(label string, predicate func() bool) {
				t.Helper()
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					if predicate() {
						return
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatal("timeout", label)
			}
			var status, lastError string
			var attempts int
			var locked, dead bool
			var delay float64
			state := func() {
				t.Helper()
				if err := db.Pool.QueryRow(t.Context(), `SELECT status,attempts,locked_at IS NOT NULL,dead_letter_at IS NOT NULL,last_error,extract(epoch FROM available_at-now())::float8 FROM sms_outbox WHERE id=$1`, id).Scan(&status, &attempts, &locked, &dead, &lastError, &delay); err != nil {
					t.Fatal(err)
				}
			}
			c := start()
			select {
			case <-received:
			case <-time.After(20 * time.Second):
				t.Fatal("fixture never received real worker request")
			}
			state()
			if status != "sending" || attempts != 1 || !locked {
				t.Fatal("claim not durable", status, attempts, locked)
			}
			stop(c, syscall.SIGKILL)
			state()
			if status != "sending" || attempts != 1 || !locked {
				t.Fatal("kill unexpectedly completed claim")
			}
			var evidenceBefore int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sms_delivery_evidence WHERE outbox_id=$1", id).Scan(&evidenceBefore); err != nil || evidenceBefore != 0 {
				t.Fatal("killed attempt unexpectedly has response evidence", evidenceBefore, err)
			}
			t.Logf("SIGKILL confirmed: durable sending attempt=1; fixture received=%d; schema=%s", requests.Load(), f.schema)
			// Accelerate the five-minute lease only after proving the process is dead.
			if _, err = db.Pool.Exec(t.Context(), `UPDATE sms_outbox SET locked_at=now()-interval '6 minutes' WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
			c = start()
			competitor := start()
			expected := "pending"
			if limit == 1 {
				expected = "dead_letter"
			}
			wait("worker lease recovery", func() bool { state(); return status == expected })
			if attempts != 1 || locked || dead != (limit == 1) || lastError != "worker lease expired" || requests.Load() != 1 {
				t.Fatal("recovery invariant", status, attempts, locked, dead, lastError, requests.Load())
			}
			if limit > 1 {
				if delay < 40 || delay > 61 {
					t.Fatal("expected one-minute recovery backoff", delay)
				}
				t.Logf("recovery applied actual one-minute backoff: remaining %.2fs", delay)
				// Verify backoff first, then accelerate only the retry due time.
				if _, err = db.Pool.Exec(t.Context(), `UPDATE sms_outbox SET available_at=now() WHERE id=$1`, id); err != nil {
					t.Fatal(err)
				}
				wait("retry accepted", func() bool { state(); return status == "sent" })
				if attempts != 2 || locked || dead || requests.Load() != 2 {
					t.Fatal("retry invariant", attempts, locked, dead, requests.Load())
				}
				var erased, evidence bool
				if err = db.Pool.QueryRow(t.Context(), `SELECT octet_length(body_encrypted)=0,EXISTS(SELECT 1 FROM sms_delivery_evidence WHERE outbox_id=$1 AND attempts=2 AND outcome='accepted') FROM sms_outbox WHERE id=$1`, id).Scan(&erased, &evidence); err != nil || !erased || !evidence {
					t.Fatal("success evidence/body cleanup", erased, evidence, err)
				}
			}
			stop(c, syscall.SIGTERM)
			stop(competitor, syscall.SIGTERM)
			before := requests.Load()
			c = start()
			time.Sleep(4500 * time.Millisecond)
			state()
			stop(c, syscall.SIGTERM)
			if requests.Load() != before || status != map[bool]string{true: "dead_letter", false: "sent"}[limit == 1] {
				t.Fatal("terminal job redelivered", status, requests.Load(), before)
			}
			t.Logf("terminal=%s attempts=%d fixture_requests=%d; restart produced no additional send", status, attempts, requests.Load())
		})
	}
}
