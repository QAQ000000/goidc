package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type adminBalanceFixture struct {
	db                   *store.Store
	h                    http.Handler
	actor, customer      int64
	cookie, customerAuth string
}

func newAdminBalanceFixture(t *testing.T) adminBalanceFixture {
	t.Helper()
	db, h := testServer(t)
	f := adminBalanceFixture{db: db, h: h}
	f.actor, f.cookie = user(t, db, h, "balance-operation-admin", "admin")
	f.customer, f.customerAuth = user(t, db, h, "balance-operation-customer", "customer")
	return f
}

func (f adminBalanceFixture) call(method, key string, body any, bearer string) *httptest.ResponseRecorder {
	path := fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.customer)
	if method == "GET" {
		path += "-operation"
	}
	cookie := f.cookie
	headers := map[string]string{"Idempotency-Key": key}
	if bearer != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + bearer
	}
	return requestFrom(f.h, method, path, body, "192.0.2.87:5678", cookie, headers)
}

func (f adminBalanceFixture) apiKey(t *testing.T) string {
	t.Helper()
	out := request(f.h, "POST", "/api/v1/api-keys", map[string]string{"name": "balance-operation"}, f.cookie, "")
	expect(t, out, 201)
	return data(t, out)["secret"].(string)
}

func (f adminBalanceFixture) assertFunds(t *testing.T, balance int64, entries, audits int) {
	t.Helper()
	var gotBalance int64
	var gotEntries, gotAudits int
	err := f.db.Pool.QueryRow(t.Context(), `SELECT balance_cents,
 (SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='topup'),
 (SELECT count(*) FROM audit_events WHERE action='balance.topped_up' AND entity_id=$1::text)
 FROM users WHERE id=$1`, f.customer).Scan(&gotBalance, &gotEntries, &gotAudits)
	if err != nil || gotBalance != balance || gotEntries != entries || gotAudits != audits {
		t.Fatalf("balance=%d entries=%d audits=%d err=%v", gotBalance, gotEntries, gotAudits, err)
	}
}

func awaitBalanceResponse(t *testing.T, done <-chan *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case out := <-done:
		return out
	case <-time.After(7 * time.Second):
		t.Fatal("balance operation stalled after fixture lock released")
		return nil
	}
}

