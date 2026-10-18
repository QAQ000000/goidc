package httpapi

import (
	"encoding/hex"
	"fmt"
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

	"github.com/jackc/pgx/v5"
)

// The remote simulator commits a renewal side effect, withholds its HTTP
// response, and the test SIGKILLs an actual cmd/worker process. SQL ageing only
// accelerates lease expiry after observing the killed process and durable row.
func TestSupplierRenewalWorkerSIGKILLRecovery(t *testing.T) {
	if os.Getenv("ZJMFGO_WORKER_CRASH_ACCEPTANCE") != "1" {
		t.Skip("requires ZJMFGO_WORKER_CRASH_ACCEPTANCE=1")
	}
	base, err := url.Parse(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil || base == nil || (base.Scheme != "postgres" && base.Scheme != "postgresql") || !strings.HasSuffix(base.Path, "_test") || base.Hostname() != "127.0.0.1" || base.Port() == "" {
		t.Fatal("requires dedicated IPv4 loopback URL-form _test DSN and explicit port")
	}
	for key := range base.Query() {
		if key != "sslmode" {
			t.Fatal("test DSN may only set sslmode")
		}
	}
	cfg, err := pgx.ParseConfig(base.String())
	if err != nil || cfg.Host != "127.0.0.1" || cfg.Database != strings.TrimPrefix(base.Path, "/") || len(cfg.RuntimeParams) != 0 {
		t.Fatal("effective test database configuration mismatch")
	}
	conn, err := pgx.ConnectConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	var version int
	var host, database string
	err = conn.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int,host(inet_server_addr()),current_database()").Scan(&version, &host, &database)
	_ = conn.Close(t.Context())
	if err != nil || version < 180000 || version >= 190000 || host != "127.0.0.1" || database != cfg.Database {
		t.Fatal("requires isolated PostgreSQL 18 before creating fixtures", err)
	}
	binary := filepath.Join(t.TempDir(), "worker")
	goScript, err := filepath.Abs("../../scripts/go.sh")
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(t.Context(), goScript, "build", "-o", binary, "./cmd/worker")
	build.Dir = "../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build worker: %v %s", err, output)
	}
	for _, stage := range []string{"invoice_created", "upstream_paid"} {
		t.Run(stage, func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			after, clock := supplierRenewalClockServer(t, f, due)
			supplierArmNearDue(t, f, sid, due)
			entered, release := make(chan struct{}, 1), make(chan struct{})
			var targetCalls atomic.Int64
			target := "/host/renew"
			if stage == "upstream_paid" {
				target = "/apply_credit"
			}
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == target && targetCalls.Add(1) == 1 {
					response := httptest.NewRecorder()
					clock.Config.Handler.ServeHTTP(response, r)
					entered <- struct{}{}
					<-release
					for key, values := range response.Header() {
						w.Header()[key] = values
					}
					w.WriteHeader(response.Code)
					_, _ = w.Write(response.Body.Bytes())
					return
				}
				clock.Config.Handler.ServeHTTP(w, r)
			}))
			t.Cleanup(remote.Close)
			t.Cleanup(func() { close(release) })
			supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
			balance := autoRenewBalance(t, f.db, f.uid)
			if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 1 {
				t.Fatal("automatic local collection", n, err)
			}
			var jid int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM provision_jobs WHERE service_id=$1 AND action='renew'", sid).Scan(&jid); err != nil {
				t.Fatal(err)
			}
			scoped := *base
			query := scoped.Query()
			schema := f.db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
			if !strings.HasPrefix(schema, "zjmfgo_test_") {
				t.Fatal("worker missing isolated schema")
			}
			query.Set("search_path", schema)
			scoped.RawQuery = query.Encode()
			start := func() *exec.Cmd {
				t.Helper()
				cmd := exec.Command(binary)
				cmd.Dir = t.TempDir()
				logFile, err := os.OpenFile(filepath.Join(cmd.Dir, "worker.log"), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = logFile.Close() })
				cmd.Stdout, cmd.Stderr = logFile, logFile
				cmd.Env = []string{"DATABASE_URL=" + scoped.String(), "PUBLIC_ORIGIN=http://127.0.0.1:3088", "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(f.db.ProvisionEncryptionKey), "PAYMENT_MOCK_ENABLED=false", "SMTP_HOST=127.0.0.1", "SMTP_PORT=1", "SMTP_TLS_MODE=none"}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					if cmd.ProcessState == nil {
						_ = cmd.Process.Kill()
						_ = cmd.Wait()
					}
				})
				t.Logf("worker started pid=%d schema=%s", cmd.Process.Pid, schema)
				return cmd
			}
			stop := func(cmd *exec.Cmd) {
				t.Helper()
				if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				go func() { done <- cmd.Wait() }()
				select {
				case err := <-done:
					if err != nil {
						t.Fatal("worker TERM", err)
					}
				case <-time.After(10 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Fatal("worker TERM timeout")
				}
			}
			var status string
			var attempts int
			var dispatched bool
			var invoice int64
			var lease *int64
			inspect := func() {
				t.Helper()
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.attempts,j.external_started_at IS NOT NULL,coalesce((j.result->>'invoice_id')::bigint,0),a.running_job_id FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, jid, f.supplierID).Scan(&status, &attempts, &dispatched, &invoice, &lease); err != nil {
					t.Fatal(err)
				}
			}
			first := start()
			select {
			case <-entered:
			case <-time.After(35 * time.Second):
				t.Fatal("worker never reached remote side effect")
			}
			inspect()
			wantInvoice, wantPays := int64(0), int64(0)
			if stage == "upstream_paid" {
				wantInvoice, wantPays = 91, 1
			}
			if status != "running" || attempts != 1 || !dispatched || invoice != wantInvoice || lease == nil || *lease != jid || f.renews.Load() != 1 || f.renewalPays.Load() != wantPays {
				t.Fatal("side effect missing durable boundary", status, attempts, dispatched, invoice, lease, f.renews.Load(), f.renewalPays.Load())
			}
			supplierRenewalEvents(t, f, sid, 0)
			if err := first.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			exitErr := first.Wait()
			exit, ok := exitErr.(*exec.ExitError)
			if !ok || exit.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
				t.Fatal("worker was not SIGKILLed", exitErr)
			}
			t.Logf("confirmed pid=%d SIGKILL after %s remote side effect; checkpoint invoice=%d", first.Process.Pid, stage, invoice)
			inspect()
			if status != "running" || !dispatched {
				t.Fatal("killed worker completed its job")
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1 AND status='running'", jid); err != nil {
				t.Fatal(err)
			}
			a, b := start(), start()
			deadline := time.Now().Add(35 * time.Second)
			for time.Now().Before(deadline) {
				inspect()
				if status == "waiting_verification" {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			check := func(wanted string) {
				t.Helper()
				inspect()
				paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
				if status != wanted || attempts != 1 || !dispatched || invoice != wantInvoice || paid != 1 || unpaid != 0 || total != 1 || targetCalls.Load() != 1 || f.renews.Load() != 1 || f.renewalPays.Load() != wantPays || autoRenewBalance(t, f.db, f.uid) != balance-2500 {
					t.Fatalf("renewal replay/freeze violated: state=%s attempts=%d invoice=%d paid=%d unpaid=%d total=%d target=%d renew=%d pay=%d", status, attempts, invoice, paid, unpaid, total, targetCalls.Load(), f.renews.Load(), f.renewalPays.Load())
				}
				current, _, _ := autoRenewState(t, f.db, sid)
				if wanted == "waiting_verification" {
					if lease == nil || *lease != jid || !current.Equal(due) {
						t.Fatal("uncertain renewal lost lease or extended due", lease, current, due)
					}
					supplierRenewalEvents(t, f, sid, 0)
				} else if lease != nil || !current.Equal(after) {
					t.Fatal("verified renewal not applied", lease, current, after)
				}
			}
			check("waiting_verification")
			stop(a)
			stop(b)
			a, b = start(), start()
			time.Sleep(3 * time.Second)
			check("waiting_verification")
			body := map[string]any{"job_id": jid, "expected_attempts": attempts, "upstream_id": 42, "outcome": "delivered", "note": "真实worker进程故障后只读核验账单与完整续费期限"}
			verify := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid)
			if stage == "invoice_created" {
				// The bill exists remotely but its identifier was never persisted.
				// Existing Active is insufficient evidence to guess a paid renewal.
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 409)
				check("waiting_verification")
			} else {
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				check("succeeded")
				supplierRenewalEvents(t, f, sid, 1)
			}
			stop(a)
			stop(b)
			a, b = start(), start()
			time.Sleep(3 * time.Second)
			if stage == "invoice_created" {
				check("waiting_verification")
			} else {
				check("succeeded")
				supplierRenewalEvents(t, f, sid, 1)
			}
			stop(a)
			stop(b)
			t.Log("PASS: actual worker SIGKILL, durable renewal boundary, two workers and repeated restart never resend; recovery uses only identifiable paid invoice and confirmed period")
		})
	}
}
