package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func closePath(invoice, paymentID int64) string {
	return fmt.Sprintf("/api/v1/admin/invoices/%d/payments/%d/close", invoice, paymentID)
}
func closeActor(t *testing.T, f upgradeFixture, cookie string) (int64, string) {
	t.Helper()
	hash := tokenHash(cookieValue(cookie))
	var id int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT user_id FROM sessions WHERE token_hash=$1`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id, hash
}
func closeFixture(t *testing.T, provider string) (upgradeFixture, int64, int64, string) {
	t.Helper()
	f := configurationUpgradeFixture(t, false)
	if strings.HasPrefix(provider, "epay_") {
		f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	}
	invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "close-invoice")
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": provider, "expected_version": 1}, f.customer, "close-payment-intent")
	expect(t, w, 201)
	d := data(t, w)
	return f, invoice, int64(d["payment_id"].(float64)), d["provider_payment_id"].(string)
}
func closeVerified(op store.PaymentCloseOperation) payment.CloseResult {
	return payment.CloseResult{ProviderPaymentID: op.ProviderPaymentID, AmountCents: op.AmountCents, OperationID: op.OperationID, Status: "closed"}
}
func closeState(t *testing.T, f upgradeFixture, invoice, pid int64, wantPayment string, wantVersion int64) {
	t.Helper()
	var p, i string
	var v, ledger int64
	err := f.db.Pool.QueryRow(t.Context(), `SELECT p.status,i.status,i.version,(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=i.id) FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1 AND i.id=$2`, pid, invoice).Scan(&p, &i, &v, &ledger)
	if err != nil || p != wantPayment || i != "unpaid" || v != wantVersion || ledger != 0 {
		t.Fatalf("state p=%s i=%s version=%d ledger=%d err=%v", p, i, v, ledger, err)
	}
}

func TestPaymentCloseHTTPClosedReplayAndNewIntent(t *testing.T) {
	f, invoice, pid, _ := closeFixture(t, "mock")
	path := closePath(invoice, pid)
	body := map[string]any{"expected_version": 1}
	unauthenticated := request(f.h, "POST", path, body, "", "close-unauthenticated")
	expect(t, unauthenticated, 401)
	if unauthenticated.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("unauthenticated close response is cacheable")
	}
	expect(t, request(f.h, "POST", path, body, f.customer, "close-customer-denied"), 401)
	expect(t, request(f.h, "POST", path, map[string]any{"expected_version": 99}, f.root, "close-stale-invoice"), 409)
	expect(t, request(f.h, "POST", path, map[string]any{"expected_version": 1, "amount_cents": 1}, f.root, "close-extra-amount"), 400)
	w := request(f.h, "POST", path, body, f.root, "close-terminal-request")
	expect(t, w, 200)
	d := data(t, w)
	if d["status"] != "closed" || d["version"] != float64(2) {
		t.Fatal("incorrect terminal response", w.Body.String())
	}
	closeState(t, f, invoice, pid, "closed", 2)
	replay := request(f.h, "POST", path, body, f.root, "close-terminal-request")
	expect(t, replay, 200)
	if data(t, replay)["status"] != "closed" || data(t, replay)["replayed"] != true || data(t, replay)["operation_id"] != d["operation_id"] {
		t.Fatal("unstable replay", replay.Body.String())
	}
	expect(t, request(f.h, "POST", path, map[string]any{"expected_version": 2}, f.root, "close-terminal-request"), 409)
	var count, audits int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM payment_close_operations),(SELECT count(*) FROM audit_events WHERE action='payment.intent.closed')`).Scan(&count, &audits); err != nil || count != 1 || audits != 1 {
		t.Fatal(count, audits, err)
	}
	read := request(f.h, "GET", path, nil, f.root, "")
	expect(t, read, 200)
	if data(t, read)["status"] != "closed" || data(t, read)["close_supported"] != true || data(t, read)["operations_total"] != float64(1) || read.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(read.Body.String())
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 2}, f.customer, "close-replacement-intent"), 201)
	for _, sql := range []string{`UPDATE payment_close_operations SET result_code='tampered'`, `DELETE FROM payment_close_operations`} {
		if _, err := f.db.Pool.Exec(t.Context(), sql); err == nil {
			t.Fatal("close evidence mutable", sql)
		}
	}
}

