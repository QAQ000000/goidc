package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminTicketCreateOptionsAndPagedList(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "admin-ticket-options-root", "super_admin")
	viewerID, viewer := user(t, db, h, "admin-ticket-options-viewer", "admin")
	_, customer := user(t, db, h, "admin-ticket-options-customer", "customer")
	contentRole(t, db, viewerID, "tickets.view")
	var departmentID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('选项测试部门','admin-ticket-options') RETURNING id`).Scan(&departmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET display_name='分页测试客户' WHERE email=(SELECT email FROM users WHERE username='admin-ticket-options-customer')`); err != nil {
		t.Fatal(err)
	}
	created := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "分页目标工单", "body": "测试正文", "priority": "high", "department_id": departmentID}, customer, "")
	expect(t, created, 201)
	created2 := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "分页第二工单", "body": "测试正文", "priority": "normal", "department_id": departmentID}, customer, "")
	expect(t, created2, 201)

	options := request(h, "GET", "/api/v1/admin/tickets/create-options", nil, root, "")
	expect(t, options, 200)
	if got := data(t, options)["priorities"].([]any); len(got) != 4 {
		t.Fatalf("priority options=%#v", got)
	}
	customers := request(h, "GET", "/api/v1/admin/tickets/create-options?kind=customers&q=分页测试客户", nil, root, "")
	expect(t, customers, 200)
	if page := data(t, customers); page["total"] != float64(1) || page["items"].([]any)[0].(map[string]any)["name"] != "分页测试客户" {
		t.Fatalf("customer options did not search real customers: %#v", page)
	}
	departments := request(h, "GET", "/api/v1/admin/tickets/create-options?kind=departments", nil, root, "")
	expect(t, departments, 200)
	if page := data(t, departments); page["total"] != float64(1) || page["items"].([]any)[0].(map[string]any)["id"] != float64(departmentID) {
		t.Fatalf("department options did not return active departments: %#v", page)
	}
	assignees := request(h, "GET", fmt.Sprintf("/api/v1/admin/tickets/create-options?kind=assignees&department_id=%d", departmentID), nil, root, "")
	expect(t, assignees, 200)
	if data(t, assignees)["total"] != float64(1) {
		t.Fatalf("assignee options should include active root and ticket staff: %#v", data(t, assignees))
	}
	page := request(h, "GET", "/api/v1/admin/tickets/page?q=分页目标&priority=high&limit=1&offset=0", nil, root, "")
	expect(t, page, 200)
	pageData := data(t, page)
	items := pageData["items"].([]any)
	if pageData["total"] != float64(1) || pageData["limit"] != float64(1) || pageData["offset"] != float64(0) || len(items) != 1 || items[0].(map[string]any)["subject"] != "分页目标工单" {
		t.Fatalf("unexpected ticket page: %#v", pageData)
	}
	for _, query := range []string{"?status=unknown", "?priority=unknown", "?customer_id=0", "?department_id=invalid"} {
		expect(t, request(h, "GET", "/api/v1/admin/tickets/page"+query, nil, root, ""), 400)
	}
	expect(t, request(h, "GET", "/api/v1/admin/tickets/create-options", nil, viewer, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/tickets", map[string]any{}, viewer, "admin-ticket-viewer-deny"), 403)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE ticket_departments SET active=false WHERE id=$1`, departmentID); err != nil {
		t.Fatal(err)
	}
	departments = request(h, "GET", "/api/v1/admin/tickets/create-options?kind=departments", nil, root, "")
	expect(t, departments, 200)
	if data(t, departments)["total"] != float64(0) {
		t.Fatalf("inactive department offered for creation: %#v", data(t, departments))
	}
}

