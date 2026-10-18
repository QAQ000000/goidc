package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSupplierAutoRenewalPaymentRechecksSupplierAccount(t *testing.T) {
	for _, change := range []string{"disabled", "version"} {
		t.Run(change, func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			supplierRenewalClock(t, f, due)
			generation := supplierArmNearDue(t, f, sid, due)
			invoice, err := f.db.CreateAutoRenewalInvoice(t.Context(), f.uid, sid, due, "auto-account-before-payment-"+change, generation)
			if err != nil {
				t.Fatal(err)
			}
			statement, wanted := "UPDATE supplier_accounts SET active=false WHERE id=$1", "supplier_disabled"
			if change == "version" {
				statement, wanted = "UPDATE supplier_accounts SET version=version+1 WHERE id=$1", "supplier_renewal_quote_changed"
			}
			if _, err = f.db.Pool.Exec(t.Context(), statement, f.supplierID); err != nil {
				t.Fatal(err)
			}
			balance := autoRenewBalance(t, f.db, f.uid)
			_, err = f.db.PayAutoRenewalInvoice(t.Context(), f.uid, sid, invoice.InvoiceID, due, generation, "auto-account-payment-"+change)
			var c *store.Conflict
			if !errors.As(err, &c) || c.Code != wanted {
				t.Fatalf("changed supplier account accepted collection: want=%s err=%v", wanted, err)
			}
			paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
			var jobs int
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew'", sid).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if paid != 0 || unpaid != 1 || total != 1 || jobs != 0 || autoRenewBalance(t, f.db, f.uid) != balance || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
				t.Fatalf("changed account collected money: paid=%d unpaid=%d total=%d jobs=%d", paid, unpaid, total, jobs)
			}
			supplierRenewalEvents(t, f, sid, 0)
		})
	}
}

