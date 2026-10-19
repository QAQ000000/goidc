package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func purchasePricingGroup(t *testing.T, f mutationOrderFixture, name string, percent int) int64 {
	t.Helper()
	w := request(f.h, "POST", "/api/v1/admin/customer-groups", map[string]any{"name": name, "color": "#123456", "discount_percent": percent}, f.root, "pricing-group-"+name)
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}

func purchasePricingMembership(t *testing.T, f mutationOrderFixture, group int64) {
	t.Helper()
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/group", f.customerID), map[string]any{"group_id": group}, f.root, ""), 200)
}

func purchasePricingRule(t *testing.T, f mutationOrderFixture, group int64, name, kind string, value int64) int64 {
	t.Helper()
	w := request(f.h, "POST", "/api/v1/admin/customer-product-groups", map[string]any{"name": name, "product_ids": []int64{f.productID}}, f.root, "pricing-product-group-"+name)
	expect(t, w, 201)
	productGroup := int64(data(t, w)["id"].(float64))
	in := map[string]any{"customer_group_id": group, "product_group_id": productGroup, "discount_type": kind}
	if kind == "percent" {
		in["discount_percent"] = value
	} else {
		in["fixed_discount_cents"] = value
	}
	expect(t, request(f.h, "PUT", "/api/v1/admin/customer-product-discounts", in, f.root, "pricing-rule-"+name), 200)
	return productGroup
}

func purchasePricingQuote(t *testing.T, f mutationOrderFixture, version int64, ids ...int64) (map[string]any, store.CheckoutInput) {
	t.Helper()
	in := store.CheckoutInput{CartVersion: version, ItemIDs: ids}
	w := request(f.h, "POST", "/api/v1/cart/quote", in, f.customer, "")
	expect(t, w, 200)
	out := data(t, w)
	var ok bool
	in.QuoteHash, ok = out["quote_hash"].(string)
	if !ok || len(in.QuoteHash) != 64 {
		t.Fatal("quote did not bind its current pricing inputs", out)
	}
	return out, in
}

func purchasePricingLine(t *testing.T, value any) store.PurchasePricingLine {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var line store.PurchasePricingLine
	if err = json.Unmarshal(encoded, &line); err != nil {
		t.Fatal(err)
	}
	return line
}

func expectPurchasePricingAmounts(t *testing.T, out map[string]any, gross, group, promo, net int64) {
	t.Helper()
	for name, want := range map[string]int64{"group_discount_cents": group, "promo_discount_cents": promo, "discount_cents": group + promo, "total_cents": net} {
		if out[name] != float64(want) {
			t.Fatalf("%s=%v want=%d, result=%v", name, out[name], want, out)
		}
	}
	if subtotal, ok := out["subtotal_cents"]; ok && subtotal != float64(gross) {
		t.Fatalf("subtotal=%v want=%d", subtotal, gross)
	}
	if gross != group+promo+net {
		t.Fatal("test amounts do not conserve cents")
	}
}

