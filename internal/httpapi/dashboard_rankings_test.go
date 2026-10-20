package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDashboardRankingQueryValidation(t *testing.T) {
	for _, query := range []string{
		"month=", "month=2024-2", "month=2024-13", "month=1999-12", "month=9999-01", "month=2024-02&month=2024-03",
		"limit=", "limit=0", "limit=21", "limit=-1", "limit=no", "limit=1&limit=2", "year=2024", "customer_id=1",
		"month=2024-02;limit=1", "month=%zz", "format=csv", "format=json&format=json",
	} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", "/?"+query, nil)
		if _, _, _, ok := dashboardRankingInput(w, r); ok || w.Code != 400 {
			t.Fatalf("accepted ranking query %q: %s", query, w.Body.String())
		}
	}
	for _, query := range []string{"", "month=2024-02", "month=2024-02&limit=20&format=json"} {
		w := httptest.NewRecorder()
		start, end, limit, ok := dashboardRankingInput(w, httptest.NewRequest("GET", "/?"+query, nil))
		if !ok || limit < 1 || limit > 20 || !end.Equal(start.AddDate(0, 1, 0)) {
			t.Fatalf("valid ranking query rejected: %q %s", query, w.Body.String())
		}
		if query != "" && start.UTC().Format(time.RFC3339) != "2024-01-31T16:00:00Z" {
			t.Fatalf("month is not Shanghai natural month: %v", start)
		}
		if query == "" && (limit != 5 || start.Format("2006-01") != time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01")) {
			t.Fatalf("wrong ranking defaults: start=%v limit=%d", start, limit)
		}
	}
}

type dashboardRankingFixture struct {
	t  *testing.T
	db *store.Store
	h  http.Handler
}

func (f dashboardRankingFixture) exec(query string, args ...any) {
	f.t.Helper()
	if _, err := f.db.Pool.Exec(f.t.Context(), query, args...); err != nil {
		f.t.Fatal(err)
	}
}

