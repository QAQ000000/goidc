package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"strings"
	"sync"
	"testing"
	"time"
)

func paymentAnomalyFixture(t *testing.T, transaction string) (upgradeFixture, int64, int64, string, int64) {
	t.Helper()
	f, invoice, pid, reference := closeFixture(t, "mock")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "anomaly-close-original", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-first-event", reference, invoice, 3000, "succeeded", []byte(`{"late":1}`), transaction); err != nil {
		t.Fatal(err)
	}
	var anchor int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM payment_close_anomalies WHERE invoice_id=$1", invoice).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	return f, invoice, pid, reference, anchor
}
func paymentAnomalyInput(version int64, id string) map[string]any {
	return map[string]any{"method": "original_route", "provider_refund_id": id, "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://late-receipt/" + id, "evidence_sha256": strings.Repeat("a", 64), "note": "隔离测试：已核验迟到款全额退回", "expected_group_version": version}
}
func anomalyPaths(invoice, anchor int64) (string, string) {
	base := fmt.Sprintf("/api/v1/admin/invoices/%d", invoice)
	return base, fmt.Sprintf("%s/payment-anomaly-groups/%d/refunds", base, anchor)
}
func anomalyReviewer(t *testing.T, f upgradeFixture) (int64, string) {
	return externalRefundActor(t, f.db, f.h, "anomaly-reviewer", []string{"invoices.view", "payment_anomalies.view", "payment_anomalies.review"})
}

func anomalyOtherPayment(t *testing.T, f upgradeFixture, key string, closePayment bool, originalTransaction string) (int64, int64, string, int64) {
	t.Helper()
	invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), key+"-invoice")
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, key+"-intent")
	expect(t, w, 201)
	d := data(t, w)
	pid, reference := int64(d["payment_id"].(float64)), d["provider_payment_id"].(string)
	if !closePayment {
		return invoice, pid, reference, 0
	}
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, key+"-close", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", key+"-late", reference, invoice, 3000, "succeeded", []byte(key), originalTransaction); err != nil {
		t.Fatal(err)
	}
	var anchor int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM payment_close_anomalies WHERE payment_id=$1", pid).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	return invoice, pid, reference, anchor
}

func TestPaymentAnomalySameOriginalAcrossPaymentsCannotRefundTwice(t *testing.T) {
	f, invoice, _, _, anchor := paymentAnomalyFixture(t, "same-original-tx")
	otherInvoice, _, _, otherAnchor := anomalyOtherPayment(t, f, "same-original-other", true, "same-original-tx")
	_, firstPath := anomalyPaths(invoice, anchor)
	_, secondPath := anomalyPaths(otherInvoice, otherAnchor)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i, v := range []struct {
		path   string
		anchor int64
	}{{firstPath, anchor}, {secondPath, otherAnchor}} {
		wg.Add(1)
		go func(i int, path string, version int64) {
			defer wg.Done()
			codes <- request(f.h, "POST", path, paymentAnomalyInput(version, fmt.Sprintf("different-payout-%d", i)), f.root, fmt.Sprintf("same-original-claim-%d", i)).Code
		}(i, v.path, v.anchor)
	}
	wg.Wait()
	close(codes)
	var success, conflict int
	for code := range codes {
		switch code {
		case 201:
			success++
		case 409:
			conflict++
		default:
			t.Fatal(code)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal(success, conflict)
	}
}

func TestPaymentAnomalyRefundPreventsReverseSettlement(t *testing.T) {
	for _, mode := range []string{"pending", "confirmed", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, _, _, anchor := paymentAnomalyFixture(t, "reverse-original-tx")
			otherInvoice, otherPID, otherReference, _ := anomalyOtherPayment(t, f, "reverse-other", false, "")
			base, create := anomalyPaths(invoice, anchor)
			input := paymentAnomalyInput(anchor, "reverse-refund")
			var refundCode int
			var callbackErr error
			createRefund := func() { refundCode = request(f.h, "POST", create, input, f.root, "reverse-create").Code }
			callback := func() {
				_, callbackErr = f.db.ProcessPaymentWebhook(t.Context(), "mock", "reverse-success", otherReference, otherInvoice, 3000, "succeeded", []byte(`{"reverse":true}`), "reverse-original-tx")
			}
			if mode == "concurrent" {
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); createRefund() }()
				go func() { defer wg.Done(); callback() }()
				wg.Wait()
				if !((refundCode == 201 && callbackErr != nil) || (refundCode == 409 && callbackErr == nil)) {
					t.Fatal(refundCode, callbackErr)
				}
			} else {
				createRefund()
				if refundCode != 201 {
					t.Fatal(refundCode)
				}
				if mode == "confirmed" {
					_, reviewer := anomalyReviewer(t, f)
					var rid int64
					if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_close_anomaly_refunds").Scan(&rid); err != nil {
						t.Fatal(err)
					}
					expect(t, request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%d/confirm", base, rid), map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "独立复核"}, reviewer, "reverse-confirm"), 201)
				}
				callback()
				if callbackErr == nil {
					t.Fatal("refunded receipt settled another payment")
				}
			}
			var payments, refunds, ledger int
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM payment_transactions WHERE status='succeeded'),(SELECT count(*) FROM payment_close_anomaly_refunds WHERE status<>'rejected'),(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1)", otherInvoice).Scan(&payments, &refunds, &ledger); err != nil {
				t.Fatal(err)
			}
			if payments+refunds != 1 {
				t.Fatal(payments, refunds, ledger)
			}
			if refundCode == 201 {
				closeState(t, f, otherInvoice, otherPID, "pending", 1)
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET status='succeeded',external_transaction_id='reverse-original-tx' WHERE id=$1", otherPID); err == nil {
					t.Fatal("SQL bypass settled claimed receipt")
				}
			}
		})
	}
}

