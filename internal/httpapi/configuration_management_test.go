package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func configRevision(t *testing.T, h http.Handler, root string, pid int64) *int64 {
	t.Helper()
	w := request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, root, "")
	expect(t, w, 200)
	revision := int64(data(t, w)["revision"].(float64))
	return &revision
}
func configGroup(t *testing.T, h http.Handler, root string, pid int64, name string, required bool, options []store.ProductOptionInput) int64 {
	t.Helper()
	in := store.ProductOptionGroupInput{Name: name, Slug: name, Required: required, Active: enabled(), Options: options, ExpectedRevision: configRevision(t, h, root, pid)}
	w := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), in, root, "")
	expect(t, w, 201)
	return int64(data(t, w)["id"].(float64))
}
func configOption(t *testing.T, h http.Handler, root string, pid, gid int64, value string) int64 {
	t.Helper()
	product := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, root, ""))
	for _, g := range product["option_groups"].([]any) {
		group := g.(map[string]any)
		if group["id"] == float64(gid) {
			for _, o := range group["options"].([]any) {
				option := o.(map[string]any)
				if option["value"] == value {
					return int64(option["id"].(float64))
				}
			}
		}
	}
	t.Fatal("option not found")
	return 0
}
func configuredCart(t *testing.T, h http.Handler, customer string, pid int64, ids []int64, quantity int) (int64, int64) {
	t.Helper()
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": quantity, "option_ids": ids}, customer, "")
	expect(t, w, 200)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	items := cart["items"].([]any)
	return int64(cart["version"].(float64)), int64(items[len(items)-1].(map[string]any)["id"].(float64))
}

