package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestNativeHierarchyManagementIsAtomicAndCopiesRelationships(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "native-hierarchy-admin", "super_admin")
	_, customer := user(t, db, h, "native-hierarchy-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("native-hierarchy", nil))
	ptr := func(id int64) *int64 { return &id }
	makeGroup := func(name string, parent int64, required bool, choices map[string]int64) int64 {
		options := []store.ProductOptionInput{}
		for value, root := range choices {
			options = append(options, store.ProductOptionInput{Label: value, Value: value, Active: enabled(), PriceDeltaCents: 100, ParentOptionID: ptr(root)})
		}
		in := store.ProductOptionGroupInput{Name: name, Slug: name, Required: required, Active: enabled(), OptionType: 20, ParentGroupID: ptr(parent), Options: options, ExpectedRevision: configRevision(t, h, admin, pid)}
		w := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), in, admin, "")
		expect(t, w, 201)
		return int64(data(t, w)["id"].(float64))
	}
	root := makeGroup("region", 0, true, map[string]int64{"a": 0, "b": 0})
	a := configOption(t, h, admin, pid, root, "a")
	b := configOption(t, h, admin, pid, root, "b")
	child := makeGroup("zone", root, true, map[string]int64{"a1": a, "b1": b})
	a1 := configOption(t, h, admin, pid, child, "a1")
	b1 := configOption(t, h, admin, pid, child, "b1")
	grand := makeGroup("plan", child, true, map[string]int64{"small": a1, "large": b1})
	small := configOption(t, h, admin, pid, grand, "small")
	otherRoot := makeGroup("other", 0, false, map[string]int64{"x": 0, "y": 0})
	x := configOption(t, h, admin, pid, otherRoot, "x")
	y := configOption(t, h, admin, pid, otherRoot, "y")
	emptyChild := makeGroup("individual", root, false, nil)
	newOption := store.ProductOptionInput{Label: "individual-a", Value: "individual-a", Active: enabled(), ParentOptionID: ptr(a), ExpectedRevision: configRevision(t, h, admin, pid)}
	individualPath := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d", pid, emptyChild)
	created := request(h, "POST", individualPath+"/options", newOption, admin, "")
	expect(t, created, 201)
	individual := int64(data(t, created)["id"].(float64))
	clear := store.ProductOptionGroupInput{Name: "individual", Slug: "individual", Active: enabled(), OptionType: 20, ParentGroupID: ptr(0), ExpectedRevision: configRevision(t, h, admin, pid)}
	expect(t, request(h, "PUT", individualPath, clear, admin, ""), 409)
	clear.OptionParents = map[int64]int64{individual: 0}
	expect(t, request(h, "PUT", individualPath, clear, admin, ""), 200)
	var cleared bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT g.parent_group_id IS NULL AND o.parent_option_id IS NULL AND g.hierarchy_native AND o.hierarchy_native FROM product_option_groups g JOIN product_options o ON o.group_id=g.id WHERE o.id=$1", individual).Scan(&cleared); err != nil || !cleared {
		t.Fatal("clear did not create a native root", cleared, err)
	}
	clear.ParentGroupID = ptr(root)
	clear.OptionParents = map[int64]int64{individual: a}
	clear.ExpectedRevision = configRevision(t, h, admin, pid)
	expect(t, request(h, "PUT", individualPath, clear, admin, ""), 200)
	clear.ParentGroupID = ptr(0)
	clear.OptionParents = map[int64]int64{individual: 0}
	clear.OptionType = 0
	clear.ExpectedRevision = configRevision(t, h, admin, pid)
	expect(t, request(h, "PUT", individualPath, clear, admin, ""), 200)
	var nativeCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM product_option_groups WHERE product_id=$1 AND hierarchy_native AND legacy_id IS NULL", pid).Scan(&nativeCount); err != nil || nativeCount != 5 {
		t.Fatal("new graph depended on legacy IDs", nativeCount, err)
	}
	path := func(group int64) string { return fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d", pid, group) }
	groupInput := func(name string, parent int64) store.ProductOptionGroupInput {
		return store.ProductOptionGroupInput{Name: name, Slug: name, Active: enabled(), OptionType: 20, ParentGroupID: ptr(parent), ExpectedRevision: configRevision(t, h, admin, pid)}
	}
	assertRejected := func(group int64, in store.ProductOptionGroupInput, code string) {
		t.Helper()
		before := *configRevision(t, h, admin, pid)
		var auditsBefore, auditsAfter int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events").Scan(&auditsBefore); err != nil {
			t.Fatal(err)
		}
		w := request(h, "PUT", path(group), in, admin, "")
		expect(t, w, 409)
		if code != "" && !strings.Contains(w.Body.String(), code) {
			t.Fatal(w.Body.String())
		}
		if *configRevision(t, h, admin, pid) != before {
			t.Fatal("failed update advanced revision")
		}
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events").Scan(&auditsAfter); err != nil || auditsAfter != auditsBefore {
			t.Fatal("failed update left audit", err)
		}
	}
	cycle := groupInput("region", child)
	cycle.OptionParents = map[int64]int64{a: a1, b: b1}
	assertRejected(root, cycle, "unsupported_product_configuration")
	missingMap := groupInput("zone", otherRoot)
	assertRejected(child, missingMap, "unsupported_product_configuration")
	partialMap := groupInput("zone", otherRoot)
	partialMap.OptionParents = map[int64]int64{a1: x}
	assertRejected(child, partialMap, "unsupported_product_configuration")
	foreignMap := groupInput("zone", otherRoot)
	foreignMap.OptionParents = map[int64]int64{x: y}
	assertRejected(child, foreignMap, "invalid_product_options")
	for _, target := range []struct{ group, option int64 }{{root, 0}, {root, a}} {
		deletePath := path(target.group)
		if target.option > 0 {
			deletePath += fmt.Sprintf("/options/%d", target.option)
		}
		w := request(h, "DELETE", deletePath, map[string]any{"expected_revision": configRevision(t, h, admin, pid)}, admin, "")
		expect(t, w, 409)
		if !strings.Contains(w.Body.String(), "configuration_in_use") {
			t.Fatal("referenced parent delete accepted", w.Body.String())
		}
	}
	foreignPID := managedProductID(t, h, admin, productInput("foreign-hierarchy", nil))
	foreignGroup := configGroup(t, h, admin, foreignPID, "foreign", false, []store.ProductOptionInput{{Label: "foreign", Value: "foreign", Active: enabled()}})
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_option_groups SET option_type=20 WHERE id=$1", foreignGroup); err != nil {
		t.Fatal(err)
	}
	assertRejected(child, groupInput("zone", foreignGroup), "unsupported_product_configuration")
	_, reader := user(t, db, h, "native-hierarchy-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='native-hierarchy-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", pid), nil, reader, ""), 200)
	for _, cookie := range []string{reader, customer} {
		expect(t, request(h, "PUT", path(child), groupInput("zone", root), cookie, ""), 403)
	}
	_, buyer := user(t, db, h, "native-hierarchy-buyer", "customer")
	configuredCart(t, h, buyer, pid, []int64{a, a1, small}, 1)
	move := groupInput("zone", otherRoot)
	move.Required = true
	move.OptionParents = map[int64]int64{a1: x, b1: y}
	stale := *move.ExpectedRevision
	expect(t, request(h, "PUT", path(child), move, admin, ""), 200)
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, buyer, ""))
	if cart["items"].([]any)[0].(map[string]any)["options_valid"] != false {
		t.Fatal("old branch remained valid after parent move")
	}
	move.ExpectedRevision = &stale
	expect(t, request(h, "PUT", path(child), move, admin, ""), 409)
	// An omitted parent field preserves the graph on older clients.
	preserve := groupInput("zone", 0)
	preserve.ParentGroupID = nil
	preserve.Required = true
	expect(t, request(h, "PUT", path(child), preserve, admin, ""), 200)
	var parent int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT parent_group_id FROM product_option_groups WHERE id=$1", child).Scan(&parent); err != nil || parent != otherRoot {
		t.Fatal("omitted parent erased relation", parent, err)
	}
	_, newBuyer := user(t, db, h, "native-hierarchy-new-buyer", "customer")
	configuredCart(t, h, newBuyer, pid, []int64{a, x, a1, small}, 1)
	optionPath := fmt.Sprintf("%s/options/%d", path(child), a1)
	invalidOption := store.ProductOptionInput{Label: "a1", Value: "a1", Active: enabled(), ParentOptionID: ptr(a), ExpectedRevision: configRevision(t, h, admin, pid)}
	expect(t, request(h, "PUT", optionPath, invalidOption, admin, ""), 409)
	var optionParent int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT parent_option_id FROM product_options WHERE id=$1", a1).Scan(&optionParent); err != nil || optionParent != x {
		t.Fatal("invalid option edit changed parent", optionParent, err)
	}
	copy := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "native copy", Slug: "native-copy"}, admin, "")
	expect(t, copy, 201)
	copyID := int64(data(t, copy)["id"].(float64))
	var corrupt int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM product_option_groups g WHERE g.product_id=$1 AND (NOT g.hierarchy_native OR g.legacy_id IS NOT NULL OR EXISTS(SELECT 1 FROM product_option_groups p WHERE p.id=g.parent_group_id AND p.product_id<>g.product_id))`, copyID).Scan(&corrupt); err != nil || corrupt != 0 {
		t.Fatal("copy references source group", corrupt, err)
	}
	copyOption := func(value string) int64 {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), "SELECT po.id FROM product_options po JOIN product_option_groups g ON g.id=po.group_id WHERE g.product_id=$1 AND po.value=$2", copyID, value).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	_, copyBuyer := user(t, db, h, "native-hierarchy-copy-buyer", "customer")
	configuredCart(t, h, copyBuyer, copyID, []int64{copyOption("a"), copyOption("x"), copyOption("a1"), copyOption("small")}, 1)
	copyCart := data(t, request(h, "GET", "/api/v1/cart", nil, copyBuyer, ""))
	expect(t, request(h, "POST", "/api/v1/cart/items", map[string]any{"version": copyCart["version"], "product_id": copyID, "quantity": 1, "option_ids": []int64{copyOption("a"), copyOption("y"), copyOption("a1"), copyOption("small")}}, copyBuyer, ""), 409)
}
