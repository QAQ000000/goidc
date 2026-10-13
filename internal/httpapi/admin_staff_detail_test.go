package httpapi

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestAdminStaffDetailAndSafeRetry(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "staff-detail-root", "super_admin")
	staffID, staffCookie := user(t, db, h, "staff-detail-worker", "admin")
	customerID, _ := user(t, db, h, "staff-detail-customer", "customer")
	path := fmt.Sprintf("/api/v1/admin/staff/%d", staffID)
	w := request(h, "GET", path, nil, root, "")
	expect(t, w, 200)
	info := data(t, w)
	if info["username"] != "staff-detail-worker" || strings.Contains(w.Body.String(), "password") || strings.Contains(w.Body.String(), "token") {
		t.Fatal("unexpected staff detail or credential disclosure")
	}
	expect(t, request(h, "GET", path, nil, staffCookie, ""), 403)
	expect(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/staff/%d", customerID), nil, root, ""), 404)
	expect(t, request(h, "GET", "/api/v1/admin/staff/0", nil, root, ""), 400)
	keyResponse := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "detail-test"}, root, "")
	expect(t, keyResponse, 201)
	secret := data(t, keyResponse)["secret"].(string)
	expect(t, requestBearer(h, "GET", path, nil, secret), 403)
	payload := map[string]any{
		"username": info["username"], "email": info["email"], "display_name": "编辑后的管理员",
		"admin_role_id": info["admin_role_id"], "status": "active", "expected_version": info["admin_version"], "password": "",
	}
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			response := request(h, "PATCH", path, payload, root, "staff-safe-retry-20261013")
			if response.Code != 200 {
				t.Errorf("retry: %d %s", response.Code, response.Body.String())
			}
		}()
	}
	wg.Wait()
	var version, audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_version FROM users WHERE id=$1", staffID).Scan(&version); err != nil || version != 2 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='admin_staff.updated' AND entity_id=$2", rootID, fmt.Sprint(staffID)).Scan(&audits); err != nil || audits != 1 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
	expect(t, request(h, "PATCH", path, payload, root, "staff-other-key-20261013"), 409)
	payload["display_name"] = "改变相同幂等键的内容"
	expect(t, request(h, "PATCH", path, payload, root, "staff-safe-retry-20261013"), 409)
	// Replaying requires a live session even when the receipt already exists.
	var in store.StaffInput
	in.Username = info["username"].(string)
	in.Email = info["email"].(string)
	in.Name = "编辑后的管理员"
	in.RoleID = int64(info["admin_role_id"].(float64))
	in.Status, in.ExpectedVersion = "active", 1
	_, err := db.SaveStaffWithSession(t.Context(), rootID, staffID, "revoked-token", "staff-safe-retry-20261013", in)
	if err == nil {
		t.Fatal("revoked session replay accepted")
	}
}
