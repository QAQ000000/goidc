package httpapi

import (
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSalesReportDetailsValidation(t *testing.T) {
	for _, query := range []string{"sales_owner_id=-1", "customer_id=0", "product_id=x", "kind=unknown", "condition=unknown", "preset=unknown", "from=2024-03-01&to=2024-02-01", "from=no", "to=no", "q=" + strings.Repeat("x", 121)} {
		if _, err := parseSalesDetailFilter(httptest.NewRequest("GET", "/?year=2024&month=2&"+query, nil)); err == nil {
			t.Fatalf("accepted %s", query)
		}
	}
}

func TestSalesReportDetailsAmountsScopeAndSnapshot(t *testing.T) {
	db, h := testServer(t)
	owner, staff := user(t, db, h, "detail-sales", "admin")
	otherOwner, _ := user(t, db, h, "detail-hidden-sales", "admin")
	customer, customerCookie := user(t, db, h, "detail-customer", "customer")
	hidden, _ := user(t, db, h, "detail-hidden-customer", "customer")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var group int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps) VALUES('detail',1000,2000,3000) RETURNING id`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET customer_scope='owned',customer_ownerless=false,sales_enabled=true,sales_commission_group_id=$2 WHERE id=$1`, owner, group)
	exec(`UPDATE users SET sales_owner_id=$2 WHERE id=$1`, customer, owner)
	exec(`UPDATE users SET sales_owner_id=$2 WHERE id=$1`, hidden, otherOwner)
	p1, p2 := product(t, db, 20), product(t, db, 20)
	var order, invoice int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('detail-order',$1,'paid',101,'detail','detail') RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Historical A','monthly',1,100,0,100),($1,$3,'Historical B','monthly',1,200,0,200)`, order, p1, p2)
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) VALUES('DETAIL-101',$1,$2,'paid',101,'2024-02-01 00:00:00+08') RETURNING id`, customer, order).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('HIDDEN',$1,'paid',9999,'2024-02-10')`, hidden)
	exec(`INSERT INTO sales_commission_tiers(threshold_cents,extra_rate_bps) VALUES(100,500)`)
	exec(`INSERT INTO sales_settlements(month,actor_id) VALUES('2024-02',$1)`, owner)
	exec(`INSERT INTO sales_settlement_items(settlement_id,order_id,invoice_id,customer_id,sales_owner_id,snapshot,commission_cents) SELECT id,$2,$3,$4,$1,'{"original_rule":true,"monthly_turnover_cents":99999999}',7 FROM sales_settlements`, otherOwner, order, invoice, customer)
	path := "/api/v1/admin/reports/sales/details?year=2024&month=2"
	expect(t, request(h, "GET", path, nil, customerCookie, ""), 403)
	get := func(path string) map[string]any {
		t.Helper()
		w := request(h, "GET", path, nil, staff, "")
		expect(t, w, 200)
		return data(t, w)
	}
	result := get(path)
	if result["total"] != float64(1) {
		t.Fatalf("scope: %#v", result)
	}
	items := result["items"].([]any)
	item := items[0].(map[string]any)
	lines := item["items"].([]any)
	if len(lines) != 2 || item["amount_cents"] != float64(101) || item["estimated_commission_cents"] != float64(15) || item["confirmed_commission_cents"] != float64(7) || item["confirmed_sales_owner_id"] != float64(otherOwner) {
		t.Fatalf("invoice: %#v", item)
	}
	if _, leaked := item["confirmation_snapshot"].(map[string]any)["monthly_turnover_cents"]; leaked {
		t.Fatalf("out of scope turnover leaked: %#v", item)
	}
	if lines[0].(map[string]any)["amount_cents"] != float64(33) || lines[1].(map[string]any)["amount_cents"] != float64(68) {
		t.Fatalf("allocation: %#v", lines)
	}
	selected := result["statistics"].(map[string]any)["selected"].(map[string]any)
	if selected["amount_cents"] != float64(101) || selected["invoice_count"] != float64(1) || selected["confirmed_commission_cents"] != float64(7) {
		t.Fatalf("summary: %#v", selected)
	}
	trend := result["trend"].([]any)
	if len(trend) != 29 || trend[0].(map[string]any)["date"] != "2024-02-01" || trend[0].(map[string]any)["amount_cents"] != float64(101) || trend[28].(map[string]any)["amount_cents"] != float64(0) {
		t.Fatalf("trend: %#v", trend)
	}
	filtered := get(path + fmt.Sprintf("&product_id=%d", p2))["items"].([]any)[0].(map[string]any)
	if filtered["amount_cents"] != float64(68) || filtered["confirmed_commission_cents"] != float64(5) || len(filtered["items"].([]any)) != 1 {
		t.Fatalf("filter reallocates: %#v", filtered)
	}
	if tail := get(path + "&offset=10&limit=1"); tail["total"] != float64(1) || len(tail["items"].([]any)) != 0 {
		t.Fatalf("empty tail: %#v", tail)
	}
	options := get("/api/v1/admin/reports/sales/options")["staff"].([]any)
	if len(options) != 1 || options[0].(map[string]any)["id"] != float64(owner) {
		t.Fatalf("staff leak: %#v", options)
	}
	exec(`UPDATE sales_commission_groups SET purchase_rate_bps=2000,version=version+1 WHERE id=$1`, group)
	changed := get(path)["items"].([]any)[0].(map[string]any)
	if changed["estimated_commission_cents"] != float64(25) || changed["confirmed_commission_cents"] != float64(7) || changed["confirmation_snapshot"].(map[string]any)["original_rule"] != true {
		t.Fatalf("snapshot changed: %#v", changed)
	}
	removedExport := request(h, "GET", path+"&format=csv", nil, staff, "")
	expect(t, removedExport, 400)
	if errorData(t, removedExport)["code"] != "invalid_report_format" || removedExport.Header().Get("Content-Disposition") != "" {
		t.Fatalf("removed export returned a download: %s", removedExport.Body.String())
	}
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,101,0,1,'snapshot-credit')`, invoice, customer)
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,40,'snapshot-first','2024-02-02','snapshot-first',1,2,61),($1,$2,$3,61,'snapshot-last','2024-03-02','snapshot-last',2,3,0)`, invoice, customer, owner)
	full := get("/api/v1/admin/reports/sales/details?from=2024-02-01&to=2024-03-31")
	fullInvoice := full["items"].([]any)[0].(map[string]any)
	if fullInvoice["amount_cents"] != float64(101) || fullInvoice["confirmed_commission_cents"] != float64(7) || full["statistics"].(map[string]any)["selected"].(map[string]any)["confirmed_commission_cents"] != float64(7) {
		t.Fatalf("credit duplicates confirmation: %#v", full)
	}
	exec(`UPDATE invoices SET status='refunded' WHERE id=$1`, invoice)
	reversed := get(path)["items"].([]any)[0].(map[string]any)
	if reversed["estimated_commission_cents"] != float64(0) || reversed["confirmed_commission_cents"] != float64(0) || reversed["confirmed_original_cents"] != float64(7) || reversed["confirmation_status"] != "reversed" {
		t.Fatalf("reversal: %#v", reversed)
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sales_settlements").Scan(&count); err != nil || count != 1 {
		t.Fatalf("read creates settlement: %d %v", count, err)
	}
}

