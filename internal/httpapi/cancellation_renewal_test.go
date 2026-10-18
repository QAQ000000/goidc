package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type cancellationRenewalFixture struct {
	db                         *store.Store
	h                          http.Handler
	userID, serviceID, adminID int64
	customer, admin            string
}

func newCancellationRenewalFixture(t *testing.T, modes ...string) cancellationRenewalFixture {
	t.Helper()
	db, h := testServerConfig(t, func(cfg *config.Config) {
		epay := epayTestConfig()
		cfg.EpayAPIURL, cfg.EpayPID, cfg.EpayKey, cfg.EpayTypes = epay.EpayAPIURL, epay.EpayPID, epay.EpayKey, epay.EpayTypes
	})
	adminID, admin := user(t, db, h, "cancel-renew-admin", "super_admin")
	userID, customer := user(t, db, h, "cancel-renew-customer", "customer")
	productID := product(t, db, 1)
	manual := len(modes) > 0 && modes[0] == "manual"
	if manual {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='manual' WHERE id=$1", productID); err != nil {
			t.Fatal(err)
		}
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", userID), map[string]any{"amount_cents": 100000}, admin, "cancel-renew-topup"), 201)
	version, itemID := add(t, h, customer, productID, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "cancel-renew-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "cancel-renew-initial-pay"), 201)
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatalf("claim initial provisioning: job=%v err=%v", job, err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	if manual {
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", job.ServiceID), map[string]any{"job_id": job.ID, "upstream_id": "manual-cancellation-resource", "username": "root", "note": "隔离资源开通核对完成"}, admin, ""), 200)
	}
	var serviceID int64
	if err = db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE user_id=$1 AND product_id=$2 AND status='active'", userID, productID).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE customer_credit_accounts SET limit_cents=100000 WHERE user_id=$1", userID); err != nil {
		t.Fatal(err)
	}
	return cancellationRenewalFixture{db: db, h: h, userID: userID, serviceID: serviceID, adminID: adminID, customer: customer, admin: admin}
}

func (f cancellationRenewalFixture) cancel(t *testing.T, kind string) int64 {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/cancellation", f.serviceID), map[string]string{"type": kind, "reason": "取消续费互斥回归"}, f.customer, "cancel-renew-request")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func (f cancellationRenewalFixture) renew(t *testing.T) int64 {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.serviceID), map[string]any{}, f.customer, "cancel-renew-invoice")
	expect(t, w, 201)
	return int64(data(t, w)["invoice_id"].(float64))
}

func (f cancellationRenewalFixture) approve(id int64) *httptest.ResponseRecorder {
	return request(f.h, "POST", fmt.Sprintf("/api/v1/admin/cancellations/%d/decision", id), map[string]string{"decision": "approved", "note": "核对账单后批准"}, f.admin, "")
}

type cancellationRenewalState struct {
	balance, creditUsed, creditVersion, balanceEntries, creditPayments, intents, renewals int64
	invoiceVersion                                                                        int64
	due                                                                                   time.Time
	serviceStatus, invoiceStatus                                                          string
}

func (f cancellationRenewalFixture) state(t *testing.T, invoiceID int64) cancellationRenewalState {
	t.Helper()
	var out cancellationRenewalState
	err := f.db.Pool.QueryRow(t.Context(), `SELECT u.balance_cents,a.used_cents,a.version,s.next_due_at,s.status,
 (SELECT count(*) FROM balance_ledger WHERE user_id=u.id),
 (SELECT count(*) FROM credit_invoice_payments WHERE user_id=u.id),
 (SELECT count(*) FROM payment_transactions WHERE user_id=u.id),
 (SELECT count(*) FROM invoice_items WHERE service_id=s.id AND item_type='renew'),
 coalesce((SELECT status FROM invoices WHERE id=$3),''),
 coalesce((SELECT version FROM invoices WHERE id=$3),0)
 FROM users u JOIN customer_credit_accounts a ON a.user_id=u.id JOIN services s ON s.user_id=u.id
 WHERE u.id=$1 AND s.id=$2`, f.userID, f.serviceID, invoiceID).Scan(&out.balance, &out.creditUsed, &out.creditVersion, &out.due, &out.serviceStatus, &out.balanceEntries, &out.creditPayments, &out.intents, &out.renewals, &out.invoiceStatus, &out.invoiceVersion)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (f cancellationRenewalFixture) unchanged(t *testing.T, invoiceID int64, before cancellationRenewalState) {
	t.Helper()
	if after := f.state(t, invoiceID); after != before {
		t.Fatalf("rejected operation changed financial/service state: before=%+v after=%+v", before, after)
	}
}

func cancellationRenewalConflict(t *testing.T, w *httptest.ResponseRecorder, code string) {
	t.Helper()
	expect(t, w, 409)
	if got := errorData(t, w)["code"]; got != code {
		t.Fatalf("error code=%v want=%s body=%s", got, code, w.Body.String())
	}
}

// Epay's registered factory constructs a signed checkout URL locally. This
// exercises a real (non-mock) pending intent and credential snapshot without
// calling a merchant or replacing a mock transaction's provider in SQL.
func (f cancellationRenewalFixture) intent(t *testing.T, invoiceID int64, provider string) (int64, url.Values) {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{"provider": provider, "expected_version": 1}, f.customer, "cancel-renew-online-intent")
	expect(t, w, 201)
	d := data(t, w)
	if d["status"] != "pending" || d["provider"] != provider {
		t.Fatalf("unexpected payment intent: %s", w.Body.String())
	}
	paymentID := int64(d["payment_id"].(float64))
	if provider != "mock" {
		var snapshotBytes int
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT octet_length(credential_snapshot) FROM payment_transactions WHERE id=$1 AND provider=$2 AND status='pending'", paymentID, provider).Scan(&snapshotBytes); err != nil || snapshotBytes == 0 {
			t.Fatalf("real intent lacks encrypted merchant snapshot: bytes=%d err=%v", snapshotBytes, err)
		}
	}
	checkout, err := url.Parse(d["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	return paymentID, checkout.Query()
}

func (f cancellationRenewalFixture) assertApprovalJob(t *testing.T, kind string, w *httptest.ResponseRecorder, due time.Time, earliest time.Time) {
	t.Helper()
	expect(t, w, 200)
	d := data(t, w)
	if d["status"] != "approved" || d["job_id"] == nil {
		t.Fatalf("approval did not return terminate job: %s", w.Body.String())
	}
	var available time.Time
	var action, status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT action,status,available_at FROM provision_jobs WHERE id=$1 AND service_id=$2", int64(d["job_id"].(float64)), f.serviceID).Scan(&action, &status, &available); err != nil {
		t.Fatal(err)
	}
	if action != "terminate" || status != "pending" {
		t.Fatalf("incorrect cancellation job: action=%s status=%s", action, status)
	}
	if kind == "end_of_billing" {
		if !available.Equal(due) {
			t.Fatalf("termination still uses an old due date: available=%s latest due=%s", available, due)
		}
		if _, err := f.db.ClaimProvisionJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("future termination was claimable before latest paid due: %v", err)
		}
	} else if available.Before(earliest.Add(-time.Second)) || available.After(time.Now().Add(time.Second)) {
		t.Fatalf("immediate cancellation was not scheduled now: available=%s earliest=%s", available, earliest)
	}
}

