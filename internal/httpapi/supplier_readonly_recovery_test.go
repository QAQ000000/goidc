package httpapi

import (
	"testing"
)

// TestSupplierReadOnlySyncCrashReleasesAccount reproduces a worker crash after a
// read-only sync was durably dispatched and then abandoned. Recovery has no
// process memory and no upstream response, only durable row state
// (provision_jobs.action plus external_started_at). A read-only action cannot
// have written upstream state, so recovery must release the shared supplier
// account lease and retry the job. Under the old behaviour it entered manual
// verification and kept the lease, which stalled every queued job on that
// account and marked the service as having unresolved external delivery.
func TestSupplierReadOnlySyncCrashReleasesAccount(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	serviceID, _ := f.purchase(t, f.pid, "supplier-sync-crash-one")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	// Simulate the crash exactly as a killed worker leaves the database: the sync
	// was dispatched before the network call and the account lease was taken.
	var crashed int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,'sync','running',1,'supplier-sync-crash-dispatched',now()-interval '6 minutes',clock_timestamp()) RETURNING id`, serviceID).Scan(&crashed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, f.supplierID, crashed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	var started *string
	var lease *int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.external_started_at::text,a.running_job_id
FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, crashed, f.supplierID).Scan(&state, &started, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "pending" {
		t.Fatalf("read-only crash entered manual verification: %s", state)
	}
	if started != nil {
		t.Fatalf("read-only job kept the dispatched marker: %v", *started)
	}
	if lease != nil {
		t.Fatalf("supplier account stayed locked by a dead read-only job: %v", *lease)
	}
	// The retried sync must complete and leave no unresolved external delivery,
	// which is what used to block refunds and credential reads.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE id=$1", crashed); err != nil {
		t.Fatal(err)
	}
	retried := f.claim(t)
	if retried.ID != crashed || retried.Action != "sync" {
		t.Fatalf("recovered read-only job not claimable: %+v", retried)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *retried); err != nil {
		t.Fatal(err)
	}
	if state, service, lease := f.state(t, retried); state != "succeeded" || service != "active" || lease != nil {
		t.Fatalf("read-only retry did not finish: state=%s service=%s lease=%v", state, service, lease)
	}
	var unresolved int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM provision_jobs WHERE service_id=$1
 AND (status='waiting_verification' OR (status='running' AND external_started_at IS NOT NULL))`, serviceID).Scan(&unresolved); err != nil {
		t.Fatal(err)
	}
	if unresolved != 0 {
		t.Fatal("read-only crash left unresolved external delivery")
	}
	if f.creates.Load() != 1 {
		t.Fatalf("read-only recovery re-triggered a purchase: %d", f.creates.Load())
	}
}

