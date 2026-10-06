package httpapi

import (
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestNativeConfigurationRuleLifecycle(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "native-rules-admin", "super_admin")
	_, customer := user(t, db, h, "native-rules-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("native-rules", nil))
	option := func(value string) store.ProductOptionInput {
		return store.ProductOptionInput{Label: value, Value: value, Active: enabled()}
	}
	triggerGroup := configGroup(t, h, admin, pid, "trigger", false, []store.ProductOptionInput{option("on"), option("off")})
	resultGroup := configGroup(t, h, admin, pid, "result", false, []store.ProductOptionInput{option("allowed"), option("blocked")})
	trigger := configOption(t, h, admin, pid, triggerGroup, "on")
	allowed := configOption(t, h, admin, pid, resultGroup, "allowed")
	blocked := configOption(t, h, admin, pid, resultGroup, "blocked")
	base := fmt.Sprintf("/api/v1/admin/products/%d/configuration-rules", pid)
	input := store.NativeConfigurationRuleInput{Name: "Trigger requires allowed", Enabled: enabled(), Conditions: []store.NativeRulePredicate{{GroupID: triggerGroup, OptionID: &trigger, Operator: "equals"}}, Results: []store.NativeRulePredicate{{GroupID: resultGroup, OptionID: &allowed, Operator: "equals"}}, ExpectedRevision: configRevision(t, h, admin, pid)}
	expect(t, request(h, "POST", base, input, customer, ""), 403)
	stale := input
	stale.ExpectedRevision = new(int64)
	expect(t, request(h, "POST", base, stale, admin, ""), 409)
	wrong := input
	wrong.Results = []store.NativeRulePredicate{{GroupID: resultGroup, OptionID: &trigger, Operator: "equals"}}
	expect(t, request(h, "POST", base, wrong, admin, ""), 409)
	quantityOnFixed := input
	quantityOnFixed.Results = []store.NativeRulePredicate{{GroupID: resultGroup, OptionID: &allowed, Operator: "quantity_in", QtyMinimum: &pid, QtyMaximum: &pid}}
	expect(t, request(h, "POST", base, quantityOnFixed, admin, ""), 409)
	foreignProduct := managedProductID(t, h, admin, productInput("foreign-rule-reference", nil))
	foreignGroup := configGroup(t, h, admin, foreignProduct, "foreign", false, []store.ProductOptionInput{option("other")})
	foreignOption := configOption(t, h, admin, foreignProduct, foreignGroup, "other")
	foreign := input
	foreign.Results = []store.NativeRulePredicate{{GroupID: foreignGroup, OptionID: &foreignOption, Operator: "equals"}}
	expect(t, request(h, "POST", base, foreign, admin, ""), 409)
	if *configRevision(t, h, admin, pid) != *input.ExpectedRevision {
		t.Fatal("invalid reference changed revision")
	}
	created := request(h, "POST", base, map[string]any{
		"name": input.Name, "enabled": true, "expected_revision": *input.ExpectedRevision,
		"conditions": []map[string]any{{"group_id": triggerGroup, "option_id": trigger, "operator": "equals", "qty_minimum": nil, "qty_maximum": nil}},
		"results":    []map[string]any{{"group_id": resultGroup, "option_id": allowed, "operator": "equals", "qty_minimum": nil, "qty_maximum": nil}},
	}, admin, "")
	expect(t, created, 201)
	ruleID := int64(data(t, created)["id"].(float64))
	public := data(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""))
	if len(public["configuration_rules"].([]any)) != 1 {
		t.Fatal("enabled rule absent from public product")
	}
	version := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["version"]
	bad := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{trigger, blocked}}, customer, "")
	expect(t, bad, 409)
	if !strings.Contains(bad.Body.String(), input.Name) {
		t.Fatal("rule name missing from cart rejection", bad.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_options SET active=false WHERE id=$1", allowed); err != nil {
		t.Fatal(err)
	}
	inactive := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{}}, customer, "")
	expect(t, inactive, 409)
	if !strings.Contains(inactive.Body.String(), "invalid_configuration_rule") {
		t.Fatal("inactive reference did not fail closed", inactive.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_options SET active=true WHERE id=$1", allowed); err != nil {
		t.Fatal(err)
	}
	_, item := configuredCart(t, h, customer, pid, []int64{trigger, allowed}, 1)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE native_configuration_predicates SET option_id=$2 WHERE rule_id=$1 AND phase='result'", ruleID, blocked); err != nil {
		t.Fatal(err)
	}
	cartBeforeRuleChange := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	checkoutRule := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cartBeforeRuleChange["version"].(float64)), ItemIDs: []int64{item}}, customer, "native-rule-checkout-direct")
	expect(t, checkoutRule, 409)
	if !strings.Contains(checkoutRule.Body.String(), input.Name) {
		t.Fatal("checkout did not evaluate rule", checkoutRule.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE native_configuration_predicates SET option_id=$2 WHERE rule_id=$1 AND phase='result'", ruleID, allowed); err != nil {
		t.Fatal(err)
	}
	copyResponse := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/duplicate", pid), store.ProductDuplicateInput{Name: "Rule copy", Slug: "rule-copy"}, admin, "")
	expect(t, copyResponse, 201)
	copyID := int64(data(t, copyResponse)["id"].(float64))
	copyProduct := data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/products/%d", copyID), nil, admin, ""))
	copyRule := copyProduct["configuration_rules"].([]any)[0].(map[string]any)
	if copyRule["id"] == float64(ruleID) || copyRule["conditions"].([]any)[0].(map[string]any)["group_id"] == float64(triggerGroup) || copyRule["conditions"].([]any)[0].(map[string]any)["option_id"] == float64(trigger) || copyRule["results"].([]any)[0].(map[string]any)["option_id"] == float64(allowed) {
		t.Fatal("duplicate retained source identities", copyRule)
	}
	groupPath := fmt.Sprintf("/api/v1/admin/products/%d/option-groups/%d", pid, resultGroup)
	expect(t, request(h, "DELETE", groupPath, map[string]any{"expected_revision": configRevision(t, h, admin, pid)}, admin, ""), 409)
	input.Enabled = new(bool)
	input.ExpectedRevision = configRevision(t, h, admin, pid)
	expect(t, request(h, "PUT", fmt.Sprintf("%s/%d", base, ruleID), input, admin, ""), 200)
	public = data(t, request(h, "GET", fmt.Sprintf("/api/v1/catalog/%d", pid), nil, "", ""))
	if len(public["configuration_rules"].([]any)) != 0 {
		t.Fatal("disabled rule leaked publicly")
	}
	expect(t, request(h, "DELETE", groupPath, map[string]any{"expected_revision": configRevision(t, h, admin, pid)}, admin, ""), 409)
	input.Enabled = enabled()
	input.ExpectedRevision = configRevision(t, h, admin, pid)
	expect(t, request(h, "PUT", fmt.Sprintf("%s/%d", base, ruleID), input, admin, ""), 200)
	cart := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: int64(cart["version"].(float64)), ItemIDs: []int64{item}}, customer, "native-rule-checkout")
	expect(t, checkout, 409)
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/%d", base, ruleID), map[string]any{"expected_revision": configRevision(t, h, admin, pid)}, admin, ""), 200)
}

