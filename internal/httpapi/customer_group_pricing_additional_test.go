package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"testing"
)

func TestCustomerGroupPricingSelectiveLinesAndAuditRollback(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Selective audit group", 20)
	purchasePricingMembership(t, f, g)
	other := product(t, f.db, 10)
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "SELECTIVE-GROUP", "discount_type": "fixed", "value": 101, "applies_customer_group_ids": []int64{g}, "applies_product_ids": []int64{f.productID}}, f.root, "selective-group-code"), 201)
	_, first := add(t, f.h, f.customer, f.productID, 2)
	v, _ := add(t, f.h, f.customer, other, 1)
	var second int64
	cartResponse := request(f.h, "GET", "/api/v1/cart", nil, f.customer, "")
	expect(t, cartResponse, 200)
	for _, raw := range data(t, cartResponse)["items"].([]any) {
		row := raw.(map[string]any)
		if row["product_id"] == float64(other) {
			second = int64(row["id"].(float64))
		}
	}
	if second == 0 {
		t.Fatal("second cart product missing")
	}
	applied := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "SELECTIVE-GROUP"}, f.customer, "")
	expect(t, applied, 200)
	v = int64(data(t, applied)["version"].(float64))
	quote, in := purchasePricingQuote(t, f, v, first, second)
	var sumNet, sumPromo int64
	for _, raw := range quote["items"].([]any) {
		p := purchasePricingLine(t, raw)
		sumNet += p.NetCents
		sumPromo += p.PromoDiscountCents
		if p.ProductID == f.productID && p.PromoDiscountCents != 101 || p.ProductID == other && p.PromoDiscountCents != 0 {
			t.Fatal("selective discount leaked to another product", p)
		}
	}
	if sumNet != int64(quote["total_cents"].(float64)) || sumPromo != 101 {
		t.Fatal(quote)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_group_purchase_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='order.created' THEN RAISE EXCEPTION 'injected'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_group_purchase_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_group_purchase_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", "/api/v1/orders", in, f.customer, "selective-audit-order"), 500)
	var facts, uses int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders)+(SELECT count(*) FROM order_item_pricing)+(SELECT count(*) FROM purchase_pricing_births)+(SELECT count(*) FROM services)+(SELECT count(*) FROM invoices)+(SELECT count(*) FROM external_refund_order_stock_reservations),(SELECT used_count FROM promo_codes WHERE code='SELECTIVE-GROUP')`).Scan(&facts, &uses); err != nil || facts != 0 || uses != 0 {
		t.Fatal("partial pricing or money survived audit failure", facts, uses, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_group_purchase_audit ON audit_events"); err != nil {
		t.Fatal(err)
	}
	w := request(f.h, "POST", "/api/v1/orders", in, f.customer, "selective-audit-order")
	expect(t, w, 201)
	order := int64(data(t, w)["order_id"].(float64))
	invoice := int64(data(t, w)["invoice_id"].(float64))
	var lineNet, reportNet int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT sum(p.net_cents)::bigint,sum(purchase_invoice_item_net($2,oi.id))::bigint FROM order_items oi JOIN order_item_pricing p ON p.order_item_id=oi.id WHERE oi.order_id=$1`, order, invoice).Scan(&lineNet, &reportNet); err != nil || lineNet != sumNet || reportNet != sumNet {
		t.Fatal(lineNet, reportNet, err)
	}
}

func TestCustomerGroupPricingRecurringCouponAndUpgradeScope(t *testing.T) {
	f := newMutationOrderFixture(t)
	g := purchasePricingGroup(t, f, "Recurring scoped group", 20)
	purchasePricingMembership(t, f, g)
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "RECURRING-GROUP", "discount_type": "percent", "value": 10, "recurring": true, "applies_customer_group_ids": []int64{g}}, f.root, "recurring-group-code"), 201)
	v, item := add(t, f.h, f.customer, f.productID, 1)
	applied := request(f.h, "POST", "/api/v1/cart/promo", map[string]any{"version": v, "code": "RECURRING-GROUP"}, f.customer, "")
	expect(t, applied, 200)
	v = int64(data(t, applied)["version"].(float64))
	_, in := purchasePricingQuote(t, f, v, item)
	w := request(f.h, "POST", "/api/v1/orders", in, f.customer, "recurring-group-order")
	expect(t, w, 201)
	d := data(t, w)
	order, invoice := int64(d["order_id"].(float64)), int64(d["invoice_id"].(float64))
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.customerID), map[string]any{"amount_cents": 10000}, f.root, "recurring-group-balance"), 201)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, f.customer, "recurring-group-pay"), 201)
	job, err := f.db.ClaimProvisionJob(t.Context())
	if err != nil || job == nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var sid int64
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", order).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	renewal := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", sid), map[string]any{}, f.customer, "recurring-group-renew")
	expect(t, renewal, 201)
	if data(t, renewal)["amount_cents"] != float64(890) {
		t.Fatal("renewal did not apply saved coupon over group contract price", renewal.Body.String())
	}
	expect(t, request(f.h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "UPGRADE-GROUP", "discount_type": "percent", "value": 10, "upgrade_only": true, "applies_customer_group_ids": []int64{g}}, f.root, "upgrade-group-code"), 201)
	tx, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	q, err := store.QuoteUpgradePromo(t.Context(), tx, f.customerID, "UPGRADE-GROUP", 500, f.productID, "monthly", true)
	_ = tx.Rollback(t.Context())
	if err != nil || q.DiscountCents != 50 {
		t.Fatal(q, err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM customer_group_members WHERE customer_id=$1", f.customerID); err != nil {
		t.Fatal(err)
	}
	tx, err = f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	_, err = store.QuoteUpgradePromo(t.Context(), tx, f.customerID, "UPGRADE-GROUP", 500, f.productID, "monthly", true)
	c, ok := err.(*store.Conflict)
	if !ok || c.Code != "promo_customer_group_ineligible" {
		t.Fatal("upgrade coupon bypassed group eligibility", err)
	}
}
