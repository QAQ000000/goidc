package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

// TestOverdueSuspendSkipsUnsupportedModesWithAudit reproduces the permanent,
// silent failure of the overdue-suspension scheduler for delivery modes that
// cannot suspend. The supplier adapter advertises no suspend capability, so the
// scheduler used to insert a job that could only fail: the service stayed active
// and unpaid, no alert was raised, and finance silently missed the suspended
// revenue. The scheduler must apply the same capability gate as the manual
// action API and record durable evidence instead of queueing doomed work.
func TestOverdueSuspendSkipsUnsupportedModesWithAudit(t *testing.T) {
	// NOKVM supports suspend, so only supplier is expected to be skipped. Keep
	// both in one table so a future capability change is caught here.
	for _, test := range []struct {
		mode      string
		expectJob bool
	}{
		{"supplier", false},
		{"nokvm", true},
	} {
		t.Run(test.mode, func(t *testing.T) {
			db, h := testServer(t)
			uid, _ := user(t, db, h, "overdue-unsupported-"+test.mode, "customer")
			_, root := user(t, db, h, "overdue-unsupported-root-"+test.mode, "super_admin")
			pid := product(t, db, 10)
			serviceID, invoiceID := billingSchedulerFixture(t, db, uid, pid, 1, true)
			if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", serviceID, test.mode); err != nil {
				t.Fatal(err)
			}
			queued, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
			if err != nil {
				t.Fatal(err)
			}
			var jobs int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", serviceID).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if !test.expectJob {
				if queued != 0 || jobs != 0 {
					t.Fatalf("unsupported mode queued a doomed job: queued=%d jobs=%d", queued, jobs)
				}
				var status string
				if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", serviceID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "active" {
					t.Fatalf("skipping must not silently change local state: %s", status)
				}
				// The skip must be visible to operators, keyed to service and invoice.
				var audits int
				if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
WHERE action='service.overdue_suspension.unsupported' AND entity_type='service' AND entity_id=$1
  AND payload->>'invoice_id'=$2 AND payload->>'provision_mode'=$3`,
					fmt.Sprint(serviceID), fmt.Sprint(invoiceID), test.mode).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if audits != 1 {
					t.Fatalf("missing operator evidence: audits=%d", audits)
				}
				// Re-running the minute-level scheduler must not pile up duplicates.
				queued, err = db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
				if err != nil || queued != 0 {
					t.Fatalf("second run queued=%d err=%v", queued, err)
				}
				if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events
WHERE action='service.overdue_suspension.unsupported' AND entity_type='service' AND entity_id=$1`,
					fmt.Sprint(serviceID)).Scan(&audits); err != nil {
					t.Fatal(err)
				}
				if audits != 1 {
					t.Fatalf("evidence duplicated on repeated scheduler runs: %d", audits)
				}
				// The evidence is reachable through the admin system-log surface, which
				// is how an operator discovers the missed, revenue-losing suspension.
				w := request(h, "GET", "/api/v1/admin/logs?kind=system&q=service.overdue_suspension.unsupported", nil, root, "")
				expect(t, w, 200)
				if !strings.Contains(w.Body.String(), "service.overdue_suspension.unsupported") {
					t.Fatalf("evidence not discoverable: %s", w.Body.String())
				}
				if strings.Contains(w.Body.String(), "unsupported_") {
					t.Fatalf("unrelated logs returned: %s", w.Body.String())
				}
				var skipAudits int
				if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='service.overdue_suspension.unsupported'`).Scan(&skipAudits); err != nil {
					t.Fatal(err)
				}
				if skipAudits != 1 {
					t.Fatalf("skip evidence count=%d", skipAudits)
				}
				return
			}
			// Supported mode: the working path must be untouched.
			if queued != 1 || jobs != 1 {
				t.Fatalf("supported mode lost its suspension: queued=%d jobs=%d", queued, jobs)
			}
			var skipAudits int
			if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='service.overdue_suspension.unsupported' AND entity_id=$1`, fmt.Sprint(serviceID)).Scan(&skipAudits); err != nil {
				t.Fatal(err)
			}
			if skipAudits != 0 {
				t.Fatal("supported mode recorded a skip event")
			}
		})
	}
}

// TestOverdueSuspendStillQueuesSupportedModes guarantees the capability gate did
// not disable the working path: a simulation service with an overdue invoice
// still gets its suspension job, and the job still suspends the service.
func TestOverdueSuspendStillQueuesSupportedModes(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "overdue-supported-customer", "customer")
	pid := product(t, db, 10)
	serviceID, _ := billingSchedulerFixture(t, db, uid, pid, 1, true)
	queued, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("supported mode not queued: queued=%d err=%v", queued, err)
	}
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if job.Action != "suspend" {
		t.Fatalf("expected suspend, got %s", job.Action)
	}
	if err := db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", serviceID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "suspended" {
		t.Fatalf("supported suspension did not apply: %s", status)
	}
	var unsupported int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='service.overdue_suspension.unsupported'`).Scan(&unsupported); err != nil {
		t.Fatal(err)
	}
	if unsupported != 0 {
		t.Fatal("supported suspension recorded a skip event")
	}
}

// TestOverdueSuspendSupplierMatchesManualActionConstraint confirms the gate is
// now shared with the manual API: the API rejects supplier suspend and the
// scheduler no longer bypasses that constraint for overdue invoices.
func TestOverdueSuspendSupplierMatchesManualActionConstraint(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	serviceID, _ := f.purchase(t, f.pid, "supplier-suspend-consistency")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/services/%d/actions", serviceID)
	for _, action := range []string{"suspend", "resume", "terminate"} {
		expect(t, request(f.h, "POST", path, map[string]any{"action": action}, f.root, "supplier-unsupported-"+action), 409)
	}
	// An overdue invoice attached to that supplier service must not enqueue any of
	// the unsupported actions.
	var invoiceID int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at)
VALUES('SUP-OVERDUE-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'unpaid',2500,now()-interval '2 days') RETURNING id`, f.uid).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
VALUES($1,'供应商逾期暂停夹具','renew',2500,$2)`, invoiceID, serviceID); err != nil {
		t.Fatal(err)
	}
	queued, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
	if err != nil || queued != 0 {
		t.Fatalf("supplier overdue suspension queued=%d err=%v", queued, err)
	}
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action IN ('suspend','resume','terminate')", serviceID).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatal("scheduler bypassed the supplier capability constraint")
	}
	var mode string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT payload->>'provision_mode' FROM audit_events
WHERE action='service.overdue_suspension.unsupported' AND entity_id=$1`, fmt.Sprint(serviceID)).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "supplier" {
		t.Fatalf("skip evidence lost the delivery mode: %s", mode)
	}
}
