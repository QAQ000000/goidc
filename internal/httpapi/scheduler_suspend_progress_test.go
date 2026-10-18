package httpapi

import (
	"fmt"
	"sync"
	"testing"
)

// A manual lifecycle task advances the bounded scan without changing the
// confirmed service status or starving supported services in the next batch.
func TestOverdueSuspendProgressPastUnsupportedBatch(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "suspend-progress-customer", "customer")
	pid := product(t, db, 10)
	for i := 0; i < 100; i++ {
		sid, _ := billingSchedulerFixture(t, db, uid, pid, i, true)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", sid); err != nil {
			t.Fatal(err)
		}
	}
	manual, _ := billingSchedulerFixture(t, db, uid, pid, 100, true)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='manual' WHERE id=$1", manual); err != nil {
		t.Fatal(err)
	}
	billingSchedulerFixture(t, db, uid, pid, 101, true)
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 100 {
		t.Fatalf("first batch queued=%d err=%v", n, err)
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 2 {
		t.Fatalf("supported services behind 100 manual supplier tasks did not progress: queued=%d err=%v", n, err)
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("repeated scan queued=%d err=%v", n, err)
	}
	var audits, jobs, unsupportedActive, unsupportedJobs int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(*) FROM provision_jobs WHERE action='suspend'),
  (SELECT count(*) FROM services WHERE provision_mode='supplier' AND status='active'),
  (SELECT count(*) FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.provision_mode='supplier')`).Scan(&audits, &jobs, &unsupportedActive, &unsupportedJobs); err != nil {
		t.Fatal(err)
	}
	if audits != 100 || jobs != 102 || unsupportedActive != 100 || unsupportedJobs != 100 {
		t.Fatalf("audits=%d jobs=%d unsupported active=%d jobs=%d", audits, jobs, unsupportedActive, unsupportedJobs)
	}
}

// Historical skip audits must not prevent new manual tasks after upgrading.
func TestOverdueSuspendProgressReconsidersChangedModes(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "suspend-history-customer", "customer")
	pid := product(t, db, 10)
	for i := 0; i < 2; i++ {
		sid, iid := billingSchedulerFixture(t, db, uid, pid, i, true)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", sid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(action,entity_type,entity_id,payload)
VALUES('service.overdue_suspension.unsupported','service',$1,jsonb_build_object('invoice_id',$2::bigint,'provision_mode','supplier'))`, fmt.Sprint(sid), iid); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 2 {
		t.Fatalf("old skip audits suppressed manual tasks: n=%d err=%v", n, err)
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
		t.Fatalf("repeat n=%d err=%v", n, err)
	}
	var historical, manual, jobs int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
(SELECT count(*) FROM audit_events WHERE action='service.overdue_suspension.unsupported'),
(SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
(SELECT count(*) FROM provision_jobs WHERE status='waiting_manual' AND request_payload->>'manual_lifecycle'='true')`).Scan(&historical, &manual, &jobs); err != nil || historical != 2 || manual != 2 || jobs != 2 {
		t.Fatalf("history=%d manual=%d jobs=%d err=%v", historical, manual, jobs, err)
	}
}

func TestOverdueSuspendProgressEvidenceMatchesCurrentMode(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "suspend-mode-evidence-customer", "customer")
	pid := product(t, db, 10)
	sid, iid := billingSchedulerFixture(t, db, uid, pid, 0, true)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	// Historical evidence for another mode must not suppress the new manual
	// task or consume the bounded scan indefinitely.
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO audit_events(action,entity_type,entity_id,payload)
VALUES('service.overdue_suspension.unsupported','service',$1,
  jsonb_build_object('invoice_id',$2::bigint,'provision_mode','legacy-mode'))`, fmt.Sprint(sid), iid); err != nil {
		t.Fatal(err)
	}
	supported, _ := billingSchedulerFixture(t, db, uid, pid, 1, true)
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("current mode scan queued=%d err=%v", n, err)
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("current mode evidence failed to advance scan: queued=%d err=%v", n, err)
	}
	var audits, jobs int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend')`, supported).Scan(&audits, &jobs); err != nil || audits != 1 || jobs != 1 {
		t.Fatalf("current mode audits=%d supported jobs=%d err=%v", audits, jobs, err)
	}
}

func TestOverdueSuspendProgressConcurrentInvoices(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "suspend-concurrent-progress-customer", "customer")
	pid := product(t, db, 10)
	unsupported, _ := billingSchedulerFixture(t, db, uid, pid, 0, true)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", unsupported); err != nil {
		t.Fatal(err)
	}
	// Several invoices for one service share one pending manual task, without
	// duplicate evidence or starving the following service under concurrent scans.
	for i := 0; i < 5; i++ {
		var iid int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,due_at)
