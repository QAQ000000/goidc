package httpapi

import (
	"fmt"
	"net/http/httptest"
	"sync"
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

func TestAdminCreditConcurrentReplayAndRevocation(t *testing.T) {
	db, h := testServer(t)
	staffID, staff := user(t, db, h, "credit-mutation-staff", "admin")
	uid, customer := user(t, db, h, "credit-mutation-client", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", staffID, uid); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/credit/%d/limit", uid)
	payload := map[string]any{"limit_cents": 8000, "expected_version": 1, "note": "授信审核"}
	expect(t, request(h, "PUT", path, payload, customer, "credit-denied-client"), 403)
	expect(t, request(h, "GET", "/api/v1/admin/credit?customer_id=invalid", nil, staff, ""), 400)
	expect(t, request(h, "PUT", path, map[string]any{"limit_cents": 100000000001, "expected_version": 1}, staff, "credit-too-large"), 400)
	responses := make(chan *httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			responses <- request(h, "PUT", path, payload, staff, "credit-concurrent-key")
		}()
	}
	wg.Wait()
	close(responses)
	created, replayed := 0, 0
	for response := range responses {
		switch response.Code {
		case 201:
			created++
		case 200:
			replayed++
		default:
			t.Fatalf("concurrent adjustment: %d %s", response.Code, response.Body.String())
		}
	}
	if created != 1 || replayed != 1 {
		t.Fatal("duplicate adjustment", created, replayed)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path, payload, staff, "credit-concurrent-key"), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['credit.view'] WHERE id=(SELECT admin_role_id FROM users WHERE id=$1)", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PUT", path, payload, staff, "credit-concurrent-key"), 403)
	var balance, ledger, events int64
	if err := db.Pool.QueryRow(t.Context(), `SELECT balance_cents,(SELECT count(*) FROM balance_ledger WHERE user_id=$1),(SELECT count(*) FROM customer_credit_events WHERE user_id=$1) FROM users WHERE id=$1`, uid).Scan(&balance, &ledger, &events); err != nil || balance != 0 || ledger != 0 || events != 1 {
		t.Fatal("credit touched prepaid balance or duplicated event", balance, ledger, events, err)
	}
}
