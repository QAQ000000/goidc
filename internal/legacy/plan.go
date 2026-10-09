package legacy

import (
	"fmt"
	"strings"
)

type Issue struct {
	Entity   string         `json:"entity"`
	LegacyID int64          `json:"legacy_id"`
	Code     string         `json:"code"`
	Detail   map[string]any `json:"detail"`
}
type Report struct {
	ApplyValidationError string         `json:"apply_validation_error,omitempty"`
	Mode                 string         `json:"mode"`
	RunID                int64          `json:"run_id,omitempty"`
	Source               string         `json:"source"`
	Counts               map[string]int `json:"source_counts"`
	Mapped               map[string]int `json:"mapped"`
	Replayed             map[string]int `json:"replayed"`
	Issues               []Issue        `json:"issues"`
}

func index(records []Record) map[int64]Record {
	out := map[int64]Record{}
	for _, r := range records {
		out[number(r, "id")] = r
	}
	return out
}
func (r *Report) issue(entity string, id int64, code string, detail map[string]any) {
	if detail == nil {
		detail = map[string]any{}
	}
	r.Issues = append(r.Issues, Issue{entity, id, code, detail})
}

func Inspect(snapshot Snapshot, source string) Report {
	out := Report{Source: source, Counts: map[string]int{}, Mapped: map[string]int{}, Replayed: map[string]int{}, Issues: []Issue{}}
	for entity, records := range snapshot {
		out.Counts[entity] = len(records)
	}
	users, products, orders, invoices, services := index(snapshot["users"]), index(snapshot["products"]), index(snapshot["orders"]), index(snapshot["invoices"]), index(snapshot["services"])
	customerGroups := index(snapshot["customer_groups"])
	navigationGroups := index(snapshot["service_navigation_groups"])
	for _, r := range snapshot["users"] {
		if !ValidCMFPasswordHash(strings.TrimSpace(r["password"])) {
			out.issue("users", number(r, "id"), "password_reset_required", nil)
		}
		if group := number(r, "groupid"); group != 0 && customerGroups[group] == nil {
			out.issue("users", number(r, "id"), "missing_customer_group", map[string]any{"group_id": group})
		}
	}
	for _, r := range snapshot["contacts"] {
		if users[number(r, "uid")] == nil {
			out.issue("contacts", number(r, "id"), "missing_customer", map[string]any{"uid": number(r, "uid")})
		}
	}
	for _, r := range snapshot["products"] {
		if len(snapshot["product_config_options"]) == 0 {
			out.issue("products", number(r, "id"), "configuration_not_migrated_sale_disabled", nil)
		}
		if group := number(r, "groupid"); group != 0 && navigationGroups[group] == nil {
			out.issue("products", number(r, "id"), "missing_service_navigation_group", map[string]any{"group_id": group})
		}
	}
	configGroups, configOptions := index(snapshot["product_config_groups"]), index(snapshot["product_config_options"])
	configSubs := index(snapshot["product_config_option_subs"])
	configPrices := map[int64]bool{}
	for _, r := range snapshot["product_config_prices"] {
		configPrices[number(r, "relid")] = true
	}
	for _, r := range snapshot["product_config_links"] {
		id, gid, pid := number(r, "id"), number(r, "gid"), number(r, "pid")
		if configGroups[gid] == nil {
			out.issue("product_config_links", id, "missing_config_group", map[string]any{"group_id": gid})
		}
		if products[pid] == nil {
			out.issue("product_config_links", id, "missing_product", map[string]any{"product_id": pid})
		}
	}
	for _, r := range snapshot["product_config_options"] {
		id, gid := number(r, "id"), number(r, "gid")
		if configGroups[gid] == nil {
			out.issue("product_config_options", id, "missing_config_group", map[string]any{"group_id": gid})
		}
		typ := number(r, "option_type")
		if typ != 1 && typ != 2 && typ != 3 && typ != 4 && typ != 5 && typ != 6 && typ != 8 && typ != 10 && typ != 11 && typ != 12 && typ != 13 && typ != 14 && typ != 19 {
			out.issue("product_config_options", id, "unsupported_option_type_preserved", map[string]any{"option_type": typ})
		}
	}
	for _, r := range snapshot["product_config_option_subs"] {
		id, option := number(r, "id"), number(r, "config_id")
		if configOptions[option] == nil {
			out.issue("product_config_option_subs", id, "missing_config_option", map[string]any{"config_id": option})
		}
		if len(snapshot["product_config_prices"]) > 0 && !configPrices[id] {
			out.issue("product_config_option_subs", id, "missing_option_price", nil)
		}
	}
	for _, r := range snapshot["product_config_option_links"] {
		id, option := number(r, "id"), number(r, "config_id")
		if configOptions[option] == nil {
			out.issue("product_config_option_links", id, "missing_config_option", map[string]any{"config_id": option})
		}
	}
	for _, r := range snapshot["service_option_selections"] {
		id, option := number(r, "id"), number(r, "configid")
		if configOptions[option] == nil {
			out.issue("service_option_selections", id, "missing_config_option", map[string]any{"config_id": option})
		}
		if configSubs[number(r, "optionid")] == nil && number(r, "optionid") != 0 {
			out.issue("service_option_selections", id, "missing_config_sub_option", map[string]any{"option_id": number(r, "optionid")})
		}
	}
	for _, r := range snapshot["service_navigation_groups"] {
		if _, known := ServiceNavigationIcon(r["fa_icon"]); !known {
			out.issue("service_navigation_groups", number(r, "id"), "navigation_icon_fallback", nil)
		}
	}
	for _, r := range snapshot["service_navigation_preferences"] {
		if users[number(r, "uid")] == nil {
			out.issue("service_navigation_preferences", number(r, "id"), "missing_customer", nil)
		}
		if navigationGroups[number(r, "groupid")] == nil {
			out.issue("service_navigation_preferences", number(r, "id"), "missing_service_navigation_group", nil)
		}
		if r["is_show"] == "" {
			out.issue("service_navigation_preferences", number(r, "id"), "null_navigation_visibility_hidden", nil)
		}
	}
	invoiceOrders := map[int64]int{}
	for _, r := range snapshot["orders"] {
		id := number(r, "id")
		invoiceID := number(r, "invoiceid")
		inv, exists := invoices[invoiceID]
		if users[number(r, "uid")] == nil {
			out.issue("orders", id, "missing_customer", nil)
		}
		if !exists {
			out.issue("orders", id, "missing_invoice", map[string]any{"invoice_id": invoiceID})
			continue
		}
		invoiceOrders[invoiceID]++
		if r["uid"] != inv["uid"] {
			out.issue("orders", id, "invoice_owner_mismatch", nil)
		}
		a, e1 := Cents(r["amount"])
		b, e2 := Cents(inv["total"])
		if e1 != nil || e2 != nil || a != b {
			out.issue("orders", id, "order_invoice_amount_mismatch", map[string]any{"order_amount": r["amount"], "invoice_amount": inv["total"]})
		}
		if strings.EqualFold(r["status"], "Pending") && strings.EqualFold(inv["status"], "Cancelled") {
			out.issue("orders", id, "pending_order_cancelled_invoice", nil)
		}
	}
	sums := map[int64]int64{}
	for _, r := range snapshot["invoice_items"] {
		id, invoiceID := number(r, "id"), number(r, "invoice_id")
		if invoices[invoiceID] == nil {
			out.issue("invoice_items", id, "missing_invoice", map[string]any{"invoice_id": invoiceID})
		}
		if r["delete_time"] != "0" {
			continue
		}
		if cents, err := Cents(r["amount"]); err == nil {
			sums[invoiceID] += cents
		}
		if r["type"] == "host" && services[number(r, "rel_id")] == nil {
			out.issue("invoice_items", id, "missing_host_record", map[string]any{"host_id": number(r, "rel_id")})
		}
	}
	for _, r := range snapshot["invoices"] {
		id := number(r, "id")
		if invoiceOrders[id] == 0 {
			out.issue("invoices", id, "standalone_invoice", nil)
		}
		if invoiceOrders[id] > 1 {
			out.issue("invoices", id, "multiple_orders_for_invoice", nil)
		}
		if amount, err := Cents(r["total"]); err == nil && amount != sums[id] {
			out.issue("invoices", id, "invoice_item_total_mismatch", map[string]any{"total": r["total"], "item_sum_cents": sums[id]})
		}
		out.issue("invoices", id, "historical_invoice_requires_review", nil)
	}
	for _, r := range snapshot["services"] {
		id := number(r, "id")
		order := orders[number(r, "orderid")]
		if products[number(r, "productid")] == nil || users[number(r, "uid")] == nil || order == nil {
			out.issue("services", id, "missing_relationship", nil)
			continue
		}
		if order["uid"] != r["uid"] {
			out.issue("services", id, "order_owner_mismatch", nil)
		}
		inv := invoices[number(order, "invoiceid")]
		if r["domainstatus"] == "Active" && (inv == nil || inv["status"] != "Paid") {
			out.issue("services", id, "active_host_without_paid_invoice", nil)
		}
		out.issue("services", id, "upstream_resource_requires_verification", nil)
	}
	return out
}

func status(entity, raw string) (string, error) {
	options := map[string]map[string]string{
		"orders":   {"Pending": "pending", "Active": "active", "Completed": "active", "Cancelled": "cancelled", "Fraud": "cancelled", "Refunded": "refunded"},
		"invoices": {"Unpaid": "unpaid", "Paid": "paid", "Cancelled": "cancelled", "Refunded": "refunded"},
		"services": {"Pending": "pending_payment", "Active": "active", "Suspended": "suspended", "Cancelled": "cancelled", "Fraud": "cancelled", "Completed": "terminated", "Deleted": "terminated"},
	}
	value, ok := options[entity][raw]
	if !ok {
		return "", fmt.Errorf("unsupported %s state %q", entity, raw)
	}
	return value, nil
}
