package httpapi

import (
	"net/http/httptest"
	"testing"
)

func TestExtraReportValidation(t *testing.T) {
	s := &Server{}
	for _, handler := range []func(*httptest.ResponseRecorder){
		func(w *httptest.ResponseRecorder) { s.salesReport(w, httptest.NewRequest("GET", "/?year=bad", nil)) },
		func(w *httptest.ResponseRecorder) { s.ticketReport(w, httptest.NewRequest("GET", "/?month=13", nil)) },
	} {
		w := httptest.NewRecorder()
		handler(w)
		expect(t, w, 400)
	}
}

func TestExtraReportsScopeAndEmptyContracts(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "reports-staff", "admin")
	_, customer := user(t, db, h, "reports-customer", "customer")
	allowedID, _ := user(t, db, h, "reports-owned", "customer")
	deniedID, _ := user(t, db, h, "reports-other", "customer")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec("UPDATE administrators SET customer_scope='owned',customer_ownerless=false,ticket_scope='departments' WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, allowedID)
	for _, path := range []string{"/api/v1/admin/reports/sales?year=2024", "/api/v1/admin/reports/tickets?year=2024&month=2"} {
		expect(t, request(h, "GET", path, nil, customer, ""), 401)
	}
	salesPath := "/api/v1/admin/reports/sales?year=2024"
	w := request(h, "GET", salesPath, nil, staff, "")
	expect(t, w, 200)
	if items, ok := data(t, w)["items"].([]any); !ok || len(items) != 0 {
		t.Fatalf("empty items: %s", w.Body.String())
	}
	// Payments belong to the payment period even if their order predates it.
	exec(`INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,created_at) VALUES('old-owned',$1,123,'old-owned','test','2023-12-01'),('old-other',$2,999,'old-other','test','2023-12-01')`, allowedID, deniedID)
	exec(`INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) SELECT order_no,user_id,id,'paid',total_cents,'2023-12-31 16:00:00+00' FROM orders`)
	w = request(h, "GET", salesPath, nil, staff, "")
	expect(t, w, 200)
	items := data(t, w)["items"].([]any)
	if len(items) != 1 {
		t.Fatalf("scope: %s", w.Body.String())
	}
	item := items[0].(map[string]any)
	if item["income_cents"] != float64(123) || item["order_count"] != float64(0) || item["sales_owner_id"] != float64(staffID) {
		t.Fatalf("sales totals: %#v", item)
	}
	var dept int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Reports','reports') RETURNING id").Scan(&dept); err != nil {
		t.Fatal(err)
	}
	exec("INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, dept)
	exec(`INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,created_at) VALUES('report-visible',$1,'test','test',$2,'2024-01-31 16:00:00+00'),('report-hidden',$1,'test','test',NULL,'2024-02-01 00:00:00+00')`, allowedID, dept)
	w = request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, "")
	expect(t, w, 200)
	days := data(t, w)["items"].([]any)
	if len(days) != 29 {
		t.Fatalf("leap calendar: %s", w.Body.String())
	}
	first := days[0].(map[string]any)
	if first["date"] != "2024-02-01" || first["ticket_count"] != float64(1) || first["open_count"] != float64(1) {
		t.Fatalf("ticket scope/timezone: %#v", first)
	}
	// Report visibility does not imply access to ticket totals.
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
	w = request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, "")
	expect(t, w, 403)
	if errorData(t, w)["code"] != "permission_denied" {
		t.Fatalf("ticket permission: %s", w.Body.String())
	}
}

func TestTicketReportRetainsMergedSourceHistory(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "ticket-report-root", "super_admin")
	staffID, staff := user(t, db, h, "ticket-report-staff", "admin")
	customerID, _ := user(t, db, h, "ticket-report-customer", "customer")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	var visible, hidden int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Visible','ticket-report-visible') RETURNING id").Scan(&visible); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('Hidden','ticket-report-hidden') RETURNING id").Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	exec("UPDATE administrators SET ticket_scope='departments' WHERE id=$1", staffID)
	exec("INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, visible)
	ticket := func(number, date string, department int64) int64 {
		t.Helper()
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO tickets(ticket_no,user_id,subject,body,department_id,created_at)
VALUES($1,$2,'Report history','test',$3,$4::timestamptz) RETURNING id`, number, customerID, department, date).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	target := ticket("report-target", "2024-02-01 08:00:00+08", visible)
	source := ticket("report-source", "2024-02-02 08:00:00+08", visible)
	hiddenSource := ticket("report-hidden-source", "2024-02-03 08:00:00+08", hidden)
	assertDay := func(day int, created, open int64) {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, "")
		expect(t, w, 200)
		days := data(t, w)["items"].([]any)
		if len(days) != 29 {
			t.Fatalf("leap calendar: %s", w.Body.String())
		}
		item := days[day-1].(map[string]any)
		if item["ticket_count"] != float64(created) || item["open_count"] != float64(open) {
			t.Fatalf("day %d: want created=%d open=%d, got %#v", day, created, open, item)
		}
	}
	assertDay(1, 1, 1)
	assertDay(2, 1, 1)
	assertDay(3, 0, 0)

	// Merging closes the sources but must not erase their creation history.
	expect(t, request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{target, source, hiddenSource}}, root, ""), 200)
	assertDay(1, 1, 1)
	assertDay(2, 1, 0)
	assertDay(3, 0, 0)

	// Even historical sources use their current department for report visibility.
	exec("UPDATE tickets SET department_id=$2 WHERE id=$1", source, hidden)
	exec("UPDATE tickets SET department_id=$2 WHERE id=$1", hiddenSource, visible)
	assertDay(2, 0, 0)
	assertDay(3, 1, 0)
	// A merged source can never become a second actionable ticket in the report.
	exec("UPDATE tickets SET status='open' WHERE id=$1", hiddenSource)
	assertDay(3, 1, 0)
}
