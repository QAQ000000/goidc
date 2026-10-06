package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestTicketDepartmentScopeConcurrentTransfer(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "scope-race-staff", "admin")
	_, customer := user(t, db, h, "scope-race-owner", "customer")
	var a, b int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('A','race-a') RETURNING id").Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO ticket_departments(name,slug) VALUES('B','race-b') RETURNING id").Scan(&b); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET ticket_scope='departments' WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", staffID, a); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "Concurrent transfer", "body": "test", "priority": "normal", "department_id": a}, customer, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT id FROM tickets WHERE id=$1 FOR UPDATE", id); err != nil {
		t.Fatal(err)
	}
	result := make(chan int, 1)
	go func() {
		result <- request(h, "POST", fmt.Sprintf("/api/v1/tickets/%d/replies", id), map[string]any{"body": "blocked reply", "version": 1}, staff, "").Code
	}()
	// Observe the reply waiting on the locked ticket before changing its department.
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT version,status,%admin_ticket_department_allowed%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("reply never reached ticket lock")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE tickets SET department_id=$2,version=version+1 WHERE id=$1", id, b); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case status := <-result:
		if status != 404 {
			t.Fatalf("waiting reply status=%d", status)
		}
	case <-ctx.Done():
		t.Fatal("reply did not complete")
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM ticket_replies WHERE ticket_id=$1", id).Scan(&count); err != nil || count != 0 {
		t.Fatal("reply crossed department boundary", err)
	}
}