VALUES($1,$2,'unpaid',1234,now()-interval '2 days') RETURNING id`, fmt.Sprintf("PROGRESS-%d", i), uid).Scan(&iid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
VALUES($1,'重复服务账单','renew',1234,$2)`, iid, unsupported); err != nil {
			t.Fatal(err)
		}
	}
	supported, supportedInvoice := billingSchedulerFixture(t, db, uid, pid, 1, true)
	// Link the same supported service to another overdue invoice as well.
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
SELECT id,'交叉服务账单','service',1234,$1 FROM invoices WHERE invoice_no='PROGRESS-0'`, supported); err != nil {
		t.Fatal(err)
	}
	// Reverse the invoice/service association too, exercising invoice-first
	// lock ordering when each transaction has several candidates.
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id)
VALUES($1,'交叉服务账单','renew',1234,$2)`, supportedInvoice, unsupported); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for worker := 0; worker < 4; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for scan := 0; scan < 12; scan++ {
				if _, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 3); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var audits, distinctInvoices, jobs, unsupportedJobs int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(DISTINCT payload->>'invoice_id') FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend'),
  (SELECT count(*) FROM provision_jobs WHERE service_id=$2)`, supported, unsupported).Scan(&audits, &distinctInvoices, &jobs, &unsupportedJobs); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || distinctInvoices != 1 || jobs != 1 || unsupportedJobs != 1 {
		t.Fatalf("concurrent drain audits=%d distinct invoices=%d supported jobs=%d unsupported jobs=%d", audits, distinctInvoices, jobs, unsupportedJobs)
	}
}

// Candidate selection and audit/job creation are one transaction. If a later
// candidate fails after an earlier manual candidate has recorded evidence,
// neither the evidence nor a partial suspend job may escape the rollback.
func TestOverdueSuspendProgressRollsBackOnQueueFailure(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "suspend-progress-rollback-customer", "customer")
	pid := product(t, db, 10)
	unsupported, _ := billingSchedulerFixture(t, db, uid, pid, 0, true)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET provision_mode='supplier' WHERE id=$1", unsupported); err != nil {
		t.Fatal(err)
	}
	supported, _ := billingSchedulerFixture(t, db, uid, pid, 1, true)

	// Fail only the later, supported candidate. The first candidate must have
	// reached the manual-task audit branch before this trigger runs.
	functionName := fmt.Sprintf("test_scheduler_queue_failure_%d", supported)
	triggerName := fmt.Sprintf("test_scheduler_queue_failure_trigger_%d", supported)
	if _, err := db.Pool.Exec(t.Context(), fmt.Sprintf(`
CREATE FUNCTION %s() RETURNS trigger
LANGUAGE plpgsql AS $fn$
BEGIN
  IF NEW.service_id = %d THEN
    RAISE EXCEPTION 'forced overdue suspension queue failure';
  END IF;
  RETURN NEW;
END
$fn$`, functionName, supported)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = db.Pool.Exec(t.Context(), "DROP TRIGGER IF EXISTS "+triggerName+" ON provision_jobs")
		_, _ = db.Pool.Exec(t.Context(), "DROP FUNCTION IF EXISTS "+functionName+"()")
	}()
	if _, err := db.Pool.Exec(t.Context(), fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE INSERT ON provision_jobs
FOR EACH ROW EXECUTE FUNCTION %s()`, triggerName, functionName)); err != nil {
		t.Fatal(err)
	}

	if queued, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 2); err == nil || queued != 0 {
		t.Fatalf("forced queue failure queued=%d err=%v", queued, err)
	}
	var audits, jobs int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(*) FROM provision_jobs WHERE action='suspend')`).Scan(&audits, &jobs); err != nil {
		t.Fatal(err)
	}
	if audits != 0 || jobs != 0 {
		t.Fatalf("failed batch escaped partial writes: audits=%d jobs=%d", audits, jobs)
	}

	if _, err := db.Pool.Exec(t.Context(), "DROP TRIGGER "+triggerName+" ON provision_jobs"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "DROP FUNCTION "+functionName+"()"); err != nil {
		t.Fatal(err)
	}
	if queued, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 2); err != nil || queued != 2 {
		t.Fatalf("retry after rollback queued=%d err=%v", queued, err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT
  (SELECT count(*) FROM audit_events WHERE action='supplier.lifecycle.manual_requested'),
  (SELECT count(*) FROM provision_jobs WHERE action='suspend')`).Scan(&audits, &jobs); err != nil {
		t.Fatal(err)
	}
	if audits != 1 || jobs != 2 {
		t.Fatalf("retry did not apply complete batch: audits=%d jobs=%d", audits, jobs)
	}
}