func TestSalesReportDetailsCreditUnconfiguredAndRenewal(t *testing.T) {
	db, h := testServer(t)
	owner, root := user(t, db, h, "detail-credit-owner", "super_admin")
	customer, _ := user(t, db, h, "detail-credit-customer", "customer")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`UPDATE users SET sales_owner_id=$2 WHERE id=$1`, customer, owner)
	p := product(t, db, 10)
	var service int64
	if err := db.Pool.QueryRow(t.Context(), `WITH o AS (INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('detail-service-order',$1,1001,'service','service') RETURNING id), l AS (INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) SELECT id,$2,'Service','monthly',1,1001,0,1001 FROM o RETURNING id,order_id) INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status) SELECT $1,order_id,id,$2,'Service','detail-service','monthly','active' FROM l RETURNING id`, customer, p).Scan(&service); err != nil {
		t.Fatal(err)
	}
	var invoice int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('DETAIL-CREDIT',$1,'paid',1001,'2024-01-20') RETURNING id`, customer).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'Renew','renew',600,$2),($1,'Upgrade','upgrade',401,$2)`, invoice, service)
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,1001,0,1,'detail-credit')`, invoice, customer)
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,400,'detail-first','2024-02-01 00:00:00+08','detail-first',1,2,601),($1,$2,$3,601,'detail-last','2024-03-01 00:00:00+08','detail-last',2,3,0)`, invoice, customer, owner)
	get := func(query string) map[string]any {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/reports/sales/details?"+query, nil, root, "")
		expect(t, w, 200)
		return data(t, w)
	}
	if jan := get("year=2024&month=1"); jan["total"] != float64(0) {
		t.Fatalf("credit authorization counted: %#v", jan)
	}
	feb := get("year=2024&month=2")
	item := feb["items"].([]any)[0].(map[string]any)
	if item["amount_cents"] != float64(400) || item["estimated_commission_cents"] != nil || item["unconfigured_lines"] != float64(2) {
		t.Fatalf("unconfigured: %#v", item)
	}
	var group int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO sales_commission_groups(name,purchase_rate_bps,renewal_rate_bps,upgrade_rate_bps,include_upgrade) VALUES('credit-group',1000,2000,3000,false) RETURNING id`).Scan(&group); err != nil {
		t.Fatal(err)
	}
	exec(`UPDATE users SET sales_enabled=true,sales_commission_group_id=$2 WHERE id=$1`, owner, group)
	feb = get("year=2024&month=2&condition=credit")
	item = feb["items"].([]any)[0].(map[string]any)
	if item["estimated_commission_cents"] != float64(48) {
		t.Fatalf("renew/disabled upgrade: %#v", item)
	}
	all := get("from=2024-02-01&to=2024-03-31")
	if all["total"] != float64(1) || all["items"].([]any)[0].(map[string]any)["amount_cents"] != float64(1001) {
		t.Fatalf("credit split/unique: %#v", all)
	}
	if only := get("year=2024&month=2&kind=upgrade"); only["items"].([]any)[0].(map[string]any)["estimated_commission_cents"] != float64(0) {
		t.Fatalf("disabled != zero: %#v", only)
	}
	if none := get("year=2024&month=2&condition=unconfigured"); none["total"] != float64(0) {
		t.Fatalf("configured condition: %#v", none)
	}
	exec(`INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('DETAIL-ADJUSTMENT',$1,'paid',80,'2024-04-02'),('DETAIL-ZERO-WEIGHT',$1,'paid',50,'2024-04-03')`, customer)
	exec(`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) SELECT id,'Positive','renew',100,$1::bigint FROM invoices WHERE invoice_no='DETAIL-ADJUSTMENT' UNION ALL SELECT id,'Discount','discount',-20,NULL FROM invoices WHERE invoice_no='DETAIL-ADJUSTMENT' UNION ALL SELECT id,'Legacy zero','other',0,NULL FROM invoices WHERE invoice_no='DETAIL-ZERO-WEIGHT'`, service)
	april := get("year=2024&month=4")
	if april["total"] != float64(2) || april["statistics"].(map[string]any)["selected"].(map[string]any)["amount_cents"] != float64(130) {
		t.Fatalf("negative adjustment loses receipts: %#v", april)
	}
	for _, value := range april["items"].([]any) {
		item := value.(map[string]any)
		for _, value := range item["items"].([]any) {
			if value.(map[string]any)["amount_cents"].(float64) < 0 {
				t.Fatalf("negative allocated income: %#v", item)
			}
		}
	}
}