func TestTicketDepartmentScopeBoundaries(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scope-root", "super_admin")
	staffID, staff := user(t, db, h, "scope-staff", "admin")
	otherID, other := user(t, db, h, "scope-other", "admin")
	_, customer := user(t, db, h, "scope-owner", "customer")
	department := func(name string) int64 {
		w := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]any{"name": name, "slug": name}, root, "")
		expect(t, w, 201)
		return int64(data(t, w)["id"].(float64))
	}
	a, b := department("scope-a"), department("scope-b")
	for _, pair := range [][2]int64{{staffID, a}, {otherID, b}} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET ticket_scope='departments' WHERE id=$1", pair[0]); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Pool.Exec(t.Context(), "INSERT INTO admin_ticket_departments(user_id,department_id) VALUES($1,$2)", pair[0], pair[1]); err != nil {
			t.Fatal(err)
		}
	}
	ticket := func(dept any, cookie string) int64 {
		w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "范围验收", "body": "客户请求", "priority": "normal", "department_id": dept}, cookie, "")
		expect(t, w, 201)
		return int64(data(t, w)["id"].(float64))
	}
	ta, tb, unassigned := ticket(a, customer), ticket(b, customer), ticket(nil, customer)
	path := func(id int64) string { return fmt.Sprintf("/api/v1/tickets/%d", id) }
	adminPath := func(id int64) string { return fmt.Sprintf("/api/v1/admin/tickets/%d", id) }
	expect(t, request(h, "GET", path(ta), nil, staff, ""), 200)
	for _, id := range []int64{tb, unassigned} {
		expect(t, request(h, "GET", path(id), nil, staff, ""), 404)
		expect(t, request(h, "POST", path(id)+"/replies", map[string]any{"body": "denied", "version": 1}, staff, ""), 404)
		expect(t, request(h, "PATCH", path(id), map[string]any{"version": 1}, staff, ""), 404)
		expect(t, multipartRequest(h, path(id)+"/attachments", staff, "test.txt", []byte("blocked")), 404)
		expect(t, request(h, "GET", adminPath(id)+"/notes", nil, staff, ""), 404)
		expect(t, request(h, "POST", adminPath(id)+"/notes", map[string]any{"body": "blocked"}, staff, ""), 404)
		expect(t, request(h, "POST", adminPath(id)+"/receive", map[string]any{}, staff, ""), 404)
		expect(t, request(h, "GET", path(id), nil, customer, ""), 200)
	}
	list := request(h, "GET", "/api/v1/admin/tickets", nil, staff, "")
	expect(t, list, 200)
	if len(listData(t, list)) != 1 {
		t.Fatalf("scoped list: %s", list.Body.String())
	}
	for _, dept := range []int64{b, 0} {
		expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/tickets?department_id=%d", dept), nil, staff, ""), 403)
	}
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/ticket-assignees?department_id=%d", b), nil, staff, ""), 403)
	assignees := request(h, "GET", fmt.Sprintf("/api/v1/admin/ticket-assignees?department_id=%d", a), nil, staff, "")
	expect(t, assignees, 200)
	if strings.Contains(assignees.Body.String(), "scope-other") {
		t.Fatal("assignee outside department returned")
	}
	directory := request(h, "GET", "/api/v1/admin/ticket-departments", nil, staff, "")
	expect(t, directory, 200)
	if len(listData(t, directory)) != 1 {
		t.Fatal("department directory leaked")
	}
	attachment := multipartRequest(h, path(tb)+"/attachments", customer, "test.txt", []byte("customer file"))
	expect(t, attachment, 201)
	attachmentPath := fmt.Sprintf("%s/attachments/%d", path(tb), int64(data(t, attachment)["id"].(float64)))
	expect(t, request(h, "GET", attachmentPath, nil, staff, ""), 404)
	expect(t, request(h, "DELETE", attachmentPath, nil, staff, ""), 404)
	expect(t, request(h, "GET", attachmentPath, nil, customer, ""), 200)
	note := request(h, "POST", adminPath(tb)+"/notes", map[string]any{"body": "private-department-note"}, root, "")
	expect(t, note, 201)
	expect(t, request(h, "DELETE", fmt.Sprintf("%s/notes/%d", adminPath(tb), int64(data(t, note)["id"].(float64))), nil, staff, ""), 404)
	expect(t, request(h, "PATCH", adminPath(ta)+"/assignment", map[string]any{"department_id": b}, staff, ""), 403)
	expect(t, request(h, "PATCH", adminPath(ta)+"/assignment", map[string]any{"department_id": a, "assignee_id": otherID}, staff, ""), 409)
	expect(t, request(h, "POST", adminPath(ta)+"/transfer", map[string]any{"department_id": b}, staff, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/tickets/merge", map[string]any{"ticket_ids": []int64{ta, tb}}, staff, ""), 404)
	var merged, notes, replies int
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM tickets WHERE merged_ticket_id IS NOT NULL),(SELECT count(*) FROM ticket_notes WHERE ticket_id=$1),(SELECT count(*) FROM ticket_replies)", tb).Scan(&merged, &notes, &replies); err != nil || merged != 0 || notes != 1 || replies != 0 {
		t.Fatalf("failed operation changed records: %d %d %d %v", merged, notes, replies, err)
	}
	// Staff can still act as the customer on their own ticket outside departmental scope.
	own := ticket(b, staff)
	expect(t, request(h, "POST", adminPath(own)+"/notes", map[string]any{"body": "private-own-note"}, root, ""), 201)
	detail := request(h, "GET", path(own), nil, staff, "")
	expect(t, detail, 200)
	if data(t, detail)["admin_access"] != false || strings.Contains(detail.Body.String(), "private-own-note") {
		t.Fatal("own customer ticket exposed admin access")
	}
	expect(t, request(h, "POST", path(own)+"/replies", map[string]any{"body": "owner reply", "version": 1}, staff, ""), 200)
	detail = request(h, "GET", path(own), nil, staff, "")
	if data(t, detail)["status"] != "open" {
		t.Fatal("owner reply used administrator status")
	}
	// Capability and department are independent prerequisites.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['dashboard.view','reports.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path(ta), nil, staff, ""), 404)
	dashboard := request(h, "GET", "/api/v1/admin/dashboard", nil, staff, "")
	expect(t, dashboard, 200)
	if data(t, dashboard)["open_tickets"].(float64) != 0 {
		t.Fatal("dashboard leaked without ticket capability")
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view','tickets.manage','dashboard.view','reports.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	dashboard = request(h, "GET", "/api/v1/admin/dashboard", nil, staff, "")
	expect(t, dashboard, 200)
	if data(t, dashboard)["open_tickets"].(float64) != 1 {
		t.Fatal("dashboard ignored scope")
	}
	now := time.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60))
	report := request(h, "GET", fmt.Sprintf("/api/v1/admin/reports/new-clients?year=%d&month=%d", now.Year(), now.Month()), nil, staff, "")
	expect(t, report, 200)
	total := float64(0)
	for _, day := range data(t, report)["days"].([]any) {
		total += day.(map[string]any)["new_tickets"].(float64)
	}
	if total != 1 {
		t.Fatalf("scoped report tickets=%v", total)
	}
	// Unassigned access is independent; inactive departments retain historical access.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET ticket_unassigned=true WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path(unassigned), nil, staff, ""), 200)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/ticket-departments/%d", a), map[string]any{"name": "scope-a", "active": false, "sort_order": 0, "expected_version": 1}, root, ""), 200)
	expect(t, request(h, "GET", path(ta), nil, staff, ""), 200)
	expect(t, request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "停用部门", "body": "test", "priority": "normal", "department_id": a}, customer, ""), 409)
	expect(t, request(h, "PATCH", adminPath(unassigned)+"/assignment", map[string]any{"department_id": a}, staff, ""), 409)
	expect(t, request(h, "PATCH", adminPath(unassigned)+"/assignment", map[string]any{"department_id": b, "assignee_id": otherID}, root, ""), 200)
	expect(t, request(h, "GET", path(unassigned), nil, staff, ""), 404)
	expect(t, request(h, "GET", path(unassigned), nil, other, ""), 200)
}