func TestPaymentCloseEpayUnavailableEvenExpired(t *testing.T) {
	f, invoice, pid, _ := closeFixture(t, "epay_alipay")
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE payment_transactions SET status='expired',expires_at=clock_timestamp()-interval '1 day' WHERE id=$1`, pid); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", closePath(invoice, pid), map[string]any{"expected_version": 1}, f.root, "close-epay-not-supported")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "payment_close_unavailable" {
		t.Fatal(w.Body.String())
	}
	closeState(t, f, invoice, pid, "expired", 1)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{"expected_version": 1}, f.customer, "close-expired-balance"), 409)
	read := request(f.h, "GET", closePath(invoice, pid), nil, f.root, "")
	expect(t, read, 200)
	if data(t, read)["close_supported"] != false {
		t.Fatal("epay claimed trusted close", read.Body.String())
	}
}

func TestPaymentCloseUnknownFreezesAndPreservesOriginalRequest(t *testing.T) {
	f, invoice, pid, _ := closeFixture(t, "mock")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-unknown-original", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
		t.Fatal(err)
	}
	replay, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-unknown-original", hash)
	if err != nil || !replay.Replayed || replay.Status != "unknown" || replay.OperationID != op.OperationID {
		t.Fatal(replay, err)
	}
	_, err = f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-unknown-new-key", hash)
	var c *store.Conflict
	if !errors.As(err, &c) || c.Code != "payment_close_in_progress" {
		t.Fatal("unknown resent", err)
	}
	w := request(f.h, "POST", closePath(invoice, pid), map[string]any{"expected_version": 1}, f.root, "close-unknown-original")
	expect(t, w, 502)
	closeState(t, f, invoice, pid, "pending", 1)
}

func TestPaymentCloseUnknownBlocksFundsAndCancelledLateAnomalyBlocksDelete(t *testing.T) {
	for _, mode := range []string{"unknown", "cancelled_late"} {
		t.Run(mode, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			order, invoice := f.order(t, "close-protected-order", 1)
			w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "close-protected-intent")
			expect(t, w, 201)
			pid := int64(data(t, w)["payment_id"].(float64))
			reference := data(t, w)["provider_payment_id"].(string)
			hash := tokenHash(cookieValue(f.root))
			var actor int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1", hash).Scan(&actor); err != nil {
				t.Fatal(err)
			}
			op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-protected-operation", hash)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "unknown" {
				if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
					t.Fatal(err)
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{"expected_version": 1}, f.customer, "close-protected-balance"), 409)
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/cancel", order), map[string]any{"expected_version": orderVersion(t, f.db, order)}, f.root, "close-protected-cancel"), 409)
			} else {
				if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
					t.Fatal(err)
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/cancel", order), map[string]any{"expected_version": orderVersion(t, f.db, order)}, f.root, "close-protected-cancel"), 201)
				if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "close-cancelled-late-success", reference, invoice, op.AmountCents, "succeeded", []byte(`{"late":true}`), "late:test"); err != nil {
					t.Fatal(err)
				}
			}
			expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/orders/%d", order), map[string]any{"expected_version": orderVersion(t, f.db, order), "reason": "attempt hide frozen payment"}, f.root, "close-protected-delete"), 409)
			var deleted bool
			if err = f.db.Pool.QueryRow(t.Context(), "SELECT deleted_at IS NOT NULL FROM orders WHERE id=$1", order).Scan(&deleted); err != nil || deleted {
				t.Fatal("frozen order hidden", deleted, err)
			}
		})
	}
}

func TestPaymentCloseHTTPReadWriteScopeAndBearerBoundaries(t *testing.T) {
	f, invoice, pid, _ := closeFixture(t, "mock")
	staffID, staff := user(t, f.db, f.h, "close-http-employee", "admin")
	path := closePath(invoice, pid)
	body := map[string]any{"expected_version": 1}
	setPermissions := func(permissions []string) {
		t.Helper()
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=$1 WHERE name='close-http-employee-role'", permissions); err != nil {
			t.Fatal(err)
		}
	}
	setPermissions([]string{"invoices.view"})
	expect(t, request(f.h, "GET", path, nil, staff, ""), 200)
	expect(t, request(f.h, "POST", path, body, staff, "close-http-no-close-permission"), 403)
	setPermissions([]string{})
	expect(t, request(f.h, "GET", path, nil, staff, ""), 403)
	setPermissions([]string{"invoices.view", "payments.close"})
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST"} {
		w := request(f.h, method, path, func() any {
			if method == "GET" {
				return nil
			}
			return body
		}(), staff, "close-http-outside-scope")
		if w.Code != 403 && w.Code != 404 {
			t.Fatalf("out of scope %s status=%d body=%s", method, w.Code, w.Body.String())
		}
		bearer := requestBearer(f.h, method, path, body, "zjmfgo_"+strings.Repeat("a", 64))
		expect(t, bearer, 403)
		if errorData(t, bearer)["code"] != "session_required" {
			t.Fatal("Bearer did not reach session-only rejection", bearer.Body.String())
		}
	}
	closeState(t, f, invoice, pid, "pending", 1)
}

func TestPaymentCloseRechecksRevocationScopeAndSession(t *testing.T) {
	for _, mode := range []string{"permission", "session", "scope", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, pid, _ := closeFixture(t, "mock")
			staffID, staff := user(t, f.db, f.h, "close-employee", "admin")
			_, hash := closeActor(t, f, staff)
			op, err := f.db.BeginPaymentClose(t.Context(), staffID, invoice, pid, 1, "close-authority-original", hash)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "permission":
				_, err = f.db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='close-employee-role'`)
			case "session":
				_, err = f.db.Pool.Exec(t.Context(), `DELETE FROM sessions WHERE token_hash=$1`, hash)
			case "scope":
				_, err = f.db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, staffID)
			case "disabled":
				_, err = f.db.Pool.Exec(t.Context(), `UPDATE administrators SET status='disabled' WHERE id=$1`, staffID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err == nil {
				t.Fatal("closed after authority revoked")
			}
			if _, err = f.db.BeginPaymentClose(t.Context(), staffID, invoice, pid, 1, "close-authority-original", hash); err == nil {
				t.Fatal("replayed after authority revoked")
			}
			closeState(t, f, invoice, pid, "pending", 1)
		})
	}
}

