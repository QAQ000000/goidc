package httpapi

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminRBACRouteCoverage(t *testing.T) {
	db, h := testServer(t)
	id, cookie := user(t, db, h, "denied-staff", "admin")
	_, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", id)
	if err != nil {
		t.Fatal(err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "server.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	valid := map[string]bool{"@super": true}
	for _, p := range store.AdminPermissions {
		valid[p.Code] = true
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "HandleFunc" {
			return true
		}
		literal, ok := call.Args[0].(*ast.BasicLit)
		if !ok {
			return true
		}
		pattern, err := strconv.Unquote(literal.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(pattern, "/api/v1/admin/") {
			return true
		}
		seen[pattern] = true
		permission, ok := adminRoutePermissions[pattern]
		if !ok || !valid[permission] {
			t.Fatalf("unclassified admin route %s", pattern)
		}
		method, path, _ := strings.Cut(pattern, " ")
		for _, key := range []string{"id", "entity", "contact_id", "note_id"} {
			path = strings.ReplaceAll(path, "{"+key+"}", "1")
		}
		expect(t, request(h, method, path, map[string]any{}, cookie, ""), http.StatusForbidden)
		return true
	})
	for p := range adminRoutePermissions {
		if !seen[p] {
			t.Errorf("permission references unregistered route %s", p)
		}
	}
	if len(seen) < 70 {
		t.Fatalf("coverage unexpectedly small: %d", len(seen))
	}
}

func roleInput(name string, permissions ...string) map[string]any {
	return map[string]any{"name": name, "description": "test role", "active": true, "permissions": permissions}
}

func TestAdminRBACManagementAndRevocation(t *testing.T) {
	db, h := testServer(t)
	rootID, root := user(t, db, h, "rbac-root", "super_admin")
	w := request(h, "POST", "/api/v1/admin/roles", roleInput("账单只读", "invoices.view"), root, "")
	expect(t, w, 201)
	roleID := int64(data(t, w)["id"].(float64))
	payload := map[string]any{"username": "finance-reader", "email": "finance-reader@example.test", "display_name": "财务只读", "admin_role_id": roleID, "status": "active", "password": "integration-password"}
	w = request(h, "POST", "/api/v1/admin/staff", payload, root, "")
	expect(t, w, 201)
	staffID := int64(data(t, w)["id"].(float64))
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "finance-reader", "password": "integration-password"}, "", "")
	expect(t, login, 200)
	cookie := login.Result().Cookies()[0].String()
	expect(t, request(h, "GET", "/api/v1/admin/invoices", nil, cookie, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/customers", nil, cookie, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/invoices/1/refund", map[string]any{}, cookie, ""), 403)
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("提升", "audit.view"), cookie, ""), 403)
	keyResp := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "rbac-test"}, cookie, "")
	expect(t, keyResp, 201)
	secret := data(t, keyResp)["secret"].(string)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/invoices", nil, secret), 200)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/roles", nil, secret), 403)
	// Role revocation takes effect on the existing session and API key.
	revoke := roleInput("账单只读")
	revoke["permissions"] = []string{}
	revoke["expected_version"] = 1
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/roles/%d", roleID), revoke, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/admin/invoices", nil, cookie, ""), 403)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/invoices", nil, secret), 403)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/roles/%d", roleID), revoke, root, ""), 409)
	expect(t, request(h, "DELETE", fmt.Sprintf("/api/v1/admin/roles/%d", roleID), map[string]any{"expected_version": 2}, root, ""), 409)
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("非法", "super_admin"), root, ""), 409)
	expect(t, request(h, "POST", "/api/v1/admin/roles", roleInput("缺少读取", "tickets.manage"), root, ""), 409)
	// Cannot turn a protected super administrator or customer into ordinary staff.
	payload["expected_version"] = 1
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", rootID), payload, root, ""), 409)
	customerID, _ := user(t, db, h, "protected-customer", "customer")
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", customerID), payload, root, ""), 409)
	payload["status"] = "disabled"
	payload["password"] = ""
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", staffID), payload, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	expect(t, request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "finance-reader", "password": "integration-password"}, "", ""), 401)
	list := request(h, "GET", "/api/v1/admin/staff", nil, root, "")
	expect(t, list, 200)
	if strings.Contains(list.Body.String(), "password") || strings.Contains(list.Body.String(), secret) {
		t.Fatal("staff response exposed credentials")
	}
	var audits int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE action LIKE 'admin_%' AND actor_id=$1", rootID).Scan(&audits); err != nil || audits != 4 {
		t.Fatalf("audits=%d err=%v", audits, err)
	}
}