func TestCancellationRenewalPendingAllowsPaymentAndApprovalUsesLatestDue(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		t.Run(kind, func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			before := f.state(t, 0)
			cancellationID := f.cancel(t, kind)
			invoiceID := f.renew(t)
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, f.customer, "cancel-renew-pending-pay"), 201)
			after := f.state(t, invoiceID)
			if !after.due.After(before.due) || after.balance != before.balance-1234 || after.invoiceStatus != "paid" {
				t.Fatalf("pending cancellation incorrectly blocked renewal: before=%+v after=%+v", before, after)
			}
			var state string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM service_cancellation_requests WHERE id=$1", cancellationID).Scan(&state); err != nil || state != "pending" {
				t.Fatalf("renewal resolved the cancellation: status=%s err=%v", state, err)
			}
			started := time.Now()
			f.assertApprovalJob(t, kind, f.approve(cancellationID), after.due, started)
		})
	}
}

func TestCancellationRenewalApprovedRejectsNewInvoiceAndAllPaymentMethods(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		t.Run(kind, func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			invoiceID := f.renew(t)
			cancellationID := f.cancel(t, kind)
			expect(t, f.approve(cancellationID), 200)
			before := f.state(t, invoiceID)
			if before.invoiceStatus != "unpaid" {
				t.Fatalf("fixture renewal is not unpaid: %+v", before)
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.serviceID), map[string]any{}, f.customer, "cancel-renew-after-approval")
			cancellationRenewalConflict(t, w, "service_cancellation_approved")
			f.unchanged(t, invoiceID, before)
			for _, method := range []struct {
				name, suffix string
				body         map[string]any
			}{
				{"balance", "pay", map[string]any{"expected_version": 1}},
				{"credit", "pay-credit", map[string]any{"expected_version": 1}},
				{"online", "payments", map[string]any{"provider": "epay_alipay", "expected_version": 1}},
			} {
				t.Run(method.name, func(t *testing.T) {
					w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/%s", invoiceID, method.suffix), method.body, f.customer, "cancel-renew-blocked-"+method.name)
					cancellationRenewalConflict(t, w, "service_cancellation_approved")
					f.unchanged(t, invoiceID, before)
				})
			}
		})
	}
}

