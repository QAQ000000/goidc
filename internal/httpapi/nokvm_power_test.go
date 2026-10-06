package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestNOKVMPowerNetworkMutexAndCancellation(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			fmt.Fprint(w, `{"code":0,"data":{"id":42}}`)
		case strings.Contains(r.URL.Path, "run_status"):
			fmt.Fprint(w, `{"code":0,"data":{"code":1}}`)
		default:
			calls.Add(1)
			close(entered)
			<-release
			fmt.Fprint(w, `{"code":0}`)
		}
	}, true)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	f.process(t)
	path := fmt.Sprintf("/api/v1/services/%d/actions", f.sid)
	expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.customer, "power-cancel-command"), 202)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- f.db.ProcessProvisionJob(ctx, *job) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("command never dispatched")
	}
	lockCtx, lockCancel := context.WithTimeout(t.Context(), time.Second)
	defer lockCancel()
	tx, err := f.db.Pool.Begin(lockCtx)
	if err != nil {
		t.Fatal(err)
	}
	_, err = tx.Exec(lockCtx, "SELECT id FROM services WHERE id=$1 FOR UPDATE NOWAIT", f.sid)
	tx.Rollback(lockCtx)
	if err != nil {
		t.Fatal("network retained service lock", err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("duplicate dispatch accepted")
	}
	expect(t, request(f.h, "POST", path, map[string]string{"action": "force_reboot"}, f.customer, "power-concurrent-command"), 409)
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled worker stuck")
	}
	close(release)
	var status string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", job.ID).Scan(&status); err != nil || status != "waiting_verification" || calls.Load() != 1 {
		t.Fatal(status, calls.Load(), err)
	}
}

func TestNOKVMPowerDeliveryModeBoundaries(t *testing.T) {
	f, _, calls := powerFixture(t, `{"code":0}`)
	path := fmt.Sprintf("/api/v1/services/%d/actions", f.sid)
	for _, mode := range []string{"manual", "simulation"} {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", f.sid, mode); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"sync", "power_on", "power_off", "reboot", "force_off", "force_reboot"} {
			expect(t, request(f.h, "POST", path, map[string]string{"action": action}, f.customer, "power-mode-"+mode+"-"+action), 409)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported mode sent power command")
	}
}

func powerFixture(t *testing.T, reply string) (nokvmFixture, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	state, calls := &atomic.Int32{}, &atomic.Int32{}
	state.Store(1)
	f := nokvmFixtureSetup(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST":
			fmt.Fprint(w, `{"code":0,"data":{"id":42,"name":"vm-42","public_ip":[{"ip":"192.0.2.42"}]}}`)
		case strings.Contains(r.URL.Path, "run_status"):
			fmt.Fprintf(w, `{"code":0,"data":{"code":%d}}`, state.Load())
		case strings.Contains(r.URL.Path, "virtual_power"):
			calls.Add(1)
			if strings.HasSuffix(r.URL.Path, "/start") || strings.Contains(r.URL.Path, "restart") {
				state.Store(1)
			} else {
				state.Store(2)
			}
			fmt.Fprint(w, reply)
		default:
			fmt.Fprint(w, `{"code":0,"data":{"name":"vm-42","public_ip":[{"ip":"192.0.2.42"}]}}`)
		}
	}, true)
	f.process(t)
	return f, state, calls
}

