package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/codex/zjmfgo/internal/supplier"
)

func TestSupplierAPISettingsSchemaPermissionsAndConnectionProbe(t *testing.T) {
	f := supplierSetup(t)

	settings := request(f.h, "GET", "/api/v1/admin/supplier-api-settings", nil, f.root, "")
	expect(t, settings, 200)
	body := data(t, settings)
	providers, ok := body["providers"].([]any)
	if !ok || len(providers) == 0 {
		t.Fatalf("compiled supplier descriptors missing: %#v", body["providers"])
	}
	foundBuiltin := false
	for _, value := range providers {
		definition := value.(map[string]any)
		if definition["provider"].(map[string]any)["id"] == supplier.ProviderZJMF {
			descriptor := definition["descriptor"].(map[string]any)
			foundBuiltin = descriptor["instance_mode"] == "supplier" && descriptor["schema_version"] == float64(1) && len(descriptor["fields"].([]any)) == 3
		}
	}
	if !foundBuiltin {
		t.Fatal("builtin supplier schema missing or changed")
	}
	accounts, ok := body["accounts"].([]any)
	if !ok || len(accounts) != 1 {
		t.Fatalf("supplier account summaries missing: %#v", body["accounts"])
	}
	for _, secret := range []string{"supplier-private-password", "supplier-private-jwt"} {
		if strings.Contains(settings.Body.String(), secret) {
			t.Fatalf("supplier secret leaked in settings response: %s", secret)
		}
	}
	readerSettings := request(f.h, "GET", "/api/v1/admin/supplier-api-settings", nil, f.reader, "")
	expect(t, readerSettings, 200)
	expect(t, request(f.h, "GET", "/api/v1/admin/supplier-api-settings", nil, f.customer, ""), 401)
	expect(t, request(f.h, "PUT", "/api/v1/admin/supplier-api-settings", store.ResourceAPISettingsInput{Enabled: enabled(), RequireCertification: enabled(), RequireVerifiedPhone: enabled(), ExpectedVersion: 1}, f.reader, "resource-api-reader-denied"), 403)

	testPath := fmt.Sprintf("/api/v1/admin/suppliers/%d/test-connection", f.supplierID)
	probe := request(f.h, "POST", testPath, map[string]any{"expected_version": 1}, f.root, "supplier-connection-probe-1")
	expect(t, probe, 200)
	probeResult := data(t, probe)
	if probeResult["ok"] != true || probeResult["product_count"] != float64(1) {
		t.Fatalf("supplier connection probe failed: %#v", probeResult)
	}
	replay := request(f.h, "POST", testPath, map[string]any{"expected_version": 1}, f.root, "supplier-connection-probe-1")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatalf("connection probe was not idempotently replayed: %s", replay.Body.String())
	}
	expect(t, request(f.h, "POST", testPath, map[string]any{"expected_version": 1}, f.reader, "supplier-connection-reader"), 403)
	expect(t, request(f.h, "POST", testPath, map[string]any{"expected_version": 1}, f.root, ""), 400)

	var auditCount int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action IN ('supplier.connection_test_started','supplier.connection_test_completed') AND entity_id=$1`, fmt.Sprint(f.supplierID)).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("connection probe audit count=%d err=%v", auditCount, err)
	}
	var operationCount int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM mutation_operations WHERE scope='admin:supplier.connection_test' AND idempotency_key='supplier-connection-probe-1'`).Scan(&operationCount); err != nil || operationCount != 1 {
		t.Fatalf("connection probe operation receipt count=%d err=%v", operationCount, err)
	}
}

