package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

// The browser writes to an isolated schema; no external gateway or SMTP is contacted.
func TestConfigurationSettingsBrowserFixture(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_BROWSER_READY_FILE"), os.Getenv("ZJMFGO_BROWSER_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in browser fixture")
	}
	for _, path := range []string{ready, finish} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("fixture requires unused ready and finish paths")
		}
	}
	db, seedHandler := testServer(t)
	origin := os.Getenv("ZJMFGO_BROWSER_ORIGIN")
	if origin == "" {
		origin = "http://localhost:3091"
	}
	cfg := config.Config{PublicOrigin: origin, SMTPPort: 587, PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
	h := New(db, cfg, slog.Default()).Handler()
	user(t, db, seedHandler, "config-super", "super_admin")
	user(t, db, seedHandler, "config-reader", "admin")
	user(t, db, seedHandler, "config-customer", "customer")
	server := httptest.NewServer(h)
	defer server.Close()
	var schema string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]string{"api_origin": server.URL, "username": "config-super", "schema": schema})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	t.Log("configuration browser fixture ready")
	deadline := time.NewTimer(15 * time.Minute)
	defer deadline.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				verifyConfigurationBrowser(t, db, h)
				return
			}
		case <-deadline.C:
			t.Fatal("browser fixture timed out")
		}
	}
}

func verifyConfigurationBrowser(t *testing.T, db *store.Store, _ http.Handler) {
	t.Helper()
	var configured bool
	var ciphertext []byte
	var version int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT configured,key_ciphertext,version FROM payment_settings WHERE id=1").Scan(&configured, &ciphertext, &version); err != nil || !configured || len(ciphertext) == 0 || version < 3 {
		t.Fatal("browser did not persist encrypted payment configuration", err)
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='payment.settings_updated'").Scan(&count); err != nil || count < 2 {
		t.Fatal("browser payment configuration audit missing", err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT configured,password_encrypted,version FROM smtp_settings WHERE id=1").Scan(&configured, &ciphertext, &version); err != nil || !configured || len(ciphertext) == 0 || version < 3 {
		t.Fatal("browser did not persist encrypted SMTP configuration", err)
	}
	for _, query := range []string{
		"SELECT count(*) FROM notification_templates WHERE kind='browser_configuration' AND active=false",
		"SELECT count(*) FROM ticket_reply_preset_categories WHERE name='Browser category edited' AND sort_order=7 AND active=false",
		"SELECT count(*) FROM ticket_reply_presets WHERE title='Browser preset edited' AND active=false",
	} {
		if err := db.Pool.QueryRow(t.Context(), query).Scan(&count); err != nil || count != 1 {
			t.Fatal("browser support configuration missing", query, err)
		}
	}
}
