package httpapi

import (
	"testing"
)

func TestCreditCashReportsReceivedPeriodAndScope(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "cash-report-staff", "admin")
	owner, _ := user(t, db, h, "cash-report-owner", "customer")
	other, _ := user(t, db, h, "cash-report-other", "customer")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, owner)
	for _, uid := range []int64{owner, other} {
		var invoice int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,status,amount_cents,paid_at) VALUES('cash-'||$1::bigint,$1,'paid',1000,'2024-01-10') RETURNING id`, uid).Scan(&invoice); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,idempotency_key) VALUES($1,$2,1000,600,1,'cash-test-key')`, invoice, uid)
		exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after) VALUES($1,$2,$3,400,'cash-receipt-'||$2::bigint,'2024-02-10','repay-'||$2::bigint,1,2,600)`, invoice, uid, staffID)
	}
	response := request(h, "GET", "/api/v1/admin/reports/income?year=2024", nil, staff, "")
	expect(t, response, 200)
	result := data(t, response)
	if result["total"].(map[string]any)["income_cents"] != float64(400) {
		t.Fatal(response.Body.String())
	}
	response = request(h, "GET", "/api/v1/admin/reports/sales?year=2024&month=1", nil, staff, "")
	expect(t, response, 200)
	if len(data(t, response)["items"].([]any)) != 0 {
		t.Fatal("unreceived credit counted", response.Body.String())
	}
	response = request(h, "GET", "/api/v1/admin/reports/sales?year=2024&month=2", nil, staff, "")
	expect(t, response, 200)
	items := data(t, response)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["income_cents"] != float64(400) {
		t.Fatal(response.Body.String())
	}
	exec("UPDATE credit_invoice_payments SET status='repaid',remaining_cents=0")
	response = request(h, "GET", "/api/v1/admin/reports/income?year=2024", nil, staff, "")
	expect(t, response, 200)
	if data(t, response)["total"].(map[string]any)["income_cents"] != float64(400) {
		t.Fatal("repaid state double counted", response.Body.String())
	}
	response = request(h, "GET", "/api/v1/admin/reports/product-income?year=2024", nil, staff, "")
	expect(t, response, 200)
	if len(data(t, response)["products"].([]any)) != 0 {
		t.Fatal("unknown product allocated", response.Body.String())
	}
}
