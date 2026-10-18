package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

// Providers without suspend support produce an actionable manual job while the
// service retains its confirmed status. Supported modes still queue real work.
func TestOverdueSuspendSkipsUnsupportedModesWithAudit(t *testing.T) {
	for _, mode := range []string{"supplier", "nokvm"} {
		t.Run(mode, func(t *testing.T) {
			db, h := testServer(t)
			uid, _ := user(t, db, h, "overdue-capability-"+mode, "customer")
			_, root := user(t, db, h, "overdue-capability-root-"+mode, "super_admin")
			pid := product(t, db, 10)
			sid, iid := billingSchedulerFixture(t, db, uid, pid, 1, true)
			if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode=$2 WHERE id=$1", sid, mode); err != nil {
				t.Fatal(err)
			}
			n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
			if err != nil || n != 1 {
				t.Fatalf("queued=%d err=%v", n, err)
			}
			var state, service string
			var attempts int
			var manual, dispatched bool
			if err := db.Pool.QueryRow(t.Context(), `SELECT j.status,s.status,j.attempts,coalesce(j.request_payload->>'manual_lifecycle'='true',false),j.external_started_at IS NOT NULL
FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.id=$1 AND j.action='suspend'`, sid).Scan(&state, &service, &attempts, &manual, &dispatched); err != nil {
				t.Fatal(err)
			}
			want := "pending"
			if mode == "supplier" {
				want = "waiting_manual"
			}
			if state != want || service != "active" || attempts != 0 || dispatched || manual != (mode == "supplier") {
				t.Fatalf("state=%s service=%s attempts=%d manual=%v dispatched=%v", state, service, attempts, manual, dispatched)
			}
			var audits int
			if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested' AND entity_type='service' AND entity_id=$1 AND payload->>'invoice_id'=$2`, fmt.Sprint(sid), fmt.Sprint(iid)).Scan(&audits); err != nil {
				t.Fatal(err)
			}
			wantAudits := 0
			if mode == "supplier" {
				wantAudits = 1
			}
			if audits != wantAudits {
				t.Fatalf("manual evidence=%d want=%d", audits, wantAudits)
			}
			if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("repeat queued=%d err=%v", n, err)
			}
			if mode == "supplier" {
				w := request(h, "GET", "/api/v1/admin/logs?kind=system&q=supplier.lifecycle.manual_requested", nil, root, "")
				expect(t, w, 200)
				if !strings.Contains(w.Body.String(), "supplier.lifecycle.manual_requested") {
					t.Fatal("manual intervention not visible in system logs")
				}
				if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested' AND entity_id=$1`, fmt.Sprint(sid)).Scan(&audits); err != nil || audits != 1 {
					t.Fatalf("repeat duplicated evidence=%d err=%v", audits, err)
				}
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

// The explicit admin action and scheduler both create the same safe manual
// fallback for the default supplier; neither may claim a successful suspension.
func TestOverdueSuspendSupplierMatchesManualActionConstraint(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	sid, _ := f.purchase(t, f.pid, "supplier-suspend-consistency")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/services/%d/actions", sid)
	w := request(f.h, "POST", path, map[string]any{"action": "suspend"}, f.root, "supplier-manual-suspend")
	expect(t, w, 202)
	j := int64(data(t, w)["job_id"].(float64))
	var state string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", j).Scan(&state); err != nil || state != "waiting_manual" {
		t.Fatalf("state=%s err=%v", state, err)
	}
	var iid int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,due_at)
VALUES('SUP-OVERDUE-'||replace(gen_random_uuid()::text,'-',''),$1,'unpaid',2500,now()-interval '2 days') RETURNING id`, f.uid).Scan(&iid); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
VALUES($1,'供应商逾期暂停夹具','renew',2500,$2)`, iid, sid); err != nil {
		t.Fatal(err)
	}
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("existing manual task did not block scheduler duplicate: n=%d err=%v", n, err)
	}
	var jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'", sid).Scan(&jobs); err != nil || jobs != 1 {
		t.Fatalf("jobs=%d err=%v", jobs, err)
	}
	var service string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM services WHERE id=$1", sid).Scan(&service); err != nil || service != "active" {
		t.Fatalf("service=%s err=%v", service, err)
	}
}
