package httpapi

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"
)

type salesWriteFixture struct {
	db                     *store.Store
	h                      http.Handler
	actor, customer, owner int64
	cookie, ownerCookie    string
}

func newSalesWriteFixture(t *testing.T) salesWriteFixture {
	t.Helper()
	db, h := testServer(t)
	f := salesWriteFixture{db: db, h: h}
	f.actor, f.cookie = user(t, db, h, "sales-write-root", "super_admin")
	f.customer, _ = user(t, db, h, "sales-write-customer", "customer")
	f.owner, f.ownerCookie = user(t, db, h, "sales-write-owner", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET sales_enabled=true WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f salesWriteFixture) call(key string, owner any, version int64) *httptest.ResponseRecorder {
	return request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", f.customer), map[string]any{"sales_owner_id": owner, "expected_version": version}, f.cookie, key)
}

func (f salesWriteFixture) assertState(t *testing.T, owner any, version int64, audits, receipts int) {
	t.Helper()
	var current *int64
	var gotVersion int64
	var gotAudits, gotReceipts int
	err := f.db.Pool.QueryRow(t.Context(), `SELECT sales_owner_id,sales_version,
 (SELECT count(*) FROM audit_events WHERE action='customer.sales_assigned' AND entity_id=$1::bigint::text),
 (SELECT count(*) FROM mutation_operations WHERE scope=$2) FROM users WHERE id=$1`, f.customer, fmt.Sprintf("admin:customer.sales_owner:%d", f.customer)).Scan(&current, &gotVersion, &gotAudits, &gotReceipts)
	matching := current == nil && owner == nil
	if expected, ok := owner.(int64); ok {
		matching = current != nil && *current == expected
	}
	if err != nil || !matching || gotVersion != version || gotAudits != audits || gotReceipts != receipts {
		t.Fatalf("owner=%v version=%d audits=%d receipts=%d err=%v", current, gotVersion, gotAudits, gotReceipts, err)
	}
}

func TestCustomerSalesAssignmentUnifiedReplayAndCAS(t *testing.T) {
	f := newSalesWriteFixture(t)
	const key = "sales-write-original"
	first := f.call(key, f.owner, 1)
	expect(t, first, 200)
	if got := data(t, first); got["replayed"] != false || got["sales_version"] != float64(2) {
		t.Fatal("initial result lacks committed version", first.Body.String())
	}
	expect(t, f.call("", nil, 2), 200) // legacy clients retain CAS semantics.
	replay := f.call(key, f.owner, 1)
	expect(t, replay, 200)
	if got := data(t, replay); got["replayed"] != true || got["sales_version"] != float64(2) || got["sales_owner_id"] != float64(f.owner) {
		t.Fatal("historical result was replaced by current state", replay.Body.String())
	}
	for _, changed := range []*httptest.ResponseRecorder{f.call(key, nil, 1), f.call(key, f.owner, 2)} {
		expect(t, changed, 409)
		if errorData(t, changed)["code"] != "idempotency_conflict" {
			t.Fatal("key did not bind the complete original body", changed.Body.String())
		}
	}
	expect(t, f.call("", f.owner, 1), 409)
	// Losing eligibility later does not invalidate historical evidence.
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET sales_enabled=false WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call(key, f.owner, 1), 200)
	f.assertState(t, nil, 3, 2, 1)
	// The same key in a different administrator's actor domain is independent.
	_, other := user(t, f.db, f.h, "sales-write-other-root", "super_admin")
	f.cookie = other
	expect(t, f.call(key, nil, 3), 200)
	f.assertState(t, nil, 4, 3, 2)
}

func TestCustomerSalesAssignmentStrictHeadersAndStoreBoundary(t *testing.T) {
	f := newSalesWriteFixture(t)
	path := fmt.Sprintf("/api/v1/admin/customers/%d/sales-owner", f.customer)
	for _, key := range []string{"short", strings.Repeat("x", 129), " padded-key "} {
		out := f.call(key, f.owner, 1)
		expect(t, out, 400)
		if errorData(t, out)["code"] != "invalid_idempotency_key" {
			t.Fatal(out.Body.String())
		}
	}
	r := httptest.NewRequest("PUT", path, strings.NewReader(fmt.Sprintf(`{"sales_owner_id":%d,"expected_version":1}`, f.owner)))
	r.Header.Set("Cookie", f.cookie)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "zjmfgo")
	r.Header.Set("Origin", "http://localhost:3088")
	r.Header.Add("Idempotency-Key", "sales-header-first")
	r.Header.Add("Idempotency-Key", "sales-header-second")
	out := httptest.NewRecorder()
	f.h.ServeHTTP(out, r)
	expect(t, out, 400)
	for _, ctx := range []context.Context{t.Context(), store.WithStorageCredential(t.Context(), "", false), store.WithStorageCredential(t.Context(), tokenHash(cookieValue(f.cookie)), true)} {
		if _, err := f.db.AssignCustomerSalesOwnerWithKey(ctx, f.actor, f.customer, 1, &f.owner, "sales-store-boundary"); err == nil {
			t.Fatal("Store granted a browser-only capability without its real session")
		}
	}
	_, staff := user(t, f.db, f.h, "sales-write-ineligible-actor", "admin")
	ctx := store.WithStorageCredential(t.Context(), tokenHash(cookieValue(staff)), false)
	if _, err := f.db.AssignCustomerSalesOwnerWithKey(ctx, f.actor, f.customer, 1, &f.owner, "sales-wrong-owner-session"); err == nil {
		t.Fatal("Store accepted another administrator's session")
	}
	f.assertState(t, nil, 1, 0, 0)
}

