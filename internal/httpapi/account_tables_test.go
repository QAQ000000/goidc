package httpapi

import (
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestPhysicalAccountTablesLoginCollisionsAndCredentialOwnership(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "physical-admin", "super_admin")
	cid, customer := user(t, db, h, "physical-customer", "customer")
	adminPassword := "physical-admin-password"
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE administrators SET username='shared-account',email='shared-account@example.test',password_hash=$2 WHERE id=$1", aid, string(hash)); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE users SET email='shared-account@example.test' WHERE id=$1", cid); err != nil {
		t.Fatal(err)
	}
	var wrongProfiles, adminCarts int
	if err = db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM users WHERE id=$1)+(SELECT count(*) FROM administrators WHERE id=$2),(SELECT count(*) FROM carts WHERE user_id=$1)", aid, cid).Scan(&wrongProfiles, &adminCarts); err != nil || wrongProfiles != 0 || adminCarts != 0 {
		t.Fatalf("physical split missing profiles=%d carts=%d err=%v", wrongProfiles, adminCarts, err)
	}
	login := func(portal, password string, expected int) string {
		t.Helper()
		res := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "shared-account@example.test", "password": password, "portal": portal}, "", portal)
		expect(t, res, expected)
		if expected != 200 {
			if len(res.Result().Cookies()) != 0 {
				t.Fatal("denied login created a session")
			}
			return ""
		}
		cookies := res.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != sessionCookieForRole(data(t, res)["role"].(string)) {
			t.Fatal("wrong portal cookie")
		}
		return cookies[0].Name + "=" + cookies[0].Value
	}
	admin = login("admin", adminPassword, 200)
	customer = login("customer", "integration-password", 200)
	login("admin", "integration-password", 401)
	login("customer", adminPassword, 401)
	login("", adminPassword, 401)
	login("", "integration-password", 401)
	for _, tc := range []struct {
		portal string
		id     int64
	}{{"admin", aid}, {"customer", cid}} {
		res := portalRequest(h, "GET", "/api/v1/me", nil, admin+"; "+customer, tc.portal)
		expect(t, res, 200)
		if int64(data(t, res)["id"].(float64)) != tc.id {
			t.Fatal("cross-table identity collision")
		}
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", aid); err != nil {
		t.Fatal(err)
	}
	login("admin", adminPassword, 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin+"; "+customer, "admin"), 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin+"; "+customer, "customer"), 200)
}

func TestPhysicalAccountPasswordResetDoesNotCrossPortal(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "physical-reset-admin", "super_admin")
	cid, customer := user(t, db, h, "physical-reset-customer", "customer")
	for _, profile := range []struct {
		table string
		id    int64
	}{{"users", cid}, {"administrators", aid}} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE "+profile.table+" SET email='physical-reset@example.test' WHERE id=$1", profile.id); err != nil {
			t.Fatal(err)
		}
	}
	ambiguous := portalRequest(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "physical-reset@example.test"}, "", "")
	expect(t, ambiguous, 202)
	challenge := data(t, ambiguous)["challenge_id"].(string)
	var anonymous, delivered bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT user_id IS NULL,EXISTS(SELECT 1 FROM email_outbox WHERE verification_id=$1) FROM verification_challenges WHERE id=$1", store.VerificationID(challenge)).Scan(&anonymous, &delivered); err != nil || !anonymous || delivered {
		t.Fatal("ambiguous reset selected an account", err)
	}
	issued := portalRequest(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "physical-reset@example.test", "portal": "admin"}, "", "")
	expect(t, issued, 202)
	challenge = data(t, issued)["challenge_id"].(string)
	var owner int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT user_id FROM verification_challenges WHERE id=$1", store.VerificationID(challenge)).Scan(&owner); err != nil || owner != aid {
		t.Fatal("admin reset bound customer", owner, err)
	}
	expect(t, portalRequest(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": challenge, "code": verificationEmailCode(t, db, challenge), "password": "physical-reset-password"}, "", ""), 200)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin, "admin"), 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, customer, "customer"), 200)
	expect(t, portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "physical-reset@example.test", "password": "physical-reset-password", "portal": "admin"}, "", "admin"), 200)
	expect(t, portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "physical-reset@example.test", "password": "integration-password", "portal": "customer"}, "", "customer"), 200)
}