func expectSavedPurchasePricing(t *testing.T, f mutationOrderFixture, order int64, want store.PurchasePricingLine) {
	t.Helper()
	var encoded []byte
	var gross, group, promo, net, invoice int64
	var services int
	var recurringMin, recurringMax int64
	err := f.db.Pool.QueryRow(t.Context(), `SELECT p.snapshot,p.gross_cents,p.group_discount_cents,p.promo_discount_cents,p.net_cents,i.amount_cents,
 (SELECT count(*) FROM services WHERE order_id=o.id),
 (SELECT min(recurring_price_cents) FROM services WHERE order_id=o.id),
 (SELECT max(recurring_price_cents) FROM services WHERE order_id=o.id)
 FROM order_item_pricing p JOIN order_items oi ON oi.id=p.order_item_id JOIN orders o ON o.id=oi.order_id JOIN invoices i ON i.order_id=o.id WHERE o.id=$1`, order).Scan(&encoded, &gross, &group, &promo, &net, &invoice, &services, &recurringMin, &recurringMax)
	if err != nil {
		t.Fatal(err)
	}
	stored := purchasePricingLine(t, json.RawMessage(encoded))
	if gross != want.GrossCents || group != want.GroupDiscountCents || promo != want.PromoDiscountCents || net != want.NetCents || invoice != net || services != want.Quantity || recurringMin != want.RecurringCents || recurringMax != want.RecurringCents {
		t.Fatal("invoice, services, and line pricing diverged", gross, group, promo, net, invoice, services, recurringMin, recurringMax, want)
	}
	if stored.ItemID < 1 || stored.ProductID != want.ProductID || stored.Quantity != want.Quantity || stored.GrossCents != gross || stored.GroupDiscountCents != group || stored.PromoDiscountCents != promo || stored.NetCents != net || stored.RecurringCents != want.RecurringCents || stored.GroupSnapshot != want.GroupSnapshot || stored.PromoCode != want.PromoCode || stored.PromoVersion != want.PromoVersion {
		t.Fatal("immutable snapshot differs from accepted quote", stored, want)
	}
}

