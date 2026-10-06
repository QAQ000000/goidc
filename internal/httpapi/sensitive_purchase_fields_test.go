package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestSensitivePurchaseFieldsStoragePreservationAndPrivacy(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	fields := []store.PurchaseField{{Key: "secret", Label: "敏感资料", Type: "text", Active: true, Required: true, MaxLength: 255, Encrypted: true, ShowInvoice: true}}
	path := fmt.Sprintf("/api/v1/admin/products/%d/purchase-fields", f.pid)
	expect(t, request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Fields: fields}, f.root, ""), 409)
	f.db.PurchaseFieldEncryptionKey = []byte("01234567890123456789012345678901")
	savePurchaseDefinitions(t, f, fields)
	secret := "isolated-sensitive-example"
	version, _ := cart(t, f.h, f.customer)
	input := map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "option_ids": []int64{f.small}, "purchase_fields": map[string]string{"secret": secret}}
	expect(t, request(f.h, "POST", "/api/v1/cart/items", input, f.customer, ""), 200)
	version, item := cart(t, f.h, f.customer)
	var sealed string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT purchase_fields->>'secret' FROM cart_items WHERE id=$1", item).Scan(&sealed); err != nil || !strings.HasPrefix(sealed, "enc:v2:") || strings.Contains(sealed, secret) {
		t.Fatal("cart value not encrypted", err)
	}
	input["version"] = version
	expect(t, request(f.h, "POST", "/api/v1/cart/items", input, f.customer, ""), 200)
	w := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
	expect(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 1 || strings.Contains(w.Body.String(), secret) || strings.Contains(w.Body.String(), "enc:v2:") {
		t.Fatal("unstable identity or leaked value")
	}
	version, item = cart(t, f.h, f.customer)
	input["version"] = version
	input["purchase_fields"] = map[string]string{"secret": sealed}
	expect(t, request(f.h, "POST", "/api/v1/cart/items", input, f.customer, ""), 409)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", item), map[string]any{"version": version, "quantity": 1, "purchase_fields": map[string]string{"secret": "********"}}, f.customer, ""), 200)
	version, _ = cart(t, f.h, f.customer)
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", item), map[string]any{"version": version, "quantity": 1}, f.customer, ""), 200)
	version, _ = cart(t, f.h, f.customer)
	checkout := store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}
	key := f.db.PurchaseFieldEncryptionKey
	f.db.PurchaseFieldEncryptionKey = nil
	expect(t, request(f.h, "POST", "/api/v1/orders", checkout, f.customer, "sensitive-unconfigured"), 409)
	f.db.PurchaseFieldEncryptionKey = key
	out := request(f.h, "POST", "/api/v1/orders", checkout, f.customer, "sensitive-checkout")
	expect(t, out, 201)
	oid, iid := int64(data(t, out)["order_id"].(float64)), int64(data(t, out)["invoice_id"].(float64))
	expect(t, request(f.h, "POST", "/api/v1/orders", checkout, f.customer, "sensitive-checkout"), 200)
	var snapshot string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT purchase_fields::text FROM order_items WHERE order_id=$1", oid).Scan(&snapshot); err != nil || !strings.Contains(snapshot, "enc:v2:") || strings.Contains(snapshot, secret) {
		t.Fatal("order storage not encrypted", err)
	}
	var sid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", oid).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []struct{ path, cookie string }{{fmt.Sprintf("/api/v1/orders/%d", oid), f.customer}, {fmt.Sprintf("/api/v1/admin/orders/%d", oid), f.root}, {fmt.Sprintf("/api/v1/invoices/%d", iid), f.customer}, {fmt.Sprintf("/api/v1/services/%d", sid), f.customer}, {fmt.Sprintf("/api/v1/admin/services/%d", sid), f.root}} {
		response := request(f.h, "GET", endpoint.path, nil, endpoint.cookie, "")
		expect(t, response, 200)
		if strings.Contains(response.Body.String(), secret) || strings.Contains(response.Body.String(), "enc:v2:") {
			t.Fatalf("sensitive projection leaked at %s", endpoint.path)
		}
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/orders/%d", oid), nil, f.other, ""), 404)
	// Reserved incomplete scopes must fail as business errors rather than SQL constraint errors.
	for _, scope := range []string{"admin", "customer"} {
		deferred := fields[0]
		deferred.Scope = scope
		deferred.Required = false
		deferred.ShowInvoice = false
		if scope == "customer" {
			deferred.ProfileKey = "company"
		}
		expect(t, request(f.h, "PUT", path, store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, f.h, f.root, f.pid), Fields: []store.PurchaseField{deferred}}, f.root, ""), 409)
	}
}

func TestSensitivePurchaseFieldsRejectCopiedOwnerCiphertext(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.db.PurchaseFieldEncryptionKey = []byte("01234567890123456789012345678901")
	savePurchaseDefinitions(t, f, []store.PurchaseField{{Key: "secret", Label: "Secret", Type: "text", Active: true, Required: true, MaxLength: 255, Encrypted: true}})
	version, _ := cart(t, f.h, f.customer)
	expect(t, request(f.h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": f.pid, "quantity": 1, "option_ids": []int64{f.small}, "purchase_fields": map[string]string{"secret": "private"}}, f.customer, ""), 200)
	_, item := cart(t, f.h, f.customer)
	var values map[string]string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT purchase_fields FROM cart_items WHERE id=$1", item).Scan(&values); err != nil {
		t.Fatal(err)
	}
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, _, err = store.ValidateStoredPurchaseFields(t.Context(), tx, f.uid+1000, f.pid, values, f.db.PurchaseFieldEncryptionKey); err == nil {
		t.Fatal("copied owner value accepted")
	}
	if !strings.HasPrefix(values["secret"], "enc:v2:") {
		t.Fatal("validator mutated stored map")
	}
}
