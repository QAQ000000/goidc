package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestProductIncomeReportsDiscountedCheckoutNet(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "net-report-customer", "customer")
	adminID, admin := user(t, db, h, "net-report-admin", "super_admin")
	pid := product(t, db, 2)
	expect(t, request(h, "POST", "/api/v1/admin/promo-codes", map[string]any{
		"code": "REPORT10", "discount_type": "percent", "value": 10,
	}, admin, ""), 201)
	version, item := add(t, h, customer, pid, 1)
	promo := request(h, "POST", "/api/v1/cart/promo", map[string]any{"version": version, "code": "REPORT10"}, customer, "")
	expect(t, promo, 200)
	version = int64(data(t, promo)["version"].(float64))
	checkout := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, customer, "net-report-order")
	expect(t, checkout, 201)
	result := data(t, checkout)
	if result["total_cents"] != float64(1200) || result["discount_cents"] != float64(133) {
		t.Fatalf("checkout did not preserve discounted net: %s", checkout.Body.String())
	}
	invoice := int64(result["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 2000}, admin, "net-report-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, customer, "net-report-pay"), 201)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE invoices SET paid_at='2024-02-15T00:00:00+08' WHERE id=$1", invoice); err != nil {
		t.Fatal(err)
	}
	response := request(h, "GET", "/api/v1/admin/reports/product-income?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	rows := data(t, response)["products"].([]any)
	if len(rows) != 1 {
		t.Fatalf("product rows: %s", response.Body.String())
	}
	row := rows[0].(map[string]any)
	if row["product_id"] != float64(pid) || row["new_order_amount_cents"] != float64(1200) || row["total_amount_cents"] != float64(1200) || row["new_order_count"] != float64(1) {
		t.Fatalf("product report used gross instead of paid net: %s", response.Body.String())
	}
	// Renewal invoice lines and received credit remain separate cash sources;
	// the paid credit invoice must not also count as an ordinary new purchase.
	var serviceID, creditInvoice int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM services WHERE order_id=$1", int64(result["order_id"].(float64))).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `WITH created AS (
INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('net-report-renewal',$1,'paid',200,'2024-02-15T00:00:00+08') RETURNING id
) INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) SELECT id,'renewal','renew',200,$2 FROM created`, uid, serviceID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
VALUES('net-report-credit',$1,'paid',500,'2024-02-15T00:00:00+08') RETURNING id`, uid).Scan(&creditInvoice); err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id) VALUES($1,'credit purchase','service',500,$2)`,
		`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) SELECT $1,user_id,500,100,1,'net-report-credit' FROM services WHERE id=$2`,
	} {
		if _, err := db.Pool.Exec(t.Context(), statement, creditInvoice, serviceID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
VALUES($1,$2,$3,400,'net-report-credit-receipt','2024-02-15T00:00:00+08','net-report-credit-repay',1,2,100)`, creditInvoice, uid, adminID); err != nil {
		t.Fatal(err)
	}
	response = request(h, "GET", "/api/v1/admin/reports/product-income?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	rows = data(t, response)["products"].([]any)
	if len(rows) != 1 {
		t.Fatalf("product rows after renewal/credit: %s", response.Body.String())
	}
	row = rows[0].(map[string]any)
	if row["new_order_amount_cents"] != float64(1600) || row["new_order_count"] != float64(2) || row["renewal_amount_cents"] != float64(200) || row["renewal_order_count"] != float64(1) || row["total_amount_cents"] != float64(1800) {
		t.Fatalf("renewal/received credit cash changed: %s", response.Body.String())
	}
}

func TestProductIncomeAllocatesNetAcrossOrderLines(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "allocation-report-customer", "customer")
	_, admin := user(t, db, h, "allocation-report-admin", "super_admin")
	type line struct {
		product, amount int64
	}
	wantAmounts := map[int64]int64{}
	wantCounts := map[int64]int64{}
	for index, tc := range []struct {
		name           string
		gross          []int64
		net            int64
		productIndexes []int
		want           []int64
	}{
		// A product can appear on several lines, but its order count remains one.
		{"penny_remainder", []int64{100, 100, 100, 0}, 298, []int{0, 1, 0, 2}, []int64{199, 99, 0}},
		{"fully_discounted", []int64{100, 200}, 0, []int{0, 1}, []int64{0, 0}},
		{"zero_gross", []int64{0, 0}, 0, []int{0, 1}, []int64{0, 0}},
		// Multiplication exceeds bigint even though the individual amounts do not.
		{"large_amounts", []int64{4000000000000, 4000000000000}, 7999999999999, []int{0, 1}, []int64{3999999999999, 4000000000000}},
	} {
		products := make([]int64, len(tc.want))
		for n, amount := range tc.want {
			products[n] = product(t, db, 10)
			wantAmounts[products[n]], wantCounts[products[n]] = amount, 1
		}
		lines := make([]line, len(tc.gross))
		var gross int64
		for n, amount := range tc.gross {
			lines[n] = line{products[tc.productIndexes[n]], amount}
			gross += amount
		}
		var order int64
		key := fmt.Sprintf("allocation-report-%d", index)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,discount_cents,idempotency_key,request_hash)