func TestCustomerGroupPricingDefaultQuoteCheckoutAndReadModels(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Default pricing", 20)
	purchasePricingMembership(t, f, g)
	v, item := add(t, f.h, f.customer, f.productID, 2)
	quote, in := purchasePricingQuote(t, f, v, item)
	expectPurchasePricingAmounts(t, quote, 2666, 532, 0, 2134)
	line := purchasePricingLine(t, quote["items"].([]any)[0])
	if line.GroupSnapshot.Source != "customer_group" || line.GroupSnapshot.CustomerGroupID != g || line.GroupSnapshot.DiscountPercent != 20 || line.RecurringCents != 988 {
		t.Fatal("default rule or recurring basis missing", line)
	}
	checkout := request(f.h, "POST", "/api/v1/orders", in, f.customer, "group-default-checkout")
	expect(t, checkout, 201)
	result := data(t, checkout)
	expectPurchasePricingAmounts(t, result, 2666, 532, 0, 2134)
	order := int64(result["order_id"].(float64))
	expectSavedPurchasePricing(t, f, order, line)
	for _, read := range []struct {
		path, cookie, items string
	}{
		{fmt.Sprintf("/api/v1/orders/%d", order), f.customer, "items"},
		{fmt.Sprintf("/api/v1/admin/orders/%d", order), f.root, "items"},
		{fmt.Sprintf("/api/v1/invoices/%d", int64(result["invoice_id"].(float64))), f.customer, "order_items"},
	} {
		w := request(f.h, "GET", read.path, nil, read.cookie, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["group_discount_cents"] != float64(532) || d["promo_discount_cents"] != float64(0) {
			t.Fatal("saved discount split missing from read model", read.path, d)
		}
		stored := purchasePricingLine(t, d[read.items].([]any)[0].(map[string]any)["pricing"])
		if stored.GroupDiscountCents != 532 || stored.NetCents != 2134 || stored.GroupSnapshot.CustomerGroupID != g {
			t.Fatal("read model did not retain purchase pricing", read.path, stored)
		}
	}
}

func TestCustomerGroupPricingProductRuleOverridesAndRemovalFallsBack(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Product priority", 50)
	purchasePricingMembership(t, f, g)
	pg := purchasePricingRule(t, f, g, "Lower product reduction", "percent", 10)
	v, item := add(t, f.h, f.customer, f.productID, 1)
	quote, _ := purchasePricingQuote(t, f, v, item)
	expectPurchasePricingAmounts(t, quote, 1333, 133, 0, 1200)
	line := purchasePricingLine(t, quote["items"].([]any)[0])
	if line.GroupSnapshot.Source != "product_group" || line.GroupSnapshot.ProductGroupID != pg || line.RecurringCents != 1111 {
		t.Fatal("product rule failed to override the more generous default", line)
	}
	expect(t, request(f.h, "PUT", "/api/v1/admin/customer-product-discounts", map[string]any{"customer_group_id": g, "product_group_id": pg, "discount_type": "none", "expected_version": 1}, f.root, "pricing-rule-clear"), 200)
	quote, _ = purchasePricingQuote(t, f, v, item)
	expectPurchasePricingAmounts(t, quote, 1333, 666, 0, 667)
	line = purchasePricingLine(t, quote["items"].([]any)[0])
	if line.GroupSnapshot.Source != "customer_group" || line.RecurringCents != 617 {
		t.Fatal("clearing a product rule did not restore the default", line)
	}
	purchasePricingRule(t, f, g, "Product ten", "percent", 10)
	winner := purchasePricingRule(t, f, g, "Product thirty", "percent", 30)
	quote, _ = purchasePricingQuote(t, f, v, item)
	expectPurchasePricingAmounts(t, quote, 1333, 399, 0, 934)
	line = purchasePricingLine(t, quote["items"].([]any)[0])
	if line.GroupSnapshot.ProductGroupID != winner {
		t.Fatal("overlapping product groups did not choose the greatest actual reduction", line)
	}
}

func TestCustomerGroupPricingFixedReductionPerConfiguredService(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		reduction, net, recurring int64
	}{
		{"recurring-first", 500, 4059, 1234},
		{"includes-setup", 1800, 159, 0},
		{"capped-at-unit-gross", 10000, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			g := purchasePricingGroup(t, f, "Configured fixed", 5)
			purchasePricingMembership(t, f, g)
			purchasePricingRule(t, f, g, "Configured services", "fixed", tc.reduction)
			og := configGroup(t, f.h, f.root, f.productID, "pricing-config", true, []store.ProductOptionInput{{Label: "加配", Value: "extra", PriceDeltaCents: 500, SetupDeltaCents: 20, Active: enabled()}})
			option := configOption(t, f.h, f.root, f.productID, og, "extra")
			v, item := configuredCart(t, f.h, f.customer, f.productID, []int64{option}, 3)
			quote, in := purchasePricingQuote(t, f, v, item)
			// Each service has 1734 recurring + 119 setup; the fixed reduction is per service.
			expectPurchasePricingAmounts(t, quote, 5559, 5559-tc.net, 0, tc.net)
			line := purchasePricingLine(t, quote["items"].([]any)[0])
			if line.RecurringCents != tc.recurring || line.Quantity != 3 {
				t.Fatal("fixed discount was multiplied or split incorrectly", line)
			}
			w := request(f.h, "POST", "/api/v1/orders", in, f.customer, "pricing-fixed-checkout")
			expect(t, w, 201)
			expectSavedPurchasePricing(t, f, int64(data(t, w)["order_id"].(float64)), line)
		})
	}
}