func TestAdminRBACTicketSharedRoutes(t *testing.T) {
	db, h := testServer(t)
	_, customer := user(t, db, h, "ticket-owner", "customer")
	staffID, staff := user(t, db, h, "ticket-reader", "admin")
	if _, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions=ARRAY['tickets.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	w := request(h, "POST", "/api/v1/tickets", map[string]any{"subject": "客户问题", "body": "测试工单", "priority": "normal"}, customer, "")
	expect(t, w, 201)
	ticketID := int64(data(t, w)["id"].(float64))
	path := fmt.Sprintf("/api/v1/tickets/%d", ticketID)
	expect(t, request(h, "GET", path, nil, staff, ""), 200)
	expect(t, request(h, "HEAD", path, nil, staff, ""), 200)
	expect(t, request(h, "POST", path+"/replies", map[string]any{"body": "越权回复", "version": 1}, staff, ""), 404)
	expect(t, multipartRequest(h, path+"/attachments", staff, "test.txt", []byte("test")), 404)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/tickets/%d/notes", ticketID), map[string]any{"body": "internal"}, staff, ""), 403)
	if _, err := db.Pool.Exec(context.Background(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, staff, ""), 404)
	expect(t, request(h, "GET", path, nil, customer, ""), 200)
	var replies int
	if err := db.Pool.QueryRow(context.Background(), "SELECT count(*) FROM ticket_replies WHERE ticket_id=$1", ticketID).Scan(&replies); err != nil || replies != 0 {
		t.Fatal("denied reply changed data")
	}
}

func TestAdminRBACConcurrentRoleUpdates(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "race-root", "super_admin")
	w := request(h, "POST", "/api/v1/admin/roles", roleInput("版本竞争", "audit.view"), root, "")
	expect(t, w, 201)
	id := int64(data(t, w)["id"].(float64))
	var wg sync.WaitGroup
	results := make(chan int, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := roleInput(fmt.Sprintf("版本%d", i), "audit.view")
			p["expected_version"] = 1
			results <- request(h, "PATCH", fmt.Sprintf("/api/v1/admin/roles/%d", id), p, root, "").Code
		}(i)
	}
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for c := range results {
		counts[c]++
	}
	if counts[200] != 1 || counts[409] != 1 {
		t.Fatalf("race: %v", counts)
	}
}

