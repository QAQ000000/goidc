package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"strings"
	"testing"
)

func TestAdminJobsPermissionAndSafeSystemQueue(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "jobs-root", "super_admin")
	id, staff := user(t, db, h, "jobs-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,last_error) VALUES('secret@example.test','SECRET_SUBJECT','SECRET_BODY','system','SECRET_ERROR')`); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/admin/jobs/queue?source=email", nil, root, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 || strings.Contains(w.Body.String(), "SECRET") || strings.Contains(w.Body.String(), "last_error") {
		t.Fatal(w.Body.String())
	}
	for _, suffix := range []string{"", "?source=email", "?q=missing", "?offset=100", "?from=2100-01-01", "?to=2000-01-01"} {
		w = request(h, "GET", "/api/v1/admin/jobs/queue"+suffix, nil, staff, "")
		expect(t, w, 200)
		if len(data(t, w)["items"].([]any)) != 0 {
			t.Fatal(w.Body.String())
		}
	}
	for _, suffix := range []string{"?source=bad", "?status=bad", "?from=bad", "?limit=0", "?offset=-1"} {
		expect(t, request(h, "GET", "/api/v1/admin/jobs/queue"+suffix, nil, root, ""), 400)
	}
	w = request(h, "GET", "/api/v1/admin/jobs", nil, staff, "")
	expect(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 0 {
		t.Fatal(w.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['jobs.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", id); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", "/api/v1/admin/jobs/queue", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 0 {
		t.Fatal(w.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['services.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", id); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/jobs", nil, staff, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/jobs/queue", nil, staff, ""), 403)
	expect(t, request(h, "GET", "/api/v1/admin/logs/jobs", nil, staff, ""), 403)
}

func TestAdminJobsCustomerScope(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scope-jobs-root", "super_admin")
	staffID, staff := user(t, db, h, "scope-jobs-staff", "admin")
	otherID, _ := user(t, db, h, "scope-jobs-other", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true WHERE role='admin'"); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 12)
	for n, owner := range []int64{staffID, otherID} {
		customer, cookie := user(t, db, h, fmt.Sprintf("queue-client-%d", n), "customer")
		salesAssignment(t, h, root, customer, owner, 1)
		v, item := add(t, h, cookie, pid, 1)
		w := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: v, ItemIDs: []int64{item}}, cookie, fmt.Sprintf("queue-order-%d", n))
		expect(t, w, 201)
		invoice := int64(data(t, w)["invoice_id"].(float64))
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", customer), map[string]any{"amount_cents": 2000}, root, fmt.Sprintf("queue-credit-%d", n)), 201)
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoice), map[string]any{}, cookie, fmt.Sprintf("queue-pay-%d", n)), 201)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET last_error='SECRET_PASSWORD',result='{"token":"SECRET_TOKEN"}'`); err != nil {
		t.Fatal(err)
	}
	w := request(h, "GET", "/api/v1/admin/jobs/queue?source=provision&status=pending&q=queue-client&limit=1", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 || strings.Contains(w.Body.String(), "queue-client-1") || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/api/v1/admin/jobs/queue?source=provision&offset=1", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 || len(data(t, w)["items"].([]any)) != 0 {
		t.Fatal(w.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE provision_jobs SET status='failed',completed_at=now()"); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", "/api/v1/admin/logs/jobs?q=queue-client&limit=1", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 || strings.Contains(w.Body.String(), "queue-client-1") || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal(w.Body.String())
	}
	w = request(h, "GET", "/api/v1/admin/logs/jobs?offset=1", nil, staff, "")
	expect(t, w, 200)
	if len(data(t, w)["items"].([]any)) != 0 || data(t, w)["total"].(float64) != 1 {
		t.Fatal(w.Body.String())
	}
	expect(t, request(h, "GET", "/api/v1/admin/logs/jobs?from=bad", nil, staff, ""), 400)

	if _, err := db.Pool.Exec(t.Context(), `WITH inserted AS (INSERT INTO email_outbox(recipient,subject,body,kind) SELECT u.email,'SECRET_SUBJECT','SECRET_BODY','system' FROM services s JOIN users u ON u.id=s.user_id RETURNING id,recipient)
 INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date,email_outbox_id)
 SELECT s.id,u.id,'email','service_expiry_reminder',1,current_date,i.id FROM services s JOIN users u ON u.id=s.user_id JOIN inserted i ON i.recipient=u.email`); err != nil {
		t.Fatal(err)
	}
	w = request(h, "GET", "/api/v1/admin/jobs/queue?source=email", nil, staff, "")
	expect(t, w, 200)
	if data(t, w)["total"].(float64) != 1 || strings.Contains(w.Body.String(), "queue-client-1") || strings.Contains(w.Body.String(), "SECRET") {
		t.Fatal(w.Body.String())
	}
}