func TestCustomerGroupPricingScopedCouponStacksAfterGroupReduction(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Stackable group", 20)
	purchasePricingMembership(t, f, g)
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "GROUP-STACK", "discount_type": "percent", "value": 10, "applies_customer_group_ids": []int64{g}}, f.root, "pricing-coupon-stack"), 201)
	v, item := add(t, f.h, f.customer, f.productID, 2)
	w := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "GROUP-STACK"}, f.customer, "")
	expect(t, w, 200)
	v = int64(data(t, w)["version"].(float64))
	quote, in := purchasePricingQuote(t, f, v, item)
	// 2666 - 532 = 2134; 10% of that remaining amount is 213 cents.
	expectPurchasePricingAmounts(t, quote, 2666, 532, 213, 1921)
	line := purchasePricingLine(t, quote["items"].([]any)[0])
	if line.PromoCode != "GROUP-STACK" || line.PromoVersion != 1 || len(line.PromoCustomerGroupIDs) != 1 || line.PromoCustomerGroupIDs[0] != g || line.RecurringCents != 988 {
		t.Fatal("coupon source or one-off renewal boundary missing", line)
	}
	w = request(f.h, "POST", "/api/v1/orders", in, f.customer, "pricing-stack-checkout")
	expect(t, w, 201)
	expectPurchasePricingAmounts(t, data(t, w), 2666, 532, 213, 1921)
	expectSavedPurchasePricing(t, f, int64(data(t, w)["order_id"].(float64)), line)
	var redeemed, uses int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT r.discount_cents,p.used_count FROM promo_code_redemptions r JOIN promo_codes p ON p.id=r.promo_code_id WHERE p.code='GROUP-STACK'`).Scan(&redeemed, &uses); err != nil || redeemed != 213 || uses != 1 {
		t.Fatal("redemption included the group discount", redeemed, uses, err)
	}
}

func TestCustomerGroupPricingCouponRevalidatedAfterMembershipChange(t *testing.T) {
	for _, tc := range []struct{ name, code string }{
		{"unscoped", "promo_group_discount_incompatible"},
		{"other-group", "promo_customer_group_ineligible"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			g := purchasePricingGroup(t, f, "Current reduction", 20)
			input := map[string]any{"code": "CHANGED-GROUP", "discount_type": "percent", "value": 10}
			if tc.name == "other-group" {
				allowed := purchasePricingGroup(t, f, "Coupon eligible", 0)
				purchasePricingMembership(t, f, allowed)
				input["applies_customer_group_ids"] = []int64{allowed}
			}
			expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", input, f.root, "pricing-rejected-coupon"), 201)
			v, item := add(t, f.h, f.customer, f.productID, 1)
			w := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "CHANGED-GROUP"}, f.customer, "")
			expect(t, w, 200)
			v = int64(data(t, w)["version"].(float64))
			purchasePricingMembership(t, f, g)
			for _, path := range []string{"/api/v1/cart/quote", "/api/v1/orders"} {
				w = request(f.h, "POST", path, store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, f.customer, "pricing-rejected-checkout")
				expect(t, w, 409)
				if errorData(t, w)["code"] != tc.code {
					t.Fatal("coupon restriction was silently dropped", path, w.Body.String())
				}
			}
			w = request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "CHANGED-GROUP"}, f.customer, "")
			expect(t, w, 409)
			if errorData(t, w)["code"] != tc.code {
				t.Fatal("applying an incompatible coupon did not fail", w.Body.String())
			}
			var orders, redemptions, stock, uses, items int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders),(SELECT count(*) FROM promo_code_redemptions),stock,(SELECT used_count FROM promo_codes WHERE code='CHANGED-GROUP'),(SELECT count(*) FROM cart_items) FROM products WHERE id=$1`, f.productID).Scan(&orders, &redemptions, &stock, &uses, &items); err != nil || orders != 0 || redemptions != 0 || stock != 12 || uses != 0 || items != 1 {
				t.Fatal("rejected pricing changed financial or inventory state", orders, redemptions, stock, uses, items, err)
			}
		})
	}
}

