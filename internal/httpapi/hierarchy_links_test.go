package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestHierarchyConfigurationBranchesAndRevalidation(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "hierarchy-admin", "super_admin")
	_, customer := user(t, db, h, "hierarchy-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("hierarchy", nil))
	makeGroup := func(name string, required bool, values ...string) int64 {
		options := []store.ProductOptionInput{}
		for i, value := range values {
			options = append(options, store.ProductOptionInput{Label: value, Value: value, Active: enabled(), PriceDeltaCents: int64((i + 1) * 100)})
		}
		return configGroup(t, h, admin, pid, name, required, options)
	}
	region := makeGroup("region", false, "a", "b", "end")
	zone := makeGroup("zone", true, "a1", "b1", "a-end")
	plan := makeGroup("plan", true, "a-small", "b-small")
	a := configOption(t, h, admin, pid, region, "a")
	b := configOption(t, h, admin, pid, region, "b")
	end := configOption(t, h, admin, pid, region, "end")
	a1 := configOption(t, h, admin, pid, zone, "a1")
	b1 := configOption(t, h, admin, pid, zone, "b1")
	aEnd := configOption(t, h, admin, pid, zone, "a-end")
	aSmall := configOption(t, h, admin, pid, plan, "a-small")
	bSmall := configOption(t, h, admin, pid, plan, "b-small")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	reset := func() {
		exec(`UPDATE product_option_groups SET hierarchy_native=false,option_type=20,active=true,legacy_source='hierarchy',legacy_id=id,legacy_metadata=jsonb_build_object('linkage_pid',CASE id WHEN $1 THEN $2 WHEN $3 THEN $1 ELSE 0 END,'linkage_level','stale-ignored') WHERE product_id=$4`, zone, region, plan, pid)
		exec(`UPDATE product_options SET hierarchy_native=false,active=true,legacy_source='hierarchy',legacy_id=id,legacy_metadata=jsonb_build_object('linkage_pid',CASE id WHEN $1 THEN $2 WHEN $3 THEN $4 WHEN $5 THEN $2 WHEN $6 THEN $1 WHEN $7 THEN $3 ELSE 0 END) WHERE group_id=ANY($8)`, a1, a, b1, b, aEnd, aSmall, bSmall, []int64{region, zone, plan})
	}
	reset()
	duplicate := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "hierarchy copy", Slug: "hierarchy-copy"}, admin, "")
	expect(t, duplicate, 201)
	copyID := int64(data(t, duplicate)["id"].(float64))
	copyGroup := func(slug string) int64 {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM product_option_groups WHERE product_id=$1 AND slug=$2", copyID, slug).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	copyOption := func(value string) int64 {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT po.id FROM product_options po JOIN product_option_groups g ON g.id=po.group_id WHERE g.product_id=$1 AND po.value=$2", copyID, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	copyA, copyB1, copyASmall := copyOption("a"), copyOption("b1"), copyOption("a-small")
	var copiedParent int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT parent_group_id FROM product_option_groups WHERE id=$1", copyGroup("zone")).Scan(&copiedParent); err != nil || copiedParent != copyGroup("region") {
		t.Fatal("copied parent was not remapped", copiedParent, err)
	}
	_, copyBuyer := user(t, db, h, "hierarchy-copy-buyer", "customer")
	configuredCart(t, h, copyBuyer, copyID, []int64{copyA, copyOption("a1"), copyASmall}, 1)
	copyCart := data(t, request(h, "GET", "/api/v1/cart", nil, copyBuyer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": copyCart["version"], "product_id": copyID, "quantity": 1, "option_ids": []int64{copyA, copyB1, copyASmall}}, copyBuyer, ""), 409)
	var productCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM products").Scan(&productCount); err != nil || productCount != 2 {
		t.Fatal("hierarchy copy count incorrect", productCount, err)
	}
	public := request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", "")
	expect(t, public, 200)
	for _, rawGroup := range data(t, public)["option_groups"].([]any) {
		group := rawGroup.(map[string]any)
		want := int64(0)
		if group["id"] == float64(zone) {
			want = region
		}
		if group["id"] == float64(plan) {
			want = zone
		}
		if group["parent_group_id"] != float64(want) {
			t.Fatal("catalog parent group missing", group)
		}
		for _, rawOption := range group["options"].([]any) {
			option := rawOption.(map[string]any)
			wantParent := map[int64]int64{a1: a, b1: b, aEnd: a, aSmall: a1, bSmall: b1}[int64(option["id"].(float64))]
			if option["parent_option_id"] != float64(wantParent) {
				t.Fatal("catalog parent option missing", option)
			}
		}
	}
	for _, tc := range []struct {
		name string
		ids  []int64
		code string
	}{
		{"empty optional tree", nil, ""},
		{"branch a", []int64{a, a1, aSmall}, ""},
		{"branch b", []int64{b, b1, bSmall}, ""},
		{"root without descendants", []int64{end}, ""},
		{"second level ends branch", []int64{a, aEnd}, ""},
		{"wrong second branch", []int64{a, b1, bSmall}, "configuration_dependency"},
		{"wrong third branch", []int64{a, a1, bSmall}, "configuration_dependency"},
		{"child without parent", []int64{a1, aSmall}, "configuration_dependency"},
		{"missing reachable child", []int64{a}, "required_product_options"},
		{"missing reachable grandchild", []int64{a, a1}, "required_product_options"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
			w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": tc.ids}, customer, "")
			if tc.code == "" {
				expect(t, w, 200)
			} else {
				expect(t, w, 409)
				if !strings.Contains(w.Body.String(), tc.code) {
					t.Fatal(w.Body.String())
				}
			}
		})
	}
	_, buyer := user(t, db, h, "hierarchy-buyer", "customer")
	v, itemID := configuredCart(t, h, buyer, pid, []int64{a, a1, aSmall}, 1)
	w := request(h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", itemID), map[string]any{"version": v, "quantity": 1, "option_ids": []int64{b, a1, aSmall}}, buyer, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "configuration_dependency") {
		t.Fatal(w.Body.String())
	}
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, buyer, ""))
	if cart["version"] != float64(v) {
		t.Fatal("invalid patch changed cart version")
	}
	line := cart["items"].([]any)[0].(map[string]any)
	if line["options_valid"] != true {
		t.Fatal("invalid patch changed cart options")
	}
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{"code": "TREE10", "discount_type": "percent", "value": 10}, admin, ""), 201)
	// Change only the branch relation: quotation/checkout must revalidate even
	// when the saved product revision and option prices still match.
	exec(`UPDATE product_options SET legacy_metadata=jsonb_build_object('linkage_pid',$2::bigint) WHERE id=$1`, a1, b)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, buyer, ""))
	if cart["items"].([]any)[0].(map[string]any)["options_valid"] != false {
		t.Fatal("cart read missed stale branch")
	}
	expect(t, request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": cart["version"], "code": "TREE10"}, buyer, ""), 409)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{itemID}}, buyer, "hierarchy-invalid-checkout"), 409)
	var orders int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM orders").Scan(&orders); err != nil || orders != 0 {
		t.Fatal("invalid hierarchy created order", orders, err)
	}
	reset()
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, buyer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": cart["version"], "code": "TREE10"}, buyer, ""), 200)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, buyer, ""))
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{itemID}}, buyer, "hierarchy-valid-checkout")
	expect(t, order, 201)
	var snapshot []byte
	var price int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT config_snapshot,price_cents FROM order_items WHERE order_id=$1", int64(data(t, order)["order_id"].(float64))).Scan(&snapshot, &price); err != nil || price != 2300 || !strings.Contains(string(snapshot), "a-small") {
		t.Fatal("hierarchy price/snapshot lost", price, string(snapshot), err)
	}
	for _, tc := range []struct {
		name, sql string
		args      []any
	}{
		{"missing group", `UPDATE product_option_groups SET legacy_metadata='{"linkage_pid":9999999}' WHERE id=$1`, []any{zone}},
		{"cross-source group", `UPDATE product_option_groups SET legacy_source='other' WHERE id=$1`, []any{zone}},
		{"group cycle without selected options", `UPDATE product_option_groups SET legacy_metadata=jsonb_build_object('linkage_pid',$2::bigint) WHERE id=$1`, []any{region, plan}},
		{"missing option", `UPDATE product_options SET legacy_metadata='{"linkage_pid":9999999}' WHERE id=$1`, []any{a1}},
		{"wrong parent group", `UPDATE product_options SET legacy_metadata=jsonb_build_object('linkage_pid',$2::bigint) WHERE id=$1`, []any{aSmall, a}},
		{"cross-source option", `UPDATE product_options SET legacy_source='other' WHERE id=$1`, []any{a1}},
		{"option without parent", `UPDATE product_options SET legacy_metadata='{}' WHERE id=$1`, []any{a1}},
		{"fractional parent", `UPDATE product_option_groups SET legacy_metadata='{"linkage_pid":1.5}' WHERE id=$1`, []any{zone}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			exec(tc.sql, tc.args...)
			cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
			w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{}}, customer, "")
			expect(t, w, 409)
			if !strings.Contains(w.Body.String(), "unsupported_product_configuration") {
				t.Fatal(w.Body.String())
			}
			// Invalid metadata must not crash the admin catalog.
			expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, admin, ""), 200)
			reset()
		})
	}
	exec(`UPDATE product_option_groups SET active=false WHERE id=$1`, region)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{a1, aSmall}}, customer, ""), 409)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{}}, customer, ""), 200)
}
