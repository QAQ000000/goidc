package httpapi

import (
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This opt-in acceptance kills a real production worker, never a simulated SQL claim.
func TestSupplierWorkerSIGKILLRecovery(t *testing.T) {
	if os.Getenv("ZJMFGO_WORKER_CRASH_ACCEPTANCE") != "1" {
		t.Skip("requires ZJMFGO_WORKER_CRASH_ACCEPTANCE=1")
	}
	base, err := url.Parse(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil || base == nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") || !strings.HasSuffix(base.Path, "_test") || base.Hostname() != "127.0.0.1" {
		t.Fatal("requires dedicated IPv4 loopback URL-form _test DSN")
	}
	for k := range base.Query() {
		if k != "sslmode" {
			t.Fatal("test DSN may only set sslmode; endpoint/schema overrides forbidden")
		}
	}
	if base.Port() == "" {
		t.Fatal("explicit test PostgreSQL port required")
	}
	cfg, err := pgx.ParseConfig(base.String())
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != strings.TrimPrefix(base.Path, "/") || len(cfg.RuntimeParams) != 0 {
		t.Fatal("test DSN effective configuration mismatch")
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("test database connection failed")
	}
	var pgVersion int
	var actualHost, actualDB string
	err = conn.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int,host(inet_server_addr()),current_database()").Scan(&pgVersion, &actualHost, &actualDB)
	_ = conn.Close(t.Context())
	if err != nil || pgVersion < 180000 || pgVersion >= 190000 || actualHost != "127.0.0.1" || actualDB != cfg.Database {
		t.Fatal("requires PG18 before schema creation")
	}
	binary := filepath.Join(t.TempDir(), "worker")
	build := exec.CommandContext(t.Context(), "../../scripts/go.sh", "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	// Resolve the script from the repository root, without depending on ambient Go.
	build.Path, err = filepath.Abs("../../scripts/go.sh")
	if err != nil {
		t.Fatal(err)
	}
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	for _, action := range []string{"sync", "create"} {
		t.Run(action, func(t *testing.T) {
			f := supplierSetup(t)
			var version int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version < 180000 {
				t.Fatal("requires PG18", version, err)
			}
			f.publish(t, f.pid, "sigkill-"+action)
			sid, _ := f.purchase(t, f.pid, "sigkill-purchase-"+action)
			var blockedSID int64
			if action == "create" {
				blockedSID, _ = f.purchase(t, f.pid, "sigkill-followup-create")
			}
			var jid int64
			if action == "sync" {
				initial := f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), *initial); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1 AND action=$2 ORDER BY id DESC LIMIT 1", sid, action).Scan(&jid); err != nil {
				t.Fatal(err)
			}
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var calls atomic.Int64
			target := "/host/header"
			if action == "create" {
				target = "/cart/settle"
			}
			// A separate loopback endpoint records delivery and withholds the first response.
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == target && calls.Add(1) == 1 {
					response := httptest.NewRecorder()
					f.remote.Config.Handler.ServeHTTP(response, r)
					entered <- struct{}{}
					<-release
					for k, values := range response.Header() {
						w.Header()[k] = values
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				f.remote.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(remote.Close)
			t.Cleanup(func() { close(release) })
			// Route this disposable fixture through the response-withholding proxy;
			// the ordinary configuration API cannot retarget an existing account.
			supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
			scoped := *base
			q := scoped.Query()
			schema := f.db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
			if !strings.HasPrefix(schema, "zjmfgo_test_") {
				t.Fatal("missing isolated schema")
			}
			t.Logf("isolated schema=%s PG=%d", schema, version)
			q.Set("search_path", schema)
			scoped.RawQuery = q.Encode()
			start := func() *exec.Cmd {
				c := exec.Command(binary)
				c.Dir = t.TempDir()
				logPath := filepath.Join(c.Dir, "worker.log")
				logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = logFile.Close() })
				c.Stdout = logFile
				c.Stderr = logFile
				c.Env = []string{"DATABASE_URL=" + scoped.String(), "PUBLIC_ORIGIN=http://127.0.0.1:3088", "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(f.db.ProvisionEncryptionKey), "PAYMENT_MOCK_ENABLED=false", "SMTP_HOST=127.0.0.1", "SMTP_PORT=1", "SMTP_TLS_MODE=none"}
				if err := c.Start(); err != nil {
					t.Fatal(err)
				}
				t.Logf("worker started pid=%d", c.Process.Pid)
				t.Cleanup(func() {
					if c.ProcessState == nil {
						_ = c.Process.Kill()
						_ = c.Wait()
					}
				})
				deadline := time.Now().Add(20 * time.Second)
				for time.Now().Before(deadline) {
					body, err := os.ReadFile(logPath)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(body), "provision worker started") {
						t.Logf("worker ready pid=%d log=%s", c.Process.Pid, logPath)
						return c
					}
					time.Sleep(50 * time.Millisecond)
				}
				t.Fatalf("worker readiness timeout pid=%d log=%s", c.Process.Pid, logPath)
				return nil
			}
			wait := func(label string, p func() bool) {
				t.Helper()
				deadline := time.Now().Add(35 * time.Second)
				for time.Now().Before(deadline) {
					if p() {
						return
					}
					time.Sleep(100 * time.Millisecond)
				}
				t.Fatal("timeout", label)
			}
			first := start()
			select {
			case <-entered:
			case <-time.After(30 * time.Second):
				t.Fatal("worker never delivered request")
			}
			var state string
			var attempts int
			var dispatched bool
			var lease *int64
			inspect := func() {
				t.Helper()
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.attempts,j.external_started_at IS NOT NULL,a.running_job_id FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, jid, f.supplierID).Scan(&state, &attempts, &dispatched, &lease); err != nil {
					t.Fatal(err)
				}
			}
			inspect()
			if state != "running" || attempts != 1 || !dispatched || lease == nil || *lease != jid {
				t.Fatal("missing durable real claim/dispatch", state, attempts, dispatched, lease)
			}
			t.Logf("before kill pid=%d status=%s attempts=%d dispatched=%t lease=%d requests=%d", first.Process.Pid, state, attempts, dispatched, *lease, calls.Load())
			if err := first.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			err = first.Wait()
			exit, ok := err.(*exec.ExitError)
			if !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatal("worker was not SIGKILLed", err)
			}
			t.Logf("confirmed pid=%d signal=SIGKILL", first.Process.Pid)
			inspect()
			if state != "running" || !dispatched {
				t.Fatal("kill unexpectedly completed job")
			}
			// Only the lease clock is aged, after proof of real delivery, death and DB state.
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1 AND status='running'", jid); err != nil {
				t.Fatal(err)
			}
			// Keep the first response withheld until cleanup; subsequent calls do not block.
			a, b := start(), start()
			wanted := "succeeded"
			if action == "create" {
				wanted = "waiting_verification"
			}
			wait(wanted, func() bool { inspect(); return state == wanted })
			blockedObserved := func() bool {
				var blocked bool
				err := f.db.Pool.QueryRow(t.Context(), `SELECT status='pending' AND attempts=0 AND last_error='供应商账户正在处理其他任务或等待核验' AND locked_at IS NULL AND external_started_at IS NULL AND available_at>clock_timestamp() FROM provision_jobs WHERE service_id=$1 AND action='create'`, blockedSID).Scan(&blocked)
				return err == nil && blocked
			}
			if blockedSID != 0 {
				wait("queued create actually rejected by supplier account lease", blockedObserved)
				t.Log("PASS second create actually blocked: pending attempts=0 lease=NULL dispatch=NULL future available_at and account-busy last_error")
			}
			check := func() {
				t.Helper()
				if blockedSID != 0 && !blockedObserved() {
					t.Fatal("queued create lost observed account-busy backoff")
				}

				inspect()
				if action == "sync" {
					if state != "succeeded" || attempts != 2 || lease != nil || calls.Load() != 2 || f.creates.Load() != 1 || f.pays.Load() != 1 {
						t.Fatal("read recovery/duplicate failure", state, attempts, lease, calls.Load(), f.creates.Load(), f.pays.Load())
					}
				} else if state != "waiting_verification" || attempts != 1 || !dispatched || lease == nil || *lease != jid || calls.Load() != 1 || f.creates.Load() != 1 || f.pays.Load() != 0 {
					t.Fatal("uncertain write redispatched/unfrozen", state, attempts, lease, calls.Load(), f.pays.Load())
				}
			}
			check()
			stop := func(c *exec.Cmd) {
				t.Helper()
				_ = c.Process.Signal(syscall.SIGTERM)
				done := make(chan error, 1)
				go func() { done <- c.Wait() }()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(10 * time.Second):
					_ = c.Process.Kill()
					<-done
					t.Fatal("worker TERM timeout")
				}
			}
			stop(a)
			stop(b)
			a, b = start(), start()
			time.Sleep(6 * time.Second)
			check()
			stop(a)
			stop(b)
			t.Logf("PASS PG=%d action=%s real SIGKILL durable claim/dispatch; restart+two workers+second restart: status=%s attempts=%d target_requests=%d", version, action, state, attempts, calls.Load())
		})
	}
}
