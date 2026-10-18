package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestSupplierRenewalPaysUpstreamBeforeExtendingDue(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprint(pending), func(t *testing.T) {
			f := supplierSetup(t)
			f.publish(t, f.pid, "supplier-renewal")
			sid, _ := f.purchase(t, f.pid, "supplier-renewal-order")
			create := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *create); err != nil {
				t.Fatal(err)
			}
			var before, paidDue, after time.Time
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&before); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/services/%d/renew", sid)
			expect(t, request(f.h, "POST", path, map[string]any{}, f.other, "wrong-owner-renew"), 404)
			w := request(f.h, "POST", path, map[string]any{}, f.customer, "supplier-renew-once")
			expect(t, w, 201)
			invoice := int64(data(t, w)["invoice_id"].(float64))
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE invoice_items SET supplier_renewal_snapshot='{}' WHERE invoice_id=$1", invoice); err == nil {
				t.Fatal("renewal quotation mutable")
			}
			expect(t, request(f.h, "POST", path, map[string]any{}, f.customer, "supplier-renew-once"), 200)
			if f.renews.Load() != 0 {
				t.Fatal("quote created upstream invoice")
			}
			payPath := fmt.Sprintf("/api/v1/invoices/%d/pay", invoice)
			expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "supplier-renew-payment"), 201)
			expect(t, request(f.h, "POST", payPath, map[string]any{}, f.customer, "supplier-renew-payment"), 200)
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&paidDue); err != nil || !paidDue.Equal(before) {
				t.Fatal("local collection extended due", before, paidDue, err)
			}
			expect(t, request(f.h, "POST", path, map[string]any{}, f.customer, "supplier-renew-again"), 409)
			f.renewalDueUnchanged.Store(pending)
			job := f.claim(t)
			if job.Action != "renew" {
				t.Fatal(job)
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET request_payload='{}' WHERE id=$1", job.ID); err == nil {
				t.Fatal("queued renewal request mutable")
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			status, svc, lease := f.state(t, job)
			if f.renews.Load() != 1 || f.renewalPays.Load() != 1 || svc != "active" {
				t.Fatal(status, svc, f.renews.Load(), f.renewalPays.Load())
			}
			if pending {
				if status != "waiting_verification" || lease == nil {
					t.Fatal("unconfirmed due treated as success", status, lease)
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "renew-unknown-refund"), 409)
				body := map[string]any{"job_id": job.ID, "expected_attempts": job.Attempts, "upstream_id": 42, "outcome": "delivered", "note": "核验该续费账单已支付且到期时间延长"}
				verify := fmt.Sprintf("/api/v1/admin/services/%d/supplier-verification", sid)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 409)
				f.renewalDueUnchanged.Store(false)
				expect(t, request(f.h, "POST", verify, body, f.root, ""), 200)
				status, _, lease = f.state(t, job)
			}
			if status != "succeeded" || lease != nil {
				t.Fatal(status, lease)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "renew-delivered-refund"), 409)
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&after); err != nil || !after.Equal(time.Unix(1802678400, 0)) {
				t.Fatal(after, err)
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *job); err == nil {
				t.Fatal("finished renewal redispatched")
			}
			if f.renews.Load() != 1 || f.renewalPays.Load() != 1 {
				t.Fatal("duplicate upstream renewal/payment")
			}
			// A delayed old header must not undo a confirmed purchased period.
			f.renewalDueUnchanged.Store(true)
			if _, err := f.db.EnqueuePeriodicSupplierSyncs(t.Context()); err != nil {
				t.Fatal(err)
			}
			sync := f.claim(t)
			if err := f.db.ProcessProvisionJob(t.Context(), *sync); err != nil {
				t.Fatal(err)
			}
			var syncedDue time.Time
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&syncedDue); err != nil || !syncedDue.Equal(after) {
				t.Fatal("old sync shortened renewed period", syncedDue, err)
			}
			expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/services/%d/auto-renew", sid), map[string]any{"enabled": true}, f.customer, ""), 200)
		})
	}
}

func TestSupplierRenewalRefundBeforeDispatchPreservesDue(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "supplier-renew-refund")
	sid, _ := f.purchase(t, f.pid, "supplier-renew-refund-order")
	create := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *create); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "renew-refund-quote")
	expect(t, w, 201)
	invoice := int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "renew-refund-pay"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "renew-refund-safe"), 201)
	var status string
	var due int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT j.status,extract(epoch FROM s.next_due_at)::bigint FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.action='renew'", sid).Scan(&status, &due); err != nil || status != "cancelled" || due != 1800000000 || f.renews.Load() != 0 {
		t.Fatal(status, due, err)
	}
}

func TestSupplierAutoRenewalQueuesOnlyOneUpstreamRenewal(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "supplier-auto-renew")
	sid, _ := f.purchase(t, f.pid, "supplier-auto-renew-order")
	create := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *create); err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	supplierRenewalClock(t, f, due)
	supplierArmNearDue(t, f, sid, due)
	var before, after time.Time
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at FROM services WHERE id=$1", sid).Scan(&before); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at,(SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew') FROM services WHERE id=$1", sid).Scan(&after, &count); err != nil || !after.Equal(before) || count != 1 {
		t.Fatal(before, after, count, err)
	}
	_, pendingError, _ := autoRenewState(t, f.db, sid)
	if pendingError == "" {
		t.Fatal("unresolved paid supplier renewal lost processing reason")
	}
	renew := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *renew); err != nil {
		t.Fatal(err)
	}
	status, _, lease := f.state(t, renew)
	if status != "succeeded" || lease != nil || f.renews.Load() != 1 || f.renewalPays.Load() != 1 {
		t.Fatal(status, lease)
	}
	_, confirmedError, _ := autoRenewState(t, f.db, sid)
	if confirmedError != "" {
		t.Fatal("confirmed supplier renewal retained stale automatic error", confirmedError)
	}
}