func TestPhysicalAdministratorPasswordMFAAndKeyIsolation(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "physical-security-admin", "super_admin")
	_, customer := user(t, db, h, "physical-security-customer", "customer")
	adminKey := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "admin-key"}, admin, "admin")
	expect(t, adminKey, 201)
	adminSecret := data(t, adminKey)["secret"].(string)
	customerKey := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "customer-key"}, customer, "customer")
	expect(t, customerKey, 201)
	customerSecret := data(t, customerKey)["secret"].(string)
	enroll := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, admin, "admin")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	recovery := data(t, enroll)["recovery_codes"].([]any)[0].(string)
	var encrypted []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT mfa_secret_enc FROM administrators WHERE id=$1", aid).Scan(&encrypted); err != nil || string(encrypted) == secret || len(encrypted) == 0 {
		t.Fatal("administrator MFA not stored privately", err)
	}
	expect(t, requestBearer(h, "GET", "/api/v1/admin/dashboard", nil, adminSecret), 200)
	expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, admin, "admin"), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/dashboard", nil, adminSecret), 401)
	expect(t, requestBearer(h, "GET", "/api/v1/cart", nil, customerSecret), 200)
	password := portalRequest(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "physical-rotated-password"}, admin+"; "+customer, "admin")
	expect(t, password, 200)
	login := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"username": "physical-security-admin", "password": "physical-rotated-password", "portal": "admin"}, "", "admin")
	expect(t, login, 401)
	if errorData(t, login)["code"] != "mfa_required" {
		t.Fatal("administrator MFA bypassed")
	}
	complete := portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": errorData(t, login)["challenge"].(string), "code": recovery, "portal": "admin"}, "", "admin")
	expect(t, complete, 200)
	if cookies := complete.Result().Cookies(); len(cookies) != 1 || cookies[0].Name != adminSessionCookie {
		t.Fatal("administrator MFA issued customer cookie")
	}
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, customer, "customer"), 200)
	expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/disable", map[string]string{"password": "physical-rotated-password", "code": testTOTP(secret, time.Now())}, admin, "admin"), 200)
	var enabled bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT mfa_enabled FROM administrators WHERE id=$1", aid).Scan(&enabled); err != nil || enabled {
		t.Fatal("administrator MFA disable did not use administrator table", err)
	}
	if !strings.Contains(password.Body.String(), "changed") {
		t.Fatal("password response changed")
	}
}

func TestPhysicalRegistrationAndVerificationChallengesDoNotCrossPortal(t *testing.T) {
	db, h := testServer(t)
	const name = "physical-registration"
	const email = name + "@example.test"
	aid, admin := user(t, db, h, name, "super_admin")
	var administratorBefore string
	if err := db.Pool.QueryRow(t.Context(), "SELECT to_jsonb(a)::text FROM administrators a WHERE id=$1", aid).Scan(&administratorBefore); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET email_verification_required=true"); err != nil {
		t.Fatal(err)
	}
	issue := func(portal string) string {
		t.Helper()
		res := portalRequest(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": email, "portal": portal}, "", portal)
		expect(t, res, 202)
		return data(t, res)["challenge_id"].(string)
	}
	adminChallenge := issue("admin")
	registration := portalRequest(h, "POST", "/api/v1/auth/verification/request", map[string]string{"purpose": "register", "channel": "email", "recipient": email}, "", "customer")
	expect(t, registration, 202)
	registrationID := data(t, registration)["challenge_id"].(string)
	registrationCode := verificationEmailCode(t, db, registrationID)
	created := portalRequest(h, "POST", "/api/v1/auth/register", map[string]string{
		"username": name, "email": email, "display_name": "Independent customer", "password": "independent-customer-password", "verification_id": registrationID, "verification_code": registrationCode,
	}, "", "customer")
	expect(t, created, 201)
	cid := int64(data(t, created)["id"].(float64))
	if cid == aid {
		t.Fatal("registration reused administrator principal")
	}
	var verified bool
	var administratorAfter string
	if err := db.Pool.QueryRow(t.Context(), "SELECT u.email_verified,(SELECT to_jsonb(a)::text FROM administrators a WHERE id=$2) FROM users u WHERE u.id=$1", cid, aid).Scan(&verified, &administratorAfter); err != nil || !verified || administratorAfter != administratorBefore {
		t.Fatal("registration changed administrator or lost customer verification", err)
	}
	login := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": email, "password": "independent-customer-password", "portal": "customer"}, "", "customer")
	expect(t, login, 200)
	cookie := login.Result().Cookies()[0]
	customer := cookie.Name + "=" + cookie.Value
	customerChallenge := issue("customer")
	for _, tc := range []struct {
		challenge string
		owner     int64
	}{{adminChallenge, aid}, {customerChallenge, cid}} {
		var owner int64
		var unused bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT user_id,consumed_at IS NULL FROM verification_challenges WHERE id=$1", store.VerificationID(tc.challenge)).Scan(&owner, &unused); err != nil || owner != tc.owner || !unused {
			t.Fatal("same-recipient request invalidated another portal", owner, unused, err)
		}
	}
	// Limits remain in force independently for each account.
	for _, portal := range []string{"admin", "customer"} {
		expect(t, portalRequest(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": email, "portal": portal}, "", portal), 409)
	}
	expect(t, portalRequest(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": registrationID, "code": registrationCode, "password": "wrong-purpose-password"}, "", "customer"), 409)
	reset := func(challenge, password string) {
		t.Helper()
		expect(t, portalRequest(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": challenge, "code": verificationEmailCode(t, db, challenge), "password": password}, "", ""), 200)
	}
	reset(customerChallenge, "physical-customer-reset-password")
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, customer, "customer"), 401)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin, "admin"), 200)
	reset(adminChallenge, "physical-administrator-reset-password")
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin, "admin"), 401)
	for _, tc := range []struct{ portal, password string }{{"customer", "physical-customer-reset-password"}, {"admin", "physical-administrator-reset-password"}} {
		expect(t, portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": email, "password": tc.password, "portal": tc.portal}, "", tc.portal), 200)
	}
}
