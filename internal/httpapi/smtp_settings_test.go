package httpapi

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

func TestSMTPSettingsSecurityAndDynamicResolution(t *testing.T) {
	db, h := testServer(t)
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{17}, 32)
	actor, _ := user(t, db, h, "smtp-root", "super_admin")
	var hash string
	if e := db.Pool.QueryRow(t.Context(), "SELECT token_hash FROM sessions WHERE user_id=$1", actor).Scan(&hash); e != nil {
		t.Fatal(e)
	}
	staffID, staff := user(t, db, h, "smtp-staff", "admin")
	_, customer := user(t, db, h, "smtp-customer", "customer")
	_, root := user(t, db, h, "smtp-root2", "super_admin")
	server := New(db, config.Config{PublicOrigin: "http://localhost:3088", SMTPPort: 587, ProvisionEncryptionKey: bytes.Repeat([]byte{17}, 32)}, slog.Default())
	mux := server.Handler()
	for _, c := range []string{staff, customer} {
		expected := 403
		if c == customer {
			expected = 401
		}
		expect(t, request(mux, "GET", "/api/v1/admin/smtp-settings", nil, c, ""), expected)
	}
	response := request(mux, "GET", "/api/v1/admin/smtp-settings", nil, root, "")
	expect(t, response, 200)
	if response.Header().Get("Cache-Control") != "no-store" || bytes.Contains(response.Body.Bytes(), []byte(`"password":`)) {
		t.Fatal("secret caching")
	}
	expect(t, request(mux, "PUT", "/api/v1/admin/smtp-settings", map[string]any{"expected_version": 1}, root, ""), 400)
	expect(t, request(mux, "PUT", "/api/v1/admin/smtp-settings", map[string]any{"enabled": false, "host": "", "port": 587, "username": "", "from": "", "expected_version": 1}, root, ""), 200)
	var rootVersion int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT version FROM smtp_settings WHERE id=1").Scan(&rootVersion); err != nil || rootVersion != 2 {
		t.Fatal("registered route did not update", err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE smtp_settings SET configured=false,version=1 WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	if _, e := db.UpdateSMTPSettings(t.Context(), staffID, hash, store.SMTPSettings{Port: 587}, nil, false, 1, store.SMTPRuntimeConfig{}); e == nil {
		t.Fatal("staff write accepted")
	}
	fallback := store.SMTPRuntimeConfig{Host: "old.example", Port: 587, Username: "mailer", Password: "old-secret", From: "from@example.test"}
	out, e := db.SMTPSettings(t.Context(), fallback)
	if e != nil || out.Configured || !out.PasswordConfigured || out.Host != fallback.Host {
		t.Fatal(out, e)
	}
	out, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, nil, false, 1, fallback)
	if e != nil || !out.PasswordConfigured {
		t.Fatal(out, e)
	}
	runtime, e := db.ResolveSMTPConfig(t.Context(), fallback)
	if e != nil || runtime.Password != fallback.Password {
		t.Fatal(e)
	}
	out.Port, out.TLSMode, out.FromName = 465, "implicit_tls", "邮件服务"
	out, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, nil, false, out.Version, fallback)
	if e != nil || out.TLSMode != "implicit_tls" || out.FromName != "邮件服务" {
		t.Fatal("implicit TLS settings", e)
	}
	runtime, e = db.ResolveSMTPConfig(t.Context(), fallback)
	if e != nil || runtime.TLSMode != "implicit_tls" || runtime.FromName != out.FromName {
		t.Fatal("SMTP field resolution", e)
	}
	for _, c := range []string{staff, customer} {
		expected := 403
		if c == customer {
			expected = 401
		}
		expect(t, request(mux, "POST", "/api/v1/admin/smtp-settings/test", map[string]any{"recipient": "to@example.test", "expected_version": out.Version}, c, ""), expected)
	}
	expect(t, request(mux, "POST", "/api/v1/admin/smtp-settings/test", map[string]any{"recipient": "to@example.test", "expected_version": 1}, root, ""), 409)
	expect(t, requestBearer(mux, "POST", "/api/v1/admin/smtp-settings/test", map[string]any{"recipient": "to@example.test", "expected_version": out.Version}, "test-api-credential"), 403)
	expect(t, request(mux, "POST", "/api/v1/admin/smtp-settings/test", map[string]any{"recipient": "to@example.test", "expected_version": out.Version}, root, ""), 202)
	var pending int
	if e = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM email_outbox WHERE recipient='to@example.test' AND status='pending' AND attempts=0").Scan(&pending); e != nil || pending != 1 {
		t.Fatal("SMTP test must queue exactly once", e)
	}
	if _, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, nil, false, 1, fallback); e == nil {
		t.Fatal("stale accepted")
	}
	for _, invalid := range []store.SMTPSettings{
		{Enabled: true, Host: "smtp.example.test", Port: 465, TLSMode: "invalid", From: "from@example.test"},
		{Enabled: true, Host: "smtp.example.test", Port: 587, From: "Name <from@example.test>"},
	} {
		if _, e = db.UpdateSMTPSettings(t.Context(), actor, hash, invalid, nil, false, out.Version, fallback); e == nil {
			t.Fatal("unsupported SMTP configuration accepted")
		}
	}
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{17}, 16)
	password := "new-secret"
	if _, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, &password, false, out.Version, fallback); e == nil {
		t.Fatal("non-256-bit key accepted")
	}
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{17}, 32)
	out.Enabled = false
	out, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, nil, true, out.Version, fallback)
	if e != nil || out.PasswordConfigured {
		t.Fatal(e)
	}
	runtime, e = db.ResolveSMTPConfig(t.Context(), fallback)
	if e != nil || runtime.Host != "" {
		t.Fatal("disabled runtime", e)
	}
	var payload string
	if e = db.Pool.QueryRow(t.Context(), "SELECT payload::text FROM audit_events WHERE action='smtp.settings_updated' LIMIT 1").Scan(&payload); e != nil || strings.Contains(payload, "secret") {
		t.Fatal("audit secret", e)
	}
}