func TestPaymentAnomalyRefundLifecycleGroupsAndSeparateMoney(t *testing.T) {
	f, invoice, pid, reference, anchor := paymentAnomalyFixture(t, "merchant:late-one")
	base, create := anomalyPaths(invoice, anchor)
	_, reviewer := anomalyReviewer(t, f)
	before := data(t, request(f.h, "GET", base+"/payment-anomaly-summary", nil, f.root, ""))
	if before["anomaly_received_cents"] != float64(3000) || before["anomaly_pending_cents"] != float64(3000) {
		t.Fatal(before)
	}
	input := paymentAnomalyInput(anchor, "late-refund-one")
	w := request(f.h, "POST", create, input, f.root, "anomaly-create-0001")
	expect(t, w, 201)
	receipt := data(t, w)
	refund := receipt["refund"].(map[string]any)
	rid := int64(refund["id"].(float64))
	confirm := fmt.Sprintf("%s/payment-anomaly-refunds/%d/confirm", base, rid)
	expect(t, request(f.h, "POST", create, input, f.root, "anomaly-create-0001"), 200)
	changed := paymentAnomalyInput(anchor, "late-refund-other")
	expect(t, request(f.h, "POST", create, changed, f.root, "anomaly-create-0001"), 409)
	review := map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "独立复核退回到账"}
	expect(t, request(f.h, "POST", confirm, review, f.root, "anomaly-self-review"), 403)
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-second-event", reference, invoice, 3000, "succeeded", []byte(`{"late":2}`), "merchant:late-one"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", confirm, review, reviewer, "anomaly-stale-group"), 409)
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT max(id) FROM payment_close_anomalies WHERE payment_id=$1", pid).Scan(&version); err != nil {
		t.Fatal(err)
	}
	review["expected_group_version"] = version
	w = request(f.h, "POST", confirm, review, reviewer, "anomaly-confirm-0001")
	expect(t, w, 201)
	if data(t, w)["invoice_has_unresolved"] != false {
		t.Fatal(w.Body.String())
	}
	expect(t, request(f.h, "POST", confirm, review, reviewer, "anomaly-confirm-0001"), 200)
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-third-event", reference, invoice, 3000, "succeeded", []byte(`{"late":3}`), "merchant:late-one"); err != nil {
		t.Fatal(err)
	}
	summary := data(t, request(f.h, "GET", base+"/payment-anomaly-summary", nil, f.root, ""))
	if summary["anomaly_received_cents"] != float64(3000) || summary["anomaly_refunded_cents"] != float64(3000) || summary["anomaly_pending_cents"] != float64(0) || summary["unresolved_groups"] != float64(0) {
		t.Fatal(summary)
	}
	entries := data(t, request(f.h, "GET", base+"/payment-anomaly-entries", nil, f.root, ""))
	if entries["total"] != float64(2) {
		t.Fatal(entries)
	}
	cash := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid), nil, f.root, ""))
	if cash["gateway_income_cents"] != float64(0) || cash["external_refund_cents"] != float64(0) || cash["net_gateway_cents"] != float64(0) {
		t.Fatal(cash)
	}
	closeState(t, f, invoice, pid, "closed", 2)
	var balance, events, refunds int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents,(SELECT count(*) FROM payment_close_anomalies),(SELECT count(*) FROM payment_close_anomaly_refunds WHERE status='confirmed') FROM users WHERE id=$1", f.uid).Scan(&balance, &events, &refunds); err != nil || events != 3 || refunds != 1 {
		t.Fatal(balance, events, refunds, err)
	}
	if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-new-transaction", reference, invoice, 3000, "succeeded", []byte(`{"late":4}`), "merchant:late-two"); err != nil {
		t.Fatal(err)
	}
	summary = data(t, request(f.h, "GET", base+"/payment-anomaly-summary", nil, f.root, ""))
	if summary["anomaly_received_cents"] != float64(6000) || summary["anomaly_pending_cents"] != float64(3000) || summary["unresolved_groups"] != float64(1) {
		t.Fatal(summary)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{"expected_version": 2}, f.customer, "anomaly-new-tx-blocks-pay"), 409)
	for _, sql := range []string{
		"UPDATE payment_close_anomaly_refunds SET note='tampered'",
		"UPDATE payment_close_anomaly_refunds SET merchant_scope='epay:'||repeat('b',64)",
		"UPDATE payment_close_anomaly_refunds SET credential_sha256=repeat('b',64)",
		"UPDATE payment_close_anomaly_merchant_bindings SET merchant_scope='epay:'||repeat('b',64)",
		"UPDATE payment_close_anomaly_merchant_bindings SET credential_sha256=repeat('b',64)",
		"DELETE FROM payment_close_anomaly_merchant_bindings",
		"DELETE FROM payment_close_anomaly_refunds", "DELETE FROM payment_close_anomalies",
		"UPDATE external_refund_payout_claims SET status='released' WHERE status='confirmed'",
	} {
		if _, err := f.db.Pool.Exec(t.Context(), sql); err == nil {
			t.Fatal("immutable fact changed", sql)
		}
	}
}