func TestAdminTicketCreateIdempotencyScopesAndEmailQueue(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "admin-ticket-create-root", "super_admin")
	staffID, staff := user(t, db, h, "admin-ticket-create-staff", "admin")
	customerID, _ := user(t, db, h, "admin-ticket-create-customer", "customer")
	otherID, _ := user(t, db, h, "admin-ticket-create-other", "customer")
	var departmentID, otherDepartmentID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('创建测试部门','admin-ticket-create') RETURNING id`).Scan(&departmentID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO ticket_departments(name,slug) VALUES('无权部门','admin-ticket-other') RETURNING id`).Scan(&otherDepartmentID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true WHERE id=$1`, customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled)
VALUES('ticket-test-mail','mail','smtp','工单邮件测试',true)`); err != nil {
		t.Fatal(err)
	}
	input := map[string]any{"customer_id": customerID, "department_id": departmentID, "subject": "后台创建的工单", "body": "需要客服处理", "priority": "urgent", "cc": []string{"copy@example.test"}, "send_email": true}
	created := request(h, "POST", "/api/v1/admin/tickets", input, root, "admin-ticket-create-key")
	expect(t, created, 201)
	result := data(t, created)
	if result["version"] != float64(1) || result["email_status"] != "queued" || result["replayed"] != false {
		t.Fatalf("unexpected create result: %#v", result)
	}
	ticketID := int64(result["id"].(float64))
	replay := request(h, "POST", "/api/v1/admin/tickets", input, root, "admin-ticket-create-key")
	expect(t, replay, 200)
	if data(t, replay)["id"] != result["id"] || data(t, replay)["replayed"] != true {
		t.Fatalf("idempotent retry did not return the existing ticket: %#v", data(t, replay))
	}
	changed := map[string]any{"customer_id": customerID, "department_id": departmentID, "subject": "相同键修改正文", "body": "不同载荷", "priority": "urgent", "cc": []string{"copy@example.test"}, "send_email": true}
	expect(t, request(h, "POST", "/api/v1/admin/tickets", changed, root, "admin-ticket-create-key"), 409)
	var status, emailStatus string
	var createdBy int64
	var cc []byte
	var ticketCount, receiptCount, auditCount, outboxCount int
	err := db.Pool.QueryRow(t.Context(), `SELECT t.status,t.initial_email_status,t.created_by,t.cc,
(SELECT count(*) FROM tickets WHERE user_id=$1 AND subject='后台创建的工单'),
(SELECT count(*) FROM admin_ticket_creation_receipts WHERE ticket_id=t.id),
(SELECT count(*) FROM audit_events WHERE action='ticket.admin_created' AND entity_id=t.id::text),
(SELECT count(*) FROM email_outbox WHERE kind='ticket_created' AND (recipient='admin-ticket-create-customer@example.test' OR recipient='copy@example.test'))
FROM tickets t WHERE t.id=$2`, customerID, ticketID).Scan(&status, &emailStatus, &createdBy, &cc, &ticketCount, &receiptCount, &auditCount, &outboxCount)
	if err != nil {
		t.Fatal(err)
	}
	if status != "open" || emailStatus != "queued" || createdBy < 1 || string(cc) != `["copy@example.test"]` || ticketCount != 1 || receiptCount != 1 || auditCount != 1 || outboxCount != 2 {
		t.Fatalf("admin ticket create did not commit atomically: status=%s email=%s actor=%d cc=%s tickets=%d receipts=%d audits=%d outbox=%d", status, emailStatus, createdBy, cc, ticketCount, receiptCount, auditCount, outboxCount)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET ticket_scope='departments',customer_scope='owned',customer_ownerless=false WHERE id=$1`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_owner_id=$1 WHERE id=$2`, staffID, customerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)`, staffID, departmentID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", "/api/v1/admin/tickets", map[string]any{"customer_id": customerID, "department_id": otherDepartmentID, "subject": "越权部门", "body": "不应创建", "priority": "normal"}, staff, "admin-ticket-dept-scope"), 403)
	expect(t, request(h, "POST", "/api/v1/admin/tickets", map[string]any{"customer_id": otherID, "department_id": departmentID, "subject": "越权客户", "body": "不应创建", "priority": "normal"}, staff, "admin-ticket-customer-scope"), 404)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=false WHERE id=$1`, customerID); err != nil {
		t.Fatal(err)
	}
	skipped := request(h, "POST", "/api/v1/admin/tickets", map[string]any{"customer_id": customerID, "department_id": departmentID, "subject": "未验证邮箱", "body": "不应投递", "priority": "normal", "send_email": true}, root, "admin-ticket-unverified-email")
	expect(t, skipped, 201)
	if data(t, skipped)["email_status"] != "skipped_unverified" {
		t.Fatalf("unverified email was not skipped: %#v", data(t, skipped))
	}
	var sentRows int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM email_outbox WHERE kind='ticket_created'`).Scan(&sentRows); err != nil || sentRows != 2 {
		t.Fatalf("unverified customer should not enqueue ticket email, rows=%d err=%v", sentRows, err)
	}
	for _, invalid := range []map[string]any{
		{"customer_id": customerID, "department_id": departmentID, "subject": "坏抄送", "body": "正文", "priority": "normal", "cc": []string{"Name <bad@example.test>"}},
		{"customer_id": customerID, "department_id": departmentID, "subject": "重复抄送", "body": "正文", "priority": "normal", "cc": []string{"a@example.test", "A@example.test"}},
		{"customer_id": customerID, "department_id": departmentID, "subject": "坏优先级", "body": "正文", "priority": "critical"},
	} {
		expect(t, request(h, "POST", "/api/v1/admin/tickets", invalid, root, "admin-ticket-invalid-"+fmt.Sprint(len(invalid))), 400)
	}
}
