package httpapi

import (
	"fmt"
	"strings"
	"testing"
)

func TestSupplierCatalogEntryReadOnlyAndManageBoundary(t *testing.T) {
	f := supplierSetup(t)
	path := fmt.Sprintf("/api/v1/admin/suppliers/%d", f.supplierID)
	expect(t, request(f.h, "POST", path+"/catalog", nil, f.reader, ""), 403)
	expect(t, request(f.h, "GET", path+"/products/7", nil, f.reader, ""), 403)
	expect(t, request(f.h, "POST", path+"/import", f.in, f.reader, ""), 403)
	catalog := request(f.h, "POST", path+"/catalog", nil, f.root, "")
	expect(t, catalog, 200)
	detail := request(f.h, "GET", path+"/products/7", nil, f.root, "")
	expect(t, detail, 200)
	product := data(t, detail)
	if product["revision"] == "" || product["supported"] != true {
		t.Fatalf("missing import snapshot: %#v", product)
	}
	for _, secret := range []string{"supplier-private-password", "supplier-private-jwt"} {
		if strings.Contains(catalog.Body.String(), secret) || strings.Contains(detail.Body.String(), secret) {
			t.Fatal("supplier secret leaked")
		}
	}
	var products, orders, services int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM supplier_product_bindings),(SELECT count(*) FROM orders),(SELECT count(*) FROM services)`).Scan(&products, &orders, &services); err != nil {
		t.Fatal(err)
	}
	if products != 1 || orders != 0 || services != 0 || f.creates.Load() != 0 || f.pays.Load() != 0 {
		t.Fatalf("read-only directory mutated business state: products=%d orders=%d services=%d creates=%d pays=%d", products, orders, services, f.creates.Load(), f.pays.Load())
	}
	// The existing importer continues to enforce supplier/revision and local price/cost controls.
	stale := f.in
	stale.Slug = "stale-import"
	stale.ExpectedSupplierVersion = 999
	expect(t, request(f.h, "POST", path+"/import", stale, f.root, ""), 409)
	stale = f.in
	stale.Slug = "bad-revision"
	stale.ExpectedUpstreamRevision = "unknown"
	expect(t, request(f.h, "POST", path+"/import", stale, f.root, ""), 409)
	stale = f.in
	stale.Slug = "cheap-import"
	stale.MaximumCostCents = 1
	expect(t, request(f.h, "POST", path+"/import", stale, f.root, ""), 409)
}
