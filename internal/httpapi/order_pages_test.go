package httpapi

import (
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// The count and rows must use the same customer scope and filter predicate.
func TestAdminOrderPagesScopeFiltersAndTotals(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "order-page-root", "super_admin")
	staffID, staff := user(t, db, h, "order-page-staff", "admin")
	contentRole(t, db, staffID, "orders.view", "customers.view")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	var ownID, otherID int64
	var ownCookie string
	for n := 0; n < 2; n++ {
		id, cookie := user(t, db, h, fmt.Sprintf("order-page-customer-%d", n), "customer")
		if n == 0 {
			ownID, ownCookie = id, cookie
			salesAssignment(t, h, root, id, staffID, 1)
		} else {
			otherID = id
		}
		for j := 0; j < 2; j++ {
			version, item := add(t, h, cookie, pid, 1)
			w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, cookie, fmt.Sprintf("page-order-%d-%d", n, j))
			expect(t, w, 201)
			oid := int64(data(t, w)["order_id"].(float64))
			var sid, iid int64
			if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", oid).Scan(&sid); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), "INSERT INTO invoices(invoice_no,user_id,amount_cents,idempotency_key) VALUES($1,$2,1234,$1) RETURNING id", fmt.Sprintf("RENEW-page-%d-%d", n, j), id).Scan(&iid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), "INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'分页续费','renew',1234,$2)", iid, sid); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, path := range []string{"orders/page", "renewal-orders"} {
		base := "/api/v1/admin/" + path
		check := func(cookie, query string, total, items, offset int) map[string]any {
			t.Helper()
			w := request(h, "GET", base+"?"+query, nil, cookie, "")
			expect(t, w, 200)
			d := data(t, w)
			if d["total"] != float64(total) || len(d["items"].([]any)) != items || d["offset"] != float64(offset) {
				t.Fatalf("%s: %s", path, w.Body.String())
			}
			return d
		}
		check(root, "limit=1&offset=1", 4, 1, 1)
		d := check(staff, "limit=1&offset=1", 2, 1, 1)
		row := d["items"].([]any)[0].(map[string]any)
		if row["user_id"] != float64(ownID) {
			t.Fatal("scope leak", row)
		}
		check(staff, fmt.Sprintf("customer_id=%d", otherID), 0, 0, 0)
		check(root, fmt.Sprintf("customer_id=%d&limit=1", ownID), 2, 1, 0)
		check(staff, "status=pending", 2, 2, 0)
		check(staff, "status=paid", 0, 0, 0)
		check(staff, "q=no-such-order", 0, 0, 0)
		check(staff, "offset=5", 2, 0, 5)
		query := "q=商品"
		if path == "renewal-orders" {
			query = "q=分页续费"
		}
		check(staff, query, 2, 2, 0)
		for _, query := range []string{"customer_id=bad", "customer_id=0", "status=unknown"} {
			expect(t, request(h, "GET", base+"?"+query, nil, root, ""), 400)
		}
		expect(t, request(h, "GET", base, nil, ownCookie, ""), 403)
	}
	contentRole(t, db, staffID, "customers.view")
	for _, path := range []string{"orders/page", "renewal-orders"} {
		expect(t, request(h, "GET", "/api/v1/admin/"+path, nil, staff, ""), 403)
	}
}