func TestCustomerSalesAssignmentConcurrentReplayAndAuditRollback(t *testing.T) {
	f := newSalesWriteFixture(t)
	var wg sync.WaitGroup
	results := make(chan *httptest.ResponseRecorder, 8)
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- f.call("sales-write-concurrent", f.owner, 1) }()
	}
	wg.Wait()
	close(results)
	replays := 0
	for out := range results {
		expect(t, out, 200)
		if data(t, out)["replayed"] == true {
			replays++
		}
	}
	if replays != 7 {
		t.Fatal("identical concurrent requests did not converge", replays)
	}
	f.assertState(t, f.owner, 2, 1, 1)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_sales_write_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer.sales_assigned' THEN RAISE EXCEPTION 'injected sales audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_sales_write_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_sales_write_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call("sales-write-rollback", nil, 2), 500)
	f.assertState(t, f.owner, 2, 1, 1)
	if _, err := f.db.Pool.Exec(t.Context(), "DROP TRIGGER reject_sales_write_audit ON audit_events; DROP FUNCTION reject_sales_write_audit()"); err != nil {
		t.Fatal(err)
	}
	expect(t, f.call("sales-write-rollback", nil, 2), 200)
	f.assertState(t, nil, 3, 2, 2)
}

func TestCustomerSalesAssignmentWaitingRevocation(t *testing.T) {
	for _, replay := range []bool{false, true} {
		for _, scenario := range []string{"session_revoked", "administrator_disabled", "super_revoked"} {
			t.Run(fmt.Sprintf("replay_%t/%s", replay, scenario), func(t *testing.T) {
				f := newSalesWriteFixture(t)
				const key = "sales-write-waiting"
				if replay {
					expect(t, f.call(key, f.owner, 1), 200)
				}
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				// The global management wait is before actor/session locks,
				// proving current authority beyond the HTTP middleware.
				if _, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108190)"); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.call(key, f.owner, 1) }()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				status := 401
				switch scenario {
				case "session_revoked":
					_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.cookie)))
				case "administrator_disabled":
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", f.actor)
				case "super_revoked":
					status = 403
					_, err = f.db.Pool.Exec(t.Context(), "UPDATE administrators SET role='admin',admin_role_id=(SELECT admin_role_id FROM administrators WHERE id=$2) WHERE id=$1", f.actor, f.owner)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				expect(t, awaitBalanceResponse(t, done), status)
				if replay {
					f.assertState(t, f.owner, 2, 1, 1)
				} else {
					f.assertState(t, nil, 1, 0, 0)
				}
			})
		}
	}
}

