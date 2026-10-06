package httpapi

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testTOTP(secret string, now time.Time) string {
	decoded, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	counter := uint64(now.Unix() / int64(mfaStep/time.Second))
	mac := hmac.New(sha1.New, decoded)
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], counter)
	_, _ = mac.Write(buf[:])
	sum := mac.Sum(nil)
	idx := sum[len(sum)-1] & 0x0f
	value := (uint32(sum[idx])&0x7f)<<24 | uint32(sum[idx+1])<<16 | uint32(sum[idx+2])<<8 | uint32(sum[idx+3])
	return fmt.Sprintf("%06d", value%1000000)
}

func errorData(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Error
}

func TestMFAEnrollmentLoginAndDisable(t *testing.T) {
	db, h := testServer(t)
	userID, cookie := user(t, db, h, "mfa-customer", "customer")
	status := request(h, http.MethodGet, "/api/v1/account/mfa", nil, cookie, "")
	expect(t, status, 200)
	if data(t, status)["enabled"] != false {
		t.Fatal("MFA should start disabled")
	}
	enroll := request(h, http.MethodPost, "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
	expect(t, enroll, 200)
	enrollment := data(t, enroll)
	secret := enrollment["secret"].(string)
	recoveryCodes, ok := enrollment["recovery_codes"].([]any)
	if !ok || len(recoveryCodes) != 8 {
		t.Fatalf("invalid recovery codes: %#v", enrollment["recovery_codes"])
	}
	if len(secret) < 20 || !strings.Contains(enrollment["otpauth_uri"].(string), "otpauth://totp/") {
		t.Fatalf("invalid enrollment payload: %#v", enrollment)
	}
	var stored []byte
	var enabled bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT mfa_secret_enc,mfa_enabled FROM users WHERE id=$1", userID).Scan(&stored, &enabled); err != nil {
		t.Fatal(err)
	}
	if enabled || string(stored) == secret || len(stored) <= len(secret) {
		t.Fatal("MFA secret was not encrypted at rest")
	}
	confirm := request(h, http.MethodPost, "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, "")
	expect(t, confirm, 200)
	status = request(h, http.MethodGet, "/api/v1/account/mfa", nil, cookie, "")
	expect(t, status, 200)
	if data(t, status)["enabled"] != true {
		t.Fatal("MFA was not enabled")
	}
	login := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "mfa-customer", "password": "integration-password"}, "", "")
	expect(t, login, http.StatusUnauthorized)
	loginError := errorData(t, login)
	if loginError["code"] != "mfa_required" || loginError["challenge"] == nil {
		t.Fatalf("missing MFA challenge: %#v", loginError)
	}
	challenge := loginError["challenge"].(string)
	failed := request(h, http.MethodPost, "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": "000000"}, "", "")
	expect(t, failed, http.StatusUnauthorized)
	completed := request(h, http.MethodPost, "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", "")
	expect(t, completed, http.StatusOK)
	if len(completed.Result().Cookies()) != 1 {
		t.Fatal("MFA login did not issue a session")
	}
	recoveryLogin := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "mfa-customer", "password": "integration-password"}, "", "")
	expect(t, recoveryLogin, http.StatusUnauthorized)
	recoveryChallenge := errorData(t, recoveryLogin)["challenge"].(string)
	recoveryValue := recoveryCodes[0].(string)
	recoveryCompleted := request(h, http.MethodPost, "/api/v1/auth/login/mfa", map[string]string{"challenge": recoveryChallenge, "code": recoveryValue}, "", "")
	expect(t, recoveryCompleted, http.StatusOK)
	var usedAt any
	if err := db.Pool.QueryRow(t.Context(), "SELECT used_at FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$2", userID, tokenHash(recoveryValue)).Scan(&usedAt); err != nil || usedAt == nil {
		t.Fatalf("recovery code was not consumed: err=%v used_at=%v", err, usedAt)
	}
	reusedLogin := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "mfa-customer", "password": "integration-password"}, "", "")
	expect(t, reusedLogin, http.StatusUnauthorized)
	reusedChallenge := errorData(t, reusedLogin)["challenge"].(string)
	reused := request(h, http.MethodPost, "/api/v1/auth/login/mfa", map[string]string{"challenge": reusedChallenge, "code": recoveryValue}, "", "")
	expect(t, reused, http.StatusUnauthorized)
	disable := request(h, http.MethodPost, "/api/v1/account/mfa/disable", map[string]string{"password": "integration-password", "code": testTOTP(secret, time.Now())}, cookie, "")
	expect(t, disable, 200)
	ordinary := request(h, http.MethodPost, "/api/v1/auth/login", map[string]string{"username": "mfa-customer", "password": "integration-password"}, "", "")
	expect(t, ordinary, 200)
}
