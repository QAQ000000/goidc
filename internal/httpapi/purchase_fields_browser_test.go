package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestPurchaseFieldsBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "fields-browser-admin", func(db *store.Store, h http.Handler) {
		uid, customer := user(t, db, h, "fields-browser-customer", "customer")
		_, root := user(t, db, h, "fields-browser-root", "super_admin")
		user(t, db, h, "fields-browser-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['products.view'] WHERE name='fields-browser-reader-role'"); err != nil {
			t.Fatal(err)
		}
		pid := product(t, db, 10)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name='自定义资料云主机' WHERE id=$1", pid); err != nil {
			t.Fatal(err)
		}
		fields := purchaseDefinitions()
		expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/products/%d/purchase-fields", pid), store.PurchaseFieldsInput{ExpectedRevision: *configRevision(t, h, root, pid), Fields: fields}, root, ""), 200)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 10000}, root, "browser-fields-balance"), 201)
		_ = customer
		t.Cleanup(func() {
			var count int
			if err := db.Pool.QueryRow(context.Background(), `SELECT count(*) FROM order_items WHERE purchase_fields @> '[{"key":"hostname","value":"web-02","label":"实例名称"}]'::jsonb`).Scan(&count); err != nil || count != 1 {
				t.Errorf("browser purchase snapshot not completed: %d %v", count, err)
			}
		})
	})
}
