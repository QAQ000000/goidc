package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

func TestVerificationSecurityResetRevokesAPIKey(t *testing.T) {
	db, h := testServer(t)
	_, cookie := user(t, db, h, "reset-key-owner", "customer")
	created := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "reset security test"}, cookie, "")
	expect(t, created, 201)
	secret := data(t, created)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 200)
	id, code := issueReset(t, db, h, "reset-key-owner")
	expect(t, request(h, "POST", "/api/v1/auth/password-reset/confirm", map[string]string{"challenge_id": id, "code": code, "password": "changed-test-password"}, "", ""), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	var revoked bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT revoked_at IS NOT NULL FROM api_keys WHERE secret_hash=$1", tokenHash(secret)).Scan(&revoked); err != nil || !revoked {
		t.Fatal("reset retained API key", err)
	}
}

func TestVerificationSecurityRegistrationRejectsRecipientAndPurpose(t *testing.T) {
	db, h := testServer(t)
	_, _ = user(t, db, h, "purpose-owner", "customer")
	issued := request(h, "POST", "/api/v1/auth/verification/request", map[string]string{"purpose": "register", "channel": "email", "recipient": "correct@example.test"}, "", "")
	expect(t, issued, 202)
	id := data(t, issued)["challenge_id"].(string)
	registration := map[string]any{"username": "recipient-mismatch", "email": "different@example.test", "display_name": "Security test", "password": "test-password", "verification_id": id, "verification_code": verificationEmailCode(t, db, id)}
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 409)
	resetID, resetCode := issueReset(t, db, h, "purpose-owner")
	registration["username"] = "purpose-mismatch"
	registration["verification_id"], registration["verification_code"] = resetID, resetCode
	var email string
	if err := db.Pool.QueryRow(t.Context(), "SELECT recipient FROM verification_challenges WHERE id=$1", store.VerificationID(resetID)).Scan(&email); err != nil {
		t.Fatal(err)
	}
	registration["email"] = email
	expect(t, request(h, "POST", "/api/v1/auth/register", registration, "", ""), 409)
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM users WHERE username IN ('recipient-mismatch','purpose-mismatch')").Scan(&count); err != nil || count != 0 {
		t.Fatal("invalid registration created user", err)
	}
}

func TestVerificationSecurityBindingAccountAndRevokedSession(t *testing.T) {
	db, h := testServer(t)
	ownerID, owner := user(t, db, h, "binding-owner", "customer")
	otherID, other := user(t, db, h, "binding-other", "customer")
	var email string
	if err := db.Pool.QueryRow(t.Context(), "SELECT email FROM users WHERE id=$1", ownerID).Scan(&email); err != nil {
		t.Fatal(err)
	}
	issued := request(h, "POST", "/api/v1/account/verification/request", map[string]string{"purpose": "bind_email", "channel": "email", "recipient": email}, owner, "")
	expect(t, issued, 202)
	id := data(t, issued)["challenge_id"].(string)
	input := map[string]string{"challenge_id": id, "code": verificationEmailCode(t, db, id)}
	expect(t, request(h, "POST", "/api/v1/account/verification/confirm", input, other, ""), 409)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", otherID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/account/verification/confirm", input, other, ""), 401)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", ownerID); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() { done <- request(h, "POST", "/api/v1/account/verification/confirm", input, owner, "") }()
	waitQueryLock(t, db, "SELECT email,status FROM users WHERE id=$1 FOR UPDATE")
	if _, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(owner))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case out := <-done:
		expect(t, out, 401)
	case <-time.After(7 * time.Second):
		t.Fatal("revoked binding stalled")
	}
	var verified bool
	var attempts int
	if err = db.Pool.QueryRow(t.Context(), "SELECT email_verified FROM users WHERE id=$1", ownerID).Scan(&verified); err != nil || verified {
		t.Fatal("revoked session verified contact", err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT attempts FROM verification_challenges WHERE id=$1", store.VerificationID(id)).Scan(&attempts); err != nil || attempts != 0 {
		t.Fatal("foreign/revoked session consumed challenge", err)
	}
}

func TestVerificationSecurityBearerCannotBypassAuthSourceChecks(t *testing.T) {
	_, h := testServer(t)
	for _, path := range []string{"/api/v1/auth/register", "/api/v1/auth/verification/request", "/api/v1/auth/password-reset/request", "/api/v1/auth/password-reset/confirm"} {
		r := httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer ignored-credential")
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expect(t, w, 403)
		r = httptest.NewRequest("POST", path, strings.NewReader(`{}`))
		r.Header.Set("Authorization", "Bearer ignored-credential")
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Requested-With", "zjmfgo")
		r.Header.Set("Origin", "https://foreign.example")
		w = httptest.NewRecorder()
		h.ServeHTTP(w, r)
		expect(t, w, 403)
	}
}
