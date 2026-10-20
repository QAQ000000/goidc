package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminCustomerLevelsVersionedCRUDAndPermission(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "customer-level-root", "super_admin")
	staffID, staff := user(t, db, h, "customer-level-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if response := request(h, "GET", "/api/v1/admin/customer-levels", nil, staff, ""); response.Code != 403 {
		t.Fatalf("staff without permission status=%d body=%s", response.Code, response.Body.String())
	}
	body := map[string]any{"name": "成长客户", "min_income_cents": 10000, "min_purchase_count": 2, "min_login_count": 3, "min_recent_login_count": 1, "min_renewal_count": 1, "min_recent_renewal_count": 1, "sort_order": 2, "active": true}
	created := request(h, "POST", "/api/v1/admin/customer-levels", body, root, "customer-level-create")
	expect(t, created, 201)
	replayed := request(h, "POST", "/api/v1/admin/customer-levels", body, root, "customer-level-create")
	expect(t, replayed, 200)
	if data(t, replayed)["id"] != data(t, created)["id"] {
		t.Fatalf("idempotent create returned a different id: %s", replayed.Body.String())
	}
	id := int64(data(t, created)["id"].(float64))
	page := request(h, "GET", "/api/v1/admin/customer-levels?q=成长&active=true&limit=1", nil, root, "")
	expect(t, page, 200)
	result := data(t, page)
	if result["total"] != float64(1) || len(result["items"].([]any)) != 1 {
		t.Fatalf("unexpected customer level page: %s", page.Body.String())
	}
	path := fmt.Sprintf("/api/v1/admin/customer-levels/%d", id)
	updated := map[string]any{"name": "成长客户（更新）", "min_income_cents": 20000, "min_purchase_count": 2, "min_login_count": 3, "min_recent_login_count": 1, "min_renewal_count": 1, "min_recent_renewal_count": 1, "sort_order": 1, "active": true, "expected_version": 1}
	expect(t, request(h, "PATCH", path, updated, root, "customer-level-update"), 200)
	expect(t, request(h, "PATCH", path, updated, root, "customer-level-update"), 200)
	expect(t, request(h, "PATCH", path, updated, root, "customer-level-stale"), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, root, "customer-level-delete-stale"), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 2}, root, "customer-level-delete"), 200)
}
