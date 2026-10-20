package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
)

// This opt-in fixture runs the real API and worker against an isolated schema.
// Only Telegram's HTTP transport is replaced; no message leaves this process.
func TestNotificationCenterBrowserFixture(t *testing.T) {
	ready := os.Getenv("ZJMFGO_BROWSER_READY_FILE")
	if ready == "" {
		t.Skip("opt-in browser fixture")
	}
	ctx, cancel := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	defer func() { cancel(); workers.Wait() }()
	oldTransport := http.DefaultTransport
	defer func() { http.DefaultTransport = oldTransport }()
	http.DefaultTransport = centerTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "api.telegram.org" {
			return oldTransport.RoundTrip(r)
		}
		body := `{"ok":true,"result":true}`
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			var input struct {
				ChatID int64 `json:"chat_id"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				return nil, err
			}
			raw, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"message_id": 101, "chat": map[string]any{"id": input.ChatID, "type": "private"}}})
			body = string(raw)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	})
	adminBrowserFixture(t, "notify-browser-admin", func(db *store.Store, h http.Handler) {
		_, seedAdmin := user(t, db, h, "notify-browser-seed", "super_admin")
		uid, _ := user(t, db, h, "notify-browser-customer", "customer")
		staff, _ := user(t, db, h, "notify-browser-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['notifications.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, staff); err != nil {
			t.Fatal(err)
		}
		centerBot(t, h, seedAdmin, "browser-telegram")
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_inbox_messages(user_id,subject,body) VALUES($1,'欢迎使用通知中心','这是一条本地验收消息。')`, uid); err != nil {
			t.Fatal(err)
		}
		writeBrowserJSON(t, ready+".fixture.json", map[string]any{"customer": "notify-browser-customer@example.test", "reader": "notify-browser-reader", "instance_id": "browser-telegram", "event_trigger": ready + ".event"})
		workers.Add(1)
		go func() {
			defer workers.Done()
			timer := time.NewTicker(100 * time.Millisecond)
			defer timer.Stop()
			last := ""
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
				}
				raw, err := os.ReadFile(ready + ".event")
				if err == nil && len(raw) > 0 && string(raw) != last {
					last = string(raw)
					centerInvoice(t, db, uid, "BROWSER-"+strings.TrimSpace(last))
				}
				if _, err = notification.DrainNotificationCenter(ctx, db, 10); err != nil && ctx.Err() == nil {
					t.Error(err)
					return
				}
			}
		}()
	})
}