VALUES($1,$2,'paid',$3,$4,$1,'fixture') RETURNING id`, key, uid, tc.net, gross-tc.net).Scan(&order); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		for _, item := range lines {
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,$3,'monthly',1,$4,0,$4)`, order, item.product, tc.name, item.amount); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at)
VALUES($1,$2,$3,'paid',$4,'2024-02-15T00:00:00+08')`, key, uid, order, tc.net); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
	response := request(h, "GET", "/api/v1/admin/reports/product-income?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	rows := data(t, response)["products"].([]any)
	if len(rows) != len(wantAmounts) {
		t.Fatalf("product rows=%d want %d: %s", len(rows), len(wantAmounts), response.Body.String())
	}
	var total int64
	for _, value := range rows {
		row := value.(map[string]any)
		pid := int64(row["product_id"].(float64))
		want, ok := wantAmounts[pid]
		if !ok || row["new_order_amount_cents"] != float64(want) || row["total_amount_cents"] != float64(want) || row["new_order_count"] != float64(wantCounts[pid]) {
			t.Fatalf("incorrect net allocation for product %d: %#v; want amount=%d count=%d", pid, row, want, wantCounts[pid])
		}
		total += int64(row["total_amount_cents"].(float64))
	}
	if total != 8000000000297 {
		t.Fatalf("allocation lost cents: total=%d", total)
	}
}

func TestProductIncomeAllocatesBigintNetExactly(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "exact-net-customer", "customer")
	_, admin := user(t, db, h, "exact-net-admin", "super_admin")
	first, second := product(t, db, 10), product(t, db, 10)
	const grossLine int64 = 4000000000000000000
	const paidNet int64 = 7999999999999999999
	var order int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,discount_cents,idempotency_key,request_hash)
VALUES('exact-net-order',$1,'paid',$2,1,'exact-net-order','fixture') RETURNING id`, uid, paidNet).Scan(&order); err != nil {
		t.Fatal(err)
	}
	for _, pid := range []int64{first, second} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'Exact cents','monthly',1,$3,0,$3)`, order, pid, grossLine); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at)
VALUES('exact-net-invoice',$1,$2,'paid',$3,'2024-02-15T00:00:00+08')`, uid, order, paidNet); err != nil {
		t.Fatal(err)
	}
	response := request(h, "GET", "/api/v1/admin/reports/product-income?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	// Decode integer fields directly: float64 would hide a one-cent error here.
	var result struct {
		Data struct {
			Products []struct {
				ProductID           int64 `json:"product_id"`
				NewOrderAmountCents int64 `json:"new_order_amount_cents"`
				TotalAmountCents    int64 `json:"total_amount_cents"`
			} `json:"products"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := map[int64]int64{first: grossLine - 1, second: grossLine}
	if len(result.Data.Products) != len(want) {
		t.Fatalf("product rows: %s", response.Body.String())
	}
	for _, row := range result.Data.Products {
		amount, ok := want[row.ProductID]
		if !ok || row.NewOrderAmountCents != amount || row.TotalAmountCents != amount {
			t.Fatalf("inexact large net allocation: %+v; want amount=%d", row, amount)
		}
	}
}

func TestIncomeReportCountsInvoicesOnceAcrossRepaymentMonths(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "distinct-report-staff", "admin")
	allowed, _ := user(t, db, h, "distinct-report-allowed", "customer")
	denied, _ := user(t, db, h, "distinct-report-denied", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, allowed); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []int64{allowed, denied} {
		var invoice int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
VALUES('distinct-credit-'||$1::bigint,$1,'paid',1000,'2023-12-01T00:00:00+08') RETURNING id`, uid).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key)
VALUES($1,$2,1000,300,1,'distinct-credit-key')`, invoice, uid); err != nil {
			t.Fatal(err)
		}
		for index, payment := range []struct {
			month  int
			amount int64
		}{{1, 100}, {1, 200}, {2, 400}} {
			key := fmt.Sprintf("distinct-%d-%d", uid, index)
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
VALUES($1,$2,$3,$4,$5,$6::timestamptz,$5,1,2,300)`, invoice, uid, staffID, payment.amount, key, fmt.Sprintf("2024-%02d-10T00:00:00+08", payment.month)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at)
VALUES('distinct-cash',$1,'paid',200,'2024-02-20T00:00:00+08'),('distinct-outside',$1,'paid',999,'2025-01-01T00:00:00+08')`, allowed); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path    string
		income  float64
		count   float64
		monthly map[string]float64
	}{
		{"/api/v1/admin/reports/income?year=2024", 900, 2, map[string]float64{"2024-01": 1, "2024-02": 2}},
		{"/api/v1/admin/reports/income?year=2024&month=1", 300, 1, map[string]float64{"2024-01": 1}},
		{"/api/v1/admin/reports/income?year=2024&month=3", 0, 0, map[string]float64{}},
	} {
		response := request(h, "GET", tc.path, nil, staff, "")
		expect(t, response, 200)
		result := data(t, response)
		total := result["total"].(map[string]any)
		if total["income_cents"] != tc.income || total["invoice_count"] != tc.count {
			t.Fatalf("income/distinct total: %s", response.Body.String())
		}
		months := result["months"].([]any)
		if len(months) != len(tc.monthly) {
			t.Fatalf("monthly count: %s", response.Body.String())
		}
		for _, value := range months {
			month := value.(map[string]any)
			if want, ok := tc.monthly[month["month"].(string)]; !ok || month["invoice_count"] != want {
				t.Fatalf("monthly distinct count: %s", response.Body.String())
			}
		}
	}
}

func TestNewClientsReportUsesShanghaiCalendarInUTCSession(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "utc-calendar-admin", "super_admin")
	// Replace the fixture pool so every handler query uses UTC, including fresh
	// connections. SET on one pooled connection would not prove this boundary.
	cfg := db.Pool.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	db.Pool.Close()
	db.Pool = pool
	var timezone string
	if err := db.Pool.QueryRow(t.Context(), "SHOW timezone").Scan(&timezone); err != nil || timezone != "UTC" {
		t.Fatalf("test session timezone=%q err=%v", timezone, err)
	}
	for index, timestamp := range []string{
		"2024-01-31T15:59:59Z", // Immediately before Shanghai February.
		"2024-01-31T16:00:00Z", // First second of Shanghai February.
		"2024-02-29T15:59:59Z", // Last second of Shanghai February.
		"2024-02-29T16:00:00Z", // Immediately after Shanghai February.
	} {
		name := fmt.Sprintf("utc-calendar-%d", index)
		uid, _ := user(t, db, h, name, "customer")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET created_at=$2::timestamptz WHERE id=$1", uid, timestamp); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,created_at)
VALUES($1,$2,'paid',100,$1,'fixture',$3::timestamptz)`, name, uid, timestamp); err != nil {
			t.Fatal(err)
		}
	}
	response := request(h, "GET", "/api/v1/admin/reports/new-clients?year=2024&month=2", nil, admin, "")
	expect(t, response, 200)
	days := data(t, response)["days"].([]any)
	if len(days) != 29 {
		t.Fatalf("Shanghai leap-month calendar: %s", response.Body.String())
	}
	var newClients, newOrders, paidOrders float64
	for index, value := range days {
		day := value.(map[string]any)
		wantDate := fmt.Sprintf("2024-02-%02d", index+1)
		wantCount := float64(0)
		if index == 0 || index == 28 {
			wantCount = 1
		}
		if day["date"] != wantDate || day["new_clients"] != wantCount || day["new_orders"] != wantCount || day["paid_orders"] != wantCount {
			t.Fatalf("UTC session changed Shanghai day/range: %#v; want date=%s count=%.0f", day, wantDate, wantCount)
		}
		newClients += day["new_clients"].(float64)
		newOrders += day["new_orders"].(float64)
		paidOrders += day["paid_orders"].(float64)
	}
	if newClients != 2 || newOrders != 2 || paidOrders != 2 {
		t.Fatalf("month boundaries included/excluded wrong records: clients=%.0f orders=%.0f paid=%.0f", newClients, newOrders, paidOrders)
	}
}
