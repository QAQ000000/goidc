package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"testing"
	"time"
)

func TestCustomerEmailPhoneLoginWithoutUsernameRegistration(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "identifier-root", "super_admin")
	user(t, db, h, "13800001111", "admin") // Numeric administrator usernames remain valid.
	settings := authSettingsInput(t, h, root)
	if settings["login_username_enabled"] != false || settings["login_email_enabled"] != true || settings["login_phone_enabled"] != true {
		t.Fatal("unexpected default login methods", settings)
	}
	settings["login_username_enabled"] = true
	expect(t, request(h, "PUT", "/api/v1/admin/auth-settings", settings, root, "reject-username-login"), 400)

	registered := request(h, "POST", "/api/v1/auth/register", map[string]any{
		"email": "identifier-customer@example.test", "password": "integration-password", "display_name": "客户姓名",
	}, "", "")
	expect(t, registered, 201)
	id := int64(data(t, registered)["id"].(float64))
	var account string
	if err := db.Pool.QueryRow(t.Context(), "SELECT username FROM users WHERE id=$1", id).Scan(&account); err != nil || account == "" {
		t.Fatal("missing internal account ID", err)
	}
	login := func(identifier string) int {
		return request(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": identifier, "password": "integration-password"}, "", "").Code
	}
	if got := login(" Identifier-Customer@Example.Test "); got != 200 {
		t.Fatalf("email login=%d", got)
	}
	if got := login(account); got != 403 {
		t.Fatalf("customer internal username must not log in: %d", got)
	}
	// A stale or independently updated setting cannot re-enable customer names.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE auth_settings SET login_username_enabled=true"); err != nil {
		t.Fatal(err)
	}
	if got := login(account); got != 403 {
		t.Fatalf("customer username was re-enabled: %d", got)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+8613800000001' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if got := login("13800000001"); got != 401 {
		t.Fatalf("unverified phone logged in: %d", got)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	for _, phone := range []string{"13800000001", "+86 138-0000-0001", "008613800000001"} {
		if got := login(phone); got != 200 {
			t.Fatalf("phone %q login=%d", phone, got)
		}
	}
	// The compatibility JSON key still accepts an email, never a customer name.
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "identifier-customer@example.test", "password": "integration-password"}, "", ""), 200)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "identifier-customer@example.test", "username": "identifier-root", "password": "integration-password"}, "", ""), 400)
}

func TestCustomerPhoneMFARechecksLoginPolicy(t *testing.T) {
	db, h := testServer(t)
	id, customer := user(t, db, h, "phone-mfa-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+14155552671' WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, customer, "")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	expect(t, request(h, "POST", "/api/v1/account/mfa/confirm", map[string]any{"code": testTOTP(secret, time.Now())}, customer, ""), 200)
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "+1 (415) 555-2671", "password": "integration-password"}, "", "")
	expect(t, login, 401)
	challenge := errorData(t, login)["challenge"].(string)
	var method string
	if err := db.Pool.QueryRow(t.Context(), "SELECT login_method FROM mfa_challenges WHERE user_id=$1", id).Scan(&method); err != nil || method != "phone" {
		t.Fatalf("MFA login method=%s err=%v", method, err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE auth_settings SET login_phone_enabled=false"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE auth_settings SET login_phone_enabled=true"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", ""), 200)
	// Challenges issued before the migration cannot restore username login.
	challenge = fmt.Sprintf("%064x", 123)
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO mfa_challenges(token_hash,user_id,login_method,login_portal,expires_at) VALUES($1,$2,'username','customer',now()+interval '1 minute')", tokenHash(challenge), id); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", ""), 403)
}

func TestCustomerLoginBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "login-browser-admin", func(db *store.Store, h http.Handler) {
		id, _ := user(t, db, h, "login-browser-customer", "customer")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone='+8613800000001' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
	})
}
