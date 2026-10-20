package httpapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type groupAssignmentFixture struct {
	adminBalanceFixture
	group int64
}

func newGroupAssignmentFixture(t *testing.T) groupAssignmentFixture {
	t.Helper()
	f := groupAssignmentFixture{adminBalanceFixture: newAdminBalanceFixture(t)}
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name,discount_percent) VALUES('assignment-group',20) RETURNING id").Scan(&f.group); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f groupAssignmentFixture) groupCall(method, key string, body any, bearer string) *httptest.ResponseRecorder {
	headers := map[string]string{"Idempotency-Key": key}
	cookie := f.cookie
	if bearer != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + bearer
	}
	return requestFrom(f.h, method, fmt.Sprintf("/api/v1/admin/customers/%d/group", f.customer), body, "192.0.2.87:5678", cookie, headers)
}

func (f groupAssignmentFixture) assertMembership(t *testing.T, version int64, group any, audits, receipts int) {
	t.Helper()
	var actualVersion int64
	var actualGroup *int64
	var actualAudits, actualReceipts int
	err := f.db.Pool.QueryRow(t.Context(), `SELECT v.version,m.group_id,
 (SELECT count(*) FROM audit_events WHERE action='customer_group.assigned' AND entity_id=$1::bigint::text),
 (SELECT count(*) FROM mutation_operations WHERE scope='admin:customer_group.assign:'||$1::bigint::text)
 FROM customer_group_membership_versions v LEFT JOIN customer_group_members m ON m.customer_id=v.customer_id WHERE v.customer_id=$1`, f.customer).Scan(&actualVersion, &actualGroup, &actualAudits, &actualReceipts)
	if err != nil || actualVersion != version || actualAudits != audits || actualReceipts != receipts {
		t.Fatalf("version=%d audits=%d receipts=%d error=%v", actualVersion, actualAudits, actualReceipts, err)
	}
	if group == nil && actualGroup != nil || group != nil && (actualGroup == nil || *actualGroup != group.(int64)) {
		t.Fatal("membership differs from committed intention", actualGroup, group)
	}
}

func TestAdminCustomerGroupAssignmentExplicitInputAndHistoricalReplay(t *testing.T) {
	f := newGroupAssignmentFixture(t)
	current := f.groupCall("GET", "", nil, "")
	expect(t, current, 200)
	if got := data(t, current); len(got) != 3 || got["group_membership_version"] != float64(1) || got["group_id"] != nil || got["customer_id"] != float64(f.customer) {
		t.Fatal("current membership projection is incomplete", got)
	}
	if current.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("membership projection can be cached")
	}
	policyErrorCode(t, f.groupCall("PUT", "group-assignment-missing-field", map[string]any{"expected_version": 1}, ""), 400, "invalid_group")
	policyErrorCode(t, f.groupCall("PUT", "group-assignment-missing-version", map[string]any{"group_id": nil}, ""), 409, "group_membership_version_required")
	policyErrorCode(t, f.groupCall("PUT", "", map[string]any{"group_id": nil, "expected_version": 1}, ""), 400, "invalid_idempotency_key")
	for _, group := range []any{0, -1, "1", 1.2, 999999} {
		policyErrorCode(t, f.groupCall("PUT", "group-assignment-invalid-group", map[string]any{"group_id": group, "expected_version": 1}, ""), 400, "invalid_group")
	}
	expect(t, f.groupCall("PUT", "group-assignment-extra-input", map[string]any{"group_id": nil, "expected_version": 1, "sales_version": 1}, ""), 400)
	f.assertMembership(t, 1, nil, 0, 0)
	path := fmt.Sprintf("/api/v1/admin/customers/%d/group", f.customer)
	r := httptest.NewRequest("PUT", path, strings.NewReader(fmt.Sprintf(`{"group_id":%d,"expected_version":1}`, f.group)))
	r.Header.Set("Cookie", f.cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	r.Header.Add("Idempotency-Key", "group-duplicate-key")
	r.Header.Add("Idempotency-Key", "another-group-duplicate-key")
	duplicate := httptest.NewRecorder()
	f.h.ServeHTTP(duplicate, r)
	policyErrorCode(t, duplicate, 400, "invalid_idempotency_key")
	body := map[string]any{"group_id": f.group, "expected_version": 1}
	assigned := f.groupCall("PUT", "group-assignment-first", body, "")
	expect(t, assigned, 200)
	if got := data(t, assigned); got["group_membership_version"] != float64(2) || got["replayed"] != false {
		t.Fatal("assignment did not advance its independent version", got)
	}
	// The same-group write has a distinct, audited intent but no new membership
	// fact. Sales/profile concurrency domains are not consumed by either write.
	expect(t, f.groupCall("PUT", "group-assignment-no-change", map[string]any{"group_id": f.group, "expected_version": 2}, ""), 200)
	f.assertMembership(t, 2, f.group, 2, 2)
	policyErrorCode(t, f.groupCall("PUT", "group-assignment-stale", map[string]any{"group_id": nil, "expected_version": 1}, ""), 409, "version_conflict")
	policyErrorCode(t, f.groupCall("PUT", "group-assignment-first", map[string]any{"group_id": nil, "expected_version": 1}, ""), 409, "idempotency_conflict")
	expect(t, f.groupCall("PUT", "group-assignment-clear", map[string]any{"group_id": nil, "expected_version": 2}, ""), 200)
	// Simulate an unknown prior response by discarding its result and replaying
	// its exact request after a later clear. It must retain the old result.
	replayed := f.groupCall("PUT", "group-assignment-first", body, "")
	expect(t, replayed, 200)
	if got := data(t, replayed); got["group_id"] != float64(f.group) || got["group_membership_version"] != float64(2) || got["replayed"] != true {
		t.Fatal("historical replay recomputed current membership", got)
	}
	f.assertMembership(t, 3, nil, 3, 3)
	var salesVersion int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT sales_version FROM users WHERE id=$1", f.customer).Scan(&salesVersion); err != nil || salesVersion != 1 {
		t.Fatal("group assignment changed sales concurrency", salesVersion, err)
	}
}

