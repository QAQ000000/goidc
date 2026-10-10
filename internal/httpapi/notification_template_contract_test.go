package httpapi

import (
	"context"
	"fmt"
	"testing"
)

func TestNotificationTemplateVersionAndPermissions(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "template-contract-admin", "super_admin")
	_, customer := user(t, db, h, "template-contract-customer", "customer")
	restrictedID, restricted := user(t, db, h, "template-contract-restricted", "admin")
	if _, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", restrictedID); err != nil {
		t.Fatal(err)
	}
	for _, cookie := range []string{customer, restricted} {
		expect(t, request(h, "GET", "/api/v1/admin/notification-templates/page", nil, cookie, ""), 403)
		expect(t, request(h, "GET", "/api/v1/admin/notification-templates", nil, cookie, ""), 403)
	}
	if _, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions=ARRAY['notifications.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", restrictedID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/notification-templates/page", nil, restricted, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/notification-templates", nil, restricted, ""), 200)
	expect(t, request(h, "POST", "/api/v1/admin/notification-templates", map[string]any{}, restricted, ""), 403)
	created := request(h, "POST", "/api/v1/admin/notification-templates", map[string]any{"channel": "sms", "kind": "contract_version", "name": "版本契约模板", "body": "测试正文", "active": false}, admin, "")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	path := fmt.Sprintf("/api/v1/admin/notification-templates/%d", id)
	expect(t, request(h, "PATCH", path, map[string]any{"name": "缺少版本"}, admin, ""), 400)
	expect(t, request(h, "PATCH", path, map[string]any{"name": "越权修改", "expected_version": 1}, restricted, ""), 403)
	expect(t, request(h, "PATCH", path, map[string]any{"name": "更新后的模板", "expected_version": 1, "active": true}, admin, ""), 200)
	expect(t, request(h, "PATCH", path, map[string]any{"name": "陈旧覆盖", "expected_version": 1, "active": false}, admin, ""), 409)
	var name string
	var version int64
	var active bool
	if err := db.Pool.QueryRow(context.Background(), "SELECT name,version,active FROM notification_templates WHERE id=$1", id).Scan(&name, &version, &active); err != nil {
		t.Fatal(err)
	}
	if name != "更新后的模板" || version != 2 || !active {
		t.Fatalf("stale update altered row: %s %d %v", name, version, active)
	}
	page := request(h, "GET", "/api/v1/admin/notification-templates/page?channel=sms&q=contract_version&status=active&limit=1&offset=0", nil, admin, "")
	expect(t, page, 200)
	result := data(t, page)
	if result["total"] != float64(1) || result["limit"] != float64(1) || result["offset"] != float64(0) {
		t.Fatalf("page contract: %s", page.Body.String())
	}
	filtered := request(h, "GET", "/api/v1/admin/notification-templates/page?channel=sms&q=contract_version&status=disabled", nil, admin, "")
	expect(t, filtered, 200)
	if data(t, filtered)["total"] != float64(0) {
		t.Fatal("disabled filter ignored")
	}
	expect(t, request(h, "GET", "/api/v1/admin/notification-templates/page?status=invalid", nil, admin, ""), 400)
	var audits int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action='notification_template.updated' AND entity_id=$1", fmt.Sprint(id)).Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits != 1 {
		t.Fatalf("conflict created audit: %d", audits)
	}
}
