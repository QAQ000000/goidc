package httpapi

import (
	"fmt"
	"testing"
)

func TestCustomerRankingRequiresLedgerAndPreservesScope(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "ranking-ledger-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	customer := func(name, status string, owner *int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,status,sales_owner_id)
VALUES($1,$1||'@example.test','unused-fixture-hash',$1,'customer',$2,$3) RETURNING id`, name, status, owner).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	ledger := func(uid, delta int64) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,created_at)
VALUES($1,'adjustment',$2,0,'2020-01-01T00:00:00+08')`, uid, delta); err != nil {
			t.Fatal(err)
		}
	}
	positive := customer("ranking-positive", "active", &staffID)
	zeroNet := customer("ranking-zero-net", "active", &staffID)
	negative := customer("ranking-negative", "active", &staffID)
	for n := 0; n < 12; n++ {
		customer(fmt.Sprintf("ranking-no-ledger-%d", n), "active", &staffID)
	}
	response := request(h, "GET", "/api/v1/admin/reports/customer-ranking", nil, staff, "")
	expect(t, response, 200)
	if len(listData(t, response)) != 0 {
		t.Fatalf("customers without ledger appeared in empty ranking: %s", response.Body.String())
	}
	ledger(positive, 100)
	ledger(zeroNet, 150)
	ledger(zeroNet, -150)
	ledger(negative, -50)
	ledger(customer("ranking-inactive", "disabled", &staffID), 90000)
	ledger(customer("ranking-foreign", "active", nil), 80000)
	// The ranking is all history: period parameters must not silently change it.
	response = request(h, "GET", "/api/v1/admin/reports/customer-ranking?year=2024&month=2", nil, staff, "")
	expect(t, response, 200)
	rows := listData(t, response)
	if len(rows) != 3 {
		t.Fatalf("ranking qualification/scope: %s", response.Body.String())
	}
	for n, want := range []struct {
		id, income, expense, net int64
	}{{positive, 100, 0, 100}, {zeroNet, 150, 150, 0}, {negative, 0, 50, -50}} {
		row := rows[n]
		if row["user_id"] != float64(want.id) || row["income_cents"] != float64(want.income) || row["expense_cents"] != float64(want.expense) || row["net_cents"] != float64(want.net) {
			t.Fatalf("ranking row=%#v want=%+v", row, want)
		}
	}
	var leaders []int64
	for n := 0; n < 12; n++ {
		uid := customer(fmt.Sprintf("ranking-leader-%d", n), "active", &staffID)
		ledger(uid, int64(1000+n))
		leaders = append(leaders, uid)
	}
	response = request(h, "GET", "/api/v1/admin/reports/customer-ranking", nil, staff, "")
	expect(t, response, 200)
	rows = listData(t, response)
	if len(rows) != 10 {
		t.Fatalf("ranking must retain top ten limit: %s", response.Body.String())
	}
	for n, row := range rows {
		if row["user_id"] != float64(leaders[len(leaders)-1-n]) {
			t.Fatalf("ranking order/limit: %s", response.Body.String())
		}
	}
}

func TestDailyReportTicketMetricsCapabilityAndRevocation(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "daily-capability-staff", "admin")
	uid, _ := user(t, db, h, "daily-capability-customer", "customer")
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	var visible, hidden int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Daily visible','daily-visible') RETURNING id").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Daily hidden','daily-hidden') RETURNING id").Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE users SET ticket_scope='departments',customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID)
	exec("INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, visible)
	exec("UPDATE users SET sales_owner_id=$1,created_at='2024-02-01T12:00:00+08' WHERE id=$2", staffID, uid)
	exec(`INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash,created_at)
VALUES('daily-capability-order',$1,'paid',100,'daily-capability-order','fixture','2024-02-01T12:00:00+08')`, uid)
	for n, department := range []int64{visible, hidden} {
		var ticketID int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,created_at)
VALUES($1,$2,'capability test','test',$3,'2024-02-01T12:00:00+08') RETURNING id`, fmt.Sprintf("daily-capability-%d", n), uid, department).Scan(&ticketID); err != nil {
			t.Fatal(err)
		}
		exec(`INSERT INTO ticket_replies(ticket_id,author_id,body,created_at) VALUES($1,$2,'test','2024-02-01T12:00:00+08')`, ticketID, uid)
	}
	assertDaily := func(available bool) {
		t.Helper()
		response := request(h, "GET", "/api/v1/admin/reports/new-clients?year=2024&month=2", nil, staff, "")
		expect(t, response, 200)
		result := data(t, response)
		if result["ticket_metrics_available"] != available {
			t.Fatalf("missing capability metadata: %s", response.Body.String())
		}
		days := result["days"].([]any)
		if len(days) != 29 {
			t.Fatalf("other daily metrics unavailable: %s", response.Body.String())
		}
		for n, value := range days {
			day := value.(map[string]any)
			count := float64(0)
			if n == 0 {
				count = 1
			}
			if day["new_clients"] != count || day["new_orders"] != count || day["paid_orders"] != count {
				t.Fatalf("ticket capability changed commercial metrics: %#v", day)
			}
			for _, field := range []string{"new_tickets", "ticket_replies"} {
				metric, exists := day[field]
				if !exists || (available && metric != count) || (!available && metric != nil) {
					t.Fatalf("ticket metric must distinguish unavailable from zero: %#v", day)
				}
			}
		}
	}
	assertDaily(true)
	expect(t, request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, ""), 200)
	// Reuse the same authenticated session to prove role revocation is immediate.
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	assertDaily(false)
	response := request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, "")
	expect(t, response, 403)
	if errorData(t, response)["code"] != "permission_denied" {
		t.Fatalf("missing explicit ticket capability error: %s", response.Body.String())
	}
	// Other report access stays available; a ticket permission is independent.
	expect(t, request(h, "GET", "/api/v1/admin/reports/sales?year=2024", nil, staff, ""), 200)
	exec("UPDATE admin_roles SET permissions=array_append(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	assertDaily(true)
	expect(t, request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, ""), 200)
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'reports.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	expect(t, request(h, "GET", "/api/v1/admin/reports/new-clients?year=2024&month=2", nil, staff, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, ""), 403)
}