func TestCustomerGroupPricingPartialCartQuoteMatchesCheckout(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Selected pricing", 20)
	purchasePricingMembership(t, f, g)
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "SELECTED-101", "discount_type": "fixed", "value": 101, "applies_customer_group_ids": []int64{g}}, f.root, "pricing-partial-coupon"), 201)
	_, selected := add(t, f.h, f.customer, f.productID, 1)
	other := product(t, f.db, 10)
	v, _ := add(t, f.h, f.customer, other, 2)
	w := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "SELECTED-101"}, f.customer, "")
	expect(t, w, 200)
	v = int64(data(t, w)["version"].(float64))
	quote, in := purchasePricingQuote(t, f, v, selected)
	expectPurchasePricingAmounts(t, quote, 1333, 266, 101, 966)
	lines := quote["items"].([]any)
	if len(lines) != 1 || purchasePricingLine(t, lines[0]).ProductID != f.productID {
		t.Fatal("quote included unselected products", quote)
	}
	w = request(f.h, "POST", "/api/v1/orders", in, f.customer, "pricing-partial-checkout")
	expect(t, w, 201)
	expectPurchasePricingAmounts(t, data(t, w), 1333, 266, 101, 966)
	expectSavedPurchasePricing(t, f, int64(data(t, w)["order_id"].(float64)), purchasePricingLine(t, lines[0]))
	remaining := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
	expect(t, remaining, 200)
	items := data(t, remaining)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["product_id"] != float64(other) || items[0].(map[string]any)["quantity"] != float64(2) {
		t.Fatal("partial checkout consumed unselected services", remaining.Body.String())
	}
}

func TestCustomerGroupPricingStaleQuoteAndCommittedReplay(t *testing.T) {
	f := newMutationOrderFixture(t)
	first := purchasePricingGroup(t, f, "Old membership", 20)
	second := purchasePricingGroup(t, f, "New membership", 40)
	purchasePricingMembership(t, f, first)
	v, item := add(t, f.h, f.customer, f.productID, 1)
	_, old := purchasePricingQuote(t, f, v, item)
	purchasePricingMembership(t, f, second)
	w := request(f.h, "POST", "/api/v1/orders", old, f.customer, "pricing-stale-checkout")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "stale_quote" {
		t.Fatal("membership change did not invalidate quote", w.Body.String())
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM orders").Scan(&count); err != nil || count != 0 {
		t.Fatal("stale quote committed an order", count, err)
	}
	quote, current := purchasePricingQuote(t, f, v, item)
	expectPurchasePricingAmounts(t, quote, 1333, 533, 0, 800)
	w = request(f.h, "POST", "/api/v1/orders", current, f.customer, "pricing-committed-checkout")
	expect(t, w, 201)
	created := data(t, w)
	purchasePricingMembership(t, f, first)
	w = request(f.h, "POST", "/api/v1/orders", current, f.customer, "pricing-committed-checkout")
	expect(t, w, 200)
	replayed := data(t, w)
	expectPurchasePricingAmounts(t, replayed, 1333, 533, 0, 800)
	if replayed["replayed"] != true || replayed["order_id"] != created["order_id"] {
		t.Fatal("replay recalculated a completed purchase", replayed, created)
	}
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM orders").Scan(&count); err != nil || count != 1 {
		t.Fatal("successful replay duplicated the order", count, err)
	}
}

func TestCustomerGroupPricingAdminPriceOverridesBypassGroupDiscount(t *testing.T) {
	first, renewal := int64(700), int64(333)
	for _, tc := range []struct {
		name             string
		first, recurring *int64
		total, renewal   int64
	}{
		{"first-only", &first, nil, 700, 1234},
		{"renewal-only", nil, &renewal, 1333, 333},
		{"both", &first, &renewal, 700, 333},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			g := purchasePricingGroup(t, f, "Admin override", 50)
			purchasePricingMembership(t, f, g)
			in := adminOrderDraft(t, f)
			in.Items[0].FirstPriceOverride, in.Items[0].RenewalPriceOverride = tc.first, tc.recurring
			w := request(f.h, "POST", "/api/v1/admin/orders/quote", in, f.root, "")
			expect(t, w, 200)
			quote := data(t, w)
			expectPurchasePricingAmounts(t, quote, tc.total, 0, 0, tc.total)
			line := purchasePricingLine(t, quote["items"].([]any)[0].(map[string]any)["pricing"])
			if line.GroupSnapshot.Source != "override" || line.RecurringCents != tc.renewal {
				t.Fatal("internal pricing inherited group reduction", line)
			}
			in.QuoteHash = quote["quote_hash"].(string)
			w = request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "pricing-admin-override")
			expect(t, w, 201)
			expectSavedPurchasePricing(t, f, int64(data(t, w)["order_id"].(float64)), line)
		})
	}
}