func TestSupplierAutoRenewalDispatchFailuresRemainRecoverable(t *testing.T) {
	for _, stage := range []string{"quote_over_budget", "account_disabled_before_scan", "quote_changed_after_collection", "invoice_changed_before_upstream_payment", "account_disabled_before_dispatch", "account_version_before_dispatch"} {
		t.Run(stage, func(t *testing.T) {
			f, sid := activeSupplierRenewalFixture(t)
			due := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
			_, clock := supplierRenewalClockServer(t, f, due)
			supplierArmNearDue(t, f, sid, due)
			var changed atomic.Bool
			remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mutateQuote := changed.Load() && r.URL.Path == "/host/renewpage" && (stage == "quote_over_budget" || stage == "quote_changed_after_collection")
				mutateInvoice := changed.Load() && r.URL.Path == "/get_invoices_detail" && r.URL.Query().Get("id") == "91" && stage == "invoice_changed_before_upstream_payment"
				if !mutateQuote && !mutateInvoice {
					clock.Config.Handler.ServeHTTP(w, r)
					return
				}
				response := httptest.NewRecorder()
				clock.Config.Handler.ServeHTTP(response, r)
				var body map[string]any
				if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				data := body["data"].(map[string]any)
				if mutateQuote {
					amount := "16.01"
					if stage == "quote_over_budget" {
						amount = "17.01"
					}
					data["host"].(map[string]any)["amount"] = amount
				} else {
					detail := data["detail"].(map[string]any)
					detail["subtotal"], detail["total"] = "16.01", "16.01"
				}
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(body)
			}))
			t.Cleanup(remote.Close)
			supplierFixtureIdentityWrite(t, f.db, "UPDATE supplier_accounts SET base_url=$2,config=jsonb_set(config,'{endpoint}',to_jsonb($2::text)),identity_key='' WHERE id=$1", f.supplierID, remote.URL)
			beforeCollection := stage == "quote_over_budget" || stage == "account_disabled_before_scan"
			if stage == "quote_over_budget" {
				changed.Store(true)
			} else if stage == "account_disabled_before_scan" {
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET active=false WHERE id=$1", f.supplierID); err != nil {
					t.Fatal(err)
				}
			}
			balance := autoRenewBalance(t, f.db, f.uid)
			collected := 1
			if beforeCollection {
				collected = 0
			}
			if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != collected {
				_, reason, _ := autoRenewState(t, f.db, sid)
				t.Fatal("automatic scan result", n, err, reason)
			}
			if beforeCollection {
				paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
				current, message, _ := autoRenewState(t, f.db, sid)
				if paid != 0 || unpaid != 0 || total != 0 || !current.Equal(due) || message == "" || autoRenewBalance(t, f.db, f.uid) != balance || f.renews.Load() != 0 || f.renewalPays.Load() != 0 {
					t.Fatalf("pre-collection failure persisted funds: paid=%d unpaid=%d total=%d due=%s error=%q", paid, unpaid, total, current, message)
				}
				supplierRenewalEvents(t, f, sid, 0)
				return
			}
			changed.Store(true)
			switch stage {
			case "account_disabled_before_dispatch":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET active=false WHERE id=$1", f.supplierID); err != nil {
					t.Fatal(err)
				}
			case "account_version_before_dispatch":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE supplier_accounts SET version=version+1 WHERE id=$1", f.supplierID); err != nil {
					t.Fatal(err)
				}
			}
			job := f.claim(t)
			if job.Action != "renew" {
				t.Fatal(job)
			}
			if err := f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			status, svc, lease := f.state(t, job)
			current, _, _ := autoRenewState(t, f.db, sid)
			wantRenew := int64(0)
			if stage == "invoice_changed_before_upstream_payment" {
				wantRenew = 1
			}
			if status != "failed" || svc != "active" || lease != nil || !current.Equal(due) || f.renews.Load() != wantRenew || f.renewalPays.Load() != 0 || autoRenewBalance(t, f.db, f.uid) != balance-2500 {
				t.Fatalf("known failure erased collection or spent upstream: status=%s service=%s lease=%v due=%s renew=%d pay=%d", status, svc, lease, current, f.renews.Load(), f.renewalPays.Load())
			}
			supplierRenewalEvents(t, f, sid, 0)
			for range 2 {
				if n, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || n != 0 {
					t.Errorf("known failed paid renewal counted again as successful automatic collection: count=%d err=%v", n, err)
				}
			}
			paid, unpaid, total := autoRenewInvoiceStats(t, f.db, sid)
			var jobs int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='renew'", sid).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if paid != 1 || unpaid != 0 || total != 1 || jobs != 1 || autoRenewBalance(t, f.db, f.uid) != balance-2500 || f.renews.Load() != wantRenew || f.renewalPays.Load() != 0 {
				t.Fatalf("known failure actually repeated funds or dispatch: paid=%d unpaid=%d total=%d jobs=%d", paid, unpaid, total, jobs)
			}
			var invoice int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT (request_payload->>'local_invoice_id')::bigint FROM provision_jobs WHERE id=$1", job.ID).Scan(&invoice); err != nil {
				t.Fatal(err)
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "known-renewal-failure-refund-"+stage), 201)
			var invoiceStatus, jobStatus string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT i.status,j.status FROM invoices i JOIN provision_jobs j ON j.id=$2 WHERE i.id=$1", invoice, job.ID).Scan(&invoiceStatus, &jobStatus); err != nil {
				t.Fatal(err)
			}
			current, _, _ = autoRenewState(t, f.db, sid)
			if invoiceStatus != "refunded" || jobStatus != "cancelled" || autoRenewBalance(t, f.db, f.uid) != balance || !current.Equal(due) || f.renews.Load() != wantRenew || f.renewalPays.Load() != 0 {
				t.Fatal("known failure could not refund original local collection", invoiceStatus, jobStatus, current)
			}
			supplierRenewalEvents(t, f, sid, 0)
		})
	}
}
