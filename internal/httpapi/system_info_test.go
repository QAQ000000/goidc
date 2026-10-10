package httpapi

import "testing"

func TestAdminSystemInformationRequiresSettingsViewAndReturnsRuntimeFacts(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "system-info-root", "super_admin")
	for _, path := range []string{"/api/v1/admin/system/runtime", "/api/v1/admin/system/about"} {
		if response := request(h, "GET", path, nil, root, ""); response.Code != 200 {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
	if response := request(h, "GET", "/api/v1/admin/system/runtime", nil, "", ""); response.Code != 401 {
		t.Fatalf("unauthorized runtime status=%d", response.Code)
	}
	staffID, staff := user(t, db, h, "system-info-staff", "admin")
	contentRole(t, db, staffID, "dashboard.view")
	if response := request(h, "GET", "/api/v1/admin/system/about", nil, staff, ""); response.Code != 403 {
		t.Fatalf("missing settings permission status=%d", response.Code)
	}
	contentRole(t, db, staffID, "settings.view")
	response := request(h, "GET", "/api/v1/admin/system/about", nil, staff, "")
	if response.Code != 200 || data(t, response)["backend"] != "Go API" {
		t.Fatalf("settings viewer response=%s", response.Body.String())
	}
}
