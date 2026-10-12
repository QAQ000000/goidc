package httpapi

import (
	"fmt"
	"testing"
)

func TestAdminCreditPageAndLimitAdjustment(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "credit-contract-root", "super_admin")
	customer, _ := user(t, db, h, "credit-contract-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=1200 WHERE id=$1", customer); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO customer_credit_accounts(user_id,limit_cents,used_cents) VALUES($1,5000,1200) ON CONFLICT (user_id) DO UPDATE SET limit_cents=5000,used_cents=1200,version=1", customer); err != nil {
		t.Fatal(err)
	}
	page := request(h, "GET", "/api/v1/admin/credit?q=credit-contract-customer", nil, root, "")
	expect(t, page, 200)
	items := data(t, page)["items"].([]any)
	if len(items) != 1 || items[0].(map[string]any)["limit_cents"].(float64) != 5000 {
		t.Fatalf("unexpected credit page: %v", items)
	}
	path := fmt.Sprintf("/api/v1/admin/credit/%d/limit", customer)
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 7000, "expected_version": 1}, root, "credit-limit-key"), 201)
	replay := request(h, "PUT", path, map[string]any{"limit_cents": 7000, "expected_version": 1}, root, "credit-limit-key")
	expect(t, replay, 200)
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 8000, "expected_version": 1}, root, "credit-limit-stale"), 409)
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 8000, "expected_version": 1}, root, "credit-limit-key"), 409)
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 1000, "expected_version": 2}, root, "credit-limit-below"), 409)
	staffID, staff := user(t, db, h, "credit-contract-staff", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['credit.view','credit.manage'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 7000, "expected_version": 1}, staff, "credit-limit-key"), 404)
	scoped := data(t, request(h, "GET", "/api/v1/admin/credit?q=credit-contract-customer", nil, staff, ""))
	if scoped["total"].(float64) != 0 {
		t.Fatal("scope leaked", scoped)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", "/api/v1/admin/credit", nil, staff, ""), 403)
	var events, audits int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM customer_credit_events WHERE user_id=$1", customer).Scan(&events); err != nil || events != 1 {
		t.Fatal("events", events, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events WHERE action='credit.limit_adjusted' AND entity_id=$1::text", fmt.Sprint(customer)).Scan(&audits); err != nil || audits != 1 {
		t.Fatal("audit", audits, err)
	}
	var balance int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", customer).Scan(&balance); err != nil || balance != 1200 {
		t.Fatalf("balance changed: %d %v", balance, err)
	}
}