func TestAdminBalanceOperationConfirmationAndLegacyReplay(t *testing.T) {
	f := newAdminBalanceFixture(t)
	const key = "balance-operation-first"
	body := map[string]any{"amount_cents": 1200, "note": "  manual reason  "}
	expect(t, f.call("POST", key, body, ""), 201)
	expect(t, f.call("POST", "balance-operation-second", map[string]any{"amount_cents": 300}, ""), 201)
	// Verification and replay report the original balance, even after later
	// credits and settings that would reject a fresh top-up.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE finance_settings SET topup_enabled=false,min_topup_cents=99999"); err != nil {
		t.Fatal(err)
	}
	queried := f.call("GET", key, nil, "")
	expect(t, queried, 200)
	got := data(t, queried)
	if len(got) != 6 || got["state"] != "confirmed" || got["user_id"] != float64(f.customer) || got["amount_cents"] != float64(1200) || got["balance_cents"] != float64(1200) || got["ledger_id"] == nil || got["created_at"] == nil {
		t.Fatal("confirmation did not whitelist the original ledger fact", queried.Body.String())
	}
	if queried.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("balance result is cacheable")
	}
	body["note"] = "manual reason"
	replayed := f.call("POST", key, body, "")
	expect(t, replayed, 200)
	if data(t, replayed)["replayed"] != true || data(t, replayed)["balance_cents"] != float64(1200) {
		t.Fatal("replay did not retain historical balance", replayed.Body.String())
	}
	body["note"] = "a different reason"
	out := f.call("POST", key, body, "")
	expect(t, out, 409)
	if errorData(t, out)["code"] != "idempotency_conflict" {
		t.Fatal("same key changed the credited note")
	}
	body["note"], body["amount_cents"] = "manual reason", 1400
	expect(t, f.call("POST", key, body, ""), 409)
	f.assertFunds(t, 1500, 2, 2)

	// The key domain is customer/key, including old ledger rows with no
	// actor receipt. A different currently authorized administrator may query.
	_, other := user(t, f.db, f.h, "balance-operation-other-admin", "super_admin")
	copy := f
	copy.cookie = other
	expect(t, copy.call("GET", key, nil, ""), 200)
	otherReplay := copy.call("POST", key, map[string]any{"amount_cents": 1200, "note": "manual reason"}, "")
	expect(t, otherReplay, 200)
	if data(t, otherReplay)["replayed"] != true {
		t.Fatal("customer/key domain changed across administrators")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET balance_cents=2000 WHERE id=$1", f.customer); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,idempotency_key,note) VALUES($1,'topup',500,2000,'admin_topup','legacy-balance-operation',' legacy note ')`, f.customer); err != nil {
		t.Fatal(err)
	}
	legacy := f.call("GET", "legacy-balance-operation", nil, "")
	expect(t, legacy, 200)
	if data(t, legacy)["state"] != "confirmed" || data(t, legacy)["balance_cents"] != float64(2000) {
		t.Fatal("legacy ledger could not be verified", legacy.Body.String())
	}
	expect(t, f.call("POST", "legacy-balance-operation", map[string]any{"amount_cents": 500, "note": "legacy note"}, ""), 200)
	expect(t, f.call("POST", "legacy-balance-operation", map[string]any{"amount_cents": 500}, ""), 409)
	// An online gateway ledger entry cannot masquerade as manual credit.
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,idempotency_key,note) VALUES($1,'topup',100,2100,'gateway_topup','gateway-balance-operation','gateway')`, f.customer); err != nil {
		t.Fatal(err)
	}
	for _, missing := range []string{"unseen-balance-operation", "gateway-balance-operation"} {
		result := f.call("GET", missing, nil, "")
		expect(t, result, 200)
		if got := data(t, result); len(got) != 2 || got["state"] != "unconfirmed" || got["user_id"] != float64(f.customer) {
			t.Fatal("no manual fact must remain unconfirmed", result.Body.String())
		}
	}
	expect(t, f.call("POST", "gateway-balance-operation", map[string]any{"amount_cents": 100, "note": "gateway"}, ""), 409)
	var receipts int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations").Scan(&receipts); err != nil || receipts != 0 {
		t.Fatal("ledger verification invented an actor receipt", receipts, err)
	}
}

func TestAdminBalanceOperationPermissionsScopeAndHeaders(t *testing.T) {
	f := newAdminBalanceFixture(t)
	const key = "balance-operation-permissions"
	// This financial action needs balance.manage, not customers.view.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['balance.manage'] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call("POST", key, map[string]any{"amount_cents": 400}, ""), 201)
	expect(t, f.call("GET", key, nil, ""), 200)
	secret := f.apiKey(t)
	expect(t, f.call("GET", key, nil, secret), 200)
	expect(t, f.call("POST", key, map[string]any{"amount_cents": 400}, secret), 200)
	path := fmt.Sprintf("/api/v1/admin/customers/%d/balance-operation", f.customer)
	checks := []*httptest.ResponseRecorder{
		request(f.h, "GET", path, nil, "", key),
		request(f.h, "GET", path, nil, f.customerAuth, key),
	}
	for _, result := range checks {
		expect(t, result, 401)
		if result.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("authentication error could be cached")
		}
	}
	for _, value := range []string{"", "short", strings.Repeat("x", 129)} {
		result := f.call("GET", value, nil, "")
		expect(t, result, 400)
		if result.Header().Get("Cache-Control") != "private, no-store" || errorData(t, result)["code"] != "invalid_idempotency_key" {
			t.Fatal("invalid operation key contract", result.Body.String())
		}
	}
	for _, method := range []string{"GET", "POST"} {
		duplicatePath := path
		if method == "POST" {
			duplicatePath = strings.TrimSuffix(path, "-operation")
		}
		r := httptest.NewRequest(method, duplicatePath, strings.NewReader(`{"amount_cents":400}`))
		r.Header.Set("Cookie", f.cookie)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Requested-With", "zjmfgo")
		r.Header.Set("Origin", "http://localhost:3088")
		r.Header.Add("Idempotency-Key", key)
		r.Header.Add("Idempotency-Key", "another-valid-key")
		duplicate := httptest.NewRecorder()
		f.h.ServeHTTP(duplicate, r)
		expect(t, duplicate, 400)
		if errorData(t, duplicate)["code"] != "invalid_idempotency_key" {
			t.Fatal("ambiguous key was not rejected", duplicate.Body.String())
		}
	}
	expect(t, request(f.h, "GET", "/api/v1/admin/customers/bad/balance-operation", nil, f.cookie, key), 400)
	missing := f
	missing.customer = 999999
	expect(t, missing.call("GET", key, nil, ""), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call("GET", key, nil, ""), 404)
	expect(t, f.call("POST", key, map[string]any{"amount_cents": 400}, ""), 404)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY[]::text[] WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor); err != nil {
		t.Fatal(err)
	}
	denied := f.call("GET", key, nil, "")
	expect(t, denied, 403)
	if denied.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("permission error could be cached")
	}
	f.assertFunds(t, 400, 1, 1)
}

func TestAdminBalanceOperationConcurrentReplayAndAuditRollback(t *testing.T) {
	f := newAdminBalanceFixture(t)
	const key = "balance-operation-concurrent"
	body := map[string]any{"amount_cents": 600, "note": "one intention"}
	start := make(chan struct{})
	done := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < cap(done); i++ {
		go func() { <-start; done <- f.call("POST", key, body, "") }()
	}
	close(start)
	counts := map[int]int{}
	for i := 0; i < cap(done); i++ {
		result := awaitBalanceResponse(t, done)
		counts[result.Code]++
		if result.Code != 200 && result.Code != 201 {
			t.Fatal("concurrent top-up failed", result.Body.String())
		}
	}
	if counts[201] != 1 || counts[200] != 7 {
		t.Fatal("concurrent requests did not share one credit", counts)
	}
	f.assertFunds(t, 600, 1, 1)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_balance_operation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='balance.topped_up' THEN RAISE EXCEPTION 'injected top-up audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_balance_operation_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_balance_operation_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call("POST", "balance-operation-rollback", body, ""), 500)
	f.assertFunds(t, 600, 1, 1)
	result := f.call("GET", "balance-operation-rollback", nil, "")
	expect(t, result, 200)
	if data(t, result)["state"] != "unconfirmed" {
		t.Fatal("rolled-back credit appeared confirmed")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_balance_operation_audit ON audit_events; DROP FUNCTION reject_balance_operation_audit()"); err != nil {
		t.Fatal(err)
	}
	// The test knows the original transaction rolled back; the API's missing
	// fact alone does not tell a UI that changing its key is safe.
	expect(t, f.call("POST", "balance-operation-rollback", body, ""), 201)
	confirmed := f.call("GET", "balance-operation-rollback", nil, "")
	expect(t, confirmed, 200)
	if data(t, confirmed)["state"] != "confirmed" {
		t.Fatal("subsequent committed credit was not visible")
	}
	f.assertFunds(t, 1200, 2, 2)
}

func TestAdminBalanceOperationRechecksAfterKeyWait(t *testing.T) {
	for _, method := range []string{"GET", "POST"} {
		for _, scenario := range []string{"session_revoked", "key_revoked", "permission_revoked", "role_disabled", "administrator_disabled", "scope_revoked"} {
			t.Run(method+"/"+scenario, func(t *testing.T) {
				f := newAdminBalanceFixture(t)
				const key = "balance-operation-waiting"
				body := map[string]any{"amount_cents": 800}
				// Replays and reads must enforce current access to facts that
				// were successfully committed by this actor earlier.
				expect(t, f.call("POST", key, body, ""), 201)
				secret := ""
				if scenario == "key_revoked" {
					secret = f.apiKey(t)
				}
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("topup:%d:%s", f.customer, key)); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.call(method, key, body, secret) }()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				status := 401
				switch scenario {
				case "session_revoked":
					_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie)))
				case "key_revoked":
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret))
				case "permission_revoked":
					status = 403
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'balance.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
				case "role_disabled":
					status = 403
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET active=false WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
				case "administrator_disabled":
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", f.actor)
				case "scope_revoked":
					status = 404
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				out := awaitBalanceResponse(t, done)
				expect(t, out, status)
				if method == "GET" && out.Header().Get("Cache-Control") != "private, no-store" {
					t.Fatal("post-wait rejection could be cached")
				}
				f.assertFunds(t, 800, 1, 1)
			})
		}
	}
}

func TestAdminBalanceOperationRechecksAfterFinancialWaits(t *testing.T) {
	for _, waiting := range []string{"funds", "settings"} {
		t.Run(waiting, func(t *testing.T) {
			f := newAdminBalanceFixture(t)
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if waiting == "funds" {
				_, err = blocker.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.customer)
			} else {
				_, err = blocker.Exec(t.Context(), "SELECT id FROM finance_settings WHERE id=1 FOR UPDATE")
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.call("POST", "balance-operation-financial-wait", map[string]any{"amount_cents": 200}, "")
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			if _, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie))); err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertFunds(t, 0, 0, 0)
		})
	}
}

func TestAdminBalanceOperationPasswordRotationLockOrder(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newAdminBalanceFixture(t)
			original := f.cookie
			login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
			expect(t, login, 200)
			cookie := login.Result().Cookies()[0]
			f.cookie = cookie.Name + "=" + cookie.Value
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			// The real password change locks the administrator, then waits on
			// its original session. An inverted top-up would lock its other
			// session/key first, creating a cycle when password invalidates it.
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(cookieValue(original))); err != nil {
				t.Fatal(err)
			}
			rotation := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-balance-operation-password"}, original, "admin")
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.call("POST", "balance-operation-password-wait", map[string]any{"amount_cents": 700}, secret)
			}()
			waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, rotation), 200)
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertFunds(t, 0, 0, 0)
		})
	}
}

func waitBalanceCredentialExpiry(t *testing.T, f adminBalanceFixture, secret string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	query, hash := "SELECT expires_at<=clock_timestamp() FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie))
	if secret != "" {
		query, hash = "SELECT expires_at<=clock_timestamp() FROM api_keys WHERE secret_hash=$1", tokenHash(secret)
	}
	for {
		var expired bool
		if err := f.db.Pool.QueryRow(ctx, query, hash).Scan(&expired); err != nil {
			t.Fatal(err)
		}
		if expired {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture credential did not naturally expire")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestAdminBalanceOperationCredentialExpiryAfterWait(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newAdminBalanceFixture(t)
			const key = "balance-operation-expiring-query"
			expect(t, f.call("POST", key, map[string]any{"amount_cents": 500}, ""), 201)
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			keyLock, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer keyLock.Rollback(context.Background())
			if _, err = keyLock.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("topup:%d:%s", f.customer, key)); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.call("GET", key, nil, secret) }()
			waitUploadBlockedBy(t, f.db, keyLock.Conn().PgConn().PID())
			// Authentication has completed. Set a short future expiry, then
			// hold the unchanged credential row across that deadline, exposing
			// a stale pre-wait predicate in SELECT ... FOR SHARE.
			table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
			if secret != "" {
				table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
			}
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
				t.Fatal(err)
			}
			credentialLock, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer credentialLock.Rollback(context.Background())
			if _, err = credentialLock.Exec(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE", hash); err != nil {
				t.Fatal(err)
			}
			if err = keyLock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitUploadBlockedBy(t, f.db, credentialLock.Conn().PgConn().PID())
			waitBalanceCredentialExpiry(t, f, secret)
			if err = credentialLock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertFunds(t, 500, 1, 1)
		})
	}
}

func TestAdminBalanceOperationCredentialExpiryBeforeCommit(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newAdminBalanceFixture(t)
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('balance-operation-audit-expiry',0))"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_balance_operation_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='balance.topped_up' THEN PERFORM pg_advisory_xact_lock(hashtextextended('balance-operation-audit-expiry',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_balance_operation_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_balance_operation_audit()`); err != nil {
				t.Fatal(err)
			}
			table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
			if secret != "" {
				table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
			}
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.call("POST", "balance-operation-expiring-credit", map[string]any{"amount_cents": 700}, secret)
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			waitBalanceCredentialExpiry(t, f, secret)
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertFunds(t, 0, 0, 0)
		})
	}
}