func (f dashboardRankingFixture) order(uid int64, key, paidAt, status string, pids []int64, quantities []int, gross []int64, net int64) (int64, int64, int64) {
	f.t.Helper()
	var order, invoice, firstItem int64
	if err := f.db.Pool.QueryRow(f.t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
 VALUES($1,$2,'paid',$3,$1,'fixture') RETURNING id`, key, uid, net).Scan(&order); err != nil {
		f.t.Fatal(err)
	}
	for index, pid := range pids {
		var item int64
		if err := f.db.Pool.QueryRow(f.t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
 VALUES($1,$2,$3,'monthly',$4,100,0,$5) RETURNING id`, order, pid, fmt.Sprintf("Ranking product %d", pid), quantities[index], gross[index]).Scan(&item); err != nil {
			f.t.Fatal(err)
		}
		if index == 0 {
			firstItem = item
		}
	}
	if err := f.db.Pool.QueryRow(f.t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at)
 VALUES($1,$2,$3,$4,$5,$6::timestamptz) RETURNING id`, key, uid, order, status, net, paidAt).Scan(&invoice); err != nil {
		f.t.Fatal(err)
	}
	return order, invoice, firstItem
}

func (f dashboardRankingFixture) get(path, cookie string) map[string]any {
	f.t.Helper()
	w := request(f.h, "GET", path, nil, cookie, "")
	expect(f.t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Content-Disposition") != "" {
		f.t.Fatalf("ranking cache/download headers: %v", w.Header())
	}
	return data(f.t, w)
}

func TestDashboardRankingAmountsScopeAndShanghaiPeriod(t *testing.T) {
	db, h := testServer(t)
	f := dashboardRankingFixture{t, db, h}
	owner, staff := user(t, db, h, "ranking-owner", "admin")
	otherOwner, _ := user(t, db, h, "ranking-hidden-owner", "admin")
	uid, customer := user(t, db, h, "ranking-customer", "customer")
	hidden, _ := user(t, db, h, "ranking-hidden-customer", "customer")
	f.exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", owner)
	f.exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", uid, owner)
	f.exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", hidden, otherOwner)
	// Every fresh connection must use UTC; SET on one pooled connection is insufficient.
	cfg := db.Pool.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	db.Pool.Close()
	db.Pool = pool
	var timezone string
	if err = pool.QueryRow(t.Context(), "SHOW timezone").Scan(&timezone); err != nil || timezone != "UTC" {
		t.Fatalf("timezone=%q err=%v", timezone, err)
	}
	first, second, secret := product(t, db, 10), product(t, db, 10), product(t, db, 10)
	f.exec("UPDATE products SET active=false WHERE id=$1", first)
	order, _, item := f.order(uid, "ranking-discount", "2024-02-15T00:00:00+08", "paid", []int64{first, second}, []int{3, 1}, []int64{300, 100}, 399)
	var service int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle)
 VALUES($1,$2,$3,$4,'ranking','ranking.test','monthly') RETURNING id`, uid, order, item, first).Scan(&service); err != nil {
		t.Fatal(err)
	}
	f.exec(`WITH i AS (INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
 VALUES('ranking-renew',$1,'paid',50,'2024-02-16T00:00:00+08') RETURNING id)
 INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) SELECT id,'renewal','renew',50,$2 FROM i`, uid, service)
	for index, timestamp := range []string{"2024-01-31T15:59:59Z", "2024-01-31T16:00:00Z", "2024-02-29T15:59:59Z", "2024-02-29T16:00:00Z"} {
		amount := []int64{1000, 7, 11, 2000}[index]
		f.order(uid, fmt.Sprintf("ranking-boundary-%d", index), timestamp, "paid", []int64{second}, []int{1}, []int64{amount}, amount)
	}
	f.order(hidden, "ranking-secret", "2024-02-15T00:00:00+08", "paid", []int64{secret}, []int{1}, []int64{9999}, 9999)
	f.order(uid, "ranking-unpaid", "2024-02-15T00:00:00+08", "unpaid", []int64{second}, []int{1}, []int64{888}, 888)
	f.order(uid, "ranking-balance-refund", "2024-02-15T00:00:00+08", "refunded", []int64{second}, []int{1}, []int64{777}, 777)
	base := "/api/v1/admin/reports/"
	products := f.get(base+"product-ranking?month=2024-02&limit=1", staff)
	if products["month"] != "2024-02" || products["timezone"] != "Asia/Shanghai" || products["total"] != float64(2) || products["limit"] != float64(1) {
		t.Fatalf("ranking metadata: %#v", products)
	}
	rows := products["items"].([]any)
	if len(rows) != 1 {
		t.Fatalf("product limit failed: %#v", products)
	}
	row := rows[0].(map[string]any)
	for field, want := range map[string]float64{"product_id": float64(first), "new_order_amount_cents": 299, "new_quantity": 3, "new_order_count": 1, "renewal_amount_cents": 50, "renewal_quantity": 1, "renewal_order_count": 1, "total_amount_cents": 349} {
		if row[field] != want {
			t.Fatalf("product %s=%v want %v: %#v", field, row[field], want, row)
		}
	}
	allProducts := f.get(base+"product-ranking?month=2024-02", staff)["items"].([]any)
	if len(allProducts) != 2 || allProducts[1].(map[string]any)["total_amount_cents"] != float64(118) {
		t.Fatalf("scope, UTC boundary or discount conservation: %#v", allProducts)
	}
	staffRows := f.get(base+"staff-ranking?month=2024-02", staff)["items"].([]any)
	if len(staffRows) != 1 {
		t.Fatalf("staff scope leaked: %#v", staffRows)
	}
	staffRow := staffRows[0].(map[string]any)
	for field, want := range map[string]float64{"sales_owner_id": float64(owner), "amount_cents": 467, "invoice_count": 4, "customer_count": 1, "purchase_amount_cents": 417, "renewal_amount_cents": 50, "upgrade_amount_cents": 0, "other_amount_cents": 0} {
		if staffRow[field] != want {
			t.Fatalf("staff %s=%v want %v: %#v", field, staffRow[field], want, staffRow)
		}
	}
	for _, kind := range []string{"product-ranking", "staff-ranking"} {
		path := base + kind + "?month=2024-02"
		expect(t, request(h, "GET", path, nil, "", ""), 401)
		expect(t, request(h, "GET", path, nil, customer, ""), 401)
		expect(t, request(h, "GET", path+"&format=csv", nil, staff, ""), 400)
	}
	// The existing authenticated session loses access on role revocation.
	f.exec("UPDATE admin_roles SET permissions=ARRAY['dashboard.view'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", owner)
	for _, kind := range []string{"product-ranking", "staff-ranking"} {
		expect(t, request(h, "GET", base+kind+"?month=2024-02", nil, staff, ""), 403)
	}
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	start := time.Date(2024, 2, 1, 0, 0, 0, 0, zone)
	_, productErr := db.ReportProductRanking(t.Context(), owner, start.UTC(), start.AddDate(0, 1, 0).UTC(), 5)
	_, staffErr := db.ReportStaffRanking(t.Context(), owner, start, start.AddDate(0, 1, 0), 5)
	for _, denied := range []error{productErr, staffErr} {
		var authority *store.AccountBusinessError
		if !errors.As(denied, &authority) || authority.Status != 403 {
			t.Fatalf("Store bypassed report authority: %v", denied)
		}
	}
}

