package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// The opt-in fixture keeps UI acceptance isolated from development credentials.
func TestAdminOperationsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "operations-super", func(db *store.Store, h http.Handler) {
		user(t, db, h, "operations-reader", "admin")
	})
}
