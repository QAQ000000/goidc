package httpapi

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in real browser acceptance in the helper's isolated schema. No workers,
// supplier operations or external notification/payment transports are started.
func TestCustomerAdminIsolationBrowserFixture(t *testing.T) {
	ready := os.Getenv("ZJMFGO_BROWSER_READY_FILE")
	if ready == "" {
		t.Skip("opt-in browser fixture")
	}
	const rootUsername = "isolation-browser-root"
	const customerUsername = "isolation-browser-customer"
	const staffUsername = "isolation-browser-staff"
	adminBrowserFixture(t, rootUsername, func(db *store.Store, h http.Handler) {
		customerID, customer := user(t, db, h, customerUsername, "customer")
		staffID, _ := user(t, db, h, staffUsername, "admin")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions='{}'::text[] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)`, staffID); err != nil {
			t.Fatal(err)
		}
		productID := product(t, db, 8)
		if _, err := db.Pool.Exec(t.Context(), "UPDATE products SET name='身份隔离测试商品' WHERE id=$1", productID); err != nil {
			t.Fatal(err)
		}
		add(t, h, customer, productID, 1)
		writeBrowserJSON(t, ready+".fixture.json", map[string]any{
			"customer_email":   customerUsername + "@example.test",
			"staff_username":   staffUsername,
			"product_id":       productID,
			"external_actions": false,
		})
		t.Cleanup(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			var customerOrders, staffOrders, rootOrders, rootUsers int64
			err := db.Pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM orders WHERE user_id=$1),
 (SELECT count(*) FROM orders WHERE user_id=$2),
 (SELECT count(*) FROM orders o JOIN users u ON u.id=o.user_id WHERE u.username=$3),
 (SELECT count(*) FROM users WHERE username=$3)`, customerID, staffID, rootUsername).Scan(&customerOrders, &staffOrders, &rootOrders, &rootUsers)
			if err != nil || customerOrders != 1 || staffOrders != 0 || rootOrders != 0 || rootUsers != 1 {
				t.Errorf("identity isolation browser acceptance incomplete: customer_orders=%d staff_orders=%d root_orders=%d root_users=%d err=%v", customerOrders, staffOrders, rootOrders, rootUsers, err)
			}
		})
	})
}
