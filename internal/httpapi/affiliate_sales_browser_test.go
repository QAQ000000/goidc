package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in acceptance uses real migrated PostgreSQL and business HTTP handlers.
// Only receipt dates are moved to a closed month to exercise monthly settlement.
func TestAffiliateSalesBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "commission-super", func(db *store.Store, h http.Handler) {
		owner, root := user(t, db, h, "commission-sales", "super_admin")
		customer, cookie := user(t, db, h, "commission-buyer", "customer")
		reader, _ := user(t, db, h, "commission-reader", "admin")
		contentRole(t, db, reader, "sales.view", "affiliates.view")
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, reader); err != nil {
			t.Fatal(err)
		}
		var group int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps) VALUES('验收销售提成组',500) RETURNING id`).Scan(&group); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_enabled=true,sales_commission_group_id=$2 WHERE id=$1`, owner, group); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, customer, owner); err != nil {
			t.Fatal(err)
		}
		pid := product(t, db, 100)
		if _, err := db.Pool.Exec(t.Context(), `UPDATE products SET name='月佣金验收商品' WHERE id=$1`, pid); err != nil {
			t.Fatal(err)
		}
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customer), map[string]any{"amount_cents": 100000, "note": "隔离佣金验收充值"}, root, "commission-fixture-topup"), 201)
		zone := time.FixedZone("Asia/Shanghai", 28800)
		now := time.Now().In(zone)
		closed := time.Date(now.Year(), now.Month(), 1, 12, 0, 0, 0, zone).AddDate(0, -1, 0)
		month := closed.Format("2006-01")
		unsettled := []int64{}
		for n := 0; n < 23; n++ {
			version, item := add(t, h, cookie, pid, 1)
			created := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, cookie, fmt.Sprintf("commission-order-%02d", n))
			expect(t, created, 201)
			oid := int64(data(t, created)["order_id"].(float64))
			var iid int64
			if err := db.Pool.QueryRow(t.Context(), `SELECT id FROM invoices WHERE order_id=$1`, oid).Scan(&iid); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{}, cookie, fmt.Sprintf("commission-pay-%02d", n)), 201)
			if _, err := db.Pool.Exec(t.Context(), `UPDATE invoices SET paid_at=$2 WHERE id=$1`, iid, closed); err != nil {
				t.Fatal(err)
			}
			if n >= 21 {
				unsettled = append(unsettled, oid)
				continue
			}
			input := map[string]any{"month": month, "order_ids": []int64{oid}}
			preview := request(h, "POST", "/api/v1/admin/sales-settlements/preview", input, root, "")
			expect(t, preview, 200)
			input["preview_digest"] = data(t, preview)["preview_digest"]
			expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", input, root, fmt.Sprintf("commission-confirm-%02d", n)), 200)
		}
		manifest, _ := json.Marshal(map[string]any{"month": month, "order_ids": unsettled, "product_id": pid, "reader": "commission-reader"})
		if err := os.WriteFile(os.Getenv("ZJMFGO_BROWSER_READY_FILE")+".fixture.json", manifest, 0600); err != nil {
			t.Fatal(err)
		}
	})
}