func TestAdminCustomerGroupAssignmentConcurrentReplayAndAuditRollback(t *testing.T) {
	f := newGroupAssignmentFixture(t)
	body := map[string]any{"group_id": f.group, "expected_version": 1}
	const key = "group-assignment-concurrent"
	results := make(chan *httptest.ResponseRecorder, 8)
	var workers sync.WaitGroup
	for range 8 {
		workers.Go(func() { results <- f.groupCall("PUT", key, body, "") })
	}
	workers.Wait()
	close(results)
	fresh := 0
	for out := range results {
		expect(t, out, 200)
		if data(t, out)["replayed"] == false {
			fresh++
		}
	}
	if fresh != 1 {
		t.Fatal("concurrent key did not create exactly one intent", fresh)
	}
	f.assertMembership(t, 2, f.group, 1, 1)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_assignment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer_group.assigned' THEN RAISE EXCEPTION 'injected membership audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_assignment_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_assignment_audit()`); err != nil {
		t.Fatal(err)
	}
	body = map[string]any{"group_id": nil, "expected_version": 2}
	expect(t, f.groupCall("PUT", "group-assignment-audit-rollback", body, ""), 500)
	f.assertMembership(t, 2, f.group, 1, 1)
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_assignment_audit ON audit_events; DROP FUNCTION reject_assignment_audit()"); err != nil {
		t.Fatal(err)
	}
	expect(t, f.groupCall("PUT", "group-assignment-audit-rollback", body, ""), 200)
	f.assertMembership(t, 3, nil, 2, 2)
}

func TestAdminCustomerGroupAssignmentExactScopeAndMinimalPermission(t *testing.T) {
	f := newGroupAssignmentFixture(t)
	contentRole(t, f.db, f.actor, "customer_groups.manage")
	expect(t, f.groupCall("GET", "", nil, ""), 200)
	secret := f.apiKey(t)
	for name, ctx := range map[string]context.Context{
		"missing":       t.Context(),
		"empty_session": store.WithStorageCredential(t.Context(), "", false),
		"empty_api_key": store.WithStorageCredential(t.Context(), "", true),
		"wrong_session": store.WithStorageCredential(t.Context(), tokenHash(cookieValue(f.customerAuth)), false),
		"wrong_kind":    store.WithStorageCredential(t.Context(), tokenHash(cookieValue(f.cookie)), true),
	} {
		var failure *store.Conflict
		if _, err := f.db.AdminCustomerGroupMembership(ctx, f.actor, f.customer); !errors.As(err, &failure) || failure.Code != "unauthorized" {
			t.Fatalf("group read accepted %s credential binding: %v", name, err)
		}
		if _, err := f.db.AssignAdminCustomerGroup(ctx, f.actor, f.customer, "group-store-real-credential", store.AdminCustomerGroupAssignmentInput{GroupID: &f.group, GroupIDPresent: true, ExpectedVersion: 1}); !errors.As(err, &failure) || failure.Code != "unauthorized" {
			t.Fatalf("group mutation accepted %s credential binding: %v", name, err)
		}
	}
	if _, err := f.db.AdminCustomerGroupMembership(store.WithStorageCredential(t.Context(), tokenHash(secret), true), f.actor, f.customer); err != nil {
		t.Fatal("real API key cannot read its permitted group context", err)
	}
	body := map[string]any{"group_id": f.group, "expected_version": 1}
	expect(t, f.groupCall("PUT", "group-assignment-api-key", body, secret), 200)
	// Receipts are actor scoped; a second authorized administrator is a new
	// intent, rather than access to somebody else's original request result.
	_, other := user(t, f.db, f.h, "group-assignment-other", "admin")
	expect(t, request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/group", f.customer), body, other, "group-assignment-api-key"), 409)
	for _, customer := range []int64{f.actor, 999999} {
		expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/admin/customers/%d/group", customer), nil, f.cookie, ""), 404)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "PUT"} {
		expect(t, f.groupCall(method, "group-assignment-api-key", body, secret), 404)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='all' WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	contentRole(t, f.db, f.actor, "customers.manage")
	for _, method := range []string{"GET", "PUT"} {
		expect(t, f.groupCall(method, "group-assignment-api-key", body, secret), 403)
	}
	f.assertMembership(t, 2, f.group, 1, 1)
}

func TestAdminCustomerGroupAssignmentCredentialRevocationAfterReceiptWait(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newGroupAssignmentFixture(t)
			const key = "group-assignment-revoked-wait"
			body := map[string]any{"group_id": f.group, "expected_version": 1}
			expect(t, f.groupCall("PUT", key, body, ""), 200)
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:admin:customer_group.assign:%d:%s", f.actor, f.customer, key)); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.groupCall("PUT", key, body, secret) }()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			if secret == "" {
				_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie)))
			} else {
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(secret))
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertMembership(t, 2, f.group, 1, 1)
		})
	}
}

func TestAdminCustomerGroupAssignmentCredentialExpiryAfterWait(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newGroupAssignmentFixture(t)
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT version FROM customer_group_membership_versions WHERE customer_id=$1 FOR UPDATE", f.customer); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.groupCall("PUT", "group-assignment-expiry-wait", map[string]any{"group_id": f.group, "expected_version": 1}, secret)
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
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
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			waitUploadBlockedBy(t, f.db, credentialLock.Conn().PgConn().PID())
			waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
			if err = credentialLock.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertMembership(t, 1, nil, 0, 0)
		})
	}
}

func TestAdminCustomerGroupAssignmentDoesNotUpgradeCustomerOrderLock(t *testing.T) {
	f := newMutationOrderFixture(t)
	group := purchasePricingGroup(t, f, "assignment-order-lock", 0)
	in := adminOrderQuoted(t, f, adminOrderDraft(t, f), f.root)
	blocker, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108206)"); err != nil {
		t.Fatal(err)
	}
	order := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		order <- request(f.h, "POST", "/api/v1/admin/orders", in, f.root, "assignment-order-lock-order")
	}()
	waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
	// The real order now holds users FOR SHARE and waits for pricing. The
	// assignment must not subsequently need users FOR UPDATE behind that order.
	assignment := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		assignment <- request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/group", f.customerID), map[string]any{"group_id": group, "expected_version": 1}, f.root, "assignment-order-lock-group")
	}()
	waitQueryLock(t, f.db, "SELECT pg_advisory_xact_lock(70108206)")
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, order), 201)
	expect(t, awaitBalanceResponse(t, assignment), 200)
}

func TestAdminCustomerGroupAssignmentRecoversLostCommitAcknowledgement(t *testing.T) {
	f := newGroupAssignmentFixture(t)
	var commits atomic.Int32
	cfg := f.db.Pool.Config().Copy()
	cfg.ConnConfig.BuildFrontend = func(reader io.Reader, writer io.Writer) *pgproto3.Frontend {
		return pgproto3.NewFrontend(&uploadCommitAckReader{reader: reader, count: &commits, dropAt: 1}, writer)
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := &store.Store{Pool: pool}
	defer uncertain.Close()
	ctx := store.WithStorageCredential(t.Context(), tokenHash(cookieValue(f.cookie)), false)
	const key = "group-assignment-lost-commit"
	in := store.AdminCustomerGroupAssignmentInput{GroupID: &f.group, GroupIDPresent: true, ExpectedVersion: 1}
	_, err = uncertain.AssignAdminCustomerGroup(ctx, f.actor, f.customer, key, in)
	if err == nil || commits.Load() != 1 {
		t.Fatal("fixture did not lose the real commit acknowledgement", err, commits.Load())
	}
	f.assertMembership(t, 2, f.group, 1, 1)
	replayed := f.groupCall("PUT", key, map[string]any{"group_id": f.group, "expected_version": 1}, "")
	expect(t, replayed, 200)
	if got := data(t, replayed); got["group_membership_version"] != float64(2) || got["replayed"] != true {
		t.Fatal("unknown commit could not be recovered by its exact intention", got)
	}
	f.assertMembership(t, 2, f.group, 1, 1)
}

func TestAdminCustomerGroupAssignmentRechecksPolicyAndScopeAfterWaits(t *testing.T) {
	for _, waiting := range []string{"policy", "membership"} {
		t.Run(waiting, func(t *testing.T) {
			f := newGroupAssignmentFixture(t)
			if waiting == "membership" {
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned' WHERE id=$1", f.actor); err != nil {
					t.Fatal(err)
				}
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", f.actor, f.customer); err != nil {
					t.Fatal(err)
				}
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if waiting == "policy" {
				_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)")
			} else {
				_, err = blocker.Exec(t.Context(), "SELECT version FROM customer_group_membership_versions WHERE customer_id=$1 FOR UPDATE", f.customer)
			}
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.groupCall("PUT", "group-assignment-post-wait-policy", map[string]any{"group_id": f.group, "expected_version": 1}, "")
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			status := 403
			if waiting == "policy" {
				_, err = blocker.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'customer_groups.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
			} else {
				// An intentionally raw writer exercises the final scope predicate.
				// Normal owner assignment additionally serializes customer-sales.
				status = 404
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", f.customer)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), status)
			f.assertMembership(t, 1, nil, 0, 0)
		})
	}
}

func TestAdminCustomerGroupAssignmentPasswordRotationLockOrder(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newGroupAssignmentFixture(t)
			original := f.cookie
			login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
			expect(t, login, 200)
			cookie := login.Result().Cookies()[0]
			f.cookie = cookie.Name + "=" + cookie.Value
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
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
				rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-group-assignment-password"}, original, "admin")
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				done <- f.groupCall("PUT", "group-assignment-password-wait", map[string]any{"group_id": f.group, "expected_version": 1}, secret)
			}()
			waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, rotation), 200)
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertMembership(t, 1, nil, 0, 0)
		})
	}
}

func TestAdminCustomerGroupAssignmentCredentialExpiryBeforeCommit(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			f := newGroupAssignmentFixture(t)
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('group-assignment-audit-expiry',0))"); err != nil {
				t.Fatal(err)
			}
			if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_assignment_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer_group.assigned' THEN PERFORM pg_advisory_xact_lock(hashtextextended('group-assignment-audit-expiry',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_assignment_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_assignment_audit()`); err != nil {
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
				done <- f.groupCall("PUT", "group-assignment-expiring-write", map[string]any{"group_id": f.group, "expected_version": 1}, secret)
			}()
			waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
			waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
			if err = blocker.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertMembership(t, 1, nil, 0, 0)
		})
	}
}