func TestPaymentCloseConcurrentReservationAndCallbackPriority(t *testing.T) {
	f, invoice, pid, reference := closeFixture(t, "mock")
	actor, hash := closeActor(t, f, f.root)
	ctx, cancel := context.WithTimeout(t.Context(), 10e9)
	defer cancel()
	var wg sync.WaitGroup
	results := make(chan error, 8)
	ops := make(chan store.PaymentCloseOperation, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			op, err := f.db.BeginPaymentClose(ctx, actor, invoice, pid, 1, fmt.Sprintf("close-concurrent-%d", i), hash)
			if err == nil {
				ops <- op
			}
			results <- err
		}(i)
	}
	wg.Wait()
	close(results)
	close(ops)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else {
			var c *store.Conflict
			if !errors.As(err, &c) || c.Code != "payment_close_in_progress" {
				t.Fatal(err)
			}
		}
	}
	if success != 1 {
		t.Fatal("multiple remote close reservations", success)
	}
	op := <-ops
	if _, err := f.db.ProcessPaymentWebhook(ctx, "mock", "close-callback-first", reference, invoice, 3000, "succeeded", []byte(`{"status":"succeeded"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.CompletePaymentClose(ctx, op, closeVerified(op), hash); err == nil {
		t.Fatal("closed succeeded receipt")
	}
	var p, i, opState string
	if err := f.db.Pool.QueryRow(ctx, `SELECT p.status,i.status,c.status FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id JOIN payment_close_operations c ON c.payment_id=p.id WHERE p.id=$1`, pid).Scan(&p, &i, &opState); err != nil || p != "succeeded" || i != "paid" || opState != "succeeded" {
		t.Fatal(p, i, opState, err)
	}
}

func TestPaymentCloseLateSuccessCreatesFrozenMoneyFact(t *testing.T) {
	f, invoice, pid, reference := closeFixture(t, "epay_alipay")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-local-trusted-fixture", hash)
	if err != nil {
		t.Fatal(err)
	}
	// Direct store fixture supplies trusted adapter output; production Epay
	// does not implement CloseGateway and cannot reach this branch via HTTP.
	if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "close-late-paid", reference, invoice, 3000, "succeeded", []byte(`{"fixture":"late-paid"}`), "merchant:late-paid"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "close-late-paid", reference, invoice, 3000, "succeeded", []byte(`{"fixture":"late-paid"}`), "merchant:late-paid"); err != nil {
		t.Fatal(err)
	}
	closeState(t, f, invoice, pid, "closed", 2)
	var anomalies, amount, audit int64
	var eventState string
	if err = f.db.Pool.QueryRow(t.Context(), `SELECT count(*),sum(amount_cents),(SELECT count(*) FROM audit_events WHERE action='payment.webhook.ignored_after_close'),(SELECT status FROM payment_webhook_events WHERE event_id='close-late-paid') FROM payment_close_anomalies`).Scan(&anomalies, &amount, &audit, &eventState); err != nil || anomalies != 1 || amount != 3000 || audit != 1 || eventState != "ignored" {
		t.Fatal(anomalies, amount, audit, eventState, err)
	}
	for _, suffix := range []string{"pay", "pay-credit", "payments"} {
		body := map[string]any{"expected_version": 2}
		if suffix == "payments" {
			body["provider"] = "epay_alipay"
		}
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/%s", invoice, suffix), body, f.customer, "close-late-protected-"+suffix), 409)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/cancel", invoice), map[string]any{"expected_version": 2, "reason": "attempt cancel anomaly"}, f.root, "close-late-cancel"), 409)
	read := request(f.h, "GET", closePath(invoice, pid), nil, f.root, "")
	expect(t, read, 200)
	if !strings.Contains(read.Body.String(), "pending_verification") || strings.Contains(read.Body.String(), "integration-epay-secret") {
		t.Fatal(read.Body.String())
	}
	if _, err = f.db.Pool.Exec(t.Context(), `DELETE FROM payment_close_anomalies`); err == nil {
		t.Fatal("money anomaly mutable")
	}
}

func TestPaymentCloseAuditRollbackAndInvalidLease(t *testing.T) {
	for _, mode := range []string{"audit", "lease", "amount", "operation", "pending", "version"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, pid, _ := closeFixture(t, "mock")
			actor, hash := closeActor(t, f, f.root)
			op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-invalid-result", hash)
			if err != nil {
				t.Fatal(err)
			}
			result := closeVerified(op)
			version := int64(1)
			switch mode {
			case "audit":
				_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION close_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.intent.closed' THEN RAISE EXCEPTION 'fixture'; END IF; RETURN NEW; END $$; CREATE TRIGGER close_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION close_reject_audit()`)
			case "lease":
				_, err = f.db.Pool.Exec(t.Context(), `UPDATE payment_close_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, op.ID)
			case "amount":
				result.AmountCents++
			case "operation":
				result.OperationID += "-forged"
			case "pending":
				result.Status = "pending"
			case "version":
				_, err = f.db.Pool.Exec(t.Context(), `UPDATE invoices SET updated_at=clock_timestamp() WHERE id=$1`, invoice)
				version = 2
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.CompletePaymentClose(t.Context(), op, result, hash); err == nil {
				t.Fatal("invalid result unlocked")
			}
			closeState(t, f, invoice, pid, "pending", version)
		})
	}
}

func TestPaymentCloseRawDatabaseClosedRejected(t *testing.T) {
	f, invoice, pid, _ := closeFixture(t, "mock")
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE payment_transactions SET status='closed' WHERE id=$1`, pid); err == nil {
		t.Fatal("unverified direct close allowed")
	}
	closeState(t, f, invoice, pid, "pending", 1)
}