func TestCustomerSalesAssignmentOwnerWaitAndCommitExpiry(t *testing.T) {
	for _, boundary := range []string{"credential_wait", "owner_wait", "audit_wait"} {
		t.Run(boundary, func(t *testing.T) {
			f := newSalesWriteFixture(t)
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if boundary == "audit_wait" {
				if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(740981541)"); err != nil {
					t.Fatal(err)
				}
				if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_sales_write_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='customer.sales_assigned' THEN PERFORM pg_advisory_xact_lock(740981541); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_sales_write_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_sales_write_audit()`); err != nil {
					t.Fatal(err)
				}
			} else if boundary == "owner_wait" {
				if _, err = gate.Exec(t.Context(), "SELECT id FROM administrators WHERE id=$1 FOR UPDATE", f.owner); err != nil {
					t.Fatal(err)
				}
			}
			hash := tokenHash(cookieValue(f.cookie))
			if _, err = f.db.Pool.Exec(t.Context(), "UPDATE sessions SET expires_at=clock_timestamp()+interval '1 second' WHERE token_hash=$1", hash); err != nil {
				t.Fatal(err)
			}
			if boundary == "credential_wait" {
				if _, err = gate.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", hash); err != nil {
					t.Fatal(err)
				}
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.call("sales-write-expiry", f.owner, 1) }()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			waitBalanceCredentialExpiry(t, adminBalanceFixture{db: f.db, cookie: f.cookie}, "")
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertState(t, nil, 1, 0, 0)
		})
	}
}

func TestCustomerSalesAssignmentPasswordRotationLockOrder(t *testing.T) {
	for _, account := range []string{"actor", "proposed_owner"} {
		t.Run(account, func(t *testing.T) {
			f := newSalesWriteFixture(t)
			original := f.cookie
			if account == "actor" {
				login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "sales-write-root", "password": "integration-password", "portal": "admin"}, "", "")
				expect(t, login, 200)
				cookie := login.Result().Cookies()[0]
				f.cookie = cookie.Name + "=" + cookie.Value
			} else {
				original = f.ownerCookie
			}
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(cookieValue(original))); err != nil {
				t.Fatal(err)
			}
			rotation := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-sales-write-password"}, original, "admin")
			}()
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.call("sales-write-rotation", f.owner, 1) }()
			if account == "actor" {
				waitQueryLock(t, f.db, "SELECT role,status FROM administrators WHERE id=$1 FOR SHARE")
			} else {
				waitQueryLock(t, f.db, "SELECT id FROM administrators WHERE id=$1 AND role='admin' FOR SHARE")
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, rotation), 200)
			if account == "actor" {
				expect(t, awaitBalanceResponse(t, done), 401)
				f.assertState(t, nil, 1, 0, 0)
			} else {
				expect(t, awaitBalanceResponse(t, done), 200)
				f.assertState(t, f.owner, 2, 1, 1)
			}
		})
	}
}

func TestCustomerSalesAssignmentProfilePasswordThreeWayLockOrder(t *testing.T) {
	for _, account := range []string{"actor", "proposed_owner"} {
		t.Run(account, func(t *testing.T) {
			f := newSalesWriteFixture(t)
			username, rotatingID, rotationCookie := "sales-write-root", f.actor, f.cookie
			if account == "proposed_owner" {
				username, rotatingID, rotationCookie = "sales-write-owner", f.owner, f.ownerCookie
			}
			login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": username, "password": "integration-password", "portal": "admin"}, "", "")
			expect(t, login, 200)
			cookie := login.Result().Cookies()[0]
			profileCookie := cookie.Name + "=" + cookie.Value
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.customer); err != nil {
				t.Fatal(err)
			}
			assignment := make(chan *httptest.ResponseRecorder, 1)
			go func() { assignment <- f.call("sales-write-three-way", f.owner, 1) }()
			// Assignment owns customer-sales and is paused before the customer row.
			// It must already own both actual actor and proposed owner SHARE locks.
			waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
			profile := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				profile <- request(f.h, "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", f.customer), map[string]any{"display_name": "Profile survived assignment", "expected_version": 1}, profileCookie, "sales-profile-three-way")
			}()
			// The existing profile path owns a different session and waits for
			// customer-sales. Rotation will have to revoke this exact credential.
			waitQueryLock(t, f.db, "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))")
			rotation := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-sales-three-way-password"}, rotationCookie, "admin")
			}()
			// Rotation must wait before taking the account lock. Otherwise sales
			// would wait for that account, rotation for profile's credential, and
			// profile for sales: the cycle also exists when that account is owner.
			waitQueryLock(t, f.db, "SELECT password_hash FROM administrators WHERE id=$1 AND status='active' FOR UPDATE")
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, assignment), 200)
			expect(t, awaitBalanceResponse(t, profile), 200)
			expect(t, awaitBalanceResponse(t, rotation), 200)
			f.assertState(t, f.owner, 2, 1, 1)
			var name string
			var version, profileAudits, passwordAudits int64
			if err = f.db.Pool.QueryRow(t.Context(), `SELECT u.display_name,p.version,
 (SELECT count(*) FROM audit_events WHERE action='customer.updated' AND entity_id=$1::bigint::text),
 (SELECT count(*) FROM audit_events WHERE action='customer.password_changed' AND entity_id=$2::bigint::text)
 FROM users u JOIN customer_profiles p ON p.customer_id=u.id WHERE u.id=$1`, f.customer, rotatingID).Scan(&name, &version, &profileAudits, &passwordAudits); err != nil || name != "Profile survived assignment" || version != 2 || profileAudits != 1 || passwordAudits != 1 {
				t.Fatal("three real transactions did not commit exactly once", name, version, profileAudits, passwordAudits, err)
			}
			expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, profileCookie, "admin"), 401)
			expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, rotationCookie, "admin"), 200)
		})
	}
}

func TestCustomerSalesAssignmentRecoversLostCommitAcknowledgement(t *testing.T) {
	f := newSalesWriteFixture(t)
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
	const key = "sales-write-lost-commit"
	if _, err = uncertain.AssignCustomerSalesOwnerWithKey(ctx, f.actor, f.customer, 1, &f.owner, key); err == nil || commits.Load() != 1 {
		t.Fatal("fixture did not lose a real COMMIT acknowledgement", err, commits.Load())
	}
	f.assertState(t, f.owner, 2, 1, 1)
	out := f.call(key, f.owner, 1)
	expect(t, out, 200)
	if data(t, out)["replayed"] != true || data(t, out)["sales_version"] != float64(2) {
		t.Fatal("original body/key did not recover its committed receipt", out.Body.String())
	}
	f.assertState(t, f.owner, 2, 1, 1)
}
