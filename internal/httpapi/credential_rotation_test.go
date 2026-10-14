package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func TestPasswordChangeRevokesCustomerAPIKeys(t *testing.T) {
	db, h := testServer(t)
	customerID, cookie := user(t, db, h, "password-rotation-customer", "customer")
	_, otherCookie := user(t, db, h, "password-rotation-other", "customer")

	created := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "rotation subject"}, cookie, "")
	expect(t, created, 201)
	secret := data(t, created)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 200)

	bystander := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "rotation bystander"}, otherCookie, "")
	expect(t, bystander, 201)
	bystanderSecret := data(t, bystander)["secret"].(string)

	// The endpoint authenticates with the browser session; a bare API key must
	// not be able to rotate the password (and must not be revoked by the
	// rejected attempt).
	expect(t, requestBearer(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "bearer-should-not-work"}, secret), http.StatusUnauthorized)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 200)

	expect(t, request(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-integration-password"}, cookie, ""), 200)

	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), http.StatusUnauthorized)
	var revoked bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM api_keys WHERE secret_hash=$1", tokenHash(secret)).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("password change retained API key: revoked=%v err=%v", revoked, err)
	}
	// Unrelated accounts keep their credentials.
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, bystanderSecret), 200)
	// The session performing the change survives, other sessions do not.
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 200)

	var payload []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT payload FROM audit_events WHERE actor_id=$1 AND action='customer.password_changed' ORDER BY id DESC LIMIT 1", customerID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var flags map[string]any
	if err := json.Unmarshal(payload, &flags); err != nil {
		t.Fatal(err)
	}
	if flags["api_keys_revoked"] != true || flags["sessions_revoked"] != true {
		t.Fatalf("audit payload must record both revocations: %s", payload)
	}
}

func TestMFARotationRevokesAPIKeysAtConfirmTime(t *testing.T) {
	db, h := testServer(t)
	userID, cookie := user(t, db, h, "mfa-key-rotation", "customer")

	created := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "pre-mfa key"}, cookie, "")
	expect(t, created, 201)
	preMFASecret := data(t, created)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, preMFASecret), 200)

	enroll := request(h, http.MethodPost, "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
	expect(t, enroll, 200)
	secret := data(t, enroll)["secret"].(string)
	// Enrollment stages a secret but does not enforce anything yet, so it must
	// not invalidate credentials before the factor is confirmed.
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, preMFASecret), 200)

	confirm := request(h, http.MethodPost, "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, "")
	expect(t, confirm, 200)
	// The factor is now effective; a key that predates it would silently bypass
	// MFA, so confirm revokes it.
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, preMFASecret), http.StatusUnauthorized)
	var revoked bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM api_keys WHERE secret_hash=$1", tokenHash(preMFASecret)).Scan(&revoked); err != nil || !revoked {
		t.Fatalf("MFA confirm retained pre-MFA API key: revoked=%v err=%v", revoked, err)
	}
	// The session survives and the owner can issue a fresh key afterwards.
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 200)
	reissued := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "post-mfa key"}, cookie, "")
	expect(t, reissued, 201)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, data(t, reissued)["secret"].(string)), 200)

	var payload []byte
	if err := db.Pool.QueryRow(t.Context(), "SELECT payload FROM audit_events WHERE actor_id=$1 AND action='auth.mfa_enabled' ORDER BY id DESC LIMIT 1", userID).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	var flags map[string]any
	if err := json.Unmarshal(payload, &flags); err != nil {
		t.Fatal(err)
	}
	if flags["api_keys_revoked"] != true {
		t.Fatalf("MFA enable audit must record api_keys_revoked: %s", payload)
	}
}

func TestMFADisableKeepsExistingAPIKeys(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "mfa-disable-keys", "customer")

	enroll := request(h, http.MethodPost, "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
	expect(t, enroll, 200)
	totpSecret := data(t, enroll)["secret"].(string)
	expect(t, request(h, http.MethodPost, "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(totpSecret, time.Now())}, cookie, ""), 200)

	created := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "post-mfa key"}, cookie, "")
	expect(t, created, 201)
	apiSecret := data(t, created)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, apiSecret), 200)

	// Disabling the factor requires the password plus a TOTP/recovery code, so
	// reaching it already proves full authentication; keys issued under MFA
	// keep working and the owner can revoke them explicitly if desired. This
	// test pins that deliberate asymmetry with mfaConfirm.
	expect(t, request(h, http.MethodPost, "/api/v1/account/mfa/disable", map[string]string{"password": "integration-password", "code": testTOTP(totpSecret, time.Now())}, cookie, ""), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, apiSecret), 200)
	var revoked bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM api_keys WHERE secret_hash=$1", tokenHash(apiSecret)).Scan(&revoked); err != nil || revoked {
		t.Fatalf("MFA disable unexpectedly revoked API keys: revoked=%v err=%v", revoked, err)
	}
}
