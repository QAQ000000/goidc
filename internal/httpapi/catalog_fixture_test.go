package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestCatalogBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "catalog-super", func(db *store.Store, h http.Handler) {
		reader, _ := user(t, db, h, "catalog-reader", "admin")
		writer, _ := user(t, db, h, "catalog-editor", "admin")
		contentRole(t, db, reader, "products.view")
		contentRole(t, db, writer, "products.view", "products.manage")
		user(t, db, h, "catalog-client", "customer")
	})
}