func TestPaymentAnomalyRefundUnverifiedAndHTTPBoundaries(t *testing.T) {
	f, invoice, pid, _, anchor := paymentAnomalyFixture(t, "")
	base, create := anomalyPaths(invoice, anchor)
	expect(t, request(f.h, "POST", create, paymentAnomalyInput(anchor, "no-real-tx"), f.root, "anomaly-no-tx-create"), 409)
	summary := data(t, request(f.h, "GET", base+"/payment-anomaly-summary", nil, f.root, ""))
	if summary["unverified_groups"] != float64(1) || summary["unverified_amount_cents"] != float64(3000) || summary["anomaly_received_cents"] != float64(0) {
		t.Fatal(summary)
	}
	staffID, reader := externalRefundActor(t, f.db, f.h, "anomaly-reader", []string{"invoices.view", "payment_anomalies.view"})
	expect(t, request(f.h, "GET", base+"/payment-anomaly-groups", nil, reader, ""), 200)
	expect(t, request(f.h, "POST", create, paymentAnomalyInput(anchor, "no-write"), reader, "anomaly-reader-denied"), 403)
	expect(t, request(f.h, "GET", base+"/payment-anomaly-summary", nil, f.customer, ""), 403)
	for _, method := range []string{"GET", "POST"} {
		path := base + "/payment-anomaly-groups"
		var body any
		if method == "POST" {
			path = create
			body = paymentAnomalyInput(anchor, "bearer-no")
		}
		w := requestBearer(f.h, method, path, body, "zjmfgo_"+strings.Repeat("a", 64))
		expect(t, w, 403)
		if errorData(t, w)["code"] != "session_required" || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal(w.Body.String())
		}
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", base+"/payment-anomaly-groups", nil, reader, ""), 404)
	closeState(t, f, invoice, pid, "closed", 2)
}