func TestTicketDepartmentScopeManagement(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "scope-config-root", "super_admin")
	_, ordinary := user(t, db, h, "scope-config-operator", "admin")
	w := request(h, "POST", "/api/v1/admin/ticket-departments", map[string]any{"name": "中文部门", "slug": "config-dept"}, root, "")
	expect(t, w, 201)
	dept := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/ticket-departments/%d", dept)
	expect(t, request(h, "POST", "/api/v1/admin/ticket-departments", map[string]any{"name": "不能创建", "slug": "denied"}, ordinary, ""), 403)
	expect(t, request(h, "PATCH", path, map[string]any{}, ordinary, ""), 403)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, ordinary, ""), 403)
	key := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "scope-root-key"}, root, "")
	expect(t, key, 201)
	expect(t, requestBearer(h, "PATCH", path, map[string]any{}, data(t, key)["secret"].(string)), 403)
	role := request(h, "POST", "/api/v1/admin/roles", roleInput("工单岗位", "tickets.view", "tickets.manage"), root, "")
	expect(t, role, 201)
	input := map[string]any{"username": "new-scoped-staff", "email": "new-scoped-staff@example.test", "display_name": "受限客服", "status": "active", "password": "integration-password", "admin_role_id": int64(data(t, role)["id"].(float64))}
	created := request(h, "POST", "/api/v1/admin/staff", input, root, "")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	login := func() string {
		w := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": input["username"], "password": "integration-password"}, "", "")
		expect(t, w, 200)
		return w.Result().Cookies()[0].String()
	}
	cookie := login()
	me := request(h, "GET", "/api/v1/me", nil, cookie, "")
	expect(t, me, 200)
	if data(t, me)["ticket_scope"] != "departments" || data(t, me)["ticket_unassigned"] != false {
		t.Fatal("new staff automatically gained scope")
	}
	expect(t, request(h, "GET", "/api/v1/admin/ticket-assignees", nil, cookie, ""), 403)
	issued := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "scope-staff-key"}, cookie, "")
	expect(t, issued, 201)
	secret := data(t, issued)["secret"].(string)
	input["password"] = ""
	input["expected_version"] = 1
	input["ticket_scope"] = "departments"
	input["ticket_unassigned"] = true
	input["ticket_department_ids"] = []int64{dept, dept}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	cookie = login()
	list := request(h, "GET", "/api/v1/admin/ticket-departments", nil, cookie, "")
	expect(t, list, 200)
	if len(listData(t, list)) != 1 {
		t.Fatal("scope grant not applied")
	}
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, root, ""), 409)
	update := map[string]any{"name": "停用部门", "active": false, "sort_order": 3, "expected_version": 1}
	expect(t, request(h, "PATCH", path, update, root, ""), 200)
	expect(t, request(h, "PATCH", path, update, root, ""), 409)
	// Omitted scope fields preserve current scope on unrelated staff updates.
	delete(input, "ticket_scope")
	delete(input, "ticket_unassigned")
	delete(input, "ticket_department_ids")
	input["expected_version"] = 2
	input["display_name"] = "改名客服"
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), input, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 200)
	var allowed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_ticket_department_allowed($1,$2)", id, dept).Scan(&allowed); err != nil || !allowed {
		t.Fatal("omitted scope reset access", err)
	}
	input["expected_version"] = 3
	input["ticket_department_ids"] = []int64{999999}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), input, root, ""), http.StatusConflict)
	input["ticket_department_ids"] = []int64{}
	input["ticket_unassigned"] = false
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), input, root, ""), 200)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 1}, root, ""), 409)
	expect(t, request(h, "DELETE", path, map[string]any{"expected_version": 2}, root, ""), 200)
}

func TestTicketDepartmentScopeMigration(t *testing.T) {
	db, h := testServer(t)
	id, _ := user(t, db, h, "scope-existing", "admin")
	if _, err := db.Pool.Exec(t.Context(), `DROP FUNCTION admin_ticket_department_allowed(bigint,bigint); DROP TABLE admin_ticket_departments;
 ALTER TABLE users DROP COLUMN ticket_scope,DROP COLUMN ticket_unassigned;
 ALTER TABLE ticket_departments DROP COLUMN version;
 DELETE FROM schema_migrations WHERE version='032_ticket_department_scope.sql';`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var scope string
	if err := db.Pool.QueryRow(t.Context(), "SELECT ticket_scope FROM users WHERE id=$1", id).Scan(&scope); err != nil || scope != "all" {
		t.Fatal("existing access lost", err)
	}
}
