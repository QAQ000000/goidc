package httpapi

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestCustomerCreationPermissionsProfileAndLogin(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "profile-root", "super_admin")
	staffID, staff := user(t, db, h, "profile-staff", "admin")
	ownerID, _ := user(t, db, h, "profile-owner", "admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE admin_roles SET permissions=ARRAY['customers.view','customers.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1);`, staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET sales_enabled=true,customer_scope='owned' WHERE role='admin'`); err != nil {
		t.Fatal(err)
	}
	var group int64
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name) VALUES('profile-group') RETURNING id").Scan(&group); err != nil {
		t.Fatal(err)
	}
	base := func(name string) map[string]any {
		return map[string]any{"username": name, "email": strings.ToLower(name) + "@example.test", "password": " password123 ", "display_name": "Profile"}
	}
	for _, assignment := range []map[string]any{{"group_id": group}, {"sales_owner_id": ownerID}, {"group_id": 0}, {"sales_owner_id": 0}} {
		in := base("denied")
		for k, v := range assignment {
			in[k] = v
		}
		expect(t, request(h, "POST", "/api/v1/admin/customers", in, staff, "customer-profile-test-key"), 403)
	}
	for _, assignment := range []map[string]any{{"group_id": 999999}, {"sales_owner_id": 999999}} {
		in := base("invalid")
		for k, v := range assignment {
			in[k] = v
		}
		expect(t, request(h, "POST", "/api/v1/admin/customers", in, root, "customer-profile-test-key"), 400)
	}
	in := base("MixedCase")
	profile := map[string]any{"gender": "female", "company_name": "Example Ltd", "phone": "+86 13800138000", "qq": "123456", "country": "中国", "province": "广东", "address": "测试地址", "postcode": "12345", "source_channel": "朋友推荐", "payment_method": "wechat", "language": "en_US", "admin_note": "客户备注", "receive_marketing": false}
	for k, v := range profile {
		in[k] = v
	}
	in["group_id"] = group
	in["sales_owner_id"] = ownerID
	created := request(h, "POST", "/api/v1/admin/customers", in, root, "customer-profile-test-key")
	expect(t, created, 201)
	id := int64(data(t, created)["id"].(float64))
	if data(t, created)["username"] != "mixedcase" {
		t.Fatal(created.Body.String())
	}
	detail := request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d", id), nil, root, "")
	expect(t, detail, 200)
	got := data(t, detail)
	for k, v := range profile {
		if k == "phone" {
			k = "phone_number"
		}
		if got[k] != v {
			t.Errorf("saved %s=%v want %v", k, got[k], v)
		}
	}
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]any{"username": "MixedCase", "password": " password123 "}, "", ""), 200)
	expect(t, request(h, "POST", "/api/v1/admin/customers", base("MIXEDCASE"), root, "customer-profile-duplicate-key"), 409)
	profile["expected_version"] = 1
	profile["qq"] = "654321"
	profile["admin_note"] = "修改备注"
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", id), map[string]any{"profile": profile}, root, "customer-profile-update-key"), 200)
	got = data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d", id), nil, root, ""))
	if got["qq"] != "654321" || got["admin_note"] != "修改备注" {
		t.Fatal(got)
	}
	owned := request(h, "POST", "/api/v1/admin/customers", base("staff-created"), staff, "customer-profile-staff-key")
	expect(t, owned, 201)
	ownedID := int64(data(t, owned)["id"].(float64))
	got = data(t, request(h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d", ownedID), nil, staff, ""))
	if got["sales_owner_id"] != float64(staffID) {
		t.Fatal("new customer outside creator scope", got)
	}
	for i, password := range []string{strings.Repeat("a", 73), "short", strings.Repeat("界", 25)} {
		invalid := base(fmt.Sprintf("invalid-password-%d", i))
		invalid["password"] = password
		expect(t, request(h, "POST", "/api/v1/admin/customers", invalid, root, "customer-profile-unicode-key"), 400)
	}
	valid := base("unicode-password")
	valid["password"] = strings.Repeat("界", 24)
	response := request(h, "POST", "/api/v1/admin/customers", valid, root, "customer-profile-unicode-key")
	expect(t, response, 201)
	var hash string
	if err := db.Pool.QueryRow(t.Context(), "SELECT password_hash FROM users WHERE id=$1", int64(data(t, response)["id"].(float64))).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(valid["password"].(string))) != nil {
		t.Fatal("password changed")
	}
}

func TestCustomerCreationRechecksRevokedRoleAfterLock(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "profile-revoked", "admin")
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
		t.Fatal(err)
	}
	done := make(chan int, 1)
	go func() {
		done <- request(h, "POST", "/api/v1/admin/customers", map[string]any{"username": "revoked-created", "email": "revoked-created@example.test", "password": "password123"}, staff, "customer-profile-revoked-key").Code
	}()
	deadline := time.Now().Add(8 * time.Second)
	for {
		var waiting bool
		if err = db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM pg_locks WHERE locktype='advisory' AND objid=70108190 AND NOT granted)").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request did not reach authorization lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['customers.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 403 {
			t.Fatalf("revoked mutation=%d", code)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("request blocked")
	}
	var exists bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE username='revoked-created')").Scan(&exists); err != nil || exists {
		t.Fatalf("revoked create persisted: %v %v", exists, err)
	}
}