func TestAdminRBACMigrationExistingOperators(t *testing.T) {
	db, _ := testServer(t)
	ctx := t.Context()
	// This is the disposable random schema created by testServer, never the development schema.
	if _, err := db.Pool.Exec(ctx, `ALTER TABLE users DROP CONSTRAINT users_admin_role, DROP COLUMN admin_role_id, DROP COLUMN admin_version;
DROP TABLE admin_roles; DELETE FROM schema_migrations WHERE version='031_admin_rbac.sql';
INSERT INTO users(username,email,password_hash,display_name,role) VALUES('old-operator','old-operator@example.test','unused','Operator','admin');`); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var assigned, roles int
	if err := db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users WHERE role='admin' AND admin_role_id IS NOT NULL), (SELECT count(*) FROM admin_roles)`).Scan(&assigned, &roles); err != nil || assigned != 1 || roles != 1 {
		t.Fatalf("assignment=%d roles=%d err=%v", assigned, roles, err)
	}
	var id int64
	if err := db.Pool.QueryRow(ctx, "SELECT id FROM users WHERE username='old-operator'").Scan(&id); err != nil {
		t.Fatal(err)
	}
	grants, err := db.AdminGrants(ctx, id)
	if err != nil || len(grants) != 26 { // The immutable 031 migration grants exactly 26 capabilities.
		t.Fatalf("migration grants=%d err=%v", len(grants), err)
	}
	for _, p := range store.AdminPermissions {
		if p.Code == "balance.topup.resolve" || p.Code == "resources.view" || p.Code == "resources.manage" || p.Code == "credit.view" || p.Code == "credit.manage" || p.Code == "contracts.view" || p.Code == "contracts.manage" {
			for _, grant := range grants {
				if grant == p.Code {
					t.Fatalf("later permission implicitly granted: %s", grant)
				}
			}
			continue
		}
		if strings.HasPrefix(p.Code, "external_refunds.") || p.Code == "jobs.view" || p.Code == "content.view" || p.Code == "downloads.view" || p.Code == "downloads.manage" || p.Code == "customer_files.view" || p.Code == "customer_files.manage" || p.Code == "customer_levels.view" || p.Code == "customer_levels.manage" || p.Code == "invoices.manage" || p.Code == "invoice_applications.view" || p.Code == "invoice_applications.manage" || p.Code == "payments.reconcile" || p.Code == "settings.view" || p.Code == "settings.manage" || p.Code == "certifications.view" || p.Code == "certifications.manage" || p.Code == "affiliates.view" || p.Code == "affiliates.manage" || p.Code == "affiliates.withdrawals.review" || p.Code == "affiliates.payout" || p.Code == "sales.view" || p.Code == "sales.manage" || p.Code == "security.view" || p.Code == "security.manage" { // Introduced after 031; never expand old roles implicitly.
			for _, grant := range grants {
				if grant == p.Code {
					t.Fatalf("later permission implicitly granted: %s", grant)
				}
			}
			continue
		}
		found := false
		for _, grant := range grants {
			if grant == p.Code {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing migrated permission %s", p.Code)
		}
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('no-role','no-role@example.test','unused','No role','admin')`); err == nil {
		t.Fatal("unassigned administrator accepted")
	}
}

func TestAdminRBACStaffCredentialChanges(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "credential-root", "super_admin")
	id, cookie := user(t, db, h, "credential-staff", "admin")
	var roleID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT admin_role_id FROM users WHERE id=$1", id).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	keyResp := request(h, "POST", "/api/v1/api-keys", map[string]any{"name": "credential-test"}, cookie, "")
	expect(t, keyResp, 201)
	secret := data(t, keyResp)["secret"].(string)
	_, err := db.Pool.Exec(t.Context(), `INSERT INTO mfa_challenges(token_hash,user_id,expires_at) VALUES(repeat('a',64),$1,now()+interval '5 minutes');
`, id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO password_reset_tokens(token_hash,user_id,expires_at) VALUES(repeat('b',64),$1,now()+interval '5 minutes')`, id); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"username": "credential-staff", "email": "credential-staff@example.test", "display_name": "改名", "admin_role_id": roleID, "status": "active", "password": "updated-integration-password", "expected_version": 1}
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), payload, root, ""), 200)
	expect(t, request(h, "GET", "/api/v1/me", nil, cookie, ""), 401)
	expect(t, requestBearer(h, "GET", "/api/v1/me", nil, secret), 401)
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), payload, root, ""), 409)
	var hash string
	var challenges, resets int
	if err = db.Pool.QueryRow(t.Context(), `SELECT password_hash,(SELECT count(*) FROM mfa_challenges WHERE user_id=$1), (SELECT count(*) FROM password_reset_tokens WHERE user_id=$1 AND consumed_at IS NULL) FROM users WHERE id=$1`, id).Scan(&hash, &challenges, &resets); err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte("updated-integration-password")) != nil || challenges != 0 || resets != 0 {
		t.Fatalf("credential revocation failed challenges=%d resets=%d", challenges, resets)
	}
	login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "credential-staff", "password": "updated-integration-password"}, "", "")
	expect(t, login, 200)
	cookie = login.Result().Cookies()[0].String()
	if _, err = db.Pool.Exec(t.Context(), "UPDATE admin_roles SET active=false WHERE id=$1", roleID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/dashboard", nil, cookie, ""), 403)
	payload["status"] = "disabled"
	payload["password"] = ""
	payload["expected_version"] = 2
	expect(t, request(h, "PATCH", fmt.Sprintf("/api/v1/admin/staff/%d", id), payload, root, ""), 200)
}
