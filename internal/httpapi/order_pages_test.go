package httpapi

import (
	"fmt"
	"testing"
	"time"

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

func TestAdminOrderPagesShanghaiDatesPaymentStateAndVersion(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "order-date-root", "super_admin")
	uid, _ := user(t, db, h, "order-date-customer", "customer")
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	start := time.Date(2026, 10, 10, 0, 0, 0, 0, zone)
	var insideID, reviewID int64
	for n, created := range []time.Time{start.Add(-time.Microsecond), start, start.Add(24*time.Hour - time.Microsecond), start.Add(24 * time.Hour)} {
		var id int64
		review := "approved"
		if n == 2 {
			review = "pending"
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,created_at,review_status) VALUES($1,$2,100,$1,'date-fixture',$3,$4) RETURNING id`, fmt.Sprintf("DATE-ORDER-%d", n), uid, created, review).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			insideID = id
		}
		if n == 2 {
			reviewID = id
		}
	}
	for _, path := range []string{"/api/v1/admin/orders", "/api/v1/admin/orders/page"} {
		query := "?date_from=2026-10-10&date_to=2026-10-10"
		w := request(h, "GET", path+query, nil, root, "")
		expect(t, w, 200)
		var rows []map[string]any
		if path == "/api/v1/admin/orders" {
			rows = listData(t, w)
		} else {
			if data(t, w)["total"] != float64(2) {
				t.Fatalf("Shanghai day total: %s", w.Body.String())
			}
			for _, value := range data(t, w)["items"].([]any) {
				rows = append(rows, value.(map[string]any))
			}
		}
		if len(rows) != 2 {
			t.Fatalf("Shanghai date boundaries %s: %s", path, w.Body.String())
		}
		for _, row := range rows {
			if row["id"] != float64(insideID) && row["id"] != float64(reviewID) {
				t.Fatalf("outside day included: %#v", row)
			}
			if row["version"] != float64(1) {
				t.Fatalf("version absent: %#v", row)
			}
		}
		for _, invalid := range []string{"date_from=2026-02-30", "date_to=bad", "date_from=2026-10-11&date_to=2026-10-10", "payment_status=unknown"} {
			expect(t, request(h, "GET", path+"?"+invalid, nil, root, ""), 400)
		}
	}
	w := request(h, "GET", "/api/v1/admin/orders/page?status=review_pending", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(1) || data(t, w)["items"].([]any)[0].(map[string]any)["id"] != float64(reviewID) {
		t.Fatalf("review filter: %s", w.Body.String())
	}
	for n, paymentStatus := range []string{"unpaid", "paid", "cancelled", "refunded"} {
		var id int64
		orderStatus := "pending"
		if paymentStatus != "unpaid" {
			orderStatus = paymentStatus
		}
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,status,created_at) VALUES($1,$2,100,$1,'payment-state-fixture',$3,$4) RETURNING id`, fmt.Sprintf("PAYMENT-STATE-%d", n), uid, orderStatus, start.Add(24*time.Hour)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key) VALUES($1,$2,$3,$4,100,$1)`, fmt.Sprintf("PAYMENT-INVOICE-%d", n), uid, id, paymentStatus); err != nil {
			t.Fatal(err)
		}
		w := request(h, "GET", "/api/v1/admin/orders/page?payment_status="+paymentStatus, nil, root, "")
		expect(t, w, 200)
		if data(t, w)["total"] != float64(1) || data(t, w)["items"].([]any)[0].(map[string]any)["id"] != float64(id) {
			t.Fatalf("payment %s: %s", paymentStatus, w.Body.String())
		}
	}
	w = request(h, "GET", "/api/v1/admin/orders/page?payment_status=deferred", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(4) {
		t.Fatalf("deferred filter: %s", w.Body.String())
	}
	page := request(h, "GET", "/api/v1/admin/orders/page?date_from=2026-10-10&date_to=2026-10-10&limit=1&offset=1", nil, root, "")
	expect(t, page, 200)
	if data(t, page)["total"] != float64(2) || len(data(t, page)["items"].([]any)) != 1 || data(t, page)["offset"] != float64(1) {
		t.Fatalf("date count/paging mismatch: %s", page.Body.String())
	}
}
