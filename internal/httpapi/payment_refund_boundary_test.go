package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestEpayExternalRefundRefusalPreservesSettlement(t *testing.T) {
	f, invoice, paymentID, checkout := reconciliationFixture(t, http.NotFound)
	path := fmt.Sprintf("/api/v1/invoices/%d", invoice)
	before := data(t, request(f.h, "GET", path, nil, f.customer, ""))
	if before["external_payment_paid"] != false {
		t.Fatal("unpaid invoice incorrectly requires manual refund")
	}
	notification := epayNotification(checkout, "refund-boundary-trade")
	expect(t, epayCallback(f.h, "POST", "epay_alipay", notification), 200)
	var balance int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"external-refund-first", "external-refund-first", "external-refund-retry"} {
		w := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, key)
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "external_refund_requires_gateway") || !strings.Contains(w.Body.String(), "人工退款") {
			t.Fatal("missing explicit unsupported refund response", w.Body.String())
		}
	}
	// A repeated merchant notification after the refund refusal must remain idempotent.
	expect(t, epayCallback(f.h, "GET", "epay_alipay", notification), 200)
	for _, actor := range []struct{ prefix, cookie string }{{"", f.customer}, {"/admin", f.root}} {
		detail := data(t, request(f.h, "GET", "/api/v1"+actor.prefix+fmt.Sprintf("/invoices/%d", invoice), nil, actor.cookie, ""))
		if detail["external_payment_paid"] != true || detail["status"] != "paid" || detail["version"] != float64(2) || len(detail["ledger"].([]any)) != 0 {
			t.Fatal("refund refusal changed invoice", detail)
		}
		page := data(t, request(f.h, "GET", "/api/v1"+actor.prefix+"/invoices/page", nil, actor.cookie, ""))
		found := false
		for _, item := range page["items"].([]any) {
			row := item.(map[string]any)
			if row["id"] == float64(invoice) {
				found = row["external_payment_paid"] == true
			}
		}
		if !found {
			t.Fatal("invoice list omitted refund boundary")
		}
	}
	var after int64
	var state string
	var settlements, refunds int
	err := f.db.Pool.QueryRow(t.Context(), `SELECT u.balance_cents,p.status,
 (SELECT count(*) FROM audit_events WHERE entity_type='invoice' AND entity_id=$3::bigint::text AND action='payment.webhook.succeeded'),
 (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$3)
 FROM users u JOIN payment_transactions p ON p.user_id=u.id WHERE u.id=$1 AND p.id=$2`, f.uid, paymentID, invoice).Scan(&after, &state, &settlements, &refunds)
	if err != nil || after != balance || state != "succeeded" || settlements != 1 || refunds != 0 {
		t.Fatal("external refund mutated money or duplicated settlement", after, balance, state, settlements, refunds, err)
	}
}
