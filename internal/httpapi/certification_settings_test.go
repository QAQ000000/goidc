package httpapi

import (
	"net/http"
	"testing"
)

func TestCertificationSettingsContractAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "cert-settings-root", "super_admin")
	_, staff := user(t, db, h, "cert-settings-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['certifications.view'] WHERE id=(SELECT admin_role_id FROM users WHERE username='cert-settings-reader')"); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/certification-settings"
	reader := request(h, http.MethodGet, path, nil, staff, "")
	expect(t, reader, http.StatusOK)
	settings := data(t, reader)
	if settings["version"] != float64(1) || settings["default_provider_id"] != "manual" {
		t.Fatalf("unexpected certification settings: %#v", settings)
	}
	if _, ok := settings["instances"]; !ok {
		t.Fatal("missing certification channel instances")
	}
	input := map[string]any{
		"enabled": true, "require_for_purchase": true, "default_provider_id": "manual",
		"advanced_enabled": true, "auto_update_name": true, "require_phone_match": true,
		"notify_on_approval": true, "suspend_unverified": true, "allow_file_upload": true,
		"expected_version": 1,
	}
	expect(t, request(h, http.MethodPut, path, input, staff, "cert-settings-1"), http.StatusForbidden)
	updated := request(h, http.MethodPut, path, input, root, "cert-settings-1")
	expect(t, updated, http.StatusOK)
	if data(t, updated)["version"] != float64(2) {
		t.Fatalf("expected version 2: %v", updated.Body.String())
	}
	replay := request(h, http.MethodPut, path, input, root, "cert-settings-1")
	expect(t, replay, http.StatusOK)
	if data(t, replay)["replayed"] != true {
		t.Fatal("expected idempotent replay")
	}
	input["enabled"] = false
	expect(t, request(h, http.MethodPut, path, input, root, "cert-settings-1"), http.StatusConflict)
	input["expected_version"] = 1
	expect(t, request(h, http.MethodPut, path, input, root, "cert-settings-2"), http.StatusConflict)
	fresh := request(h, http.MethodGet, path, nil, root, "")
	expect(t, fresh, http.StatusOK)
	if data(t, fresh)["version"] != float64(2) || data(t, fresh)["suspend_unverified"] != true {
		t.Fatalf("settings were not persisted: %v", fresh.Body.String())
	}
}
