package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestMaintenanceCustomerGateAndAdminRecovery(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "maintenance-super", "super_admin")
	staffID, staff := user(t, db, h, "maintenance-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(array_remove(permissions,'settings.view'),'settings.manage') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	_, customer := user(t, db, h, "maintenance-customer", "customer")
	key := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "maintenance-customer-key"}, customer, "")
	expect(t, key, 201)
	customerKey := data(t, key)["secret"].(string)
	key = request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "maintenance-admin-key"}, root, "")
	expect(t, key, 201)
	adminKey := data(t, key)["secret"].(string)
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	profile := settings.SiteProfile
	profile.MaintenanceEnabled = true
	profile.MaintenanceMessage = "系统维护\r\n预计稍后恢复"
	profile.MaintenanceURL = "https://status.example.test/"
	w := request(h, "PUT", "/api/v1/admin/site-settings", siteInput(profile, 1), root, "")
	expect(t, w, 200)
	if data(t, w)["maintenance_message"] != "系统维护\n预计稍后恢复" {
		t.Fatal("maintenance newline normalization")
	}
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/catalog"}, {"GET", "/api/v1/catalog/1"}, {"GET", "/api/v1/categories"},
		{"GET", "/api/v1/news"}, {"GET", "/api/v1/news/1"}, {"GET", "/api/v1/knowledgebase"}, {"GET", "/api/v1/knowledgebase/1"},
		{"GET", "/api/v1/cart"}, {"POST", "/api/v1/cart/items"}, {"PATCH", "/api/v1/cart/items/1"}, {"DELETE", "/api/v1/cart/items/1"},
		{"POST", "/api/v1/cart/promo"}, {"DELETE", "/api/v1/cart/promo"}, {"POST", "/api/v1/orders"}, {"GET", "/api/v1/orders"}, {"POST", "/api/v1/orders/1/cancel"},
		{"GET", "/api/v1/account"}, {"PUT", "/api/v1/account/password"}, {"GET", "/api/v1/invoices"}, {"POST", "/api/v1/invoices/1/pay"}, {"POST", "/api/v1/invoices/1/payments"},
		{"GET", "/api/v1/services"}, {"GET", "/api/v1/services/1"}, {"POST", "/api/v1/services/1/actions"}, {"POST", "/api/v1/services/1/renew"}, {"PATCH", "/api/v1/services/1/auto-renew"},
		{"POST", "/api/v1/services/1/cancellation"}, {"DELETE", "/api/v1/services/1/cancellation"},
		{"GET", "/api/v1/tickets"}, {"GET", "/api/v1/tickets/1"}, {"POST", "/api/v1/tickets"}, {"PATCH", "/api/v1/tickets/1"}, {"POST", "/api/v1/tickets/1/replies"},
		{"GET", "/api/v1/ticket-departments"}, {"POST", "/api/v1/tickets/1/attachments"}, {"GET", "/api/v1/tickets/1/attachments/1"}, {"DELETE", "/api/v1/tickets/1/attachments/1"},
		{"GET", "/api/v1/contacts"}, {"POST", "/api/v1/contacts"}, {"PATCH", "/api/v1/contacts/1"}, {"DELETE", "/api/v1/contacts/1"},
		{"GET", "/api/v1/api-keys"}, {"POST", "/api/v1/api-keys"}, {"DELETE", "/api/v1/api-keys/1"},
		{"GET", "/api/v1/account/mfa"}, {"POST", "/api/v1/account/mfa/enroll"}, {"POST", "/api/v1/account/mfa/confirm"}, {"POST", "/api/v1/account/mfa/disable"}, {"POST", "/api/v1/auth/register"},
	} {
		for _, cookie := range []string{"", customer, "zjmfgo_session=invalid"} {
			w = request(h, tc.method, tc.path, map[string]any{}, cookie, "")
			expect(t, w, 503)
			if errorData(t, w)["code"] != "site_maintenance" || w.Header().Get("Retry-After") != "60" || errorData(t, w)["maintenance_url"] != profile.MaintenanceURL {
				t.Fatal("maintenance error contract", tc)
			}
		}
	}
	for _, credential := range []string{customerKey, adminKey} {
		expect(t, requestBearer(h, "GET", "/api/v1/catalog", nil, credential), 503)
	}
	r := httptest.NewRequest("GET", "/api/v1/catalog", nil)
	r.Header.Set("Authorization", "Bearer "+customerKey)
	r.Header.Set("Cookie", root)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 503)
	for _, cookie := range []string{root, staff} {
		expect(t, request(h, "GET", "/api/v1/catalog", nil, cookie, ""), 200)
		expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, cookie, ""), 200)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()-interval '1 second' WHERE user_id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/catalog", nil, staff, ""), 503)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=now()+interval '1 hour' WHERE user_id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, requestBearer(h, "GET", "/api/v1/admin/dashboard", nil, adminKey), 200)
	expect(t, request(h, "GET", "/api/v1/admin/site-settings", nil, staff, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/site-settings", nil, customer, ""), 403)
	for _, path := range []string{"/healthz", "/readyz", "/api/v1/site"} {
		expect(t, request(h, "GET", path, nil, "", ""), 200)
	}
	expect(t, request(h, "GET", "/api/v1/me", nil, customer, ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"identifier": "maintenance-customer@example.test", "password": "integration-password"}, "", ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/request", map[string]any{"identifier": "maintenance-super"}, "", ""), 202)
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/catalog", nil, staff, ""), 503)
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, staff, ""), 401)
	profile.MaintenanceEnabled = false
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(profile, 2), root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/catalog", nil, "", ""), 200)
	expect(t, request(h, "GET", "/api/v1/cart", nil, customer, ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/logout", nil, customer, ""), 200)
}

func TestMaintenanceSettingsValidation(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "maintenance-validation", "super_admin")
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	missing := siteInput(settings.SiteProfile, 1)
	delete(missing, "maintenance_enabled")
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", missing, root, ""), 400)
	for _, target := range []string{"/news", "//status.example.test", "http://status.example.test", "https://user:password@status.example.test", "https://localhost:3088/news", "https://LOCALHOST.:3088/news", "https://status.example.test/with space", "https://status.example.test/\\path"} {
		p := settings.SiteProfile
		p.MaintenanceURL = target
		expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(p, 1), root, ""), 409)
	}
	for _, message := range []string{"bad\x00text", strings.Repeat("长", 1001)} {
		p := settings.SiteProfile
		p.MaintenanceMessage = message
		expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(p, 1), root, ""), 409)
	}
	settings.MaintenanceEnabled = true
	settings.MaintenanceMessage = ""
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(settings.SiteProfile, 1), root, ""), 200)
	w := request(h, "GET", "/api/v1/catalog", nil, "", "")
	expect(t, w, 503)
	if errorData(t, w)["message"] != "站点维护中，请稍后再试。" {
		t.Fatal("missing fallback message")
	}
}

