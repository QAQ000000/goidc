package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestGlobalConfigurationProjectionLifecycle(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "global-config-admin", "super_admin")
	reader, rc := user(t, db, h, "global-config-reader", "admin")
	contentRole(t, db, reader, "products.view")
	pid := managedProductID(t, h, root, productInput("global-config-one", nil))
	pid2 := managedProductID(t, h, root, productInput("global-config-two", nil))
	path := "/api/v1/admin/global-configuration-groups"
	in := store.GlobalConfigurationInput{Name: "Shared backup", Definitions: []store.GlobalDefinition{{ProductOptionGroupInput: store.ProductOptionGroupInput{Name: "Backup", Slug: "backup", OptionType: 1, Active: enabled(), Required: true}, Items: []store.GlobalOption{{ProductOptionInput: store.ProductOptionInput{Label: "Daily", Value: "daily", Active: enabled(), PriceDeltaCents: 100}, Prices: []store.GlobalOptionPrice{{BillingCycle: "monthly", PriceCents: 350, Active: true}}}}}}}
	in.Definitions = append(in.Definitions, store.GlobalDefinition{ProductOptionGroupInput: store.ProductOptionGroupInput{Name: "Storage", Slug: "storage", OptionType: 4, Active: enabled(), QtyMinimum: 1, QtyMaximum: 10, QtyStage: 1}, Items: []store.GlobalOption{{ProductOptionInput: store.ProductOptionInput{Label: "GB", Value: "gb", Active: enabled(), PriceTierChanges: map[string][]store.OptionPriceTier{"monthly": {{Minimum: 1, Maximum: 10, PriceCents: 25}}}}}}})
	expect(t, request(h, "GET", path, nil, rc, ""), 200)
	expect(t, request(h, "POST", path, in, rc, "global-reader-create"), 403)
	w := request(h, "POST", path, in, root, "global-create-key")
	expect(t, w, 200)
	gid := int64(data(t, w)["id"].(float64))
	detail := fmt.Sprintf("%s/%d", path, gid)
	replay := request(h, "POST", path, in, root, "global-create-key")
	expect(t, replay, 200)
	if data(t, replay)["id"] != float64(gid) {
		t.Fatal("idempotency created another group")
	}
	in.Name = "Changed"
	expect(t, request(h, "POST", path, in, root, "global-create-key"), 409)
	in.Name = "Shared backup"
	assign := store.GlobalConfigurationInput{ExpectedVersion: 1, ProductIDs: []int64{pid, pid2}, ExpectedRevisions: map[int64]int64{pid: *configRevision(t, h, root, pid), pid2: *configRevision(t, h, root, pid2)}}
	expect(t, request(h, "PUT", detail+"/assignments", assign, root, "global-assign-key"), 200)
	var group, option int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT g.id,o.id FROM product_option_groups g JOIN product_options o ON o.group_id=g.id WHERE g.product_id=$1 AND g.global_group_id=$2 AND g.slug='backup'", pid, gid).Scan(&group, &option); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "DELETE", detail, map[string]any{"expected_version": 2}, root, "global-delete-used"), 409)
	// Trigger checks both sides of moves, including moving an existing managed
	// option/price/tier out of ownership rather than just editing its fields.
	independent := configGroup(t, h, root, pid, "independent", false, []store.ProductOptionInput{{Label: "Local", Value: "local", Active: enabled()}})
	independentOption := configOption(t, h, root, pid, independent, "local")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_options SET group_id=$2 WHERE id=$1", option, independent); err == nil {
		t.Fatal("managed option moved out of global ownership")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_option_prices SET option_id=$2 WHERE option_id=$1", option, independentOption); err == nil {
		t.Fatal("managed price moved out of global ownership")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_option_price_tiers SET option_id=$2 WHERE option_id IN (SELECT o.id FROM product_options o JOIN product_option_groups g ON g.id=o.group_id WHERE g.global_group_id=$1)", gid, independentOption); err == nil {
		t.Fatal("managed tier moved out of global ownership")
	}
	_, customer := user(t, db, h, "global-config-customer", "customer")
	cartVersion, cartItem := configuredCart(t, h, customer, pid, []int64{option}, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: cartVersion, ItemIDs: []int64{cartItem}}, customer, "global-snapshot-order")
	expect(t, order, 201)
	orderID := int64(data(t, order)["order_id"].(float64))
	var snapshotBefore []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", orderID).Scan(&snapshotBefore); err != nil {
		t.Fatal(err)
	}
	configuredCart(t, h, customer, pid, []int64{option}, 1)
	local := store.ProductOptionInput{Label: "Drift", Value: "daily", Active: enabled(), ExpectedRevision: configRevision(t, h, root, pid)}
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, group, option), local, root, ""), 409)
	in.ExpectedVersion = 2
	in.Definitions[0].Items[0].Prices[0].PriceCents = 550
	expect(t, request(h, "PUT", detail, in, root, "global-sync-key"), 200)
	for _, p := range []int64{pid, pid2} {
		var price int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT pp.price_cents FROM product_option_prices pp JOIN product_options o ON o.id=pp.option_id JOIN product_option_groups g ON g.id=o.group_id WHERE g.product_id=$1 AND g.global_group_id=$2", p, gid).Scan(&price); err != nil || price != 550 {
			t.Fatalf("price projection: %d %v", price, err)
		}
	}
	var snapshotAfter []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT config_snapshot FROM order_items WHERE order_id=$1", orderID).Scan(&snapshotAfter); err != nil {
		t.Fatal(err)
	}
	if string(snapshotBefore) != string(snapshotAfter) {
		t.Fatal("global update mutated historical snapshot")
	}
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	for _, raw := range cart["items"].([]any) {
		if raw.(map[string]any)["current"] != false {
			t.Fatal("global update failed to stale existing cart")
		}
	}
	var stable int64
	_ = db.Pool.QueryRow(t.Context(), "SELECT id FROM product_options WHERE group_id=$1 AND value='daily'", group).Scan(&stable)
	if stable != option {
		t.Fatal("projection changed option IDs")
	}
	in.ExpectedVersion = 3
	in.Definitions[0].Items = nil
	expect(t, request(h, "PUT", detail, in, root, "global-removal-key"), 409)
	fixed := managedProductID(t, h, root, productInput("global-fixed", nil))
	if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET provision_mode='nokvm' WHERE id=$1", fixed); err != nil {
		t.Fatal(err)
	}
	fixedAssign := store.GlobalConfigurationInput{ExpectedVersion: 3, ProductIDs: []int64{pid, pid2, fixed}, ExpectedRevisions: map[int64]int64{fixed: *configRevision(t, h, root, fixed)}}
	expect(t, request(h, "PUT", detail+"/assignments", fixedAssign, root, "global-fixed-assign"), 409)
	copy := request(h, "POST", path+"/copy", store.GlobalConfigurationInput{SourceID: gid, ExpectedVersion: 3, Name: "Independent copy"}, root, "global-deep-copy")
	expect(t, copy, 200)
	copyID := int64(data(t, copy)["id"].(float64))
	var copiedPrice int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT (definitions->0->'items'->0->'prices'->0->>'price_cents')::bigint FROM global_configuration_groups WHERE id=$1", copyID).Scan(&copiedPrice); err != nil || copiedPrice != 550 {
		t.Fatalf("deep price copy %d %v", copiedPrice, err)
	}
	duplication := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "Detached product", Slug: "global-product-copy"}, root, "")
	expect(t, duplication, 201)
	copiedProduct := int64(data(t, duplication)["id"].(float64))
	var shared bool
	_ = db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM product_option_groups WHERE product_id=$1 AND global_group_id IS NOT NULL)", copiedProduct).Scan(&shared)
	if shared {
		t.Fatal("product copy shared global ownership")
	}
	detach := store.GlobalConfigurationInput{ExpectedVersion: 3, ProductIDs: []int64{}, ExpectedRevisions: map[int64]int64{pid: *configRevision(t, h, root, pid), pid2: *configRevision(t, h, root, pid2)}}
	expect(t, request(h, "PUT", detail+"/assignments", detach, root, "global-detach-noack"), 409)
	detach.AcknowledgeDetach = true
	expect(t, request(h, "PUT", detail+"/assignments", detach, root, "global-detach-key"), 200)
	local.ExpectedRevision = configRevision(t, h, root, pid)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d/options/%d", pid, group, option), local, root, ""), 200)
	expect(t, request(h, "DELETE", detail, map[string]any{"expected_version": 4}, root, "global-delete-final"), 200)
}