func TestCancellationRenewalApprovalRejectsUnresolvedPayment(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		for _, paymentState := range []string{"real_pending", "real_expired", "close_checking", "close_unknown"} {
			t.Run(kind+"/"+paymentState, func(t *testing.T) {
				f := newCancellationRenewalFixture(t)
				invoiceID := f.renew(t)
				cancellationID := f.cancel(t, kind)
				provider := "epay_alipay"
				if strings.HasPrefix(paymentState, "close_") {
					provider = "mock"
				}
				paymentID, _ := f.intent(t, invoiceID, provider)
				if paymentState == "real_expired" {
					if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", paymentID); err != nil {
						t.Fatal(err)
					}
					if n, err := f.db.ExpirePaymentIntents(t.Context()); err != nil || n != 1 {
						t.Fatalf("expire real pending payment: n=%d err=%v", n, err)
					}
					var status string
					if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM payment_transactions WHERE id=$1", paymentID).Scan(&status); err != nil || status != "expired" {
						t.Fatalf("real payment did not expire: status=%s err=%v", status, err)
					}
				}
				if strings.HasPrefix(paymentState, "close_") {
					// Mock intents alone do not block approval. Reserve a real
					// close operation to isolate the unresolved-close admission.
					hash := tokenHash(strings.TrimPrefix(f.admin, "zjmfgo_session="))
					op, err := f.db.BeginPaymentClose(t.Context(), f.adminID, invoiceID, paymentID, 1, "cancel-renew-close-reservation", hash)
					if err != nil {
						t.Fatal(err)
					}
					if paymentState == "close_unknown" {
						if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
							t.Fatal(err)
						}
					}
					var unresolved bool
					var status string
					if err = f.db.Pool.QueryRow(t.Context(), "SELECT status,payment_close_operation_unresolved(id) FROM payment_close_operations WHERE id=$1", op.ID).Scan(&status, &unresolved); err != nil || !unresolved || status != strings.TrimPrefix(paymentState, "close_") {
						t.Fatalf("close fixture is not unresolved: status=%s unresolved=%v err=%v", status, unresolved, err)
					}
				}
				before := f.state(t, invoiceID)
				cancellationRenewalConflict(t, f.approve(cancellationID), "invoice_payment_pending")
				f.unchanged(t, invoiceID, before)
				var state string
				var jobs int
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT cr.status,(SELECT count(*) FROM provision_jobs WHERE service_id=cr.service_id AND action='terminate') FROM service_cancellation_requests cr WHERE id=$1`, cancellationID).Scan(&state, &jobs); err != nil || state != "pending" || jobs != 0 {
					t.Fatalf("rejected approval mutated cancellation: status=%s jobs=%d err=%v", state, jobs, err)
				}
			})
		}
	}
}

func TestCancellationRenewalCallbackBeforeApprovalUsesLatestDue(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		t.Run(kind, func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			before := f.state(t, 0)
			invoiceID := f.renew(t)
			cancellationID := f.cancel(t, kind)
			_, checkout := f.intent(t, invoiceID, "epay_alipay")
			cancellationRenewalConflict(t, f.approve(cancellationID), "invoice_payment_pending")
			callback := epayNotification(checkout, "cancel-renew-settled-trade")
			for _, method := range []string{"POST", "GET"} {
				w := epayCallback(f.h, method, "epay_alipay", callback)
				if w.Code != 200 || w.Body.String() != "success" {
					t.Fatalf("signed renewal callback failed: status=%d body=%s", w.Code, w.Body.String())
				}
			}
			after := f.state(t, invoiceID)
			if after.invoiceStatus != "paid" || !after.due.After(before.due) || after.balance != before.balance || after.creditUsed != before.creditUsed {
				t.Fatalf("online settlement did not extend service without local charge: before=%+v after=%+v", before, after)
			}
			started := time.Now()
			f.assertApprovalJob(t, kind, f.approve(cancellationID), after.due, started)
		})
	}
}

func TestCancellationRenewalApprovedPreventsAutomaticCharge(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		t.Run(kind, func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			autoRenewArm(t, f.db, f.h, f.customer, f.serviceID, 60)
			cancellationID := f.cancel(t, kind)
			expect(t, f.approve(cancellationID), 200)
			before := f.state(t, 0)
			for pass := 0; pass < 2; pass++ {
				if processed, err := f.db.ProcessAutoRenewals(t.Context(), 20); err != nil || processed != 0 {
					t.Fatalf("approved cancellation was automatically renewed: processed=%d err=%v", processed, err)
				}
				f.unchanged(t, 0, before)
			}
		})
	}
}

func TestCancellationRenewalExecutionFences(t *testing.T) {
	for _, mode := range []string{"simulation", "nokvm"} {
		t.Run(mode+"_reschedules_extended_entitlement", func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			// Model an old already-due task and a later entitlement correction.
			// No payment fence is bypassed and no job scheduling time is edited.
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=clock_timestamp()-interval '1 minute' WHERE id=$1", f.serviceID); err != nil {
				t.Fatal(err)
			}
			cancellationID := f.cancel(t, "end_of_billing")
			approved := f.approve(cancellationID)
			expect(t, approved, 200)
			jobID := int64(data(t, approved)["job_id"].(float64))
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=clock_timestamp()+interval '1 month',provision_mode=$2,delivery_snapshot='{}'::jsonb WHERE id=$1", f.serviceID, mode); err != nil {
				t.Fatal(err)
			}
			// For NOKVM the fixture deliberately has neither an adapter nor a
			// valid delivery snapshot: postponement must precede their parsing.
			before := f.state(t, 0)
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil || job.ID != jobID || job.Action != "terminate" {
				t.Fatalf("claim original due cancellation job: job=%+v err=%v", job, err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatalf("extended entitlement was not postponed before delivery: %v", err)
			}
			f.unchanged(t, 0, before)
			var available time.Time
			var state string
			var dispatched, released bool
			var stock, audits int
			err = f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.available_at,j.external_started_at IS NOT NULL,s.inventory_released,p.stock,
 (SELECT count(*) FROM audit_events WHERE action='service.cancellation.execution_rescheduled' AND payload->>'job_id'=$2)
 FROM provision_jobs j JOIN services s ON s.id=j.service_id JOIN products p ON p.id=s.product_id WHERE j.id=$1`, jobID, fmt.Sprint(jobID)).Scan(&state, &available, &dispatched, &released, &stock, &audits)
			if err != nil || state != "pending" || !available.Equal(before.due) || dispatched || released || stock != 0 || audits != 1 {
				t.Fatalf("postponement did not preserve resource/dispatch/audit: state=%s available=%s due=%s dispatched=%v released=%v stock=%d audits=%d err=%v", state, available, before.due, dispatched, released, stock, audits, err)
			}
			if _, err = f.db.ClaimProvisionJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("postponed cancellation was immediately claimable: %v", err)
			}
		})
	}
	for _, original := range []string{"active", "suspended"} {
		t.Run("manual_confirmation_commits_blocked_job_and_audit/"+original, func(t *testing.T) {
			f := newCancellationRenewalFixture(t, "manual")
			if original == "suspended" {
				w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/actions", f.serviceID), map[string]string{"action": "suspend"}, f.customer, "cancel-renew-manual-suspend")
				expect(t, w, 202)
				job, err := f.db.ClaimProvisionJob(t.Context())
				if err != nil || job == nil || job.Action != "suspend" {
					t.Fatalf("claim manual suspension: job=%+v err=%v", job, err)
				}
				if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
					t.Fatal(err)
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.serviceID), map[string]any{"job_id": job.ID, "note": "人工核实资源已经暂停"}, f.admin, ""), 200)
			}
			before := f.state(t, 0)
			if before.serviceStatus != original {
				t.Fatalf("incorrect original manual resource status: got=%s want=%s", before.serviceStatus, original)
			}
			cancellationID := f.cancel(t, "immediate")
			approved := f.approve(cancellationID)
			expect(t, approved, 200)
			jobID := int64(data(t, approved)["job_id"].(float64))
			job, err := f.db.ClaimProvisionJob(t.Context())
			if err != nil || job == nil || job.ID != jobID {
				t.Fatalf("claim manual cancellation: job=%+v err=%v", job, err)
			}
			if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
				t.Fatal(err)
			}
			var state string
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM provision_jobs WHERE id=$1", jobID).Scan(&state); err != nil || state != "waiting_manual" {
				t.Fatalf("manual cancellation did not await confirmation: status=%s err=%v", state, err)
			}
			// Waiting for an operator is not proof of a resource state change.
			// Both active and suspended must survive cancellation reconsideration.
			f.unchanged(t, 0, before)
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE service_cancellation_requests SET status='pending',resolved_at=NULL,resolved_by=NULL WHERE id=$1", cancellationID); err != nil {
				t.Fatal(err)
			}
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/services/%d/delivery", f.serviceID), map[string]any{"job_id": jobID, "note": "确认前发现取消已改审"}, f.admin, "")
			cancellationRenewalConflict(t, w, "cancellation_job_blocked")
			f.unchanged(t, 0, before)
			var completed, released, dispatched bool
			var stock, audits int
			err = f.db.Pool.QueryRow(t.Context(), `SELECT j.status,j.completed_at IS NOT NULL,j.external_started_at IS NOT NULL,s.inventory_released,p.stock,
 (SELECT count(*) FROM audit_events WHERE action='service.cancellation.execution_cancelled' AND payload->>'job_id'=$2 AND payload->>'reason'='cancellation_not_approved')
 FROM provision_jobs j JOIN services s ON s.id=j.service_id JOIN products p ON p.id=s.product_id WHERE j.id=$1`, jobID, fmt.Sprint(jobID)).Scan(&state, &completed, &dispatched, &released, &stock, &audits)
			if err != nil || state != "cancelled" || !completed || dispatched || released || stock != 0 || audits != 1 {
				t.Fatalf("409 rolled back protection or changed resource: state=%s completed=%v dispatched=%v released=%v stock=%d audits=%d err=%v", state, completed, dispatched, released, stock, audits, err)
			}
		})
	}
}

