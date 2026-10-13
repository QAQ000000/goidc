package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestSalesSettlementLifecycle(t *testing.T) {
	db, h := testServer(t)
	owner, root := user(t, db, h, "sales-confirm-root", "super_admin")
	customer, cookie := user(t, db, h, "sales-confirm-customer", "customer")
	deniedID, denied := user(t, db, h, "sales-confirm-denied", "admin")
	var gid int64
	if e := db.Pool.QueryRow(t.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps) VALUES('confirm',7500) RETURNING id`).Scan(&gid); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool.Exec(t.Context(), `UPDATE users SET sales_enabled=true,sales_commission_group_id=$2 WHERE id=$1`, owner, gid); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, customer, owner); e != nil {
		t.Fatal(e)
	}
	pid := product(t, db, 10)
	version, item := add(t, h, cookie, pid, 1)
	created := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, cookie, "confirm-order")
	expect(t, created, 201)
	oid := int64(data(t, created)["order_id"].(float64))
	var iid int64
	if e := db.Pool.QueryRow(t.Context(), `UPDATE invoices SET status='paid',paid_at='2026-08-10T12:00:00+08' WHERE order_id=$1 RETURNING id`, oid).Scan(&iid); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool.Exec(t.Context(), `UPDATE orders SET status='paid' WHERE id=$1`, oid); e != nil {
		t.Fatal(e)
	}
	body := map[string]any{"month": "2026-08", "order_ids": []int64{oid}}
	if _, e := db.Pool.Exec(t.Context(), `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,1333,1333,1,'sales-credit-test')`, iid, customer); e != nil {
		t.Fatal(e)
	}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, root, ""), 409)
	if _, e := db.Pool.Exec(t.Context(), `UPDATE credit_invoice_payments SET remaining_cents=0,status='repaid' WHERE invoice_id=$1`, iid); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool.Exec(t.Context(), `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,1333,'sales-cash-receipt','2026-09-02T12:00:00+08','sales-credit-repay',1,2,0)`, iid, customer, owner); e != nil {
		t.Fatal(e)
	}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, root, ""), 409)
	body["month"] = "2026-09"

	p := request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, root, "")
	expect(t, p, 200)

	_, otherRoot := user(t, db, h, "sales-confirm-other-root", "super_admin")
	otherPreview := request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, otherRoot, "")
	expect(t, otherPreview, 200)
	if data(t, otherPreview)["preview_digest"] != data(t, p)["preview_digest"] {
		t.Fatal("full-scope actor changed commission")
	}
	contentRole(t, db, deniedID, "sales.view", "sales.manage")
	if _, e := db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, deniedID); e != nil {
		t.Fatal(e)
	}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, denied, ""), 403)
	body["preview_digest"] = data(t, p)["preview_digest"]
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", body, denied, "scope-denied-key"), 403)

	if _, e := db.Pool.Exec(t.Context(), `UPDATE sales_commission_groups SET version=version+1,purchase_rate_bps=8000 WHERE id=$1`, gid); e != nil {
		t.Fatal(e)
	}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", body, root, "stale-key"), 409)
	delete(body, "preview_digest")
	p = request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, root, "")
	expect(t, p, 200)
	body["preview_digest"] = data(t, p)["preview_digest"]

	if _, e := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_sales_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_sales_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_sales_audit()`); e != nil {
		t.Fatal(e)
	}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", body, root, "failed-audit-key"), 500)
	var count int
	if e := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM sales_settlements`).Scan(&count); e != nil || count != 0 {
		t.Fatalf("audit rollback: count=%d err=%v", count, e)
	}
	if _, e := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_sales_audit ON audit_events; DROP FUNCTION reject_sales_audit()`); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 2)
	for n := 0; n < 2; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- request(h, "POST", "/api/v1/admin/sales-settlements", body, root, "confirm-key")
		}()
	}
	wg.Wait()
	close(results)
	var id float64
	for w := range results {
		expect(t, w, 200)
		id = data(t, w)["id"].(float64)
	}
	changed := map[string]any{"month": "2026-06", "order_ids": []int64{oid}, "preview_digest": body["preview_digest"]}
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", changed, root, "confirm-key"), 409)
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements", body, root, "duplicate"), 409)
	if _, e := db.Pool.Exec(t.Context(), `UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE username='sales-confirm-denied'`); e != nil {
		t.Fatal(e)
	}
	contentRole(t, db, deniedID, "sales.view")
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/sales-settlements/%.0f", id), nil, denied, ""), 404)
	list := request(h, "GET", "/api/v1/admin/sales-settlements", nil, denied, "")
	expect(t, list, 200)
	if data(t, list)["total"] != float64(0) {
		t.Fatal(list.Body.String())
	}
	// The trigger and audit participate in the invoice transaction.
	if _, e := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_sales_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'audit unavailable'; END $$; CREATE TRIGGER reject_sales_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_sales_audit()`); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Pool.Exec(t.Context(), `UPDATE invoices SET status='refunded' WHERE id=$1`, iid); e == nil {
		t.Fatal("refund committed without reversal audit")
	}
	if _, e := db.Pool.Exec(t.Context(), `DROP TRIGGER reject_sales_audit ON audit_events; DROP FUNCTION reject_sales_audit()`); e != nil {
		t.Fatal(e)
	}

	tx, e := db.Pool.Begin(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = tx.Exec(t.Context(), `UPDATE invoices SET status='refunded' WHERE id=$1`, iid); e != nil {
		t.Fatal(e)
	}
	tx.Rollback(t.Context())
	d := request(h, "GET", fmt.Sprintf("/api/v1/admin/sales-settlements/%.0f", id), nil, root, "")
	expect(t, d, 200)
	if data(t, d)["status"] != "confirmed" {
		t.Fatal(d.Body.String())
	}
	if _, e = db.Pool.Exec(t.Context(), `UPDATE invoices SET status='refunded' WHERE id=$1`, iid); e != nil {
		t.Fatal(e)
	}
	d = request(h, "GET", fmt.Sprintf("/api/v1/admin/sales-settlements/%.0f", id), nil, root, "")
	expect(t, d, 200)
	if data(t, d)["status"] != "reversed" || data(t, d)["net_commission_cents"] != float64(0) {
		t.Fatal(d.Body.String())
	}
	if _, e = db.Pool.Exec(t.Context(), `UPDATE invoices SET status='paid' WHERE id=$1`, iid); e != nil {
		t.Fatal(e)
	}
	delete(body, "preview_digest")
	expect(t, request(h, "POST", "/api/v1/admin/sales-settlements/preview", body, root, ""), 409)
}
