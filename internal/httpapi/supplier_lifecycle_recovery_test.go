package httpapi

import (
	"fmt"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

func supplierLifecycleDebt(t *testing.T, f *supplierLifecycleFixture, label string) int64 {
	t.Helper()
	var invoiceID, orderID int64
	// Each automatic invoice owns its order; the existing delivered purchase
	// already has a unique invoice. The service line links this later charge.
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES($1,$2,'pending',100,$1,'supplier-billing-fixture') RETURNING id`, "LIFECYCLE-ORDER-"+label, f.uid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at)
VALUES($1,$2,$3,'unpaid',100,now()-interval '2 days') RETURNING id`, "LIFECYCLE-DEBT-"+label, f.uid, orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'供应商原订单逾期服务费用','service',100,$2)`, invoiceID, f.sid); err != nil {
		t.Fatal(err)
	}
	return invoiceID
}
func supplierLifecyclePayDebt(t *testing.T, f *supplierLifecycleFixture, invoiceID int64, key string) {
	t.Helper()
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, f.customer, key), 201)
}
func supplierLifecycleSystemSuspend(t *testing.T, f *supplierLifecycleFixture, supported bool) (int64, *store.ProvisionJob) {
	t.Helper()
	iid := supplierLifecycleDebt(t, f, "original")
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("suspend queued=%d err=%v", n, err)
	}
	var j store.ProvisionJob
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT id,service_id,action,attempts FROM provision_jobs WHERE service_id=$1 AND action='suspend' ORDER BY id DESC LIMIT 1`, f.sid).Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts); err != nil {
		t.Fatal(err)
	}
	var proven bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT request_payload->'system_overdue_suspension'='true'::jsonb AND (request_payload->>'billing_invoice_id')::bigint=$2 FROM provision_jobs WHERE id=$1`, j.ID, iid).Scan(&proven); err != nil || !proven {
		t.Fatalf("missing scheduler suspension provenance: %v", err)
	}
	if supported {
		j = *f.claim(t)
		if err := f.db.ProcessProvisionJob(t.Context(), j); err != nil {
			t.Fatal(err)
		}
	} else {
		f.remote(func(s *supplier.Service) { s.Status = "Suspended" })
		expect(t, f.verify(t, &j, f.root), 200)
	}
	f.check(t, &j, "succeeded", "suspended", false)
	return iid, &j
}

func TestSupplierLifecyclePaidOverdueSuspensionRecoversByCapability(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%v", supported), func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, supported)
			iid, suspension := supplierLifecycleSystemSuspend(t, f, supported)
			supplierLifecyclePayDebt(t, f, iid, "supplier-lifecycle-clear-overdue")
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
				t.Fatalf("recovery queued=%d err=%v", n, err)
			}
			var resume store.ProvisionJob
			var key string
			var source, invoice int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT id,service_id,action,attempts,idempotency_key,(request_payload->>'billing_suspension_job_id')::bigint,(request_payload->>'billing_invoice_id')::bigint FROM provision_jobs WHERE service_id=$1 AND action='resume' ORDER BY id DESC LIMIT 1`, f.sid).Scan(&resume.ID, &resume.ServiceID, &resume.Action, &resume.Attempts, &key, &source, &invoice); err != nil {
				t.Fatal(err)
			}
			if key != fmt.Sprintf("paid-resume-service-%d-suspension-%d", f.sid, suspension.ID) || source != suspension.ID || invoice != iid {
				t.Fatalf("recovery identity key=%s source=%d invoice=%d", key, source, invoice)
			}
			if supported {
				f.check(t, &resume, "pending", "suspended", false)
				resume = *f.claim(t)
				if err := f.db.ProcessProvisionJob(t.Context(), resume); err != nil {
					t.Fatal(err)
				}
			} else {
				f.check(t, &resume, "waiting_manual", "suspended", false)
				f.remote(func(s *supplier.Service) { s.Status = "Active" })
				expect(t, f.verify(t, &resume, f.root), 200)
			}
			f.check(t, &resume, "succeeded", "active", false)
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("replay queued=%d err=%v", n, err)
			}
			want := 0
			if supported {
				want = 2
			}
			if f.calls() != want {
				t.Fatalf("lifecycle upstream writes=%d want=%d", f.calls(), want)
			}
		})
	}
}