func TestConfigurationLifecycleAmountsAndImmutableSnapshot(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "configuration-root", "super_admin")
	customerID, customer := user(t, db, h, "configuration-customer", "customer")
	product := productInput("config-product", nil)
	product.ProvisionMode = "simulation"
	pid := managedProductID(t, h, root, product)
	option := store.ProductOptionInput{Label: "Linux", Value: "linux", PriceDeltaCents: 500, SetupDeltaCents: 20, Active: enabled(), SortOrder: 10}
	inactive := store.ProductOptionInput{Label: "Windows", Value: "windows", PriceDeltaCents: 900, Active: new(bool), SortOrder: 1}
	gid := configGroup(t, h, root, pid, "os", true, []store.ProductOptionInput{option, inactive})
	oid := configOption(t, h, root, pid, gid, "linux")
	other := configOption(t, h, root, pid, gid, "windows")
	base := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d", pid, gid)
	op := fmt.Sprintf("%s/options/%d", base, oid)
	public := data(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""))
	groups := public["option_groups"].([]any)
	if len(groups) != 1 || len(groups[0].(map[string]any)["options"].([]any)) != 1 {
		t.Fatal("inactive option leaked")
	}
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1}, customer, ""), 409)
	v, item := configuredCart(t, h, customer, pid, []int64{oid}, 2)
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["items"].([]any)[0].(map[string]any)
	if cart["price_cents"] != float64(2500) || cart["setup_fee_cents"] != float64(120) || cart["total_cents"] != float64(5240) || cart["options_valid"] != true {
		t.Fatal("cart effective amounts wrong", cart)
	}
	expect(t, request(h, "DELETE", op, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 409)
	expect(t, request(h, "DELETE", base, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 409)
	option.PriceDeltaCents = 600
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, option, root, ""), 200)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "configuration-stale-order"), 409)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["items"].([]any)[0].(map[string]any)
	if cart["current"] != false || cart["price_cents"] != float64(2600) {
		t.Fatal("stale cart not reflected")
	}
	v, item = configuredCart(t, h, customer, pid, []int64{oid}, 2)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "configuration-order")
	expect(t, order, 201)
	orderID := int64(data(t, order)["order_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customerID), map[string]int64{"amount_cents": 10000}, root, "configured-service-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, order)["invoice_id"].(float64))), map[string]any{}, customer, "configured-service-pay"), 201)
	for range 2 {
		job, err := db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
			t.Fatal(err)
		}
	}
	var price, setup, total, recurring int64
	var snapshot []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT price_cents,setup_fee_cents,total_cents,config_snapshot FROM order_items WHERE order_id=$1", orderID).Scan(&price, &setup, &total, &snapshot); err != nil {
		t.Fatal(err)
	}
	if price != 2600 || setup != 120 || total != 5440 {
		t.Fatal("order components lost configuration deltas", price, setup, total)
	}
	var saved []store.OptionSnapshot
	if err := json.Unmarshal(snapshot, &saved); err != nil || len(saved) != 1 || saved[0].Value != "linux" || saved[0].GroupName != "os" || saved[0].PriceDeltaCents != 600 {
		t.Fatal("snapshot invalid", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT coalesce(s.recurring_price_cents,oi.price_cents) FROM services s JOIN order_items oi ON oi.id=s.order_item_id WHERE s.order_id=$1 LIMIT 1", orderID).Scan(&recurring); err != nil || recurring != 2600 {
		t.Fatal("renewal basis not snapshotted", err, recurring)
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/admin/orders/%d", orderID), nil, root, "")
	expect(t, detail, 200)
	if len(data(t, detail)["items"].([]any)[0].(map[string]any)["config_snapshot"].([]any)) != 1 {
		t.Fatal("admin snapshot missing")
	}
	option.Label = "Renamed Linux"
	option.Value = "new-linux"
	option.PriceDeltaCents = 800
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, option, root, ""), 200)
	var after []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", orderID).Scan(&after); err != nil || !bytes.Equal(snapshot, after) {
		t.Fatal("historical snapshot changed", err)
	}
	var serviceID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1 ORDER BY id LIMIT 1", orderID).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	renew := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", serviceID), map[string]any{}, customer, "configured-renew")
	expect(t, renew, 201)
	var renewalAmount int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", int64(data(t, renew)["invoice_id"].(float64))).Scan(&renewalAmount); err != nil || renewalAmount != 2600 {
		t.Fatal("renewal lost original configuration amount", err, renewalAmount)
	}
	expect(t, request(h, "DELETE", op, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 409)
	// Last enabled required option cannot be removed or disabled.
	option.Active = new(bool)
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, option, root, ""), 409)
	inactive.Active = enabled()
	inactive.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", fmt.Sprintf("%s/options/%d", base, other), inactive, root, ""), 200)
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, option, root, ""), 200)
	group := store.ProductOptionGroupInput{Name: "os", Slug: "os", Required: true, Active: new(bool), ExpectedRevision: configRevision(t, h, root, pid)}
	expect(t, request(h, "PUT", base, group, root, ""), 200)
	public = data(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""))
	if len(public["option_groups"].([]any)) != 0 {
		t.Fatal("disabled group leaked")
	}
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "configuration-order"), 200)
	v, item = configuredCart(t, h, customer, pid, []int64{}, 1)
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "without-disabled-group"), 201)
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/options/%d", base, other), map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 200)
	expect(t, request(h, "DELETE", base, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 409)
}

