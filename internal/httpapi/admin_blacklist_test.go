package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminBlacklistContractsAndLoginBlock(t *testing.T) {
	db, h := testServer(t)
	adminID, admin := user(t, db, h, "blacklist-root", "super_admin")
	_, reader := user(t, db, h, "blacklist-reader", "admin")
	var readerID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM users WHERE username='blacklist-reader'").Scan(&readerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['security.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", readerID); err != nil {
		t.Fatal(err)
	}
	// The route-level permission check must reject a role without security.view.
	expect(t, request(h, "GET", "/api/v1/admin/blacklist", nil, reader, ""), 403)
	created := request(h, "POST", "/api/v1/admin/blacklist", map[string]any{
		"subject_type": "username", "subject": "blocked-admin", "operation_type": "login", "remote_addr": "192.0.2.4", "reason": "测试阻断", "expected_version": 0,
	}, admin, "blacklist-create")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	version := int64(data(t, created)["version"].(float64))
	page := data(t, request(h, "GET", "/api/v1/admin/blacklist?q=blocked-admin&active=true", nil, admin, ""))
	if page["total"].(float64) != 1 || len(page["items"].([]any)) != 1 {
		t.Fatalf("blacklist page: %#v", page)
	}
	updated := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/blacklist/%d", id), map[string]any{
		"subject_type": "username", "subject": "blocked-admin", "operation_type": "login", "remote_addr": "192.0.2.4", "reason": "更新原因", "expected_version": version,
	}, admin, "blacklist-update")
	expect(t, updated, 200)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/blacklist/%d", id), map[string]any{
		"subject_type": "username", "subject": "blocked-admin", "operation_type": "login", "remote_addr": "192.0.2.4", "reason": "冲突", "expected_version": version,
	}, admin, "blacklist-conflict"), 409)
	replay := request(h, "PATCH", fmt.Sprintf("/api/v1/admin/blacklist/%d", id), map[string]any{
		"subject_type": "username", "subject": "blocked-admin", "operation_type": "login", "remote_addr": "192.0.2.4", "reason": "更新原因", "expected_version": version,
	}, admin, "blacklist-update")
	expect(t, replay, 200)
	// A blocked username is rejected before password verification.
	blocked := request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "blocked-admin", "password": "anything"}, "", "")
	expect(t, blocked, 403)
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM users WHERE id=$1", adminID).Scan(&adminID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/blacklist/%d", id), map[string]any{"expected_version": int64(data(t, updated)["version"].(float64))}, admin, "blacklist-delete"), 200)
	_, _ = user(t, db, h, "auto-block-admin", "super_admin")
	for i := 0; i < 3; i++ {
		expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auto-block-admin", "password": "wrong-password"}, "", ""), 401)
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "auto-block-admin", "password": "wrong-password"}, "", ""), 403)
	// Repeated failures automatically block the source address, never the
	// account, so a known administrator username cannot be globally locked.
	var usernameBlocks, ipBlocks int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_blacklist WHERE subject_type='username' AND subject='auto-block-admin'").Scan(&usernameBlocks); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auth_blacklist WHERE subject_type='ip' AND subject='192.0.2.1'").Scan(&ipBlocks); err != nil {
		t.Fatal(err)
	}
	if usernameBlocks != 0 || ipBlocks != 1 {
		t.Fatalf("automatic login block username=%d ip=%d", usernameBlocks, ipBlocks)
	}
}
