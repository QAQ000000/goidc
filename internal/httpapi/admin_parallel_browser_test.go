package httpapi

import (
	"net/http"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Browser acceptance uses disposable, migrated PG18 data and no upstream writes.
func TestAdminParallelBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "parallel-super", func(db *store.Store, h http.Handler) {
		customer, _ := user(t, db, h, "parallel-customer", "customer")
		pid := product(t, db, 10)
		sid, _ := billingSchedulerFixture(t, db, customer, pid, 1001, false)
		if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET name='香港资源验收',resource_info='{"dedicated_ip":"203.0.113.42","power":"on"}' WHERE id=$1`, sid); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE customer_credit_accounts SET limit_cents=50000,used_cents=10000 WHERE user_id=$1`, customer); err != nil {
			t.Fatal(err)
		}
	})
}