func TestDashboardRankingCreditReceiptsCurrentOwnerAndStableOrder(t *testing.T) {
	db, h := testServer(t)
	f := dashboardRankingFixture{t, db, h}
	rootID, root := user(t, db, h, "ranking-root", "super_admin")
	firstOwner, firstCookie := user(t, db, h, "ranking-first-owner", "admin")
	secondOwner, _ := user(t, db, h, "ranking-second-owner", "admin")
	_, _ = user(t, db, h, "ranking-no-receipts", "admin")
	first, _ := user(t, db, h, "ranking-credit-customer", "customer")
	second, _ := user(t, db, h, "ranking-second-customer", "customer")
	unassigned, _ := user(t, db, h, "ranking-unassigned-customer", "customer")
	f.exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", first, firstOwner)
	f.exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", second, secondOwner)
	f.exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", firstOwner)
	p1, p2, p3 := product(t, db, 10), product(t, db, 10), product(t, db, 10)
	_, invoice, _ := f.order(first, "ranking-credit-order", "2024-01-10T00:00:00+08", "paid", []int64{p1}, []int{2}, []int64{300}, 300)
	f.exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key)
 VALUES($1,$2,300,0,1,'ranking-credit-order')`, invoice, first)
	for index, receipt := range []struct {
		amount int64
		date   string
	}{{40, "2024-02-02T00:00:00+08"}, {60, "2024-02-20T00:00:00+08"}, {200, "2024-03-01T00:00:00+08"}} {
		key := fmt.Sprintf("ranking-credit-receipt-%d", index)
		f.exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
 VALUES($1,$2,$3,$4,$5,$6::timestamptz,$5,$7,$8,0)`, invoice, first, rootID, receipt.amount, key, receipt.date, index+1, index+2)
	}
	f.order(second, "ranking-tie", "2024-02-10T00:00:00+08", "paid", []int64{p2}, []int{1}, []int64{100}, 100)
	f.order(unassigned, "ranking-unassigned", "2024-02-10T00:00:00+08", "paid", []int64{p3}, []int{1}, []int64{999}, 999)
	base := "/api/v1/admin/reports/"
	products := f.get(base+"product-ranking?month=2024-02&limit=20", root)["items"].([]any)
	if len(products) != 3 {
		t.Fatalf("credit product missing: %#v", products)
	}
	for _, item := range products {
		row := item.(map[string]any)
		if row["product_id"] == float64(p1) && (row["new_order_amount_cents"] != float64(100) || row["new_quantity"] != float64(2) || row["new_order_count"] != float64(1)) {
			t.Fatalf("credit receipts duplicated units or counted original invoice: %#v", row)
		}
	}
	ranking := f.get(base+"staff-ranking?month=2024-02&limit=1", root)
	rows := ranking["items"].([]any)
	if ranking["total"] != float64(2) || len(rows) != 1 || rows[0].(map[string]any)["sales_owner_id"] != float64(firstOwner) || rows[0].(map[string]any)["amount_cents"] != float64(100) {
		t.Fatalf("ranking tie/limit, unassigned or zero-receipt staff: %#v", ranking)
	}
	if !strings.Contains(ranking["attribution_basis"].(string), "unassigned_excluded") {
		t.Fatalf("missing current-owner basis: %#v", ranking)
	}
	march := f.get(base+"staff-ranking?month=2024-03", root)["items"].([]any)
	if len(march) != 1 || march[0].(map[string]any)["amount_cents"] != float64(200) || march[0].(map[string]any)["invoice_count"] != float64(1) {
		t.Fatalf("credit receipt month changed: %#v", march)
	}
	// A historical invoice status change cannot erase real repayment receipts.
	f.exec("UPDATE invoices SET status='refunded' WHERE id=$1", invoice)
	refunded := f.get(base+"staff-ranking?month=2024-03", root)["items"].([]any)
	if len(refunded) != 1 || refunded[0].(map[string]any)["amount_cents"] != float64(200) {
		t.Fatalf("historical credit refund erased actual receipts: %#v", refunded)
	}
	f.exec("UPDATE users SET sales_owner_id=$2 WHERE id=$1", first, secondOwner)
	after := f.get(base+"staff-ranking?month=2024-02", root)["items"].([]any)
	if len(after) != 1 || after[0].(map[string]any)["sales_owner_id"] != float64(secondOwner) || after[0].(map[string]any)["amount_cents"] != float64(200) || after[0].(map[string]any)["invoice_count"] != float64(2) || after[0].(map[string]any)["customer_count"] != float64(2) {
		t.Fatalf("ranking did not follow current customer owner: %#v", after)
	}
	for _, kind := range []string{"product-ranking", "staff-ranking"} {
		empty := f.get(base+kind+"?month=2024-02", firstCookie)
		if empty["total"] != float64(0) || len(empty["items"].([]any)) != 0 {
			t.Fatalf("moved customer remained in old owner scope: %#v", empty)
		}
	}
	var settlementCount int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sales_settlements").Scan(&settlementCount); err != nil || settlementCount != 0 {
		t.Fatalf("read created commission settlement: count=%d err=%v", settlementCount, err)
	}
}
