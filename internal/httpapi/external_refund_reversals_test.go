package httpapi

import (
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestExternalRefundTopupHTTPReversal(t *testing.T) { externalRefundTopupHTTPReversal(t, false) }
func TestExternalRefundResolvedTopupHTTPReversal(t *testing.T) {
	externalRefundTopupHTTPReversal(t, true)
}
func externalRefundTopupHTTPReversal(t *testing.T, resolved bool) {
	f := onlineTopupFixture(t)
	var invoice int64
	var callback url.Values
	if resolved {
		invoice, callback = topupLiabilityPendingHTTP(t, f, "refund-topup-http")
		expect(t, request(f.h, "POST", topupLiabilityPath(invoice)+"/resolve", map[string]any{"expected_version": topupLiabilityVersion(t, f, invoice), "note": "isolated liability credit"}, f.root, "refund-resolved-credit"), 201)
	} else {
		invoice = onlineTopupCreate(t, f, "refund-topup-http")
		checkout := epayIntent(t, f, invoice, "epay_alipay", "refund-topup-http-intent")
		callback = epayNotification(checkout, "refund-topup-http-trade")
		expect(t, epayCallback(f.h, "POST", "epay_alipay", callback), 200)
	}
	var payment, before int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT p.id,u.balance_cents FROM payment_transactions p JOIN users u ON u.id=p.user_id WHERE p.invoice_id=$1`, invoice).Scan(&payment, &before); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
	_, requester := externalRefundActor(t, f.db, f.h, "topup-http-requester", []string{"external_refunds.view", "external_refunds.create"})
	_, reviewer := externalRefundActor(t, f.db, f.h, "topup-http-reviewer", []string{"external_refunds.view", "external_refunds.review"})
	_, reader := externalRefundActor(t, f.db, f.h, "topup-http-reader", []string{"external_refunds.view"})
	in := map[string]any{"payment_id": payment, "amount_cents": 1234, "method": "bank_transfer", "provider_refund_id": "topup-http-refund", "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://topup-http-refund", "evidence_sha256": strings.Repeat("a", 64), "note": "actual external transfer receipt, not original route", "expected_version": topupLiabilityVersion(t, f, invoice)}
	expect(t, request(f.h, "POST", path, in, reader, "topup-http-reader-denied"), 403)
	expect(t, request(f.h, "POST", path, in, f.customer, "topup-http-customer-denied"), 401)
	w := request(f.h, "POST", path, in, requester, "topup-http-create")
	expect(t, w, 201)
	refund := data(t, w)["refund"].(map[string]any)
	reversal := refund["reversal"].(map[string]any)
	if reversal["status"] != "pending" || reversal["amount_cents"] != float64(1234) || reversal["ledger_id"] != nil {
		t.Fatal("pending reversal projection", reversal)
	}
	expect(t, request(f.h, "POST", path, in, requester, "topup-http-create"), 200)
	in["amount_cents"] = 1233
	expect(t, request(f.h, "POST", path, in, requester, "topup-http-create"), 409)
	confirm := fmt.Sprintf("%s/%.0f/confirm", path, refund["id"])
	expect(t, request(f.h, "POST", confirm, map[string]any{"expected_version": 99, "note": "stale"}, reviewer, "topup-http-stale"), 409)
	body := map[string]any{"expected_version": 1, "note": "independent bank receipt verification"}
	w = request(f.h, "POST", confirm, body, reviewer, "topup-http-confirm")
	expect(t, w, 201)
	reversal = data(t, w)["refund"].(map[string]any)["reversal"].(map[string]any)
	if reversal["status"] != "applied" || reversal["ledger_id"] == nil || reversal["applied_at"] == nil {
		t.Fatal("applied reversal projection", reversal)
	}
	expect(t, request(f.h, "POST", confirm, body, reviewer, "topup-http-confirm"), 200)
	expect(t, epayCallback(f.h, "POST", "epay_alipay", callback), 200)
	detail := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, ""))
	if detail["status"] != "refunded" {
		t.Fatal("refund status lost", detail)
	}
	var after, negative int64
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT u.balance_cents,p.status,(SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1 AND delta_cents=-1234) FROM users u JOIN payment_transactions p ON p.user_id=u.id WHERE p.invoice_id=$1`, invoice).Scan(&after, &status, &negative); err != nil || after != before-1234 || status != "succeeded" || negative != 1 {
		t.Fatalf("money invariant before=%d after=%d payment=%s reversals=%d err=%v", before, after, status, negative, err)
	}
	summary := data(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid), nil, f.root, ""))
	if summary["gateway_income_cents"] != float64(1234) || summary["external_refund_cents"] != float64(1234) || summary["net_gateway_cents"] != float64(0) {
		t.Fatal("cash refund double counted", summary)
	}
}

func TestExternalRefundTopupHTTPRejectsUnpostedLiability(t *testing.T) {
	f := onlineTopupFixture(t)
	invoice, _ := topupLiabilityPendingHTTP(t, f, "refund-topup-unposted")
	var payment int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", invoice).Scan(&payment); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", invoice)
	in := map[string]any{"payment_id": payment, "amount_cents": 1234, "method": "original_route", "provider_refund_id": "refund-unposted-receipt", "received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://unposted", "evidence_sha256": strings.Repeat("b", 64), "note": "isolated unsupported liability evidence", "expected_version": topupLiabilityVersion(t, f, invoice)}
	{
		w := request(f.h, "POST", path, in, f.root, "refund-unposted-only")
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "external_refund_topup_not_credited") {
			t.Fatal("missing explicit liability boundary", w.Body.String())
		}
		var facts, reservations int
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM external_refunds WHERE invoice_id=$1),(SELECT count(*) FROM external_refund_reversal_reservations WHERE invoice_id=$1)`, invoice).Scan(&facts, &reservations); err != nil || facts != 0 || reservations != 0 {
			t.Fatalf("unsupported liability left state: facts=%d reserves=%d err=%v", facts, reservations, err)
		}
	}
}
