package httpapi

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func authSettingsInput(t *testing.T, h http.Handler, cookie string) map[string]any {
	t.Helper()
	out := data(t, request(h, "GET", "/api/v1/admin/auth-settings", nil, cookie, ""))
	delete(out, "updated_at")
	delete(out, "email_channel_ready")
	delete(out, "sms_channel_ready")
	out["expected_version"] = out["version"]
	delete(out, "version")
	return out
}

func TestAuthSettingsContractAndEnforcement(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "auth-settings-root", "super_admin")
	staffID, staff := user(t, db, h, "auth-settings-reader", "admin")
	_, customer := user(t, db, h, "auth-settings-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/auth-settings"
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "GET", path, nil, customer, ""), 403)
	expect(t, request(h, "GET", path, nil, staff, ""), 200)
	in := authSettingsInput(t, h, root)
	in["login_enabled"] = false
	in["verification_email_enabled"] = false
	in["email_verification_required"] = false
	in["login_max_attempts"] = 2
	in["login_lockout_minutes"] = 5
	expect(t, request(h, "PUT", path, in, staff, "auth-settings-denied"), 403)
	expect(t, request(h, "PUT", path, in, root, ""), 400)
	saved := request(h, "PUT", path, in, root, "auth-settings-save-1")
	expect(t, saved, 200)
	if data(t, saved)["version"] != float64(2) {
		t.Fatal(saved.Body.String())
	}
	replay := request(h, "PUT", path, in, root, "auth-settings-save-1")
	expect(t, replay, 200)
	if data(t, replay)["replayed"] != true {
		t.Fatal(replay.Body.String())
	}
	expect(t, request(h, "PUT", path, in, root, "auth-settings-stale"), 409)
	changed := map[string]any{}
	for k, v := range in {
		changed[k] = v
	}
	changed["login_enabled"] = true
	expect(t, request(h, "PUT", path, changed, root, "auth-settings-save-1"), 409)
	site, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	site.EmailVerificationRequired = true
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(site.SiteProfile, site.Version), root, ""), 409)
	login := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auth-settings-customer", "password": "integration-password"}, "", "")
	expect(t, login, 403)
	if !strings.Contains(login.Body.String(), "login_disabled") {
		t.Fatal(login.Body.String())
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auth-settings-root", "password": "integration-password"}, "", ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET registration_enabled=false,version=version+1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/register", map[string]any{"username": "auth-settings-new", "email": "new@example.test", "password": "integration-password", "display_name": "New Customer"}, "", ""), 403)
	channels := request(h, "GET", "/api/v1/auth/verification/channels", nil, "", "")
	expect(t, channels, 200)
	if strings.Contains(channels.Body.String(), `"email"`) {
		t.Fatal(channels.Body.String())
	}
	expect(t, request(h, "POST", "/api/v1/auth/verification/request", map[string]any{"purpose": "register", "channel": "email", "recipient": "new@example.test"}, "", ""), 409)
	var auditCount, receiptCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM audit_events WHERE action='auth.settings_updated'),(SELECT count(*) FROM admin_setting_operations WHERE operation='auth.settings')").Scan(&auditCount, &receiptCount); err != nil || auditCount != 1 || receiptCount != 1 {
		t.Fatal(auditCount, receiptCount, err)
	}
}

func TestAuthSettingsSiteVersionAndLoginMethod(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "auth-method-root", "super_admin")
	_, _ = user(t, db, h, "auth-method-customer", "customer")
	in := authSettingsInput(t, h, root)
	in["login_username_enabled"] = false
	expect(t, request(h, "PUT", "/api/v1/admin/auth-settings", in, root, "auth-method-save-1"), 200)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auth-method-customer", "password": "integration-password"}, "", ""), 403)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auth-method-customer@example.test", "password": "integration-password"}, "", ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auth-method-root", "password": "integration-password"}, "", ""), 200)
	settings, err := db.SiteSettings(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	settings.RegistrationEnabled = false
	expect(t, request(h, "PUT", "/api/v1/admin/site-settings", siteInput(settings.SiteProfile, settings.Version), root, ""), 200)
	if version := data(t, request(h, "GET", "/api/v1/admin/auth-settings", nil, root, ""))["version"]; version != float64(3) {
		t.Fatal("auth settings version did not advance", version)
	}
	in["expected_version"] = 2
	expect(t, request(h, "PUT", "/api/v1/admin/auth-settings", in, root, "auth-method-stale-2"), 409)
}