func TestPaymentAnomalyRefundConcurrencyAndRollback(t *testing.T) {
	f, invoice, _, _, anchor := paymentAnomalyFixture(t, "merchant:race")
	_, create := anomalyPaths(invoice, anchor)
	var wg sync.WaitGroup
	codes := make(chan int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes <- request(f.h, "POST", create, paymentAnomalyInput(anchor, fmt.Sprintf("race-%d", i)), f.root, fmt.Sprintf("anomaly-race-%d", i)).Code
		}(i)
	}
	wg.Wait()
	close(codes)
	success := 0
	for code := range codes {
		if code == 201 {
			success++
		} else if code != 409 {
			t.Fatal(code)
		}
	}
	if success != 1 {
		t.Fatal(success)
	}
	var count, claims int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM payment_close_anomaly_refunds),(SELECT count(*) FROM external_refund_payout_claims)").Scan(&count, &claims); err != nil || count != 1 || claims != 1 {
		t.Fatal(count, claims, err)
	}
}

func TestPaymentAnomalySharedPayoutClaimAcrossRefundWorkflows(t *testing.T) {
	for _, mode := range []string{"ordinary_first", "anomaly_first", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			f, invoice, _, _, anchor := paymentAnomalyFixture(t, "merchant:late-shared")
			base, create := anomalyPaths(invoice, anchor)
			ordinaryInvoice, ordinaryInput := externalRefundPaidInvoice(t, f.db, f.h, f.root, f.customer, f.uid, "ordinary-shared")
			ordinaryPath := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", ordinaryInvoice)
			ordinaryInput["provider_refund_id"] = "shared-real-payout"
			anomalyInput := paymentAnomalyInput(anchor, "shared-real-payout")
			var ordinaryCode, anomalyCode int
			var ordinaryID, anomalyID int64
			ordinary := func() {
				w := request(f.h, "POST", ordinaryPath, ordinaryInput, f.root, "ordinary-global-claim")
				ordinaryCode = w.Code
				if w.Code == 201 {
					ordinaryID = int64(data(t, w)["refund"].(map[string]any)["id"].(float64))
				}
			}
			anomaly := func() {
				w := request(f.h, "POST", create, anomalyInput, f.root, "anomaly-global-claim")
				anomalyCode = w.Code
				if w.Code == 201 {
					anomalyID = int64(data(t, w)["refund"].(map[string]any)["id"].(float64))
				}
			}
			switch mode {
			case "ordinary_first":
				ordinary()
				anomaly()
			case "anomaly_first":
				anomaly()
				ordinary()
			default:
				var wg sync.WaitGroup
				wg.Add(2)
				go func() { defer wg.Done(); ordinary() }()
				go func() { defer wg.Done(); anomaly() }()
				wg.Wait()
			}
			if !((ordinaryCode == 201 && anomalyCode == 409) || (ordinaryCode == 409 && anomalyCode == 201)) {
				t.Fatal(ordinaryCode, anomalyCode)
			}
			_, reviewer := externalRefundActor(t, f.db, f.h, "shared-reviewer", []string{"invoices.view", "payment_anomalies.view", "payment_anomalies.review", "external_refunds.view", "external_refunds.review"})
			if ordinaryCode == 201 {
				expect(t, request(f.h, "POST", fmt.Sprintf("%s/%d/reject", ordinaryPath, ordinaryID), map[string]any{"expected_version": 1, "note": "独立驳回重复证据"}, reviewer, "ordinary-release-claim"), 201)
				expect(t, request(f.h, "POST", create, anomalyInput, f.root, "anomaly-after-release"), 201)
			} else {
				expect(t, request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%d/reject", base, anomalyID), map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "独立驳回重复证据"}, reviewer, "anomaly-release-claim"), 201)
				expect(t, request(f.h, "POST", ordinaryPath, ordinaryInput, f.root, "ordinary-after-release"), 201)
			}
			var active, released int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FILTER(WHERE status<>'released'),count(*) FILTER(WHERE status='released') FROM external_refund_payout_claims WHERE provider_refund_id='shared-real-payout'").Scan(&active, &released); err != nil || active != 1 || released != 1 {
				t.Fatal(active, released, err)
			}
		})
	}
}