func TestMaintenancePaymentWebhookAndMFALogin(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "maintenance-mfa-admin", "super_admin")
	_, customer := user(t, db, h, "maintenance-payment-owner", "customer")
	pid := product(t, db, 2)
	version, item := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "maintenance-order")
	expect(t, order, 201)
	invoice := int64(data(t, order)["invoice_id"].(float64))
	intent := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]string{"provider": "mock"}, customer, "maintenance-intent")
	expect(t, intent, 201)
	enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, root, "")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	expect(t, request(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, root, ""), 200)
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.MaintenanceEnabled = true
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(settings.SiteProfile, 1), root, ""), 200)
	body, _ := json.Marshal(map[string]any{"event_id": "maintenance-event", "payment_id": data(t, intent)["provider_payment_id"], "invoice_id": invoice, "amount_cents": 1333, "status": "succeeded"})
	expect(t, signedWebhook(h, body, "invalid"), 401)
	expect(t, signedWebhook(h, body, paymentSignature(body)), 201)
	expect(t, signedWebhook(h, body, paymentSignature(body)), 200)
	var status string
	if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoice).Scan(&status); err != nil || status != "paid" {
		t.Fatal("callback did not settle during maintenance", err)
	}
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "maintenance-mfa-admin", "password": "integration-password"}, "", "")
	expect(t, login, 401)
	challenge := errorData(t, login)["challenge"].(string)
	verified := request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", "")
	expect(t, verified, 200)
	cookie := verified.Result().Cookies()[0].String()
	expect(t, request(h, "GET", "/api/v1/admin/site-settings", nil, cookie, ""), 200)
	expect(t, request(h, "GET", "/api/v1/catalog", nil, cookie, ""), 200)
}
