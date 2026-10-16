package httpapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func verificationPath(invoice, pid int64) string {
	return strings.TrimSuffix(closePath(invoice, pid), "/close") + "/close-verifications"
}

type closeVerificationFixture struct {
	upgradeFixture
	invoice, pid       int64
	reference          string
	operation          store.PaymentCloseOperation
	verifier, reviewer string
}

func newCloseVerificationFixture(t *testing.T, record bool) closeVerificationFixture {
	t.Helper()
	f, invoice, pid, reference := closeFixture(t, "mock")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-verify-original", hash)
	if err != nil {
		t.Fatal(err)
	}
	if record {
		g := payment.MockGateway{Secret: []byte("integration-payment-secret")}
		if _, err = g.Close(t.Context(), payment.CloseRequest{ProviderPaymentID: reference, AmountCents: op.AmountCents, OperationID: op.OperationID}); err != nil {
			t.Fatal(err)
		}
	}
	if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
		t.Fatal(err)
	}
	_, verifier := externalRefundActor(t, f.db, f.h, "close-verifier", []string{"invoices.view", "payments.close_verify"})
	_, reviewer := externalRefundActor(t, f.db, f.h, "close-reviewer", []string{"invoices.view", "payments.close_review"})
	return closeVerificationFixture{f, invoice, pid, reference, op, verifier, reviewer}
}
func (f closeVerificationFixture) lookup(t *testing.T, key string) int64 {
	t.Helper()
	w := request(f.h, "POST", verificationPath(f.invoice, f.pid), map[string]any{"close_operation_id": f.operation.ID, "expected_version": 1}, f.verifier, key)
	expect(t, w, 201)
	return int64(data(t, w)["verification"].(map[string]any)["id"].(float64))
}
func (f closeVerificationFixture) paidCandidate(t *testing.T) int64 {
	t.Helper()
	actor, hash := closeActor(t, f.upgradeFixture, f.verifier)
	v, err := f.db.BeginPaymentCloseVerification(t.Context(), actor, f.invoice, f.pid, f.operation.ID, 1, "close-verify-paid-request", hash)
	if err != nil {
		t.Fatal(err)
	}
	// Explicit trusted adapter result fixture. No merchant or debug endpoint.
	result := payment.CloseStatusResult{OperationID: f.operation.OperationID, ProviderPaymentID: f.reference, AmountCents: f.operation.AmountCents, Status: "succeeded", ExternalTransactionID: "fixture:verified-payment"}
	if _, err = f.db.CompletePaymentCloseVerification(t.Context(), v, result, "verified", hash); err != nil {
		t.Fatal(err)
	}
	return v.ID
}
func TestPaymentCloseVerificationClosedHTTPIndependentReplayAndProtection(t *testing.T) {
	f := newCloseVerificationFixture(t, true)
	path := verificationPath(f.invoice, f.pid)
	w := request(f.h, "GET", path, nil, f.verifier, "")
	expect(t, w, 200)
	if data(t, w)["lookup_supported"] != true || data(t, w)["can_request"] != true {
		t.Fatal(w.Body.String())
	}
	id := f.lookup(t, "close-lookup-once")
	replay := request(f.h, "POST", path, map[string]any{"close_operation_id": f.operation.ID, "expected_version": 1}, f.verifier, "close-lookup-once")
	expect(t, replay, 200)
	if data(t, replay)["verification"].(map[string]any)["status"] != "awaiting_review" {
		t.Fatal(replay.Body.String())
	}
	review := fmt.Sprintf("%s/%d/confirm", path, id)
	body := map[string]any{"expected_version": 1, "note": "独立核验原关单证据"}
	expect(t, request(f.h, "POST", review, body, f.root, "close-original-actor-self"), 409)
	expect(t, request(f.h, "POST", review, body, f.verifier, "close-verifier-no-review"), 403)
	w = request(f.h, "POST", review, body, f.reviewer, "close-independent-review")
	expect(t, w, 201)
	if data(t, w)["verification"].(map[string]any)["status"] != "applied_closed" {
		t.Fatal(w.Body.String())
	}
	expect(t, request(f.h, "POST", review, body, f.reviewer, "close-independent-review"), 200)
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "closed", 2)
	var original string
	var unresolved bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status,payment_close_operation_unresolved(id) FROM payment_close_operations WHERE id=$1", f.operation.ID).Scan(&original, &unresolved); err != nil || original != "unknown" || unresolved {
		t.Fatal(original, unresolved, err)
	}
	old := request(f.h, "POST", closePath(f.invoice, f.pid), map[string]any{"expected_version": 1}, f.root, "close-verify-original")
	expect(t, old, 200)
	if data(t, old)["status"] != "closed" || data(t, old)["resolution"] == nil || data(t, old)["operation_id"] != f.operation.OperationID {
		t.Fatal(old.Body.String())
	}
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", f.invoice), nil, f.root, "")
	expect(t, detail, 200)
	p := data(t, detail)["payments"].([]any)[0].(map[string]any)
	if p["close_status"] != "unknown" || p["close_frozen"] != false || p["effective_close_status"] != "closed" {
		t.Fatal(detail.Body.String())
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", f.invoice), map[string]any{"provider": "mock", "expected_version": 2}, f.customer, "close-verified-retry-intent"), 201)
	for _, table := range []string{"payment_close_verification_requests", "payment_close_verification_results", "payment_close_verification_decisions"} {
		if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM "+table); err == nil {
			t.Fatal("mutable evidence", table)
		}
	}
}
func TestPaymentCloseVerificationUnknownAndUnsupportedStayFrozen(t *testing.T) {
	f := newCloseVerificationFixture(t, false)
	id := f.lookup(t, "close-unknown-evidence")
	path := fmt.Sprintf("%s/%d/confirm", verificationPath(f.invoice, f.pid), id)
	w := request(f.h, "POST", path, map[string]any{"expected_version": 1, "note": "不能用未付款代替关闭"}, f.reviewer, "close-unknown-no-apply")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "close_verification_unverified" {
		t.Fatal(w.Body.String())
	}
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "pending", 1)
	e, invoice, pid, _ := closeFixture(t, "epay_alipay")
	actor, hash := closeActor(t, e, e.root)
	op, err := e.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "close-epay-unknown-op", hash)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
		t.Fatal(err)
	}
	w = request(e.h, "POST", verificationPath(invoice, pid), map[string]any{"close_operation_id": op.ID, "expected_version": 1}, e.root, "close-epay-lookup-only")
	expect(t, w, 201)
	if data(t, w)["verification"].(map[string]any)["result_status"] != "unsupported" {
		t.Fatal(w.Body.String())
	}
}
func TestPaymentCloseVerificationPaidUsesSharedSettlementAndCallbackWins(t *testing.T) {
	for _, first := range []string{"review", "callback"} {
		t.Run(first, func(t *testing.T) {
			f := newCloseVerificationFixture(t, false)
			id := f.paidCandidate(t)
			raw, _ := json.Marshal(payment.Webhook{EventID: "verified-paid-callback", ProviderPaymentID: f.reference, InvoiceID: f.invoice, AmountCents: f.operation.AmountCents, Status: "succeeded"})
			// The mock signature path carries no external transaction, so use a trusted
			// Epay-style validated callback core with the exact stable transaction ID.
			callback := func() {
				if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "verified-paid-callback", f.reference, f.invoice, f.operation.AmountCents, "succeeded", raw, "fixture:verified-payment"); err != nil {
					t.Fatal(err)
				}
			}
			version := int64(1)
			if first == "callback" {
				callback()
				version = 2
			}
			w := request(f.h, "POST", fmt.Sprintf("%s/%d/confirm", verificationPath(f.invoice, f.pid), id), map[string]any{"expected_version": version, "note": "独立复核可信收款证据"}, f.reviewer, "close-paid-review")
			expect(t, w, 201)
			if first == "review" {
				callback()
			}
			var state, invoiceState string
			var count int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.status,i.status,(SELECT count(*) FROM payment_webhook_events WHERE status='processed') FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1`, f.pid).Scan(&state, &invoiceState, &count); err != nil || state != "succeeded" || invoiceState != "paid" {
				t.Fatal(state, invoiceState, count, err)
			}
			old := request(f.h, "POST", closePath(f.invoice, f.pid), map[string]any{"expected_version": 1}, f.root, "close-verify-original")
			expect(t, old, 200)
			if data(t, old)["status"] != "succeeded" || data(t, old)["resolution"] == nil {
				t.Fatal(old.Body.String())
			}
		})
	}
}
func TestPaymentCloseVerificationConcurrentReviewAndRollback(t *testing.T) {
	f := newCloseVerificationFixture(t, true)
	id := f.lookup(t, "close-concurrent-candidate")
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			w := request(f.h, "POST", fmt.Sprintf("%s/%d/confirm", verificationPath(f.invoice, f.pid), id), map[string]any{"expected_version": 1, "note": "并发独立复核"}, f.reviewer, fmt.Sprintf("close-concurrent-review-%d", n))
			codes <- w.Code
		}(i)
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 201 {
			success++
		} else if code != 409 {
			t.Fatal("unexpected race response", code)
		}
	}
	if success != 1 {
		t.Fatal("duplicate decisions", success)
	}
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "closed", 2)
}
func TestPaymentCloseVerificationAuditRollback(t *testing.T) {
	f := newCloseVerificationFixture(t, true)
	id := f.lookup(t, "close-audit-candidate")
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_close_verification_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.close_verification.applied_closed' THEN RAISE EXCEPTION 'fixture audit rejection'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_close_verification_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_close_verification_audit()`); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", fmt.Sprintf("%s/%d/confirm", verificationPath(f.invoice, f.pid), id), map[string]any{"expected_version": 1, "note": "审计失败必须回滚"}, f.reviewer, "close-audit-review")
	expect(t, w, 500)
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "pending", 1)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM payment_close_verification_decisions").Scan(&count); err != nil || count != 0 {
		t.Fatal(count, err)
	}
}

