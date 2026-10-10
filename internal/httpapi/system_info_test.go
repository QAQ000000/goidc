package httpapi

import "testing"

func TestAdminSystemInformationRequiresSettingsViewAndReturnsRuntimeFacts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "system-info-root", "super_admin")
	for _, path := range []string{"/api/v1/admin/system/runtime", "/api/v1/admin/system/about", "/api/v1/admin/system/database"} {
		if response := request(h, "GET", path, nil, root, ""); response.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	database := request(h, "GET", "/api/v1/admin/system/database", nil, root, "")
	if data(t, database)["status"] != "ready" {
		t.Fatalf("database status response=%s", database.Body.String())
	}
	filtered := request(h, "GET", "/api/v1/admin/system/database?q=notification_templates&limit=1", nil, root, "")
	expect(t, filtered, 200)
	page := data(t, filtered)
	items := page["items"].([]any)
	if page["total"] != float64(1) || page["limit"] != float64(1) || len(items) != 1 || items[0].(map[string]any)["name"] != "notification_templates" {
		t.Fatalf("database table filter failed: %s", filtered.Body.String())
	}
	empty := request(h, "GET", "/api/v1/admin/system/database?q=nonexistent_restore_table", nil, root, "")
	expect(t, empty, 200)
	if data(t, empty)["total"] != float64(0) || len(data(t, empty)["items"].([]any)) != 0 {
		t.Fatalf("database table empty failed: %s", empty.Body.String())
	}
	if response := request(h, "GET", "/api/v1/admin/system/runtime", nil, "", ""); response.Code != 401 {
		t.Fatalf("unauthorized runtime status=%d", response.Code)
	}
	staffID, staff := user(t, db, h, "system-info-staff", "admin")
	contentRole(t, db, staffID, "dashboard.view")
	if response := request(h, "GET", "/api/v1/admin/system/about", nil, staff, ""); response.Code != 403 {
		t.Fatalf("missing settings permission status=%d", response.Code)
	}
	if response := request(h, "GET", "/api/v1/admin/system/database", nil, staff, ""); response.Code != 403 {
		t.Fatalf("missing database settings permission status=%d", response.Code)
	}
	contentRole(t, db, staffID, "settings.view")
	response := request(h, "GET", "/api/v1/admin/system/about", nil, staff, "")
	if response.Code != 200 || data(t, response)["backend"] != "Go API" {
		t.Fatalf("settings viewer response=%s", response.Body.String())
	}
}