func TestSupplierLifecyclePaidRecoveryPreservesOtherSuspensionReasons(t *testing.T) {
	for _, reason := range []string{"admin", "upstream", "another_overdue_invoice", "legacy_canonical_without_marker", "new_upstream_hold"} {
		t.Run(reason, func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, true)
			var paid int64
			if reason == "another_overdue_invoice" || reason == "new_upstream_hold" {
				paid, _ = supplierLifecycleSystemSuspend(t, f, true)
				if reason == "another_overdue_invoice" {
					supplierLifecycleDebt(t, f, "another")
				} else {
					f.remote(func(s *supplier.Service) {
						s.SuspendReason = "上游新增人工核验冻结"
						s.SuspendReasonType = "manual"
					})
					f.queue(t, "sync", "supplier-lifecycle-new-upstream-hold")
					if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				paid = supplierLifecycleDebt(t, f, "manual-reason")
				if reason == "admin" || reason == "legacy_canonical_without_marker" {
					f.queue(t, "suspend", "supplier-lifecycle-admin-hold")
					if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
						t.Fatal(err)
					}
				} else {
					// A read-only reconciliation can observe an external suspension,
					// but it cannot turn that into a system billing provenance.
					f.remote(func(s *supplier.Service) { s.Status = "Suspended" })
					f.queue(t, "sync", "supplier-lifecycle-upstream-hold")
					if err := f.db.ProcessProvisionJob(t.Context(), *f.claim(t)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if reason == "legacy_canonical_without_marker" {
				// Preserve the historical row as it was; a matching key alone
				// cannot establish that the scheduler authorized the action.
				if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,completed_at)
VALUES($1,'suspend',$2,'succeeded',1,clock_timestamp())`, f.sid, fmt.Sprintf("overdue-suspend-service-%d-invoice-%d", f.sid, paid)); err != nil {
					t.Fatal(err)
				}
			}
			supplierLifecyclePayDebt(t, f, paid, "supplier-lifecycle-reason-payment")
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 0 {
				t.Fatalf("unrelated suspension recovery queued=%d err=%v", n, err)
			}
			var status string
			var count int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='resume') FROM services WHERE id=$1`, f.sid).Scan(&status, &count); err != nil || status != "suspended" || count != 0 {
				t.Fatalf("status=%s resumes=%d err=%v", status, count, err)
			}
		})
	}
}

func TestSupplierLifecyclePaidRecoveryDispatchRechecksNewDebt(t *testing.T) {
	for _, supported := range []bool{false, true} {
		t.Run(fmt.Sprintf("supported=%v", supported), func(t *testing.T) {
			f := newSupplierLifecycleFixture(t, supported)
			iid, _ := supplierLifecycleSystemSuspend(t, f, supported)
			supplierLifecyclePayDebt(t, f, iid, "supplier-lifecycle-before-new-debt")
			if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
				t.Fatalf("recovery queued=%d err=%v", n, err)
			}
			supplierLifecycleDebt(t, f, "after-queued-recovery")
			var j *store.ProvisionJob
			if supported {
				j = f.claim(t)
				if j.Action != "resume" {
					t.Fatalf("claimed %s", j.Action)
				}
				if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
					t.Fatal(err)
				}
			} else {
				j = &store.ProvisionJob{}
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT id,service_id,action,attempts FROM provision_jobs WHERE service_id=$1 AND action='resume' ORDER BY id DESC LIMIT 1`, f.sid).Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts); err != nil {
					t.Fatal(err)
				}
				f.remote(func(s *supplier.Service) { s.Status = "Active" })
				expect(t, f.verify(t, j, f.root), 200)
			}
			if supported {
				f.check(t, j, "cancelled", "suspended", false)
			} else {
				// A person may already have resumed upstream. Preserve that observed
				// fact, then let the outstanding debt authorize a fresh suspension.
				f.check(t, j, "succeeded", "active", false)
				if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
					t.Fatalf("new debt did not re-suspend: n=%d err=%v", n, err)
				}
				var pending int
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='suspend' AND status='waiting_manual'", f.sid).Scan(&pending); err != nil || pending != 1 {
					t.Fatalf("new manual suspension=%d err=%v", pending, err)
				}
			}
			want := 0
			if supported {
				want = 1
			}
			if f.calls() != want {
				t.Fatal("stale recovery sent an upstream resume")
			}
		})
	}
}

func TestSupplierLifecycleManualPausePaidBeforeConfirmationPreservesFact(t *testing.T) {
	f := newSupplierLifecycleFixture(t, false)
	iid := supplierLifecycleDebt(t, f, "paid-before-manual-pause-confirmation")
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("manual pause queued=%d err=%v", n, err)
	}
	var j store.ProvisionJob
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id,service_id,action,attempts FROM provision_jobs WHERE service_id=$1 AND action='suspend'", f.sid).Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts); err != nil {
		t.Fatal(err)
	}
	supplierLifecyclePayDebt(t, f, iid, "supplier-lifecycle-paid-while-operator-suspends")
	// A pending task records what the platform sent, not what a human has
	// already done in another console. A paid invoice cannot erase that fact.
	f.remote(func(s *supplier.Service) { s.Status = "Suspended" })
	expect(t, f.verify(t, &j, f.root), 200)
	f.check(t, &j, "succeeded", "suspended", false)
	if n, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil || n != 1 {
		t.Fatalf("paid manual suspension recovery queued=%d err=%v", n, err)
	}
	var resumes int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='resume' AND status='waiting_manual'", f.sid).Scan(&resumes); err != nil || resumes != 1 {
		t.Fatalf("manual resumes=%d err=%v", resumes, err)
	}
	if f.calls() != 0 {
		t.Fatal("manual reconciliation sent an upstream write")
	}
}

func TestSupplierLifecyclePaidRecoveryConcurrentScansQueueOnce(t *testing.T) {
	f := newSupplierLifecycleFixture(t, true)
	iid, _ := supplierLifecycleSystemSuspend(t, f, true)
	supplierLifecyclePayDebt(t, f, iid, "supplier-lifecycle-concurrent-pay")
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 4 {
				if _, err := f.db.EnqueueOverdueServiceSuspensions(t.Context(), 100); err != nil {
					errCh <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='resume'", f.sid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resume rows=%d err=%v", count, err)
	}
	if f.calls() != 1 {
		t.Fatal("scheduler dispatched an upstream write")
	}
}
