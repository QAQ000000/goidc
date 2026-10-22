package httpapi

import (
	"fmt"
	"net/url"
	"testing"
)

func TestCustomerProductDiscountMatrixIdentityLifecycle(t *testing.T) {
	f := newMutationOrderFixture(t)
	const endpoint = "/api/v1/admin/customer-product-discounts"
	page := func(query string, limit, offset int) map[string]any {
		t.Helper()
		w := request(f.h, "GET", fmt.Sprintf("%s?q=%s&limit=%d&offset=%d", endpoint, url.QueryEscape(query), limit, offset), nil, f.root, "")
		expect(t, w, 200)
		return data(t, w)
	}
	empty := page("matrix", 2, 0)
	if empty["total"] != float64(0) || len(empty["items"].([]any)) != 0 {
		t.Fatal("empty matrix must return an empty page", empty)
	}
	groups := []int64{
		purchasePricingGroup(t, f, "matrix customer A", 10),
		purchasePricingGroup(t, f, "matrix customer B", 20),
	}
	productGroups := make([]int64, 2)
	for i := range productGroups {
		w := request(f.h, "POST", "/api/v1/admin/customer-product-groups", map[string]any{"name": fmt.Sprintf("matrix product %d", i), "product_ids": []int64{f.productID}}, f.root, fmt.Sprintf("matrix-product-%d", i))
		expect(t, w, 201)
		productGroups[i] = int64(data(t, w)["id"].(float64))
	}
	seen := make(map[string]bool)
	for _, offset := range []int{0, 2} {
		result := page("matrix", 2, offset)
		items := result["items"].([]any)
		if result["total"] != float64(4) || result["offset"] != float64(offset) || len(items) != 2 {
			t.Fatal("matrix pagination changed", result)
		}
		for _, item := range items {
			row := item.(map[string]any)
			customerID, productID := row["customer_group_id"], row["product_group_id"]
			if (customerID != float64(groups[0]) && customerID != float64(groups[1])) || (productID != float64(productGroups[0]) && productID != float64(productGroups[1])) {
				t.Fatal("unset rule lost its group identities", row)
			}
			key := fmt.Sprint(customerID, "/", productID)
			if seen[key] || row["id"] != nil || row["version"] != float64(0) || row["discount_type"] != "none" {
				t.Fatal("matrix row is duplicated or invents a saved rule", row)
			}
			seen[key] = true
		}
	}
	if filtered := page("matrix product 0", 25, 0); filtered["total"] != float64(2) {
		t.Fatal("product-name filter must retain both customer pairs", filtered)
	}
	find := func() map[string]any {
		t.Helper()
		for _, value := range page("matrix product 0", 25, 0)["items"].([]any) {
			row := value.(map[string]any)
			if row["customer_group_id"] == float64(groups[0]) {
				return row
			}
		}
		t.Fatal("group pair disappeared")
		return nil
	}
	for index, kind := range []string{"percent", "fixed", "none"} {
		row := find()
		input := map[string]any{
			"customer_group_id": row["customer_group_id"], "product_group_id": row["product_group_id"],
			"discount_type": kind, "discount_percent": 0, "fixed_discount_cents": 0, "expected_version": row["version"],
		}
		if kind == "percent" {
			input["discount_percent"] = 25
		} else if kind == "fixed" {
			input["fixed_discount_cents"] = 1234
		}
		w := request(f.h, "PUT", endpoint, input, f.root, "matrix-rule-"+kind)
		expect(t, w, 200)
		next := find()
		if next["customer_group_id"] != row["customer_group_id"] || next["product_group_id"] != row["product_group_id"] || next["discount_type"] != kind {
			t.Fatal("rule lifecycle changed pair identity or type", next)
		}
		if kind == "none" {
			if next["id"] != nil || next["version"] != float64(0) {
				t.Fatal("cleared rule must retain an unset pair", next)
			}
		} else if next["id"] == nil || next["version"] != float64(index+1) || next["discount_percent"] != float64(input["discount_percent"].(int)) || next["fixed_discount_cents"] != float64(input["fixed_discount_cents"].(int)) {
			t.Fatal("saved rule projection changed", next)
		}
	}
}
