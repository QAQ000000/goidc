package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Disposable acceptance data; no worker, upstream calls or real notifications.
func TestAdminOptionsOrderBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "options-super", func(db *store.Store, h http.Handler) {
		reader, _ := user(t, db, h, "options-reader", "admin")
		contentRole(t, db, reader, "products.view", "settings.view")
		customer, _ := user(t, db, h, "options-client", "customer")
		first, second := product(t, db, 100), product(t, db, 100)
		if _, err := db.Pool.Exec(t.Context(), `UPDATE products SET name=CASE WHEN id=$1 THEN '验收商品甲' ELSE '验收商品乙' END WHERE id=ANY($2)`, first, []int64{first, second}); err != nil {
			t.Fatal(err)
		}
		manifest, err := json.Marshal(map[string]any{"customer_id": customer, "product_ids": []int64{first, second}})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
	})
}
