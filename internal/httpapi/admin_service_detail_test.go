package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestAdminServiceDetailIncludesRelationsWithoutSupplierSecrets(t *testing.T) {
	f := supplierSetup(t)
	f.publish(t, f.pid, "detail-supplier-vm")
	sid, invoice := f.purchase(t, f.pid, "detail-supplier-order")
	path := fmt.Sprintf("/api/v1/admin/services/%d", sid)
	w := request(f.h, "GET", path, nil, f.root, "")
	expect(t, w, 200)
	detail := data(t, w)
	if detail["product_name"] != "Local VM" || detail["supplier_name"] != "Supplier" || detail["upstream_product_id"] != float64(7) || detail["invoice_id"] != float64(invoice) || detail["invoice_status"] != "paid" || detail["order_no"] == "" {
		t.Fatalf("missing service relations: product=%v supplier=%v invoice=%v", detail["product_name"], detail["supplier_name"], detail["invoice_id"])
	}
	for _, secret := range []string{"supplier-private-password", "credentials_encrypted", "delivery_snapshot", "private-jwt"} {
		if strings.Contains(w.Body.String(), secret) {
			t.Fatal("supplier secret included in service detail")
		}
	}
	expect(t, request(f.h, "GET", path, nil, f.reader, ""), 200)
	expect(t, request(f.h, "GET", path, nil, f.customer, ""), 401)
	w = request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, f.customer, "")
	expect(t, w, 200)
	for _, field := range []string{"supplier_name", "upstream_product_id", "supplier_running_job_id", "first_price_cents", "invoice_id"} {
		if _, exposed := data(t, w)[field]; exposed {
			t.Fatalf("admin relation exposed to customer: %s", field)
		}
	}
}
