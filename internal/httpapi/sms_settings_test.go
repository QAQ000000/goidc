package httpapi

import (
	"bytes"
	"errors"
	"github.com/codex/zjmfgo/internal/store"
	"testing"
	"time"
)

func TestSMSSettingsAndEncryptedOutbox(t *testing.T) {
	db, h := testServer(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{17}, 32)
	actor, cookie := user(t, db, h, "sms-root", "super_admin")
	_, staff := user(t, db, h, "sms-staff", "admin")
	expect(t, request(h, "GET", "/api/v1/admin/sms-settings", nil, staff, ""), 403)
	response := request(h, "GET", "/api/v1/admin/sms-settings", nil, cookie, "")
	expect(t, response, 200)
	if bytes.Contains(response.Body.Bytes(), []byte(`"token":`)) || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("secret caching")
	}
	var hash string
	if e := db.Pool.QueryRow(t.Context(), `SELECT token_hash FROM sessions WHERE user_id=$1`, actor).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	token := "fixture-secret"
	settings := store.SMSSettings{Enabled: true, Provider: "generic_http_json", Endpoint: "http://127.0.0.1:19488/sms"}
	out, e := db.UpdateSMSSettings(t.Context(), actor, hash, settings, &token, false, 1)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = db.UpdateSMSSettings(t.Context(), actor, hash, settings, nil, false, 1); e == nil {
		t.Fatal("stale version accepted")
	}
	expect(t, request(h, "POST", "/api/v1/admin/sms-settings/test", map[string]any{"recipient": "+8613800138000", "expected_version": 1}, cookie, ""), 409)
	var secret []byte
	if e = db.Pool.QueryRow(t.Context(), `SELECT token_encrypted FROM sms_settings`).Scan(&secret); e != nil || bytes.Contains(secret, []byte(token)) {
		t.Fatal("token plaintext", e)
	}
	runtime, e := db.ResolveSMSConfig(t.Context())
	if e != nil || runtime.Token != token {
		t.Fatal("runtime resolution", e)
	}
	tx, e := db.Pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback(t.Context())
	challenge := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, e = tx.Exec(t.Context(), `INSERT INTO verification_challenges(id,purpose,channel,recipient,code_hash,expires_at) VALUES($1,'password_reset','sms','+8613800138000',$1,now()+interval '1 minute')`, challenge); e != nil {
		t.Fatal(e)
	}
	if e = db.QueueSMSVerification(t.Context(), tx, "+8613800138000", "123456", "password_reset", challenge, time.Now().Add(time.Minute)); e != nil {
		t.Fatal(e)
	}
	if e = tx.Commit(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = db.Pool.QueryRow(t.Context(), `SELECT body_encrypted FROM sms_outbox`).Scan(&secret); e != nil || bytes.Contains(secret, []byte("123456")) {
		t.Fatal("code plaintext", e)
	}
	job, e := db.ClaimSMSJob(t.Context())
	if e != nil || job.Body != "验证码：123456" {
		t.Fatal("claim", e)
	}
	if e = db.CompleteSMSJob(t.Context(), *job, errors.New("fixture-secret 123456")); e != nil {
		t.Fatal(e)
	}
	var message string
	if e = db.Pool.QueryRow(t.Context(), `SELECT last_error FROM sms_outbox`).Scan(&message); e != nil || message != "SMS delivery failed" {
		t.Fatal("delivery error leaked", e)
	}
	if _, e = db.Pool.Exec(t.Context(), `UPDATE verification_challenges SET consumed_at=now(); UPDATE sms_outbox SET available_at=now()`); e != nil {
		t.Fatal(e)
	}
	if _, e = db.ClaimSMSJob(t.Context()); e == nil {
		t.Fatal("consumed code claimed")
	}
	if _, e = db.Pool.Exec(t.Context(), `UPDATE sms_outbox SET expires_at=now()-interval '1 second'`); e != nil {
		t.Fatal(e)
	}
	if e = db.RecoverStaleSMSJobs(t.Context()); e != nil {
		t.Fatal(e)
	}
	if e = db.Pool.QueryRow(t.Context(), `SELECT body_encrypted FROM sms_outbox`).Scan(&secret); e != nil || len(secret) != 0 {
		t.Fatal("expired payload retained", e)
	}
	settings.Enabled = false
	if _, e = db.UpdateSMSSettings(t.Context(), actor, hash, settings, nil, true, out.Version); e != nil {
		t.Fatal(e)
	}
	runtime, e = db.ResolveSMSConfig(t.Context())
	if e != nil || runtime.Enabled || runtime.Token != "" {
		t.Fatal("disabled channel active", e)
	}
}