func TestPaymentCloseTopupReservationReuseCancelAndLateAnomaly(t *testing.T) {
	for _, mode := range []string{"reuse", "cancel", "late"} {
		t.Run(mode, func(t *testing.T) {
			f := onlineTopupFixture(t)
			invoice := onlineTopupCreate(t, f, "close-topup-create")
			checkout := epayIntent(t, f, invoice, "epay_alipay", "close-topup-first-intent")
			var pid int64
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_transactions WHERE invoice_id=$1`, invoice).Scan(&pid); err != nil {
				t.Fatal(err)
			}
			actor, hash := closeActor(t, f, f.root)
			op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-topup-local-proof", hash)
			if err != nil {
				t.Fatal(err)
			}
			var acceptedBefore string
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT accepted_settings::text FROM balance_topups WHERE invoice_id=$1`, invoice).Scan(&acceptedBefore); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
				t.Fatal(err)
			}
			read := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, "")
			expect(t, read, 200)
			if data(t, read)["topup_can_retry"] != true {
				t.Fatal("closed accepted topup not retryable", read.Body.String())
			}
			// Policies changed after acceptance do not rewrite/duplicate the existing
			// capacity reservation. A valid customer session is still mandatory.
			if _, err = f.db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=false,min_topup_cents=9999,max_topup_cents=10000,max_balance_cents=1,require_active_order=true,version=version+1`); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "reuse":
				intent := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "epay_alipay", "expected_version": 2}, f.customer, "close-topup-second-intent")
				expect(t, intent, 201)
				var snapshot string
				var held int64
				if err = f.db.Pool.QueryRow(t.Context(), `SELECT accepted_settings::text,(SELECT sum(amount_cents) FROM balance_topups WHERE user_id=$2 AND status='accepted') FROM balance_topups WHERE invoice_id=$1`, invoice, f.uid).Scan(&snapshot, &held); err != nil || snapshot != acceptedBefore || held != 1234 {
					t.Fatal(snapshot, held, err)
				}
				if _, err = f.db.ProcessPaymentWebhook(t.Context(), "epay_alipay", "close-topup-new-paid", data(t, intent)["provider_payment_id"].(string), invoice, 1234, "succeeded", []byte(`{"event":"new-paid"}`), "new-merchant-trade"); err != nil {
					t.Fatal(err)
				}
				var credits int
				if err = f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM balance_ledger WHERE reference_type='gateway_topup' AND reference_id=$1`, invoice).Scan(&credits); err != nil || credits != 1 {
					t.Fatal(credits, err)
				}
			case "cancel":
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/balance/topups/%d/cancel", invoice), map[string]any{"expected_version": 2, "note": "trusted upstream close"}, f.customer, "close-topup-cancel-safe"), 200)
				var state, snapshot string
				var accepted bool
				if err = f.db.Pool.QueryRow(t.Context(), `SELECT status,accepted_settings::text,accepted_at IS NOT NULL FROM balance_topups WHERE invoice_id=$1`, invoice).Scan(&state, &snapshot, &accepted); err != nil || state != "cancelled" || !accepted || snapshot != acceptedBefore {
					t.Fatal(state, snapshot, accepted, err)
				}
			case "late":
				callback := epayNotification(checkout, "topup-after-trusted-close")
				expect(t, epayCallback(f.h, "POST", "epay_alipay", callback), 200)
				read = request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, "")
				expect(t, read, 200)
				if data(t, read)["topup_can_retry"] != false || data(t, read)["payment_close_anomaly"] != true {
					t.Fatal("late money did not freeze topup", read.Body.String())
				}
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/balance/topups/%d/cancel", invoice), map[string]any{"expected_version": 2, "note": "cannot cancel paid anomaly"}, f.customer, "close-topup-cancel-anomaly"), 409)
			}
		})
	}
}

func TestPaymentCloseActiveQueryAndPaidProviderResultsDoNotUnlock(t *testing.T) {
	for _, mode := range []string{"query", "paid"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, pid, _ := closeFixture(t, "epay_alipay")
			actor, hash := closeActor(t, f, f.root)
			if mode == "query" {
				if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id) VALUES($1,$2)`, pid, actor); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-active-query-denied", hash); err == nil {
					t.Fatal("active query did not block close")
				}
			} else {
				op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-provider-already-paid", hash)
				if err != nil {
					t.Fatal(err)
				}
				result := closeVerified(op)
				result.Status = "succeeded"
				result.ExternalTransactionID = "provider-already-paid"
				if _, err = f.db.CompletePaymentClose(t.Context(), op, result, hash); err == nil {
					t.Fatal("provider paid result unlocked")
				}
			}
			closeState(t, f, invoice, pid, "pending", 1)
		})
	}
}