func TestMFASettingsPolicyAndSecrets(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "mfa-settings-root", "super_admin")
	staffID, staff := user(t, db, h, "mfa-settings-reader", "admin")
	_, customer := user(t, db, h, "mfa-settings-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['settings.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/admin/security/mfa"
	expect(t, request(h, "GET", path, nil, customer, ""), 403)
	view := request(h, "GET", path, nil, staff, "")
	expect(t, view, 200)
	if strings.Contains(view.Body.String(), "secret") || strings.Contains(view.Body.String(), "recovery") {
		t.Fatal(view.Body.String())
	}
	in := map[string]any{"customer_enabled": false, "admin_enabled": false, "expected_version": 1}
	expect(t, request(h, "PUT", path, in, staff, "mfa-policy-denied"), 403)
	expect(t, request(h, "PUT", path, in, root, ""), 400)
	expect(t, request(h, "PUT", path, in, root, "mfa-policy-save-1"), 200)
	expect(t, request(h, "PUT", path, in, root, "mfa-policy-save-1"), 200)
	expect(t, request(h, "PUT", path, in, root, "mfa-policy-stale"), 409)
	expect(t, request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, customer, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE mfa_settings SET customer_enabled=true"); err != nil {
		t.Fatal(err)
	}
	enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, customer, "")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	expect(t, request(h, "POST", "/api/v1/account/mfa/confirm", map[string]any{"code": testTOTP(secret, time.Now())}, customer, ""), 200)
	challengeLogin := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "mfa-settings-customer", "password": "integration-password"}, "", "")
	expect(t, challengeLogin, 401)
	challenge := errorData(t, challengeLogin)["challenge"].(string)
	auth := authSettingsInput(t, h, root)
	auth["login_enabled"] = false
	expect(t, request(h, "PUT", "/api/v1/admin/auth-settings", auth, root, "mfa-login-close-1"), 200)
	expect(t, request(h, "POST", "/api/v1/auth/login/mfa", map[string]any{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE mfa_settings SET customer_enabled=false"); err != nil {
		t.Fatal(err)
	}
	view = request(h, "GET", path, nil, root, "")
	if strings.Contains(view.Body.String(), secret) || strings.Contains(view.Body.String(), "recovery") {
		t.Fatal(view.Body.String())
	}
}

func TestMFAEnrollCannotReplaceConfirmedSecret(t *testing.T) {
	db, h := testServer(t)
	userID, cookie := user(t, db, h, "mfa-enroll-race", "customer")
	enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	lock, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lock.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", userID); err != nil {
		t.Fatal(err)
	}
	confirmDone := make(chan int, 1)
	go func() {
		confirmDone <- request(h, "POST", "/api/v1/account/mfa/confirm", map[string]any{"code": testTOTP(secret, time.Now())}, cookie, "").Code
	}()
	waitForMFALock(t, db, "SELECT mfa_enabled,mfa_secret_enc FROM users")
	enrollDone := make(chan int, 1)
	go func() {
		enrollDone <- request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "").Code
	}()
	waitForMFALock(t, db, "SELECT mfa_enabled FROM users")
	if err = lock.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	if status := <-confirmDone; status != 200 {
		t.Fatal("confirm status", status)
	}
	if status := <-enrollDone; status != 409 {
		t.Fatal("enroll replaced confirmed secret", status)
	}
	var enabled bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT mfa_enabled FROM users WHERE id=$1", userID).Scan(&enabled); err != nil || !enabled {
		t.Fatal(enabled, err)
	}
}

func waitForMFALock(t *testing.T, db *store.Store, fragment string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for ctx.Err() == nil {
		var count int
		if err := db.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%'||$1||'%'", fragment).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count > 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("MFA request did not wait for user lock:", fragment)
}
