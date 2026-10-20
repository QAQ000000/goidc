package httpapi

import (
	"fmt"
	"testing"
	"time"
)

func TestDashboardAggregatesPeriodsAndReviewQueues(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "dashboard-customer", "customer")
	otherID, _ := user(t, db, h, "dashboard-other", "customer")
	staffID, staff := user(t, db, h, "dashboard-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1;`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, uid, staffID); err != nil {
		t.Fatal(err)
	}
	_, admin := user(t, db, h, "dashboard-admin", "super_admin")
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	year := time.Date(now.Year(), 1, 1, 0, 0, 0, 0, now.Location())
	for _, item := range []struct {
		amount int64
		paid   time.Time
	}{{12345, today}, {6789, year.AddDate(-1, 0, 0)}} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(user_id,invoice_no,status,amount_cents,paid_at) VALUES($1,'DASH-'||($2::bigint)::text,'paid',$2::bigint,$3)`, uid, item.amount, item.paid); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoices(user_id,invoice_no,status,amount_cents,paid_at) VALUES($1,'DASH-OTHER','paid',500,$2)`, otherID, today); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,status) VALUES($1,'pending')`, otherID); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []int64{uid, otherID} {
		var accountID int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO affiliate_accounts(user_id,program_id,status) SELECT $1,id,'approved' FROM affiliate_programs ORDER BY id LIMIT 1 RETURNING id`, owner).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		for _, status := range []string{"submitted", "reviewing", "approved", "paid"} {
			if _, err := db.Pool.Exec(t.Context(), `INSERT INTO affiliate_withdrawals(account_id,amount_cents,fee_cents,payout_cents,status,payout_method,payout_target_ciphertext,payout_target_aad,payout_target_masked,idempotency_key) VALUES($1,100,0,100,$2,'bank',decode('01','hex'),'dashboard','masked','dashboard-'||$2::text)`, accountID, status); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO certification_profiles(user_id,status) VALUES($1,'pending')`, uid); err != nil {
		t.Fatal(err)
	}
	for index, item := range []struct {
		status  string
		created time.Time
	}{{"pending", today.AddDate(0, 0, -6)}, {"pending", today.AddDate(0, 0, -7)}, {"paid", today}} {
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO orders(user_id,order_no,status,total_cents,idempotency_key,request_hash,created_at) VALUES($1,'DASH-'||$2::text,$3,12345,'dashboard-'||$2::text,'dashboard',$4)`, uid, fmt.Sprint(index), item.status, item.created); err != nil {
			t.Fatal(err)
		}
	}
	response := request(h, "GET", "/api/v1/admin/dashboard", nil, admin, "")
	expect(t, response, 200)
	result := data(t, response)
	for key, want := range map[string]float64{"today_income_cents": 12845, "month_income_cents": 12845, "annual_income_cents": 12845, "total_income_cents": 19634, "pending_certifications": 2, "pending_withdrawals": 4, "seven_days_pending_orders": 1, "seven_days_paid_orders": 1, "today_paid_orders": 1, "total_orders": 3} {
		if result[key] != want {
			t.Errorf("%s=%v, want %v", key, result[key], want)
		}
	}
	for _, permissions := range [][]string{{"dashboard.view"}, {"dashboard.view", "certifications.view", "affiliates.view", "reports.view"}} {
		if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=$2 WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)`, staffID, permissions); err != nil {
			t.Fatal(err)
		}
		response := request(h, "GET", "/api/v1/admin/dashboard", nil, staff, "")
		expect(t, response, 200)
		result := data(t, response)
		for key, want := range map[string]float64{"customers": 1, "today_income_cents": 12345, "total_income_cents": 19134, "seven_days_pending_orders": 1, "total_orders": 3} {
			if result[key] != want {
				t.Errorf("scoped %s=%v, want %v", key, result[key], want)
			}
		}
		certs, withdrawals := float64(0), float64(0)
		if len(permissions) > 1 {
			certs, withdrawals = 1, 2
		}
		if result["pending_certifications"] != certs || result["pending_withdrawals"] != withdrawals {
			t.Fatalf("permission-scoped queues: %v", result)
		}
		if len(permissions) > 1 {
			annual := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/income?year=%d", now.Year()), nil, staff, "")
			expect(t, annual, 200)
			if total := data(t, annual)["total"].(map[string]any)["income_cents"]; total != result["annual_income_cents"] {
				t.Fatalf("annual report/dashboard mismatch: %v/%v", total, result["annual_income_cents"])
			}
			monthly := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/income?year=%d&month=%d", now.Year(), now.Month()), nil, staff, "")
			expect(t, monthly, 200)
			if total := data(t, monthly)["total"].(map[string]any)["income_cents"]; total != result["month_income_cents"] {
				t.Fatalf("monthly report/dashboard mismatch: %v/%v", total, result["month_income_cents"])
			}
		}
	}
	if items, ok := result["expiring_services"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty expiry list=%v", result["expiring_services"])
	}
}