func TestPaymentAnomalyAuditFailureRollsBackAllFacts(t *testing.T) {
	f, invoice, pid, _, anchor := paymentAnomalyFixture(t, "merchant:audit")
	base, create := anomalyPaths(invoice, anchor)
	_, reviewer := anomalyReviewer(t, f)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION anomaly_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action LIKE 'payment_anomaly.%' THEN RAISE EXCEPTION 'injected anomaly audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER anomaly_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION anomaly_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	in := paymentAnomalyInput(anchor, "anomaly-audit-outgoing")
	expect(t, request(f.h, "POST", create, in, f.root, "anomaly-audit-create"), 500)
	var refunds, claims, operations int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM payment_close_anomaly_refunds),(SELECT count(*) FROM external_refund_payout_claims),(SELECT count(*) FROM payment_close_anomaly_refund_operations)").Scan(&refunds, &claims, &operations); err != nil || refunds != 0 || claims != 0 || operations != 0 {
		t.Fatal(refunds, claims, operations, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER anomaly_reject_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", create, in, f.root, "anomaly-audit-create")
	expect(t, w, 201)
	id := int64(data(t, w)["refund"].(map[string]any)["id"].(float64))
	if _, err := f.db.Pool.Exec(t.Context(), "CREATE TRIGGER anomaly_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION anomaly_reject_audit()"); err != nil {
		t.Fatal(err)
	}
	confirm := fmt.Sprintf("%s/payment-anomaly-refunds/%d/confirm", base, id)
	body := map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "核对实际全额退回"}
	expect(t, request(f.h, "POST", confirm, body, reviewer, "anomaly-audit-confirm"), 500)
	var status, claim string
	var unresolved bool
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT r.status,c.status,payment_close_anomaly_unresolved(r.anchor_anomaly_id) FROM payment_close_anomaly_refunds r JOIN external_refund_payout_claims c ON c.source_kind='payment_anomaly_refund' AND c.source_id=r.id WHERE r.id=$1", id).Scan(&status, &claim, &unresolved); err != nil || status != "pending_review" || claim != "reserved" || !unresolved {
		t.Fatal(status, claim, unresolved, err)
	}
	closeState(t, f, invoice, pid, "closed", 2)
}

func TestPaymentAnomalyOriginalAmountIndependentOfCurrentInvoice(t *testing.T) {
	f, invoice, pid, reference := closeFixture(t, "mock")
	actor, hash := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, "anomaly-original-amount", hash)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
		t.Fatal(err)
	}
	input := manualInvoiceInput(f.uid)
	input.ExpectedVersion = 2
	input.Items = []store.ManualInvoiceItem{{Description: "changed after trusted close", AmountCents: 4500}}
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/invoices/%d", invoice), input, f.root, "anomaly-edit-closed-invoice"), 200)
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-original-old-amount", reference, invoice, 3000, "succeeded", []byte(`{"original":true}`), "merchant:original-amount"); err != nil {
		t.Fatal(err)
	}
	var anchor int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM payment_close_anomalies WHERE payment_id=$1", pid).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	base, create := anomalyPaths(invoice, anchor)
	_, reviewer := anomalyReviewer(t, f)
	w := request(f.h, "POST", create, paymentAnomalyInput(anchor, "original-amount-refund"), f.root, "anomaly-original-refund")
	expect(t, w, 201)
	refund := data(t, w)["refund"].(map[string]any)
	if refund["amount_cents"] != float64(3000) {
		t.Fatal(refund)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%.0f/confirm", base, refund["id"]), map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "仅核对原迟到交易"}, reviewer, "anomaly-original-confirm"), 201)
	var amount, version int64
	var state string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents,version,status FROM invoices WHERE id=$1", invoice).Scan(&amount, &version, &state); err != nil || amount != 4500 || version != 3 || state != "unpaid" {
		t.Fatal(amount, version, state, err)
	}
	payment := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 3}, f.customer, "anomaly-replacement-new-amount")
	expect(t, payment, 201)
	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "anomaly-replacement-normal", data(t, payment)["provider_payment_id"].(string), invoice, 4500, "succeeded", []byte(`{"replacement":true}`), "merchant:normal-payment"); err != nil {
		t.Fatal(err)
	}
	var paid string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoice).Scan(&paid); err != nil || paid != "paid" {
		t.Fatal(paid, err)
	}
}