func TestCancellationRenewalConcurrentApprovalAndPayment(t *testing.T) {
	for _, kind := range []string{"immediate", "end_of_billing"} {
		t.Run(kind, func(t *testing.T) {
			f := newCancellationRenewalFixture(t)
			invoiceID := f.renew(t)
			cancellationID := f.cancel(t, kind)
			before := f.state(t, invoiceID)
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			base := f.h
			f.h = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				base.ServeHTTP(w, r.WithContext(ctx))
			})
			// Reserve the observer before the barrier and concurrent handlers
			// acquire connections. Observation must not exhaust the same pool.
			observer, err := f.db.Pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer observer.Release()
			barrier, err := f.db.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer barrier.Rollback(context.Background())
			var barrierPID int
			if err = barrier.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&barrierPID); err != nil {
				t.Fatal(err)
			}
			if _, err = barrier.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.serviceID); err != nil {
				t.Fatal(err)
			}
			started := time.Now()
			approval := make(chan *httptest.ResponseRecorder, 1)
			go func() { approval <- f.approve(cancellationID) }()
			var approvalPID int
			ticker := time.NewTicker(10 * time.Millisecond)
			defer ticker.Stop()
			for approvalPID == 0 {
				err = observer.QueryRow(ctx, `SELECT coalesce(min(pid),0) FROM pg_stat_activity
 WHERE datname=current_database() AND $1=ANY(pg_blocking_pids(pid))`, barrierPID).Scan(&approvalPID)
				if err != nil {
					t.Fatal("approval lock observation failed", err)
				}
				if approvalPID == 0 {
					select {
					case result := <-approval:
						t.Fatalf("approval returned without reaching the service lock: status=%d body=%s", result.Code, result.Body.String())
					case <-ctx.Done():
						t.Fatal("approval did not reach the service barrier", ctx.Err())
					case <-ticker.C:
					}
				}
			}
			for _, method := range []struct {
				name, suffix string
				body         map[string]any
			}{
				{"balance", "pay", map[string]any{"expected_version": 1}},
				{"credit", "pay-credit", map[string]any{"expected_version": 1}},
				{"online", "payments", map[string]any{"provider": "epay_alipay", "expected_version": 1}},
			} {
				w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/%s", invoiceID, method.suffix), method.body, f.customer, "cancel-renew-concurrent-"+method.name)
				cancellationRenewalConflict(t, w, "service_action_pending")
				f.unchanged(t, invoiceID, before)
				var stillBlocked bool
				if err = observer.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE pid=$1 AND $2=ANY(pg_blocking_pids(pid)))`, approvalPID, barrierPID).Scan(&stillBlocked); err != nil || !stillBlocked {
					t.Fatalf("payment did not overlap the blocked approval: method=%s blocked=%v err=%v", method.name, stillBlocked, err)
				}
			}
			if err = barrier.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			select {
			case w := <-approval:
				f.assertApprovalJob(t, kind, w, before.due, started)
			case <-ctx.Done():
				t.Fatal("approval did not finish after barrier release", ctx.Err())
			}
			f.unchanged(t, invoiceID, before)
		})
	}
}
