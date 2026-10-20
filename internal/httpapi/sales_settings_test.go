package httpapi

import (
	"fmt"
	"testing"
)

func TestSalesSettingsPermissionsAndVersioning(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "sales-settings-root", "super_admin")
	_, reader := user(t, db, h, "sales-settings-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['sales.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=(SELECT id FROM administrators WHERE username='sales-settings-reader'))"); err != nil {
		t.Fatal(err)
	}
	_, denied := user(t, db, h, "sales-settings-denied", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=(SELECT id FROM administrators WHERE username='sales-settings-denied'))"); err != nil {
		t.Fatal(err)
	}

	settings := request(h, "GET", "/api/v1/admin/sales-settings", nil, reader, "")
	expect(t, settings, 200)
	if data(t, settings)["version"].(float64) != 1 {
		t.Fatalf("initial settings version: %#v", data(t, settings)["version"])
	}
	expect(t, request(h, "GET", "/api/v1/admin/sales-settings", nil, denied, ""), 403)

	payload := map[string]any{"order_assignment": "round_robin", "registration_assignment": "customer_choice", "expected_version": 1}
	updated := request(h, "PUT", "/api/v1/admin/sales-settings", payload, root, "sales-settings-update-1")
	expect(t, updated, 200)
	if data(t, updated)["version"].(float64) != 2 {
		t.Fatalf("updated settings version: %#v", data(t, updated)["version"])
	}
	replay := request(h, "PUT", "/api/v1/admin/sales-settings", payload, root, "sales-settings-update-1")
	expect(t, replay, 200)
	if data(t, replay)["version"].(float64) != 2 {
		t.Fatalf("replayed settings version: %#v", data(t, replay)["version"])
	}
	payload["order_assignment"] = "random"
	expect(t, request(h, "PUT", "/api/v1/admin/sales-settings", payload, root, "sales-settings-update-2"), 409)
	_ = rootID
}

func TestSalesCommissionGroupsTiersAndStaff(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "sales-commission-root", "super_admin")
	staffID, staff := user(t, db, h, "sales-commission-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['sales.view','sales.manage'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}

	groups := data(t, request(h, "GET", "/api/v1/admin/sales-commission-groups", nil, staff, ""))
	if groups["total"].(float64) != 0 || len(groups["items"].([]any)) != 0 {
		t.Fatalf("unexpected initial groups: %#v", groups)
	}
	group := request(h, "POST", "/api/v1/admin/sales-commission-groups", map[string]any{
		"name": "标准销售", "purchase_rate_bps": 500, "renewal_rate_bps": 300, "upgrade_rate_bps": 200,
		"include_renewal": true, "include_upgrade": true, "active": true, "expected_version": 0,
	}, staff, "sales-group-create-1")
	expect(t, group, 201)
	groupID := int64(data(t, group)["id"].(float64))
	version := int64(data(t, group)["version"].(float64))
	groupList := data(t, request(h, "GET", "/api/v1/admin/sales-commission-groups", nil, root, ""))
	if groupList["total"].(float64) != 1 {
		t.Fatalf("group count: %#v", groupList["total"])
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/sales-commission-groups/%d", groupID), map[string]any{
		"name": "标准销售", "purchase_rate_bps": 600, "renewal_rate_bps": 300, "upgrade_rate_bps": 200,
		"include_renewal": true, "include_upgrade": true, "active": true, "expected_version": version,
	}, staff, "sales-group-update-1"), 200)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/sales-commission-groups/%d", groupID), map[string]any{
		"name": "标准销售", "purchase_rate_bps": 700, "renewal_rate_bps": 300, "upgrade_rate_bps": 200,
		"include_renewal": true, "include_upgrade": true, "active": true, "expected_version": version,
	}, staff, "sales-group-update-2"), 409)

	tier := request(h, "POST", "/api/v1/admin/sales-commission-tiers", map[string]any{"threshold_cents": 100000, "extra_rate_bps": 100, "active": true, "expected_version": 0}, staff, "sales-tier-create-1")
	expect(t, tier, 201)
	tierID := int64(data(t, tier)["id"].(float64))
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/sales-commission-tiers/%d", tierID), map[string]any{"threshold_cents": 100000, "extra_rate_bps": 200, "active": true, "expected_version": int64(data(t, tier)["version"].(float64))}, staff, "sales-tier-update-1"), 200)

	staffPage := data(t, request(h, "GET", "/api/v1/admin/sales-staff", nil, staff, ""))
	if staffPage["total"].(float64) < 2 {
		t.Fatalf("sales staff page: %#v", staffPage["total"])
	}
	var adminVersion int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_version FROM administrators WHERE id=$1", rootID).Scan(&adminVersion); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/sales-staff/%d", rootID), map[string]any{"sales_enabled": true, "customer_scope": "owned", "customer_ownerless": false, "sales_commission_group_id": groupID, "expected_version": adminVersion}, staff, "sales-staff-update-1"), 200)
	var enabled bool
	var assignedGroupID *int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT sales_enabled,sales_commission_group_id FROM administrators WHERE id=$1", rootID).Scan(&enabled, &assignedGroupID); err != nil {
		t.Fatal(err)
	}
	if !enabled || assignedGroupID == nil || *assignedGroupID != groupID {
		t.Fatalf("sales staff binding: enabled=%v group=%v", enabled, assignedGroupID)
	}
}