func TestCustomerGroupPricingRecurringPriceFrozenAfterGroupChanges(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Frozen recurring", 20)
	purchasePricingMembership(t, f, g)
	v, item := add(t, f.h, f.customer, f.productID, 1)
	quote, in := purchasePricingQuote(t, f, v, item)
	w := request(f.h, "POST", "/api/v1/orders", in, f.customer, "pricing-frozen-purchase")
	expect(t, w, 201)
	result := data(t, w)
	order, invoice := int64(result["order_id"].(float64)), int64(result["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.customerID), map[string]any{"amount_cents": 10000}, f.root, "pricing-frozen-balance"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "pricing-frozen-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal("missing service activation", err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "PATCH", fmt.Sprintf("/api/v1/admin/customer-groups/%d", g), map[string]any{"discount_percent": 90, "expected_version": 1}, f.root, "pricing-frozen-group-update"), 200)
	var service int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", order).Scan(&service); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/services/%d", service)
	w = request(f.h, "GET", path, nil, f.customer, "")
	expect(t, w, 200)
	if d := data(t, w); d["recurring_price_cents"] != float64(988) || d["renewal_amount_cents"] != float64(988) {
		t.Fatal("live group edit changed the saved service price", d)
	}
	w = request(f.h, "POST", path+"/renew", map[string]any{}, f.customer, "pricing-frozen-renew")
	expect(t, w, 201)
	if data(t, w)["amount_cents"] != float64(988) {
		t.Fatal("renewal used current group configuration", w.Body.String())
	}
	expectSavedPurchasePricing(t, f, order, purchasePricingLine(t, quote["items"].([]any)[0]))
	// Historical pricing is immutable even for a database writer.
	if _, err = f.db.Pool.Exec(t.Context(), `UPDATE order_item_pricing SET net_cents=net_cents WHERE order_item_id IN (SELECT id FROM order_items WHERE order_id=$1)`, order); err == nil {
		t.Fatal("purchase evidence could be rewritten after settlement")
	}
}