func TestPaymentCloseVerificationSentConflictAndExpiredOriginalImmutable(t *testing.T) {
	for _, mode := range []string{"sent_version_conflict", "expired_original"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, pid, _ := closeFixture(t, "mock")
			actor, hash := closeActor(t, f, f.root)
			op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "verify-original-boundary", hash)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "sent_version_conflict" {
				gateway := payment.MockGateway{Secret: []byte("integration-payment-secret")}
				result, err := gateway.Close(t.Context(), payment.CloseRequest{ProviderPaymentID: op.ProviderPaymentID, AmountCents: op.AmountCents, OperationID: op.OperationID})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE invoices SET updated_at=clock_timestamp() WHERE id=$1", invoice); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.CompletePaymentClose(t.Context(), op, result, hash); err == nil {
					t.Fatal("version conflict unlocked")
				}
				var state string
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM payment_close_operations WHERE id=$1", op.ID).Scan(&state); err != nil || state != "unknown" {
					t.Fatal(state, err)
				}
				if _, err = f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 2, "verify-should-not-send-again", hash); err == nil {
					t.Fatal("close resend allowed after a sent result")
				}
			} else {
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE payment_close_operations SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", op.ID); err != nil {
					t.Fatal(err)
				}
				if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
					t.Fatal(err)
				}
				var state string
				if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM payment_close_operations WHERE id=$1", op.ID).Scan(&state); err != nil || state != "checking" {
					t.Fatal(state, err)
				}
				if _, err = f.db.Pool.Exec(t.Context(), "UPDATE payment_close_operations SET status='unknown',completed_at=clock_timestamp() WHERE id=$1", op.ID); err == nil {
					t.Fatal("expired original mutable")
				}
				_, verifier := externalRefundActor(t, f.db, f.h, "expired-verify-reader", []string{"invoices.view", "payments.close_verify"})
				w := request(f.h, "POST", verificationPath(invoice, pid), map[string]any{"close_operation_id": op.ID, "expected_version": 1}, verifier, "verify-expired-read-only")
				expect(t, w, 201)
				if data(t, w)["verification"].(map[string]any)["result_status"] != "unknown" {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
}
func TestPaymentCloseVerificationCallbackClearsEffectiveFreezeWithoutChangingUnknown(t *testing.T) {
	f := newCloseVerificationFixture(t, false)
	raw, _ := json.Marshal(payment.Webhook{EventID: "verified-signed-callback", ProviderPaymentID: f.reference, InvoiceID: f.invoice, AmountCents: f.operation.AmountCents, Status: "succeeded"})
	expect(t, signedWebhook(f.h, raw, paymentSignature(raw)), 201)
	read := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", f.invoice), nil, f.root, "")
	expect(t, read, 200)
	value := data(t, read)
	p := value["payments"].([]any)[0].(map[string]any)
	if value["status"] != "paid" || value["real_payment_pending"] != false || p["close_frozen"] != false || p["close_status"] != "unknown" {
		t.Fatal(read.Body.String())
	}
}
func TestPaymentCloseVerificationPaidCoreAuditFailureRollsBackAllFacts(t *testing.T) {
	f := newCloseVerificationFixture(t, false)
	id := f.paidCandidate(t)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_close_paid_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.close_verification.applied_paid' THEN RAISE EXCEPTION 'fixture payment audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_close_paid_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_close_paid_audit()`); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", fmt.Sprintf("%s/%d/confirm", verificationPath(f.invoice, f.pid), id), map[string]any{"expected_version": 1, "note": "资金审计失败全部回滚"}, f.reviewer, "close-paid-audit-failure")
	expect(t, w, 500)
	closeState(t, f.upgradeFixture, f.invoice, f.pid, "pending", 1)
	var events, decisions int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM payment_webhook_events WHERE event_id LIKE 'close-verification:%'),(SELECT count(*) FROM payment_close_verification_decisions)").Scan(&events, &decisions); err != nil || events != 0 || decisions != 0 {
		t.Fatal(events, decisions, err)
	}
}
func TestPaymentCloseVerificationConcurrentLookupSameKeyReservesOnce(t *testing.T) {
	f := newCloseVerificationFixture(t, true)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			w := request(f.h, "POST", verificationPath(f.invoice, f.pid), map[string]any{"close_operation_id": f.operation.ID, "expected_version": 1}, f.verifier, "verify-concurrent-same-key")
			codes <- w.Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 && code != 201 && code != 202 {
			t.Fatal(code)
		}
	}
	var requests, results int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM payment_close_verification_requests),(SELECT count(*) FROM payment_close_verification_results)").Scan(&requests, &results); err != nil || requests != 1 || results != 1 {
		t.Fatal(requests, results, err)
	}
}
