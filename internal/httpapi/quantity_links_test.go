package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestQuantityLinksEnforceConditionsAndAllResults(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "quantity-links-admin", "super_admin")
	_, customer := user(t, db, h, "quantity-links-customer", "customer")
	_, checkoutCustomer := user(t, db, h, "quantity-links-checkout", "customer")
	pid := managedProductID(t, h, admin, productInput("quantity-links", nil))
	leftGroup := configGroup(t, h, admin, pid, "disk", true, []store.ProductOptionInput{{Label: "SSD", Value: "ssd", Active: enabled()}, {Label: "NVMe", Value: "nvme", Active: enabled()}})
	rightGroup := configGroup(t, h, admin, pid, "bandwidth", true, []store.ProductOptionInput{{Label: "带宽", Value: "bw", Active: enabled()}})
	thirdGroup := configGroup(t, h, admin, pid, "os", true, []store.ProductOptionInput{{Label: "允许", Value: "allow", Active: enabled()}, {Label: "拒绝", Value: "deny", Active: enabled()}})
	ssd := configOption(t, h, admin, pid, leftGroup, "ssd")
	nvme := configOption(t, h, admin, pid, leftGroup, "nvme")
	bw := configOption(t, h, admin, pid, rightGroup, "bw")
	allow := configOption(t, h, admin, pid, thirdGroup, "allow")
	deny := configOption(t, h, admin, pid, thirdGroup, "deny")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE product_option_groups SET option_type=4,qty_minimum=1,qty_maximum=20 WHERE id=ANY($1)`, []int64{leftGroup, rightGroup}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE product_options SET legacy_source='quantity-links',legacy_id=id WHERE group_id=ANY($1)`, []int64{leftGroup, rightGroup, thirdGroup}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO legacy_product_config_option_links(config_option_group_id,sub_ids,relation,link_type,relation_legacy_id,legacy_source,legacy_id)
VALUES($1,$2,'seq','condition',0,'quantity-links',500),($3,$4,'seq','result',500,'quantity-links',501),($5,$6,'seq','result',500,'quantity-links',502)`, leftGroup, fmt.Sprintf(`{"%d":{"qty_minimum":"3","qty_maximum":"5"}}`, ssd), rightGroup, fmt.Sprintf(`{"%d":{"qty_minimum":10,"qty_maximum":12}}`, bw), thirdGroup, fmt.Sprintf(`{"%d":{"qty_minimum":0,"qty_maximum":0}}`, allow)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name     string
		disk, bw int64
		os       int64
		status   int
	}{
		{"below condition", 2, 1, deny, 200},
		{"above condition", 6, 1, deny, 200},
		{"lower condition and result boundaries", 3, 10, allow, 200},
		{"upper condition and result boundaries", 5, 12, allow, 200},
		{"below result range", 4, 9, allow, 409},
		{"above result range", 4, 13, allow, 409},
		{"second result must also hold", 4, 10, deny, 409},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
			// The quantity predicate belongs to the group, not its archived tier ID.
			w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{nvme, bw, tc.os}, "option_quantities": map[string]int64{fmt.Sprint(nvme): tc.disk, fmt.Sprint(bw): tc.bw}}, customer, "")
			expect(t, w, tc.status)
			if tc.status == 409 && !strings.Contains(w.Body.String(), "configuration_dependency") {
				t.Fatal("missing dependency error", w.Body.String())
			}
			if tc.name == "lower condition and result boundaries" {
				created := request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "LINK10", "discount_type": "percent", "value": 10}, admin, "")
				expect(t, created, 201)
				checkoutCart := data(t, request(h, "GET", "/api/v1/cart", nil, checkoutCustomer, ""))
				validCart := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": checkoutCart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{nvme, bw, allow}, "option_quantities": map[string]int64{fmt.Sprint(nvme): 3, fmt.Sprint(bw): 10}}, checkoutCustomer, "")
				expect(t, validCart, 200)
				checkoutCart = data(t, request(h, "GET", "/api/v1/cart", nil, checkoutCustomer, ""))
				applied := request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": checkoutCart["version"], "code": "LINK10"}, checkoutCustomer, "")
				expect(t, applied, 200)
				checkoutCart = data(t, request(h, "GET", "/api/v1/cart", nil, checkoutCustomer, ""))
				line := checkoutCart["items"].([]any)[0].(map[string]any)
				order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(checkoutCart["version"].(float64)), ItemIDs: []int64{int64(line["id"].(float64))}}, checkoutCustomer, "quantity-link-checkout")
				expect(t, order, 201)
			}
		})
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE legacy_product_config_option_links SET relation='sneq' WHERE legacy_source='quantity-links' AND legacy_id=500`); err != nil {
		t.Fatal(err)
	}
	for _, disk := range []int64{2, 6} {
		cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
		expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{nvme, bw, allow}, "option_quantities": map[string]int64{fmt.Sprint(nvme): disk, fmt.Sprint(bw): 1}}, customer, ""), 409)
	}
}