func TestAdminCustomerGroupAssignmentCreationPasswordPricingLockOrder(t *testing.T) {
	f := newGroupAssignmentFixture(t)
	login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
	expect(t, login, 200)
	cookie := login.Result().Cookies()[0]
	rotationCookie := cookie.Name + "=" + cookie.Value
	blocker, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(context.Background())
	if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108206)"); err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(t.Context(), "SELECT id FROM customer_groups WHERE id=$1 FOR UPDATE", f.group); err != nil {
		t.Fatal(err)
	}
	creation := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		creation <- request(f.h, "POST", "/api/v1/admin/customers", map[string]any{"username": "assignment-lock-created", "email": "assignment-lock-created@example.test", "password": "customer-password", "group_id": f.group}, f.cookie, "group-assignment-create-wait")
	}()
	// Creation already owns its original credential and is paused before it
	// requests pricing. Password owns the account and waits for that credential.
	waitQueryLock(t, f.db, "SELECT id FROM customer_groups WHERE id=$1 FOR SHARE")
	rotation := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-creation-group-password"}, rotationCookie, "admin")
	}()
	waitQueryLock(t, f.db, "DELETE FROM sessions WHERE user_id=$1 AND ($2='' OR token_hash<>$2)")
	assignment := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		assignment <- f.groupCall("PUT", "group-assignment-create-password", map[string]any{"group_id": f.group, "expected_version": 1}, "")
	}()
	// Assignment must wait for the administrator before owning or queuing for
	// pricing. Otherwise releasing this blocker forms a three-transaction cycle.
	waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
	if err = blocker.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	expect(t, awaitBalanceResponse(t, creation), 201)
	expect(t, awaitBalanceResponse(t, rotation), 200)
	expect(t, awaitBalanceResponse(t, assignment), 401)
	f.assertMembership(t, 1, nil, 0, 0)
}
