package httpapi

import (
	"fmt"
	"testing"
)

// Cross-migration regression: historical late receipts and unknown close rows
// must remain immutable without becoming permanent false holds after resolution.
func TestPaymentResolutionEffectiveGuardsAcrossWorkflows(t *testing.T) {
	f, invoice, oldPayment, _, anchor := paymentAnomalyFixture(t, "fixture:resolved-original")
	base, create := anomalyPaths(invoice, anchor)
	_, reviewer := anomalyReviewer(t, f)
	w := request(f.h, "POST", create, paymentAnomalyInput(anchor, "cross-resolution-payout"), f.root, "cross-resolution-create")
	expect(t, w, 201)
	refund := data(t, w)["refund"].(map[string]any)
	expect(t, request(f.h, "POST", fmt.Sprintf("%s/payment-anomaly-refunds/%.0f/confirm", base, refund["id"]), map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "独立确认隔离fixture退回证据"}, reviewer, "cross-resolution-confirm"), 201)

	w = request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 2}, f.customer, "cross-resolution-new-intent")
	expect(t, w, 201)
	intent := data(t, w)
	newPayment := int64(intent["payment_id"].(float64))
	actor, session := closeActor(t, f, f.root)
	op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, newPayment, 2, "cross-resolution-new-close", session)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("%s/payments/%d/close-verifications", base, newPayment)
	w = request(f.h, "GET", path, nil, f.root, "")
	expect(t, w, 200)
	if data(t, w)["can_request"] != true {
		t.Fatal("resolved historical anomaly must not block new close lookup", w.Body.String())
	}
	w = request(f.h, "POST", path, map[string]any{"close_operation_id": op.ID, "expected_version": 2}, f.root, "cross-resolution-lookup")
	expect(t, w, 201)
	if data(t, w)["verification"].(map[string]any)["result_status"] != "unknown" {
		t.Fatal("missing mock evidence must stay unknown", w.Body.String())
	}

	if _, err = f.db.ProcessPaymentWebhook(t.Context(), "mock", "cross-resolution-new-success", intent["provider_payment_id"].(string), invoice, 3000, "succeeded", []byte(`{"fixture":"new-payment"}`), "fixture:separate-genuine-receipt"); err != nil {
		t.Fatal(err)
	}
	var invoiceStatus, oldStatus, newStatus, historicalClose string
	var version, events, unresolved, originalUnknown int64
	err = f.db.Pool.QueryRow(t.Context(), `SELECT i.status,i.version,old.status,p.status,c.status,
 (SELECT count(*) FROM payment_close_anomalies WHERE invoice_id=i.id),
 (SELECT count(*) FROM payment_close_anomalies WHERE invoice_id=i.id AND payment_close_anomaly_unresolved(id)),
 (SELECT count(*) FROM payment_close_operations WHERE payment_id=p.id AND payment_close_operation_unresolved(id))
 FROM invoices i JOIN payment_transactions old ON old.id=$2 JOIN payment_transactions p ON p.id=$3 JOIN payment_close_operations c ON c.id=$4 WHERE i.id=$1`, invoice, oldPayment, newPayment, op.ID).Scan(&invoiceStatus, &version, &oldStatus, &newStatus, &historicalClose, &events, &unresolved, &originalUnknown)
	if err != nil || invoiceStatus != "paid" || version != 3 || oldStatus != "closed" || newStatus != "succeeded" || historicalClose != "unknown" || events != 1 || unresolved != 0 || originalUnknown != 0 {
		t.Fatal(invoiceStatus, version, oldStatus, newStatus, historicalClose, events, unresolved, originalUnknown, err)
	}
	w = request(f.h, "GET", base, nil, f.root, "")
	expect(t, w, 200)
	if data(t, w)["payment_close_anomaly"] != false {
		t.Fatal("history must not project as unresolved", w.Body.String())
	}
	for _, sql := range []string{"UPDATE payment_close_operations SET result_code='rewritten' WHERE id=$1", "DELETE FROM payment_close_operations WHERE id=$1"} {
		if _, err = f.db.Pool.Exec(t.Context(), sql, op.ID); err == nil {
			t.Fatal("historical unknown changed", sql)
		}
	}
}