func TestCustomerGroupPricingWritersTakePricingLockBeforeGroupRows(t *testing.T) {
	for _, action := range []string{"update", "delete", "discount"} {
		t.Run(action, func(t *testing.T) {
			f := newMutationOrderFixture(t)
			group := purchasePricingGroup(t, f, "Writer lock", 20)
			pg := purchasePricingRule(t, f, group, "Locked product group", "percent", 10)
			reader, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer reader.Rollback(t.Context())
			if err = store.GroupPricingReadLock(t.Context(), reader); err != nil {
				t.Fatal(err)
			}
			var readerPID int
			if err = reader.QueryRow(t.Context(), "SELECT pg_backend_pid()").Scan(&readerPID); err != nil {
				t.Fatal(err)
			}
			method, path := "PATCH", fmt.Sprintf("/api/v1/admin/customer-product-groups/%d", pg)
			body := map[string]any{"name": "Changed group", "product_ids": []int64{f.productID}, "expected_version": 1}
			if action == "delete" {
				method, body = "DELETE", map[string]any{"expected_version": 1}
			} else if action == "discount" {
				method, path = "PUT", "/api/v1/admin/customer-product-discounts"
				body = map[string]any{"customer_group_id": group, "product_group_id": pg, "discount_type": "percent", "discount_percent": 30, "expected_version": 1}
			}
			finished := make(chan *httptest.ResponseRecorder, 1)
			go func() { finished <- request(f.h, method, path, body, f.root, "pricing-lock-order-"+action) }()
			// Observe an actual advisory-lock wait before checking row availability;
			// scheduling a goroutine alone would not prove its transaction began.
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				err = f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)) AND wait_event='advisory' AND query='SELECT pg_advisory_xact_lock(70108206)')`, readerPID).Scan(&waiting)
				if err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case response := <-finished:
					t.Fatal("writer bypassed the pricing read lock", response.Body.String())
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("writer did not reach the pricing lock within the deadline")
				}
				time.Sleep(10 * time.Millisecond)
			}
			probe, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			var id int64
			if action == "discount" {
				err = probe.QueryRow(t.Context(), `SELECT id FROM customer_product_group_discounts WHERE customer_group_id=$1 AND product_group_id=$2 FOR UPDATE NOWAIT`, group, pg).Scan(&id)
			} else {
				err = probe.QueryRow(t.Context(), `SELECT id FROM customer_product_groups WHERE id=$1 FOR UPDATE NOWAIT`, pg).Scan(&id)
			}
			probe.Rollback(t.Context())
			if err != nil {
				t.Fatal("writer held a group row while waiting for pricing lock", err)
			}
			if err = reader.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-finished:
				expect(t, response, 200)
			case <-time.After(3 * time.Second):
				t.Fatal("writer failed to finish after reader released the pricing lock")
			}
		})
	}
}

func TestCustomerGroupPricingLegacyProductGroupReceiptsReplay(t *testing.T) {
	f := newMutationOrderFixture(t)
	var actor int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM users WHERE username='order-mutation-root'").Scan(&actor); err != nil {
		t.Fatal(err)
	}
	// Reproduce predecessor payloads independently of the new Store input types.
	type oldGroupInput struct {
		Name            string  `json:"name"`
		ProductIDs      []int64 `json:"product_ids"`
		ExpectedVersion int64   `json:"expected_version"`
	}
	type oldDiscountInput struct {
		CustomerGroupID int64  `json:"customer_group_id"`
		ProductGroupID  int64  `json:"product_group_id"`
		DiscountType    string `json:"discount_type"`
		DiscountPercent int    `json:"discount_percent"`
		FixedCents      int64  `json:"fixed_discount_cents"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	create := oldGroupInput{"Legacy product group", []int64{}, 0}
	update := oldGroupInput{"Legacy updated group", []int64{}, 7}
	deletion := struct {
		ExpectedVersion int64 `json:"expected_version"`
	}{9}
	discount := oldDiscountInput{99, 88, "fixed", 0, 300, 2}
	for _, tc := range []struct {
		method, path, operation string
		body, fingerprint       any
	}{
		{"POST", "/api/v1/admin/customer-product-groups", "customer_product_group.create", create, create},
		{"PATCH", "/api/v1/admin/customer-product-groups/88", "customer_product_group.update:88", update, struct {
			ID    int64
			Input oldGroupInput
		}{88, update}},
		{"DELETE", "/api/v1/admin/customer-product-groups/88", "customer_product_group.delete:88", deletion, struct {
			ID    int64
			Input any
		}{88, deletion}},
		{"PUT", "/api/v1/admin/customer-product-discounts", "customer_product_discount.save", discount, discount},
	} {
		key := "legacy-pricing-" + tc.operation
		if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,'{"id":777,"version":8}'::jsonb)`, actor, "admin:"+tc.operation, key, settingMutationHash(tc.fingerprint)); err != nil {
			t.Fatal(err)
		}
		w := request(f.h, tc.method, tc.path, tc.body, f.root, key)
		expect(t, w, 200)
		if d := data(t, w); d["id"] != float64(777) || d["version"] != float64(8) || d["replayed"] != true {
			t.Fatal("predecessor settings receipt changed during extraction", tc.operation, d)
		}
	}
	var mutations int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM customer_product_groups)+(SELECT count(*) FROM customer_product_group_discounts)+(SELECT count(*) FROM audit_events WHERE action LIKE 'customer_product_%')`).Scan(&mutations); err != nil || mutations != 0 {
		t.Fatal("receipt replay repeated product-group side effects", mutations, err)
	}
}