// TestSupplierDispatchedWriteCrashStillRequiresVerification locks in the
// conservative half of the contract for every non-read-only action shape: an
// action that may have written upstream keeps the account lease, enters
// waiting_verification and is never resent automatically.
func TestSupplierDispatchedWriteCrashStillRequiresVerification(t *testing.T) {
	for _, action := range []string{"create", "suspend", "terminate", "power_off"} {
		t.Run(action, func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "local-vm")
			serviceID, _ := f.purchase(t, f.pid, "supplier-write-crash-"+action)
			if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
				t.Fatal(err)
			}
			var crashed int64
			var err error
			if action == "create" {
				// Re-arm the delivered create job as a crashed dispatch. The
				// attempt counter is fenced exactly as a newer worker attempt.
				err = f.db.Pool.QueryRow(t.Context(), `UPDATE provision_jobs
SET status='running',attempts=attempts+1,locked_at=now()-interval '6 minutes',external_started_at=clock_timestamp(),completed_at=NULL
WHERE id=(SELECT id FROM provision_jobs WHERE service_id=$1 AND action='create') RETURNING id`, serviceID).Scan(&crashed)
			} else {
				err = f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,$2,'running',1,$3,now()-interval '6 minutes',clock_timestamp()) RETURNING id`, serviceID, action, "supplier-write-crash-"+action).Scan(&crashed)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, f.supplierID, crashed); err != nil {
				t.Fatal(err)
			}
			if err = f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
				t.Fatal(err)
			}
			var state string
			var lease *int64
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT j.status,a.running_job_id FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, crashed, f.supplierID).Scan(&state, &lease); err != nil {
				t.Fatal(err)
			}
			if state != "waiting_verification" {
				t.Fatalf("%s crash must require verification, got %s", action, state)
			}
			if lease == nil || *lease != crashed {
				t.Fatalf("%s crash released the account lease: %v", action, lease)
			}
			var started bool
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1", crashed).Scan(&started); err != nil || !started {
				t.Fatalf("%s crash lost the dispatch marker: %v %v", action, started, err)
			}
			// The conservative path never redispatches a possibly-written action.
			if _, err = f.db.ClaimProvisionJob(t.Context()); err == nil {
				t.Fatalf("%s was retried automatically", action)
			}
		})
	}
}

// TestSupplierMixedCrashRecoverySeparatesReadOnlyAndWriteJobs proves the two
// classes are separated from durable markers alone within one recovery pass: the
// read-only job is released and retried while the write job keeps the lease and
// waits for an administrator.
func TestSupplierMixedCrashRecoverySeparatesReadOnlyAndWriteJobs(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	readOnlyService, _ := f.purchase(t, f.pid, "supplier-mixed-crash-one")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	in := f.in
	in.Slug = "local-vm-mixed-two"
	pid := f.importProduct(t, in)
	// Build a second paid service on the same supplier account directly; recovery
	// only inspects provision_jobs, so no second upstream purchase is needed.
	var orderID, itemID, writeService int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES('MIX-'||replace(gen_random_uuid()::text,'-',''),$1,'active',2500,'supplier-mixed-crash-order','fixture') RETURNING id`, f.uid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'Local VM','monthly',1,2500,0,2500) RETURNING id`, orderID, pid).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,unit_number,recurring_price_cents,upstream_id)
VALUES($1,$2,$3,$4,'混合崩溃服务',$5,'monthly','active','supplier',1,2500,'42') RETURNING id`, f.uid, orderID, itemID, pid, "supplier-mixed-crash.example").Scan(&writeService); err != nil {
		t.Fatal(err)
	}
	var readOnlyJobID, writeJobID int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,'sync','running',1,'supplier-mixed-readonly',now()-interval '6 minutes',clock_timestamp()) RETURNING id`, readOnlyService).Scan(&readOnlyJobID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,'terminate','running',1,'supplier-mixed-write',now()-interval '6 minutes',clock_timestamp()) RETURNING id`, writeService).Scan(&writeJobID); err != nil {
		t.Fatal(err)
	}
	// The account lease belongs to the write job, exactly as a crash would leave it.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, f.supplierID, writeJobID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	var readOnlyState string
	var readOnlyStarted *string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,external_started_at::text FROM provision_jobs WHERE id=$1`, readOnlyJobID).Scan(&readOnlyState, &readOnlyStarted); err != nil {
		t.Fatal(err)
	}
	var writeState string
	var writeStarted *string
	var lease *int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.external_started_at::text,a.running_job_id
FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, writeJobID, f.supplierID).Scan(&writeState, &writeStarted, &lease); err != nil {
		t.Fatal(err)
	}
	if readOnlyState != "pending" || readOnlyStarted != nil {
		t.Fatalf("read-only job not released: state=%s started=%v", readOnlyState, readOnlyStarted)
	}
	if writeState != "waiting_verification" || writeStarted == nil {
		t.Fatalf("write job not conservative: state=%s started=%v", writeState, writeStarted)
	}
	if lease == nil || *lease != writeJobID {
		t.Fatalf("write job lost the account lease: %v", lease)
	}
	// Only the read-only job is claimable.
	next := f.claim(t)
	if next.ID != readOnlyJobID {
		t.Fatalf("unexpected claimable job %d (want %d)", next.ID, readOnlyJobID)
	}
	if err := f.db.ProcessProvisionJob(t.Context(), *next); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.ClaimProvisionJob(t.Context()); err == nil {
		t.Fatal("dispatched write job was automatically retried")
	}
}

// TestSupplierCrashedReadOnlyJobExhaustedAttemptsFailsClosed keeps the
// attempt-budget branch honest: a read-only dispatch that already used up its
// retry budget is failed with a retryable error instead of looping forever or
// draining to manual verification.
func TestSupplierCrashedReadOnlyJobExhaustedAttemptsFailsClosed(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "local-vm")
	serviceID, _ := f.purchase(t, f.pid, "supplier-sync-crash-budget")
	if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
		t.Fatal(err)
	}
	var crashed int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,status,attempts,idempotency_key,locked_at,external_started_at)
VALUES($1,'sync','running',5,'supplier-sync-crash-budget',now()-interval '6 minutes',clock_timestamp()) RETURNING id`, serviceID).Scan(&crashed); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, f.supplierID, crashed); err != nil {
		t.Fatal(err)
	}
	if err := f.db.RecoverStaleProvisionJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	var state string
	var completed *string
	var lease *int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.completed_at::text,a.running_job_id
FROM provision_jobs j JOIN supplier_accounts a ON a.id=$2 WHERE j.id=$1`, crashed, f.supplierID).Scan(&state, &completed, &lease); err != nil {
		t.Fatal(err)
	}
	if state != "failed" || completed == nil {
		t.Fatalf("exhausted read-only job not failed: state=%s completed=%v", state, completed)
	}
	if lease != nil {
		t.Fatal("exhausted read-only job kept the account lease")
	}
	// The exhausted read-only job never blocks later work on the shared account.
	if _, err := f.db.ClaimProvisionJob(t.Context()); err == nil {
		t.Fatal("exhausted read-only job was retried again")
	}
}
