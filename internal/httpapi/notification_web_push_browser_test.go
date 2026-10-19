package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in E2 fixture: the real API/schema and a configured compiled push
// instance. No worker runs and every push route remains disabled. The browser
// runner can simulate PushManager without contacting browser-vendor services.
func TestWebPushBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "webpush-browser-admin", func(db *store.Store, h http.Handler) {
		_, admin := user(t, db, h, "webpush-browser-seed", "super_admin")
		user(t, db, h, "webpush-browser-customer", "customer")
		user(t, db, h, "webpush-browser-other", "customer")
		webPushInstance(t, h, admin, "browser-web-push")
	})
}