func TestNativeConfigurationRuleEmptyAndChangedTypeFailClosed(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "native-rule-corrupt-admin", "super_admin")
	_, customer := user(t, db, h, "native-rule-corrupt-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("native-rule-corrupt", nil))
	group := configGroup(t, h, admin, pid, "quantity", false, []store.ProductOptionInput{{Label: "Disk", Value: "disk", Active: enabled()}})
	option := configOption(t, h, admin, pid, group, "disk")
	var id int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO native_configuration_rules(product_id,name,enabled) VALUES($1,'broken',true) RETURNING id", pid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	version := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["version"]
	add := func() *string {
		w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{}}, customer, "")
		expect(t, w, 409)
		body := w.Body.String()
		return &body
	}
	if body := add(); !strings.Contains(*body, "invalid_configuration_rule") {
		t.Fatal("empty rule passed", *body)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO native_configuration_predicates(rule_id,phase,sort_order,group_id,option_id,operator) VALUES($1,'condition',0,$2,$3,'equals')", id, group, option); err != nil {
		t.Fatal(err)
	}
	if body := add(); !strings.Contains(*body, "invalid_configuration_rule") {
		t.Fatal("missing result passed", *body)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO native_configuration_predicates(rule_id,phase,sort_order,group_id,option_id,operator,qty_minimum,qty_maximum) VALUES($1,'result',0,$2,$3,'quantity_in',1,2)", id, group, option); err != nil {
		t.Fatal(err)
	}
	if body := add(); !strings.Contains(*body, "invalid_configuration_rule") {
		t.Fatal("changed type passed", *body)
	}
}

func TestNativeQuantityRuleTypeChange(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "native-quantity-admin", "super_admin")
	_, customer := user(t, db, h, "native-quantity-customer", "customer")
	pid := managedProductID(t, h, admin, productInput("native-quantity", nil))
	groupResponse := request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/option-groups", pid), store.ProductOptionGroupInput{Name: "storage", Slug: "storage", OptionType: 4, QtyMinimum: 1, QtyMaximum: 10, Active: enabled(), Options: []store.ProductOptionInput{{Label: "SSD", Value: "ssd", Active: enabled()}}, ExpectedRevision: configRevision(t, h, admin, pid)}, admin, "")
	expect(t, groupResponse, 201)
	group := int64(data(t, groupResponse)["id"].(float64))
	option := configOption(t, h, admin, pid, group, "ssd")
	one, two := int64(1), int64(2)
	in := store.NativeConfigurationRuleInput{Name: "storage range", Enabled: enabled(), Conditions: []store.NativeRulePredicate{{GroupID: group, OptionID: &option, Operator: "equals"}}, Results: []store.NativeRulePredicate{{GroupID: group, OptionID: &option, Operator: "quantity_in", QtyMinimum: &one, QtyMaximum: &two}}, ExpectedRevision: configRevision(t, h, admin, pid)}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/products/%d/configuration-rules", pid), in, admin, ""), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE product_option_groups SET option_type=1 WHERE id=$1", group); err != nil {
		t.Fatal(err)
	}
	version := data(t, request(h, "GET", "/api/v1/cart", nil, customer, ""))["version"]
	w := request(h, "POST", "/api/v1/cart/items", map[string]any{"version": version, "product_id": pid, "quantity": 1, "option_ids": []int64{option}}, customer, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "invalid_configuration_rule") {
		t.Fatal("changed type did not fail closed", w.Body.String())
	}
}
