package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
)

func TestSupplierUnattendedInvoiceRecoveryPaymentAndActivation(t *testing.T) {
	f := supplierSetup(t)
	f.invoiceOnly.Store(true)
	f.paymentResponseIncomplete.Store(true)
	f.remoteStatus.Store("Pending")
	f.publish(t, f.pid, "supplier-unattended")
	sid, _ := f.purchase(t, f.pid, "supplier-unattended-order")
	j := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *j); err != nil {
		t.Fatal(err)
	}
	state, service, lease := f.state(t, j)
	if state != "succeeded" || service != "provisioning" || lease != nil || f.creates.Load() != 1 || f.pays.Load() != 1 {
		t.Fatal("unattended purchase did not finish", state, service, lease, f.creates.Load(), f.pays.Load())
	}
	var resource string
	var invoice, cost int64
	var pending int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT s.upstream_id,(j.result->>'invoice_id')::bigint,(j.result->>'cost_cents')::bigint,
 (SELECT count(*) FROM provision_jobs WHERE service_id=s.id AND action='sync' AND status='pending')
 FROM services s JOIN provision_jobs j ON j.service_id=s.id AND j.id=$2 WHERE s.id=$1`, sid, j.ID).Scan(&resource, &invoice, &cost, &pending); err != nil || resource != "42" || invoice != 90 || cost != 1700 || pending != 1 {
		t.Fatal("purchase identity/cost/follow-up missing", resource, invoice, cost, pending, err)
	}
	f.remoteStatus.Store("Active")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET available_at=now() WHERE service_id=$1 AND action='sync'", sid); err != nil {
		t.Fatal(err)
	}
	sync := f.claim(t)
	if err := f.db.ProcessProvisionJob(t.Context(), *sync); err != nil {
		t.Fatal(err)
	}
	state, service, lease = f.state(t, sync)
	if state != "succeeded" || service != "active" || lease != nil || f.creates.Load() != 1 || f.pays.Load() != 1 {
		t.Fatal("activation repeated purchase/payment", state, service, lease, f.creates.Load(), f.pays.Load())
	}
	for _, request := range []struct {
		path, code string
		body       any
	}{
		{"configuration-quotes", "unsupported_supplier_upgrade", map[string]any{"expected_version": 1, "option_ids": []int{}}},
		{"product-quotes", "unsupported_supplier_upgrade", map[string]any{"expected_version": 1, "target_product_id": f.pid}},
		{"cancellation", "unsupported_supplier_cancellation", map[string]any{"type": "immediate", "reason": "上游取消明确拒绝"}},
	} {
		w := requestHTTPBoundary(t, f, sid, request.path, request.body)
		if w != request.code {
			t.Fatal("wrong unsupported action", request.path, w)
		}
	}
}

func requestHTTPBoundary(t *testing.T, f *supplierFixture, service int64, action string, body any) string {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/%s", service, action), body, f.customer, "unattended-refuse-"+action)
	expect(t, w, 409)
	var response struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	return response.Error.Code
}
