package httpapi

import (
	"bytes"
	"net/http"
	"regexp"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func verificationEmailCode(t *testing.T, db *store.Store, challenge string) string {
	t.Helper()
	var sealed []byte
	var body string
	if err := db.Pool.QueryRow(t.Context(), "SELECT body,body_encrypted FROM email_outbox WHERE verification_id=$1", store.VerificationID(challenge)).Scan(&body, &sealed); err != nil {
		t.Fatal(err)
	}
	if body != "" || len(sealed) == 0 {
		t.Fatal("verification not encrypted")
	}
	raw, err := db.VerificationPayload(store.VerificationID(challenge), sealed, false)
	if err != nil {
		t.Fatal(err)
	}
	code := regexp.MustCompile(`[0-9]{6}`).FindString(string(raw))
	if len(code) != 6 || bytes.Contains(sealed, []byte(code)) {
		t.Fatal("invalid or exposed code")
	}
	return code
}

func issueReset(t *testing.T, db *store.Store, h interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}, identifier string) (string, string) {
	t.Helper()
	w := request(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": identifier}, "", "")
	expect(t, w, 202)
	id := data(t, w)["challenge_id"].(string)
	return id, verificationEmailCode(t, db, id)
}

func TestVerificationWrongAttemptsExpiryResendAndConcurrency(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "otp-customer", "customer")
	id, code := issueReset(t, db, h, "otp-customer")
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/request", map[string]string{"identifier": "otp-customer"}, "", ""), 409)
	wrong := "000000"
	if code == wrong {
		wrong = "999999"
	}
	for range 5 {
		expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": id, "code": wrong, "password": "new-test-password"}, "", ""), 409)
	}
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": id, "code": code, "password": "new-test-password"}, "", ""), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE verification_challenges SET created_at=now()-interval '61 seconds'"); err != nil {
		t.Fatal(err)
	}
	next, nextCode := issueReset(t, db, h, "otp-customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE verification_challenges SET expires_at=now()-interval '1 second' WHERE id=$1", store.VerificationID(next)); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": next, "code": nextCode, "password": "new-test-password"}, "", ""), 409)
	if err := db.RecoverStaleEmailJobs(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ClaimEmailJob(t.Context()); err == nil {
		t.Fatal("claimed expired verification")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE verification_challenges SET created_at=now()-interval '61 seconds'"); err != nil {
		t.Fatal(err)
	}
	final, finalCode := issueReset(t, db, h, "otp-customer")
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": final, "code": finalCode, "password": "new-test-password"}, "", "").Code
		}()
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for status := range results {
		counts[status]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatal("concurrent verification", counts)
	}
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
}

func TestRegistrationEmailCodeRecipientAndPurpose(t *testing.T) {
	db, h := testServer(t)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET email_verification_required=true"); err != nil {
		t.Fatal(err)
	}
	registration := map[string]any{"username": "verified-new", "email": "verified@example.test", "display_name": "验证码用户", "password": "test-password"}
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 409)
	issued := request(h, "POST", "/api/v1/auth/verification/request", map[string]string{"purpose": "register", "channel": "email", "recipient": "verified@example.test"}, "", "")
	expect(t, issued, 202)
	id := data(t, issued)["challenge_id"].(string)
	code := verificationEmailCode(t, db, id)
	registration["verification_id"], registration["verification_code"] = id, code
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 201)
	var verified bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT email_verified FROM users WHERE username='verified-new'").Scan(&verified); err != nil || !verified {
		t.Fatal("registration unverified", err)
	}
	registration["username"] = "replayed-new"
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 409)
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": id, "code": code, "password": "wrong-purpose-password"}, "", ""), 409)
}
