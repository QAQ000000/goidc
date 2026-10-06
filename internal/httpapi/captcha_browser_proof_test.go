package httpapi

import (
	"encoding/json"
	"log/slog"
	"net"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
)

// This fixture requires a real browser to consume issued challenges and create
// a session. Inserting consumed rows cannot satisfy its session/audit assertions.
func TestCaptchaCorrectBrowserProof(t *testing.T) {
	ready, finish := os.Getenv("ZJMFGO_CAPTCHA_READY_FILE"), os.Getenv("ZJMFGO_CAPTCHA_FINISH_FILE")
	if ready == "" || finish == "" {
		t.Skip("opt-in browser proof")
	}
	db, h := testServer(t)
	id, _ := user(t, db, h, "captcha-proof-admin", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	var baseline int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='auth.login_succeeded' AND actor_id=$1", id).Scan(&baseline); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:18099")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(New(db, config.Config{PublicOrigin: "http://127.0.0.1:3099"}, slog.Default()).Handler())
	server.Listener = listener
	server.Start()
	defer server.Close()
	var schema string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&schema); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(map[string]any{"api_origin": server.URL, "username": "captcha-proof-admin", "schema": schema})
	if err := os.WriteFile(ready, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(ready)
	defer os.Remove(finish)
	timer := time.NewTimer(8 * time.Minute)
	defer timer.Stop()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-timer.C:
			t.Fatal("browser proof timed out")
		case <-tick.C:
			if _, err := os.Stat(finish); err == nil {
				goto verify
			}
		}
	}
verify:
	var consumed, sessions, logins int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM auth_captcha_challenges WHERE consumed_at IS NOT NULL),
 (SELECT count(*) FROM sessions WHERE user_id=$1 AND expires_at>now()),
 (SELECT count(*) FROM audit_events WHERE action='auth.login_succeeded' AND actor_id=$1)`, id).Scan(&consumed, &sessions, &logins); err != nil {
		t.Fatal(err)
	}
	if consumed < 2 || sessions != 1 || logins != baseline+1 {
		t.Fatalf("incomplete browser proof: consumed=%d sessions=%d logins=%d baseline=%d", consumed, sessions, logins, baseline)
	}
	t.Logf("browser CAPTCHA proof: consumed=%d active_sessions=%d new_login_audits=%d", consumed, sessions, logins-baseline)
}
