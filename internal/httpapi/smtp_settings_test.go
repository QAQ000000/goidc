package httpapi

import (
	"bytes"
	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"log/slog"
	"net/http"
	"strings"
	"testing"
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
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/admin/smtp-settings", server.sessionAuth(server.smtpSettings, false))
	mux.HandleFunc("PUT /api/v1/admin/smtp-settings", server.sessionAuth(server.updateSMTPSettings, false))
	for _, c := range []string{staff, customer} {
		expect(t, request(mux, "GET", "/api/v1/admin/smtp-settings", nil, c, ""), 403)
	}
	response := request(mux, "GET", "/api/v1/admin/smtp-settings", nil, root, "")
	expect(t, response, 200)
	if response.Header().Get("Cache-Control") != "no-store" || bytes.Contains(response.Body.Bytes(), []byte(`"password":`)) {
		t.Fatal("secret caching")
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
	if _, e = db.UpdateSMTPSettings(t.Context(), actor, hash, out, nil, false, 1, fallback); e == nil {
		t.Fatal("stale accepted")
	}
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
