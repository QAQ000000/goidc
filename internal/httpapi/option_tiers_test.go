package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestQuantityOptionPriceTiersApplyToCartPromoAndOrder(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "tier-admin", "super_admin")
	_, customer := user(t, db, h, "tier-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("tier-product", nil))
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), map[string]any{"name": "storage", "slug": "storage", "required": true, "option_type": 4, "qty_minimum": 1, "qty_maximum": 6, "options": []map[string]any{{"label": "SSD", "value": "ssd", "active": true}}}, admin, "")
	expect(t, group, 201)
	gid := int64(data(t, group)["id"].(float64))
	oid := configOption(t, h, admin, pid, gid, "ssd")
	revision := configRevision(t, h, admin, pid)
	tiers := map[string]any{"billing_cycle": "monthly", "expected_revision": revision, "tiers": []map[string]int64{
		{"qty_minimum": 1, "qty_maximum": 3, "price_cents": 100, "setup_fee_cents": 7},
		{"qty_minimum": 4, "qty_maximum": 6, "price_cents": 80, "setup_fee_cents": 11},
	}}
	path := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d/price-tiers", pid, gid, oid)
	expect(t, request(h, "PUT", path, tiers, admin, ""), 200)
	product := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, admin, ""))
	option := product["option_groups"].([]any)[0].(map[string]any)["options"].([]any)[0].(map[string]any)
	if len(option["price_tiers"].([]any)) != 2 {
		t.Fatal("admin product did not expose price tiers", option)
	}
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	added := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{oid}, "option_quantities": map[string]int64{fmt.Sprint(oid): 5}}, customer, "")
	expect(t, added, 200)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	item := cart["items"].([]any)[0].(map[string]any)
	// The fixture product is 2000/100; tier 1 contributes 300 and tier 2 contributes 160.
	if item["price_cents"] != float64(2460) || item["setup_fee_cents"] != float64(111) {
		t.Fatal("tier amount not applied", item)
	}
	created := request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "TIER10", "discount_type": "percent", "value": 10}, admin, "")
	expect(t, created, 201)
	checkoutCart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	applied := request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": checkoutCart["version"], "code": "TIER10"}, customer, "")
	expect(t, applied, 200)
	checkoutCart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	line := checkoutCart["items"].([]any)[0].(map[string]any)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(checkoutCart["version"].(float64)), ItemIDs: []int64{int64(line["id"].(float64))}}, customer, "tier-order")
	expect(t, order, 201)
	var price, setup int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents,setup_fee_cents FROM order_items WHERE order_id=$1", int64(data(t, order)["order_id"].(float64))).Scan(&price, &setup); err != nil {
		t.Fatal(err)
	}
	if price != 2460 || setup != 111 {
		t.Fatalf("order did not retain tier amount: %d/%d", price, setup)
	}
	bad := request(h, "PUT", path, map[string]any{"billing_cycle": "monthly", "expected_revision": configRevision(t, h, admin, pid), "tiers": []map[string]int64{{"qty_minimum": 1, "qty_maximum": 4, "price_cents": 1}, {"qty_minimum": 4, "qty_maximum": 6, "price_cents": 2}}}, admin, "")
	expect(t, bad, 409)
	if !strings.Contains(bad.Body.String(), "invalid_option_pricing") {
		t.Fatal("missing invalid tier error", bad.Body.String())
	}
}