func TestResourceAPISettingsVersionIdempotencyAndBearerGate(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "resource-api-root", "super_admin")
	_, customer := user(t, db, h, "resource-api-customer", "customer")
	keyResponse := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "before-disable"}, customer, "")
	expect(t, keyResponse, 201)
	key := data(t, keyResponse)["secret"].(string)

	disabled := request(h, "PUT", "/api/v1/admin/supplier-api-settings", map[string]any{
		"enabled": false, "require_certification": true, "require_verified_phone": true, "expected_version": 1,
	}, root, "resource-api-settings-disable")
	expect(t, disabled, 200)
	if data(t, disabled)["version"] != float64(2) {
		t.Fatalf("resource API settings version did not advance: %s", disabled.Body.String())
	}
	replay := request(h, "PUT", "/api/v1/admin/supplier-api-settings", map[string]any{
		"enabled": false, "require_certification": true, "require_verified_phone": true, "expected_version": 1,
	}, root, "resource-api-settings-disable")
	expect(t, replay, 200)
	if data(t, replay)["version"] != float64(2) {
		t.Fatalf("resource API settings replay changed result: %s", replay.Body.String())
	}
	expect(t, request(h, "PUT", "/api/v1/admin/supplier-api-settings", map[string]any{
		"enabled": true, "require_certification": false, "require_verified_phone": false, "expected_version": 1,
	}, root, "resource-api-settings-disable"), 409)
	expect(t, request(h, "PUT", "/api/v1/admin/supplier-api-settings", map[string]any{
		"enabled": true, "require_certification": false, "require_verified_phone": false, "expected_version": 1,
	}, root, "resource-api-settings-stale"), 409)
	var auditCount, operationCount int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='resource_api.settings_updated'`, rootID).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("resource API settings audit count=%d err=%v", auditCount, err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='admin:resource_api.settings' AND idempotency_key='resource-api-settings-disable'`, rootID).Scan(&operationCount); err != nil || operationCount != 1 {
		t.Fatalf("resource API settings receipt count=%d err=%v", operationCount, err)
	}
	blocked := requestBearer(h, "GET", "/api/v1/cart", nil, key)
	expect(t, blocked, 403)
	if !strings.Contains(blocked.Body.String(), "api_access_disabled") {
		t.Fatalf("disabled resource API did not block existing bearer keys: %s", blocked.Body.String())
	}
	expect(t, request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "after-disable"}, customer, ""), 409)
}

func TestResourceAPIKeyEligibilityGates(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "resource-api-eligibility-root", "super_admin")
	customerID, customer := user(t, db, h, "resource-api-eligibility-customer", "customer")
	settings := map[string]any{"enabled": true, "require_certification": false, "require_verified_phone": true, "expected_version": 1}
	expect(t, request(h, "PUT", "/api/v1/admin/supplier-api-settings", settings, root, "resource-api-require-phone"), 200)
	denied := request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "phone-required"}, customer, "")
	expect(t, denied, 409)
	if !strings.Contains(denied.Body.String(), "api_phone_required") {
		t.Fatalf("missing phone gate error: %s", denied.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+12025550123' WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "phone-not-verified"}, customer, ""), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "phone-verified"}, customer, ""), 201)

	settings = map[string]any{"enabled": true, "require_certification": true, "require_verified_phone": true, "expected_version": 2}
	expect(t, request(h, "PUT", "/api/v1/admin/supplier-api-settings", settings, root, "resource-api-require-cert"), 200)
	denied = request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "certification-required"}, customer, "")
	expect(t, denied, 409)
	if !strings.Contains(denied.Body.String(), "api_certification_required") {
		t.Fatalf("missing certification gate error: %s", denied.Body.String())
	}
	submission := request(h, "POST", "/api/v1/account/certification", map[string]any{
		"type": "individual", "payload": map[string]string{"name": "Resource API Fixture", "id_number": "FIXTURE-ID-123"},
	}, customer, "")
	expect(t, submission, 202)
	profileID := int64(data(t, submission)["profile_id"].(float64))
	reviewPath := fmt.Sprintf("/api/v1/admin/certifications/%d/review", profileID)
	expect(t, request(h, "POST", reviewPath, map[string]any{"decision": "approved", "expected_version": 1}, root, "resource-api-certification-review"), 200)
	expect(t, request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "certification-approved"}, customer, ""), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='' WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	denied = request(h, "POST", "/api/v1/api-keys", map[string]string{"name": "phone-unbound"}, customer, "")
	expect(t, denied, 409)
}

func TestResourceAPISettingsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "resource-api-settings-super", func(db *store.Store, h http.Handler) {
		_, setupAdmin := user(t, db, h, "resource-api-settings-setup", "super_admin")
		remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/zjmf_api_login":
				fmt.Fprint(w, `{"status":200,"jwt":"fixture-supplier-token"}`)
			case "/cart/all":
				if r.Header.Get("Authorization") != "Bearer fixture-supplier-token" {
					t.Error("supplier catalogue request did not use the fixture token")
				}
				fmt.Fprint(w, `{"status":200,"data":{"products":[{"products":[{"id":7,"name":"Fixture product","stock_control":1,"qty":5}]}]}}`)
			default:
				t.Errorf("unexpected supplier probe path %s", r.URL.Path)
				http.NotFound(w, r)
			}
		}))
		t.Cleanup(remote.Close)
		created := request(h, "POST", "/api/v1/admin/suppliers", store.SupplierInput{Name: "浏览器验收供应商", Provider: supplier.ProviderZJMF, BaseURL: remote.URL, Username: "fixture-account", Password: "fixture-password", Active: enabled()}, setupAdmin, "")
		expect(t, created, 201)
	})
}