func (f nokvmFixture) powerInvariant(t *testing.T) string {
	t.Helper()
	var out string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(s.status,s.next_due_at,p.stock,u.balance_cents,(SELECT count(*) FROM balance_ledger),(SELECT status FROM invoices WHERE id=$2))::text FROM services s JOIN products p ON p.id=s.product_id JOIN users u ON u.id=s.user_id WHERE s.id=$1`, f.sid, f.invoice).Scan(&out)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNOKVMPowerAcceptanceSyncIdempotencyAndPermissions(t *testing.T) {
	f, state, calls := powerFixture(t, `{"code":0}`)
	before := f.powerInvariant(t)
	path := fmt.Sprintf("/api/v1/services/%d/actions", f.sid)
	readerID, reader := user(t, f.db, f.h, "power-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.other, "power-other-user"), 404)
	expect(t, request(f.h, "POST", strings.Replace(path, "/services/", "/admin/services/", 1), map[string]string{"action": "reboot"}, reader, "power-reader-denied"), 403)
	for i, action := range []string{"power_off", "power_on", "reboot", "force_reboot", "force_off"} {
		key := "power-customer-" + action
		expect(t, request(f.h, "POST", path, map[string]string{"action": action}, f.customer, key), 202)
		job := f.process(t)
		if calls.Load() != int32(i+1) {
			t.Fatal("wire count", calls.Load())
		}
		var power string
		var accepted bool
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.resource_info->>'power',(j.result->>'command_accepted')::boolean FROM services s JOIN provision_jobs j ON j.id=$2 WHERE s.id=$1`, f.sid, job.ID).Scan(&power, &accepted); err != nil || power != "processing" || !accepted {
			t.Fatal(power, accepted, err)
		}
		replay := request(f.h, "POST", path, map[string]string{"action": action}, f.customer, key)
		expect(t, replay, 200)
		if data(t, replay)["replayed"] != true {
			t.Fatal("missing replay")
		}
		expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.customer, key+"-repeat"), 409)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND action='sync' AND status='pending'", f.sid); err != nil {
			t.Fatal(err)
		}
		f.process(t)
		expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.customer, key+"-cooldown"), 409)
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET created_at=now()-interval '1 minute' WHERE service_id=$1", f.sid); err != nil {
			t.Fatal(err)
		}
		if f.powerInvariant(t) != before {
			t.Fatal("power changed billing/stock/balance")
		}
	}
	state.Store(1)
	expect(t, request(f.h, "POST", path, map[string]string{"action": "sync"}, f.customer, "power-customer-refresh"), 202)
	f.process(t)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET status='suspended' WHERE id=$1", f.sid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.customer, "power-suspended-user"), 409)
}

func TestNOKVMPowerUnknownManualVerificationAndAuditRollback(t *testing.T) {
	for _, mode := range []string{"applied", "not_applied", "audit"} {
		t.Run(mode, func(t *testing.T) {
			reply := `{"code":8,"code":0}`
			if mode == "audit" {
				reply = `{"code":0}`
			}
			f, _, calls := powerFixture(t, reply)
			before := f.powerInvariant(t)
			path := fmt.Sprintf("/api/v1/services/%d/actions", f.sid)
			expect(t, request(f.h, "POST", path, map[string]string{"action": "reboot"}, f.customer, "power-uncertain-reboot"), 202)
			if mode == "audit" {
				if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION power_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='service.nokvm.completed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER power_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION power_reject_audit()`); err != nil {
					t.Fatal(err)
				}
			}
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = f.db.ProcessProvisionJob(t.Context(), *job)
			if mode == "audit" {
				if err == nil {
					t.Fatal("audit rollback missing")
				}
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET locked_at=now()-interval '6 minutes' WHERE id=$1", job.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
					t.Fatal(err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			var status string
			var syncs int
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status,(SELECT count(*) FROM provision_jobs WHERE service_id=$2 AND action='sync') FROM provision_jobs WHERE id=$1", job.ID, f.sid).Scan(&status, &syncs); err != nil || status != "waiting_verification" || syncs != 0 {
				t.Fatal(status, syncs, err)
			}
			expect(t, request(f.h, "POST", path, map[string]string{"action": "force_reboot"}, f.customer, "power-uncertain-repeat"), 409)
			if mode != "audit" {
				body := store.DeliveryVerification{JobID: job.ID, Resolution: mode, Note: "已核对供应商实际操作记录"}
				verify := fmt.Sprintf("/api/v1/admin/services/%d/delivery-verification", f.sid)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='sync'", f.sid).Scan(&syncs); err != nil {
					t.Fatal(err)
				}
				want := 0
				if mode == "applied" {
					want = 1
				}
				if syncs != want {
					t.Fatal("verification sync count", syncs)
				}
			}
			if calls.Load() != 1 || f.powerInvariant(t) != before {
				t.Fatal("repeated command or billing changed")
			}
		})
	}
}
