package httpapi

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Opt-in: builds and starts the real worker; allow roughly two minutes.
func TestSupplierResidentWorkerAutoRenewal(t *testing.T) {
	if os.Getenv("ZJMFGO_WORKER_ACCEPTANCE") != "1" {
		t.Skip("set ZJMFGO_WORKER_ACCEPTANCE=1 for real worker process acceptance")
	}
	for _, frozen := range []bool{false, true} {
		t.Run(fmt.Sprintf("unconfirmed_%t", frozen), func(t *testing.T) {
			f := supplierSetup(t)
			var version int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT current_setting('server_version_num')::int").Scan(&version); err != nil || version < 180000 {
				t.Fatal("requires PostgreSQL 18", version, err)
			}
			f.publish(t, f.pid, "resident-worker")
			sid, _ := f.purchase(t, f.pid, "resident-worker-purchase")
			create := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *create); err != nil {
				t.Fatal(err)
			}
			// Make the existing fake's unrenewed period due soon; the immutable remote
			// period remains its original timestamp, as in the existing renewal tests.
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET auto_renew=true,next_due_at=now()+interval '1 hour' WHERE id=$1", sid); err != nil {
				t.Fatal(err)
			}
			var before time.Time
			var balance int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT s.next_due_at,u.balance_cents FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=$1", sid).Scan(&before, &balance); err != nil {
				t.Fatal(err)
			}
			f.renewalDueUnchanged.Store(frozen)
			dir := t.TempDir()
			binary := filepath.Join(dir, "worker")
			build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/worker")
			build.Dir = "../.."
			if out, err := build.CombinedOutput(); err != nil {
				t.Fatalf("build: %v %s", err, out)
			}
			scopedURL, err := url.Parse(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
			if err != nil || (scopedURL.Scheme != "postgres" && scopedURL.Scheme != "postgresql") {
				t.Fatal("acceptance requires URL-form test DSN")
			}
			if scopedURL.Hostname() != "127.0.0.1" && scopedURL.Hostname() != "localhost" && scopedURL.Hostname() != "::1" {
				t.Fatal("acceptance requires loopback PostgreSQL")
			}
			query := scopedURL.Query()
			query.Set("search_path", f.db.Pool.Config().ConnConfig.RuntimeParams["search_path"])
			scopedURL.RawQuery = query.Encode()
			dsn := scopedURL.String()
			// Explicitly carry the isolated search_path to the subprocess. Fail closed before any
			// subprocess starts: the base test DSN alone must never reach the worker.
			schema := f.db.Pool.Config().ConnConfig.RuntimeParams["search_path"]
			if !strings.HasPrefix(schema, "zjmfgo_test_") || !strings.Contains(dsn, schema) {
				t.Fatal("missing isolated schema in worker DSN")
			}
			type child struct {
				cmd     *exec.Cmd
				out     *bytes.Buffer
				done    chan error
				stopped bool
			}
			start := func() *child {
				c := &child{out: new(bytes.Buffer), done: make(chan error, 1)}
				c.cmd = exec.CommandContext(t.Context(), binary)
				c.cmd.Cancel = func() error { return c.cmd.Process.Signal(syscall.SIGTERM) }
				c.cmd.WaitDelay = 10 * time.Second
				c.cmd.Dir = dir // no project .env or inherited working-directory configuration
				c.cmd.Env = []string{"DATABASE_URL=" + dsn, "PUBLIC_ORIGIN=http://127.0.0.1:3088", "PROVISION_ENCRYPTION_KEY=" + hex.EncodeToString(f.db.ProvisionEncryptionKey), "PAYMENT_MOCK_ENABLED=false", "SMTP_HOST=127.0.0.1", "SMTP_PORT=1", "SMTP_TLS_MODE=none"}
				c.cmd.Stdout = c.out
				c.cmd.Stderr = c.out
				if err := c.cmd.Start(); err != nil {
					t.Fatal(err)
				}
				go func() { c.done <- c.cmd.Wait() }()
				t.Cleanup(func() {
					if !c.stopped {
						c.cmd.Process.Signal(syscall.SIGTERM)
						select {
						case <-c.done:
						case <-time.After(10 * time.Second):
							c.cmd.Process.Kill()
							<-c.done
							t.Error("worker required KILL")
						}
						c.stopped = true
					}
				})
				return c
			}
			stop := func(c *child) {
				t.Helper()
				if err := c.cmd.Process.Signal(syscall.SIGTERM); err != nil {
					t.Fatal(err)
				}
				select {
				case err := <-c.done:
					if err != nil {
						t.Fatalf("TERM: %v %s", err, c.out.String())
					}
				case <-time.After(10 * time.Second):
					t.Fatal("TERM timeout")
				}
				c.stopped = true
				if err := c.cmd.Process.Signal(syscall.Signal(0)); err == nil {
					t.Fatal("worker remains alive")
				}
			}
			wait := func(label string, predicate func() bool) {
				t.Helper()
				deadline := time.Now().Add(25 * time.Second)
				for time.Now().Before(deadline) {
					if predicate() {
						return
					}
					select {
					case <-t.Context().Done():
						t.Fatal(t.Context().Err())
					case <-time.After(100 * time.Millisecond):
					}
				}
				t.Fatal("timeout:", label)
			}
			a, b := start(), start()
			var jid int64
			var attempts int
			wait("startup scan and frozen renewal", func() bool {
				var status string
				err := f.db.Pool.QueryRow(t.Context(), "SELECT id,status,attempts FROM provision_jobs WHERE service_id=$1 AND action='renew'", sid).Scan(&jid, &status, &attempts)
				return err == nil && ((!frozen && status == "succeeded") || (frozen && status == "waiting_verification"))
			})
			check := func(frozen bool) {
				t.Helper()
				var invoices, jobs, debits, runs int
				var currentBalance int64
				var due time.Time
				var lease *int64
				var state, svc string
				err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM invoice_items WHERE service_id=$1 AND item_type='renew'),(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew'),(SELECT count(*) FROM balance_ledger WHERE user_id=$2 AND delta_cents<0),(SELECT count(*) FROM scheduler_runs WHERE task_key='automatic_renewals' AND status='succeeded' AND is_full_pass),u.balance_cents,s.next_due_at,a.running_job_id,j.status,s.status FROM services s JOIN users u ON u.id=s.user_id JOIN supplier_accounts a ON a.id=$3 JOIN provision_jobs j ON j.id=$4 WHERE s.id=$1`, sid, f.uid, f.supplierID, jid).Scan(&invoices, &jobs, &debits, &runs, &currentBalance, &due, &lease, &state, &svc)
				if err != nil {
					t.Fatal(err)
				}
				if invoices != 1 || jobs != 1 || debits != 2 || runs != 1 || currentBalance != balance-2500 || f.renews.Load() != 1 || f.renewalPays.Load() != 1 || svc != "active" {
					t.Fatalf("duplicate/incomplete chain invoices=%d jobs=%d debits=%d runs=%d balance=%d renew=%d pay=%d service=%s", invoices, jobs, debits, runs, currentBalance, f.renews.Load(), f.renewalPays.Load(), svc)
				}
				if frozen {
					if state != "waiting_verification" || lease == nil || !due.Equal(before) {
						t.Fatal("unconfirmed period not frozen", state, lease, due, before)
					}
				} else if state != "succeeded" || lease != nil || !due.Equal(time.Unix(1802678400, 0)) {
					t.Fatal("confirmed period not applied", state, lease, due)
				}
			}
			check(frozen)
			stop(a)
			stop(b)
			a, b = start(), start()
			// Cross an actual minute tick of both restarted worker processes.
			select {
			case <-t.Context().Done():
				t.Fatal(t.Context().Err())
			case <-time.After(65 * time.Second):
			}
			check(frozen)
			if frozen {
				f.renewalDueUnchanged.Store(false)
				body := map[string]any{"job_id": jid, "expected_attempts": attempts, "upstream_id": 42, "outcome": "delivered", "note": "loopback worker acceptance confirms paid invoice and extended period"}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid), body, f.root, ""), 200)
			}
			check(false)
			stop(a)
			stop(b)
			// Restart after verified completion must also preserve the single renewal.
			a, b = start(), start()
			time.Sleep(3 * time.Second)
			check(false)
			stop(a)
			stop(b)
			t.Log("PASS: immediate scheduler, two workers, one invoice/debit/job/upstream renewal/payment, unconfirmed period frozen across minute and restart, verified period applied and lease released; all six worker processes exited via TERM")
		})
	}
}