func TestLegacyConfigurationLinksAreEvaluatedDuringCartValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "legacy-link-root", "super_admin")
	_, customer := user(t, db, h, "legacy-link-customer", "customer")
	_, otherCustomer := user(t, db, h, "legacy-link-other", "customer")
	pid := managedProductID(t, h, root, productInput("legacy-link-product", nil))
	leftGroup := configGroup(t, h, root, pid, "source", false, []store.ProductOptionInput{{Label: "启用", Value: "on", Active: enabled()}})
	rightGroup := configGroup(t, h, root, pid, "target", false, []store.ProductOptionInput{{Label: "允许", Value: "allow", Active: enabled()}, {Label: "拒绝", Value: "deny", Active: enabled()}})
	left := configOption(t, h, root, pid, leftGroup, "on")
	allow := configOption(t, h, root, pid, rightGroup, "allow")
	deny := configOption(t, h, root, pid, rightGroup, "deny")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE product_options SET legacy_source='fixture',legacy_id=CASE id WHEN $1 THEN 101 WHEN $2 THEN 201 WHEN $3 THEN 202 END WHERE id=ANY($4)`, left, allow, deny, []int64{left, allow, deny}); err != nil {
		t.Fatal("seed legacy option identity", err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO legacy_product_config_option_links(config_option_group_id,sub_ids,relation,link_type,relation_legacy_id,legacy_source,legacy_id)
VALUES($1,$2,'seq','condition',0,'fixture',500),($3,$4,'seq','result',500,'fixture',501)`, leftGroup, `{"101":{"qty_minimum":0,"qty_maximum":0}}`, rightGroup, `{"201":{"qty_minimum":0,"qty_maximum":0}}`); err != nil {
		t.Fatal("seed legacy links", err)
	}
	valid := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{left, allow}}, customer, "")
	copy := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "constrained copy", Slug: "constrained-copy"}, root, "")
	expect(t, copy, 409)
	if !strings.Contains(copy.Body.String(), "unsupported_product_duplicate") {
		t.Fatal("copy silently dropped dependency rules", copy.Body.String())
	}
	var copies int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM products WHERE slug='constrained-copy'").Scan(&copies); err != nil || copies != 0 {
		t.Fatal("rejected copy left product", copies, err)
	}
	expect(t, valid, 200)
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	item := cart["items"].([]any)[0].(map[string]any)
	if item["options_valid"] != true {
		t.Fatal("valid legacy combination was rejected", item)
	}
	otherInvalid := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{left, deny}}, otherCustomer, "")
	expect(t, otherInvalid, 409)
	if !strings.Contains(otherInvalid.Body.String(), "configuration_dependency") {
		t.Fatal("missing dependency error", otherInvalid.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO legacy_product_config_option_links(config_option_group_id,sub_ids,relation,link_type,relation_legacy_id,legacy_source,legacy_id)
VALUES($1,$2,'seq','condition',0,'fixture',502)`, leftGroup, `{"101":{"qty_minimum":1,"qty_maximum":2}}`); err != nil {
		t.Fatal("seed quantity link", err)
	}
	unsupported := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{left, allow}}, otherCustomer, "")
	expect(t, unsupported, 409)
	if !strings.Contains(unsupported.Body.String(), "unsupported_product_configuration") {
		t.Fatal("missing unsupported dependency error", unsupported.Body.String())
	}
}

func TestQuantityConfigurationUsesRangeStepAndPriceSnapshot(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "quantity-config-customer", "customer")
	_, admin := user(t, db, h, "quantity-config-admin", "super_admin")
	pid := product(t, db, 100)
	group := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), map[string]any{
		"name": "磁盘", "slug": "disk", "required": true, "option_type": 4, "qty_minimum": 2, "qty_maximum": 8, "qty_stage": 2,
		"options": []map[string]any{{"label": "SSD", "value": "ssd", "price_delta_cents": 300, "setup_delta_cents": 10}},
	}, admin, "quantity-group")
	expect(t, group, 201)
	var optionID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM product_options WHERE group_id=$1", int64(data(t, group)["id"].(float64))).Scan(&optionID); err != nil {
		t.Fatal(err)
	}
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	for _, quantity := range []int64{0, 1, 9, 1000001} {
		rejected := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{optionID}, "option_quantities": map[string]int64{fmt.Sprint(optionID): quantity}}, customer, "")
		expect(t, rejected, 409)
	}
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{optionID}}, customer, ""), 409)
	bad := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{optionID}, "option_quantities": map[string]int64{fmt.Sprint(optionID): 3}}, customer, "")
	expect(t, bad, 409)
	if !strings.Contains(bad.Body.String(), "invalid_product_option_quantity") {
		t.Fatal("missing quantity step error", bad.Body.String())
	}
	good := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{optionID}, "option_quantities": map[string]int64{fmt.Sprint(optionID): 4}}, customer, "")
	expect(t, good, 200)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	item := cart["items"].([]any)[0].(map[string]any)
	if item["price_cents"] != float64(2434) || item["setup_fee_cents"] != float64(139) || item["total_cents"] != float64(2573) {
		t.Fatalf("quantity price mismatch: %#v", item)
	}
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{int64(item["id"].(float64))}}, customer, "quantity-config-order")
	expect(t, checkout, 201)
	var quantities []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT option_quantities FROM order_items WHERE order_id=$1", int64(data(t, checkout)["order_id"].(float64))).Scan(&quantities); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(quantities), fmt.Sprintf("\"%d\": 4", optionID)) {
		t.Fatalf("quantity snapshot missing: %s", quantities)
	}
}

func TestFixedConfigurationRejectsMultipliedDiscount(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "fixed-quantity-customer", "customer")
	_, admin := user(t, db, h, "fixed-quantity-admin", "super_admin")
	pid := product(t, db, 100)
	gid := configGroup(t, h, admin, pid, "discount", false, []store.ProductOptionInput{{Label: "优惠", Value: "discount", PriceDeltaCents: -100, Active: enabled()}})
	oid := configOption(t, h, admin, pid, gid, "discount")
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{oid}, "option_quantities": map[string]int64{fmt.Sprint(oid): 2}}, customer, ""), 409)
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": cart["version"], "product_id": pid, "quantity": 1, "option_ids": []int64{oid}}, customer, ""), 200)
	cart = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	item := cart["items"].([]any)[0].(map[string]any)
	if item["price_cents"] != float64(1134) {
		t.Fatal("legacy fixed price changed", item)
	}
	path := fmt.Sprintf("/api/v1/cart/items/%d", int64(item["id"].(float64)))
	expect(t, request(h, "PATCH", path, map[string]any{"version": cart["version"], "quantity": 1, "option_ids": []int64{oid}, "option_quantities": map[string]int64{fmt.Sprint(oid): 2}}, customer, ""), 409)
	// Corrupted or previously accepted cart data must also fail checkout.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE cart_items SET option_quantities=$1 WHERE id=$2", fmt.Sprintf(`{"%d":2}`, oid), int64(item["id"].(float64))); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{int64(item["id"].(float64))}}, customer, "fixed-quantity-checkout"), 409)
}

func TestConfigurationPermissionsValidationAndDeletion(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "config-permission-root", "super_admin")
	rid, reader := user(t, db, h, "config-reader", "admin")
	wid, writer := user(t, db, h, "config-writer", "admin")
	_, customer := user(t, db, h, "config-client", "customer")
	contentRole(t, db, rid, "products.view")
	contentRole(t, db, wid, "products.view", "products.manage")
	pid := managedProductID(t, h, root, productInput("permission-config", nil))
	otherPID := managedProductID(t, h, root, productInput("other-config", nil))
	gid := configGroup(t, h, writer, pid, "editable", false, nil)
	base := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d", pid, gid)
	option := store.ProductOptionInput{Label: "配置项 😀", Value: "value", PriceDeltaCents: -500, Active: enabled(), ExpectedRevision: configRevision(t, h, root, pid)}
	for _, cookie := range []string{reader, customer, ""} {
		expected := 403
		if cookie == "" {
			expected = 401
		}
		expect(t, request(h, "POST", base+"/options", option, cookie, ""), expected)
	}
	created := request(h, "POST", base+"/options", option, writer, "")
	expect(t, created, 201)
	oid := int64(data(t, created)["id"].(float64))
	op := fmt.Sprintf("%s/options/%d", base, oid)
	for _, cookie := range []string{reader, customer} {
		expect(t, request(h, "PUT", op, option, cookie, ""), 403)
		expect(t, request(h, "DELETE", op, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, cookie, ""), 403)
	}
	expect(t, request(h, "POST", base+"/options", option, writer, ""), 409) // stale revision
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "POST", base+"/options", option, writer, ""), 409) // duplicate value
	option.ExpectedRevision = nil
	expect(t, request(h, "PUT", op, option, writer, ""), 409)
	option.ExpectedRevision = configRevision(t, h, root, pid)
	option.Label = "bad\x00label"
	expect(t, request(h, "PUT", op, option, writer, ""), 409)
	option.Label = "configured"
	option.SortOrder = -1
	expect(t, request(h, "PUT", op, option, writer, ""), 409)
	option.SortOrder = 0
	option.ExpectedRevision = configRevision(t, h, root, otherPID)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", otherPID, gid, oid), option, writer, ""), 404)
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", base+"/options/999999", option, writer, ""), 404)
	emptyRequired := store.ProductOptionGroupInput{Name: "empty", Slug: "empty", Required: true, Active: enabled()}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), emptyRequired, writer, ""), 409)
	invalidSlug := store.ProductOptionGroupInput{Name: "bad", Slug: "-bad", Active: enabled()}
	expect(t, request(h, "PUT", base, invalidSlug, writer, ""), 409)
	key := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "configuration-editor"}, writer, "")
	expect(t, key, 201)
	secret := data(t, key)["secret"].(string)
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, requestBearer(h, "PUT", op, option, secret), 200)
	contentRole(t, db, wid, "products.view")
	option.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, requestBearer(h, "PUT", op, option, secret), 403)
	expect(t, request(h, "DELETE", op, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 200)
	expect(t, request(h, "DELETE", base, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 200)
	expect(t, request(h, "DELETE", base, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 404)
}

func TestConfigurationNegativeAmountsAndCartRecovery(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "config-negative-root", "super_admin")
	_, customer := user(t, db, h, "config-negative-client", "customer")
	pid := managedProductID(t, h, root, productInput("negative-config", nil))
	in := store.ProductOptionInput{Label: "减价", Value: "discount", PriceDeltaCents: -2100, SetupDeltaCents: 500, Active: enabled()}
	gid := configGroup(t, h, root, pid, "discount", false, []store.ProductOptionInput{in})
	oid := configOption(t, h, root, pid, gid, "discount")
	op := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, gid, oid)
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{oid}}, customer, ""), 409)
	in.PriceDeltaCents = -500
	in.SetupDeltaCents = -101
	in.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, in, root, ""), 200)
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{oid}}, customer, ""), 409)
	in.SetupDeltaCents = -100
	in.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, in, root, ""), 200)
	v, item := configuredCart(t, h, customer, pid, []int64{oid}, 1)
	in.Active = new(bool)
	in.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", op, in, root, ""), 200)
	c := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["items"].([]any)[0].(map[string]any)
	if c["options_valid"] != false || c["active"] != false || c["current"] != false {
		t.Fatal("inactive selection not reflected")
	}
	expect(t, request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, customer, "disabled-option-order"), 409)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/cart/items/%d", item), map[string]any{"version": v, "quantity": 1, "option_ids": []int64{}}, customer, ""), 200)
	c = data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["items"].([]any)[0].(map[string]any)
	if c["options_valid"] != true || c["current"] != true || c["total_cents"] != float64(2100) {
		t.Fatal("cart configuration recovery failed")
	}
	expect(t, request(h, "DELETE", op, map[string]any{"expected_revision": configRevision(t, h, root, pid)}, root, ""), 200)
}

func TestConfigurationRacesAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "config-race-root", "super_admin")
	_, customer := user(t, db, h, "config-race-client", "customer")
	pid := managedProductID(t, h, root, productInput("race-config", nil))
	in := store.ProductOptionInput{Label: "small", Value: "small", Active: enabled()}
	gid := configGroup(t, h, root, pid, "size", false, []store.ProductOptionInput{in})
	oid := configOption(t, h, root, pid, gid, "small")
	op := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, gid, oid)
	in.ExpectedRevision = configRevision(t, h, root, pid)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() { codes <- request(h, "PUT", op, in, root, "").Code })
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("option update race", counts)
	}
	rev := configRevision(t, h, root, pid)
	var updated int
	var revision int64
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION config_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='product.option.updated' THEN RAISE EXCEPTION 'configuration audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER config_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION config_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	in.ExpectedRevision = rev
	in.Label = "should rollback"
	expect(t, request(h, "PUT", op, in, root, ""), 500)
	if err := db.Pool.QueryRow(t.Context(), "SELECT revision,(SELECT count(*) FROM product_options WHERE id=$2 AND label='should rollback') FROM products WHERE id=$1", pid, oid).Scan(&revision, &updated); err != nil || revision != *rev || updated != 0 {
		t.Fatal("audit failure committed", err)
	}
	// Add and delete serialize through the catalog lock. A referenced option can never disappear.
	start := make(chan struct{})
	result := make(chan int, 2)
	wg.Go(func() {
		<-start
		result <- request(h, "DELETE", op, map[string]any{"expected_revision": rev}, root, "").Code
	})
	wg.Go(func() {
		<-start
		result <- request(h, "POST", "/api/v1/cart/items", map[string]any{"version": 1, "product_id": pid, "quantity": 1, "option_ids": []int64{oid}}, customer, "").Code
	})
	close(start)
	wg.Wait()
	close(result)
	counts = map[int]int{}
	for code := range result {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("configuration deletion race", counts)
	}
}

func TestConfigurationBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "configuration-super", func(db *store.Store, h http.Handler) {
		rid, _ := user(t, db, h, "configuration-reader", "admin")
		contentRole(t, db, rid, "products.view")
		user(t, db, h, "configuration-client", "customer")
		_, root := user(t, db, h, "configuration-fixture-writer", "super_admin")
		managedProductID(t, h, root, productInput("config-browser", nil))
	})
}
