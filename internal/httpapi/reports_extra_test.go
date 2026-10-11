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
	exec("UPDATE users SET customer_scope='owned',customer_ownerless=false,ticket_scope='departments' WHERE id=$1", staffID)
	exec("UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, allowedID)
	for _, path := range []string{"/api/v1/admin/reports/sales?year=2024", "/api/v1/admin/reports/tickets?year=2024&month=2"} {
		expect(t, request(h, "GET", path, nil, customer, ""), 403)
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
	exec("UPDATE admin_roles SET permissions=array_remove(permissions,'tickets.view') WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID)
	w = request(h, "GET", "/api/v1/admin/reports/tickets?year=2024&month=2", nil, staff, "")
	expect(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 0 {
		t.Fatalf("ticket permission: %s", w.Body.String())
	}
}
