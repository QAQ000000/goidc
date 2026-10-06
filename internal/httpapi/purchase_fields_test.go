package httpapi

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func purchaseDefinitions() []store.PurchaseField {
	return []store.PurchaseField{
		{Key: "hostname", Label: "主机名称", Type: "text", Required: true, Active: true, ShowInvoice: true, MaxLength: 40, Pattern: `[a-z][a-z0-9-]*`, SortOrder: 0},
		{Key: "purpose", Label: "部署用途", Type: "textarea", Active: true, MaxLength: 255, SortOrder: 1},
		{Key: "region", Label: "部署地区", Type: "select", Required: true, Active: true, MaxLength: 40, Choices: []string{"上海", "广州"}, SortOrder: 2},
		{Key: "accepted", Label: "已确认用途", Type: "checkbox", Required: true, Active: true, MaxLength: 20, SortOrder: 3},
	}
}
func savePurchaseDefinitions(t *testing.T, f upgradeFixture, fields []store.PurchaseField) {
	t.Helper()
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/purchase-fields", f.pid), store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Fields: fields}, f.root, ""), 200)
}

func TestPurchaseFieldsVariantsSnapshotInvoicePrivacyAndCopy(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	savePurchaseDefinitions(t, f, purchaseDefinitions())
	values := map[string]string{"hostname": "web-one", "purpose": "private purpose", "region": "上海", "accepted": "true"}
	version, _ := cart(t, f.h, f.customer)
	input := map[string]any{"version": version, "product_id": f.pid, "quantity": 2, "option_ids": []int64{f.small}, "purchase_fields": values}
	expect(t, request(f.h, "POST", "/api/v1/cart/items", input, f.customer, ""), 200)
	version, first := cart(t, f.h, f.customer)
	values2 := map[string]string{"hostname": "web-two", "purpose": "private purpose", "region": "上海", "accepted": "true"}
	input["version"] = version
	input["purchase_fields"] = values2
	input["quantity"] = 1
	expect(t, request(f.h, "POST", "/api/v1/cart/items", input, f.customer, ""), 200)
	w := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
	expect(t, w, 200)
	d := data(t, w)
	if len(d["items"].([]any)) != 2 {
		t.Fatal("different purchase values merged", d)
	}
	items := []int64{}
	for _, entry := range d["items"].([]any) {
		items = append(items, int64(entry.(map[string]any)["id"].(float64)))
	}
	version = int64(d["version"].(float64))
	// Quantity-only updates preserve values; changing to an existing variant conflicts.
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", first), map[string]any{"version": version, "quantity": 2}, f.customer, ""), 200)
	version, _ = cart(t, f.h, f.customer)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", first), map[string]any{"version": version, "quantity": 2, "purchase_fields": values2}, f.customer, ""), 409)
	w = request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: items}, f.customer, "purchase-fields-checkout")
	expect(t, w, 201)
	oid, iid := int64(data(t, w)["order_id"].(float64)), int64(data(t, w)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: items}, f.customer, "purchase-fields-checkout"), 200)
	order := request(f.h, "GET", fmt.Sprintf("/api/v1/orders/%d", oid), nil, f.customer, "")
	expect(t, order, 200)
	if !strings.Contains(order.Body.String(), "private purpose") {
		t.Fatal("order lost purchase snapshot")
	}
	adminOrder := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/orders/%d", oid), nil, f.root, "")
	expect(t, adminOrder, 200)
	if !strings.Contains(adminOrder.Body.String(), "private purpose") {
		t.Fatal("administrator order lost purchase snapshot")
	}
	invoice := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", iid), nil, f.customer, "")
	expect(t, invoice, 200)
	if strings.Contains(invoice.Body.String(), "private purpose") || strings.Contains(invoice.Body.String(), "已确认用途") || !strings.Contains(invoice.Body.String(), "web-one") {
		t.Fatal("invoice exposure setting ignored", invoice.Body.String())
	}
	// Current catalog edits do not mutate purchased names or values.
	changed := purchaseDefinitions()
	changed[0].Label = "新的名称"
	changed[1].Active = false
	savePurchaseDefinitions(t, f, changed)
	var sid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT min(id) FROM services WHERE order_id=$1", oid).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	w = request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, f.customer, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), "private purpose") || !strings.Contains(w.Body.String(), "主机名称") || strings.Contains(w.Body.String(), "新的名称") {
		t.Fatal("service snapshot changed", w.Body.String())
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/services/%d", sid), nil, f.other, ""), 404)
	copy := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", f.pid), map[string]any{"name": "字段副本", "slug": "purchase-fields-copy"}, f.root, "")
	expect(t, copy, 201)
	copyID := int64(data(t, copy)["id"].(float64))
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_purchase_fields WHERE product_id=$1", copyID).Scan(&count); err != nil || count != 4 {
		t.Fatal("definition copy missing", count, err)
	}
	w = request(f.h, "GET", fmt.Sprintf("/api/v1/catalog/%d", f.pid), nil, "", "")
	expect(t, w, 200)
	if strings.Contains(w.Body.String(), "private purpose") || len(data(t, w)["purchase_field_definitions"].([]any)) != 3 {
		t.Fatal("catalog exposed values or inactive fields")
	}
}

func TestPurchaseFieldsRequiredRevisionPermissionsAndRollback(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	fields := purchaseDefinitions()
	savePurchaseDefinitions(t, f, fields)
	version, _ := cart(t, f.h, f.customer)
	expect(t, request(f.h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "option_ids": []int64{f.small}}, f.customer, ""), 409)
	_, reader := user(t, f.db, f.h, "purchase-fields-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='purchase-fields-reader-role'"); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/products/%d/purchase-fields", f.pid)
	expect(t, request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Fields: fields}, reader, ""), 403)
	expect(t, request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: 1, Fields: fields}, f.root, ""), 409)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_fields_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='product.purchase_fields.updated' THEN RAISE EXCEPTION 'reject audit'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_fields_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_fields_audit()`); err != nil {
		t.Fatal(err)
	}
	revision := *configRevision(t, f.h, f.root, f.pid)
	expect(t, request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: revision, Fields: []store.PurchaseField{}}, f.root, ""), 500)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_purchase_fields WHERE product_id=$1", f.pid).Scan(&count); err != nil || count != 4 || *configRevision(t, f.h, f.root, f.pid) != revision {
		t.Fatal("failed audit replaced fields or revision", count, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_fields_audit"); err != nil {
		t.Fatal(err)
	}
	// Catalog updates serialize and reject the loser with the stale revision.
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: revision, Fields: fields}, f.root, "").Code
		}()
	}
	wg.Wait()
	close(codes)
	seen := map[int]int{}
	for code := range codes {
		seen[code]++
	}
	if seen[200] != 1 || seen[409] != 1 {
		t.Fatal("concurrent overwrite", seen)
	}
	values := map[string]string{"hostname": "stale-host", "region": "上海", "accepted": "true"}
	version, _ = cart(t, f.h, f.customer)
	expect(t, request(f.h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "option_ids": []int64{f.small}, "purchase_fields": values}, f.customer, ""), 200)
	version, item := cart(t, f.h, f.customer)
	fields[0].Pattern = "new-[a-z]+"
	savePurchaseDefinitions(t, f, fields)
	w := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
	expect(t, w, 200)
	if data(t, w)["items"].([]any)[0].(map[string]any)["fields_valid"] != false {
		t.Fatal("cart did not flag invalid fields")
	}
	expect(t, request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, f.customer, "stale-field-checkout"), 409)
}
