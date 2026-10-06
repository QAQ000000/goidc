package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestMaintenanceBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "maintenance-super", func(db *store.Store, h http.Handler) {
		user(t, db, h, "maintenance-staff", "admin")
		user(t, db, h, "maintenance-customer", "customer")
	})
}
