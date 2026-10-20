package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in real API/Next/browser acceptance; random test schema and no outgoing
// merchant, notification or supplier actions. The finish file owns cleanup.
func TestCustomerGroupPricingBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "group-pricing-browser-admin", func(db *store.Store, h http.Handler) {
		uid, customer := user(t, db, h, "group-pricing-browser-customer", "customer")
		_, root := user(t, db, h, "group-pricing-seed-admin", "super_admin")
		group := request(h, "POST", "/api/v1/admin/customer-groups", map[string]any{"name": "浏览器会员组", "discount_percent": 20}, root, "group-browser-group")
		expect(t, group, 201)
		gid := int64(data(t, group)["id"].(float64))
		purchasePricingMembership(t, mutationOrderFixture{db: db, h: h, root: root, customerID: uid}, gid)
		first, second := product(t, db, 12), product(t, db, 12)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name=CASE id WHEN $1 THEN '会员优惠云主机' ELSE '保留在购物车的云主机' END WHERE id=ANY($2)", first, []int64{first, second}); err != nil {
			t.Fatal(err)
		}
		for _, code := range []string{"OPEN10", "GROUP10"} {
			in := map[string]any{"code": code, "discount_type": "percent", "value": 10}
			if code == "GROUP10" {
				in["applies_customer_group_ids"] = []int64{gid}
			}
			expect(t, request(h, "POST", "/api/v1/admin/promo-codes", in, root, "group-browser-code-"+code), 201)
		}
		add(t, h, customer, first, 2)
		add(t, h, customer, second, 1)
		writeBrowserJSON(t, os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", map[string]any{"customer_username": "group-pricing-browser-customer", "group_id": gid, "product_id": first, "expected_selected_total_cents": 1921, "external_actions": false})
		t.Cleanup(func() {
			var orders, net, group, promo int64
			err := db.Pool.QueryRow(context.Background(), `SELECT count(DISTINCT o.id),coalesce(sum(p.net_cents),0)::bigint,coalesce(sum(p.group_discount_cents),0)::bigint,coalesce(sum(p.promo_discount_cents),0)::bigint FROM orders o JOIN order_items oi ON oi.order_id=o.id JOIN order_item_pricing p ON p.order_item_id=oi.id WHERE o.user_id=$1`, uid).Scan(&orders, &net, &group, &promo)
			if err != nil || orders != 1 || net != 1921 || group != 532 || promo != 213 {
				t.Errorf("browser did not complete exact selected purchase: %d %d %d %d %v", orders, net, group, promo, err)
			}
		})
	})
}
