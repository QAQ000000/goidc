package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"golang.org/x/crypto/bcrypt"
)

// Creation already owns pricing before it waits for its selected group. The
// pricing writer waits for creation, and rotation then waits for their account.
// Observe actual blockers rather than relying on PostgreSQL's lock queue order.
func waitPricingCreationRotationChain(t *testing.T, db *store.Store, blocker uint32, includeRotation bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 7*time.Second)
	defer cancel()
	for {
		var ready bool
		err := db.Pool.QueryRow(ctx, `WITH creators AS (
 SELECT pid FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock'
 AND query='SELECT id FROM customer_groups WHERE id=$1 FOR SHARE'
 AND $1::integer=ANY(pg_blocking_pids(pid))
), writers AS (
 SELECT w.pid FROM pg_stat_activity w JOIN creators c ON c.pid=ANY(pg_blocking_pids(w.pid))
 WHERE w.datname=current_database() AND w.wait_event_type='Lock'
 AND w.query='SELECT pg_advisory_xact_lock(70108206)'
)
SELECT EXISTS(SELECT 1 FROM creators) AND EXISTS(SELECT 1 FROM writers)
 AND (NOT $2::boolean OR EXISTS(
 SELECT 1 FROM pg_stat_activity r WHERE r.datname=current_database() AND r.wait_event_type='Lock'
 AND r.query='SELECT password_hash FROM administrators WHERE id=$1 AND status=''active'' FOR UPDATE'
 AND (EXISTS(SELECT 1 FROM creators c WHERE c.pid=ANY(pg_blocking_pids(r.pid)))
   OR EXISTS(SELECT 1 FROM writers w WHERE w.pid=ANY(pg_blocking_pids(r.pid))))
))`, blocker, includeRotation).Scan(&ready)
		if err != nil {
			t.Fatal(err)
		}
		if ready {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("fixture -> creation/group -> writer/pricing with account rotation was not observed", includeRotation)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func assertPricingCreationRotationFacts(t *testing.T, f adminBalanceFixture, username, key string, group int64, retainedCookie, password string) {
	t.Helper()
	var created, creationAudits, receipts, passwordAudits, memberships, sessions int
	var retained, revoked bool
	var hash string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM users WHERE username=$2 AND role='customer'),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='customer.created' AND entity_id=(SELECT id::text FROM users WHERE username=$2)),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope='admin:customer.create' AND idempotency_key=$3),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='customer.password_changed'),
 (SELECT count(*) FROM customer_group_members WHERE customer_id=(SELECT id FROM users WHERE username=$2) AND group_id=$4),
 (SELECT count(*) FROM sessions WHERE user_id=$1),
 EXISTS(SELECT 1 FROM sessions WHERE user_id=$1 AND token_hash=$5),
 NOT EXISTS(SELECT 1 FROM api_keys WHERE user_id=$1 AND revoked_at IS NULL),
 password_hash FROM administrators WHERE id=$1`, f.actor, username, key, group, tokenHash(cookieValue(retainedCookie))).Scan(&created, &creationAudits, &receipts, &passwordAudits, &memberships, &sessions, &retained, &revoked, &hash)
	if err != nil || created != 1 || creationAudits != 1 || receipts != 1 || passwordAudits != 1 || memberships != 1 || sessions != 1 || !retained || !revoked {
		t.Fatal("creation or password rotation did not commit exactly once with credential revocation", created, creationAudits, receipts, passwordAudits, memberships, sessions, retained, revoked, err)
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil || bcrypt.CompareHashAndPassword([]byte(hash), []byte("integration-password")) == nil {
		t.Fatal("password rotation did not preserve the expected final password")
	}
}

// Exercise each distinct pricing-writer entry, including both branches of the
// promo lifecycle. These are authenticated HTTP requests against real PG locks;
// a started goroutine alone is never evidence that an interleaving occurred.
type adminPricingAuthorityFixture struct {
	adminBalanceFixture
	group               int64
	method, path, table string
	scope, action, key  string
	body                any
	success             int
}

func newAdminPricingAuthorityFixture(t *testing.T, operation string) adminPricingAuthorityFixture {
	t.Helper()
	f := adminPricingAuthorityFixture{adminBalanceFixture: newAdminBalanceFixture(t), key: "pricing-authority-mutation", method: "PATCH", success: 200}
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name,discount_percent) VALUES('authority-group',20) RETURNING id").Scan(&f.group); err != nil {
		t.Fatal(err)
	}
	switch operation {
	case "customer_group":
		f.path, f.table = fmt.Sprintf("/api/v1/admin/customer-groups/%d", f.group), "customer_groups"
		f.scope, f.action = fmt.Sprintf("admin:customer_group.update:%d", f.group), "customer_group.updated"
		f.body = map[string]any{"name": "authority-updated", "expected_version": 1}
	case "product_group":
		var id int64
		if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_product_groups(name) VALUES('authority-product-group') RETURNING id").Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.path, f.table = fmt.Sprintf("/api/v1/admin/customer-product-groups/%d", id), "customer_product_groups"
		f.scope, f.action = fmt.Sprintf("admin:customer_product_group.update:%d", id), "customer_product_group.updated"
		f.body = map[string]any{"name": "authority-updated", "product_ids": []int64{}, "expected_version": 1}
	case "promo_create":
		f.method, f.path, f.table, f.success = "POST", "/api/v1/admin/promo-codes", "promo_codes", 201
		f.scope, f.action = "admin:promo_code.create", "promo_code.created"
		f.body = map[string]any{"code": "AUTHORITY-CREATED", "discount_type": "percent", "value": 10}
	case "promo_update":
		var id int64
		if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO promo_codes(code,discount_type,value,created_by) VALUES('AUTHORITY-SEEDED','percent',10,$1) RETURNING id", f.actor).Scan(&id); err != nil {
			t.Fatal(err)
		}
		f.path, f.table = fmt.Sprintf("/api/v1/admin/promo-codes/%d", id), "promo_codes"
		f.scope, f.action = fmt.Sprintf("admin:promo_code.update.%d", id), "promo_code.updated"
		f.body = map[string]any{"notes": "authority-updated", "expected_version": 1}
	default:
		t.Fatalf("unknown pricing writer %q", operation)
	}
	return f
}

func (f adminPricingAuthorityFixture) mutate(secret string) *httptest.ResponseRecorder {
	cookie := f.cookie
	headers := map[string]string{"Idempotency-Key": f.key}
	if secret != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + secret
	}
	return requestFrom(f.h, f.method, f.path, f.body, "192.0.2.87:5678", cookie, headers)
}

func (f adminPricingAuthorityFixture) snapshot(t *testing.T) string {
	t.Helper()
	// Table names come exclusively from the four cases above. Include complete
	// committed business rows, audit events and receipts so a rejected replay
	// cannot conceal any extra version, timestamp, audit or receipt mutation.
	var snapshot string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'rows',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY id),'[]'::jsonb) FROM `+f.table+` r),
 'audits',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a WHERE action=$1),
 'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY idempotency_key),'[]'::jsonb) FROM mutation_operations m WHERE actor_id=$2 AND scope=$3))::text`, f.action, f.actor, f.scope).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func (f adminPricingAuthorityFixture) assertSnapshot(t *testing.T, before string) {
	t.Helper()
	if after := f.snapshot(t); after != before {
		t.Fatalf("rejected pricing mutation changed committed business/audit/receipt facts\nbefore=%s\nafter=%s", before, after)
	}
}

func (f adminPricingAuthorityFixture) assertCommittedMutation(t *testing.T) {
	t.Helper()
	var businessSQL string
	switch f.table {
	case "customer_groups":
		businessSQL = "SELECT count(*) FROM customer_groups WHERE name='authority-updated' AND version=2"
	case "customer_product_groups":
		businessSQL = "SELECT count(*) FROM customer_product_groups g WHERE name='authority-updated' AND version=2 AND NOT EXISTS(SELECT 1 FROM customer_product_group_products p WHERE p.group_id=g.id)"
	case "promo_codes":
		if f.method == "POST" {
			businessSQL = "SELECT count(*) FROM promo_codes WHERE code='AUTHORITY-CREATED' AND version=1"
		} else {
			businessSQL = "SELECT count(*) FROM promo_codes WHERE code='AUTHORITY-SEEDED' AND notes='authority-updated' AND version=2"
		}
	default:
		t.Fatal("unknown pricing business table", f.table)
	}
	var business, audits, receipts, keyedReceipts int
	err := f.db.Pool.QueryRow(t.Context(), "SELECT ("+businessSQL+`),
 (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action=$2),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope=$3),
 (SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope=$3 AND idempotency_key=$4)`, f.actor, f.action, f.scope, f.key).Scan(&business, &audits, &receipts, &keyedReceipts)
	if err != nil || business != 1 || audits != 1 || receipts != 1 || keyedReceipts != 1 {
		t.Fatal("pricing mutation did not commit exactly once", business, audits, receipts, keyedReceipts, err)
	}
}

func (f adminPricingAuthorityFixture) credential(secret string) (table, column, hash string) {
	if secret != "" {
		return "api_keys", "secret_hash", tokenHash(secret)
	}
	return "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
}

func (f adminPricingAuthorityFixture) expireSoon(t *testing.T, secret string) {
	t.Helper()
	table, column, hash := f.credential(secret)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+interval '1 second' WHERE "+column+"=$1", hash); err != nil {
		t.Fatal(err)
	}
}

func pricingAuthorityCases(t *testing.T, replayCases bool, run func(*testing.T, adminPricingAuthorityFixture, string)) {
	t.Helper()
	for _, operation := range []string{"customer_group", "product_group", "promo_create", "promo_update"} {
		for _, credential := range []string{"session", "api_key"} {
			modes := []string{"new"}
			if replayCases {
				modes = append(modes, "replay")
			}
			for _, mode := range modes {
				t.Run(operation+"/"+credential+"/"+mode, func(t *testing.T) {
					f := newAdminPricingAuthorityFixture(t, operation)
					if mode == "replay" {
						expect(t, f.mutate(""), f.success)
					}
					secret := ""
					if credential == "api_key" {
						secret = f.apiKey(t)
					}
					run(t, f, secret)
				})
			}
		}
	}
}

func pricingRotationSession(t *testing.T, f adminPricingAuthorityFixture) string {
	t.Helper()
	login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
	expect(t, login, 200)
	cookie := login.Result().Cookies()[0]
	return cookie.Name + "=" + cookie.Value
}

func TestAdminPricingMutationPasswordRotationLockOrder(t *testing.T) {
	pricingAuthorityCases(t, true, func(t *testing.T, f adminPricingAuthorityFixture, secret string) {
		before := f.snapshot(t)
		rotationCookie := pricingRotationSession(t, f)
		blocker, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(cookieValue(rotationCookie))); err != nil {
			t.Fatal(err)
		}
		rotation := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-pricing-password"}, rotationCookie, "admin")
		}()
		// Password already owns the account and is paused on its retained
		// session. Writer must not own its other session/API key at this point.
		waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
		if err = blocker.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, rotation), 200)
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
}

func TestAdminPricingMutationCreationPasswordLockOrder(t *testing.T) {
	pricingAuthorityCases(t, true, func(t *testing.T, f adminPricingAuthorityFixture, secret string) {
		before := f.snapshot(t)
		var priorReceipts int
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3", f.actor, f.scope, f.key).Scan(&priorReceipts); err != nil || priorReceipts < 0 || priorReceipts > 1 {
			t.Fatal("unexpected initial pricing receipt count", priorReceipts, err)
		}
		rotationCookie := pricingRotationSession(t, f)
		blocker, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer blocker.Rollback(context.Background())
		if _, err = blocker.Exec(t.Context(), "SELECT id FROM customer_groups WHERE id=$1 FOR UPDATE", f.group); err != nil {
			t.Fatal(err)
		}
		creation := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			creation <- request(f.h, "POST", "/api/v1/admin/customers", map[string]any{"username": "pricing-lock-created", "email": "pricing-lock-created@example.test", "password": "customer-password", "group_id": f.group}, f.cookie, "pricing-authority-customer-create")
		}()
		waitQueryLock(t, f.db, "SELECT id FROM customer_groups WHERE id=$1 FOR SHARE")
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		// This also proves creation acquired pricing before the group row.
		// With the old row -> pricing order the writer instead owns pricing,
		// so this exact blocker chain cannot form (and the test must fail).
		waitPricingCreationRotationChain(t, f.db, blocker.Conn().PgConn().PID(), false)
		rotation := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-pricing-password"}, rotationCookie, "admin")
		}()
		// Rotation cannot invalidate either already authorized writer while
		// they retain their shared administrator locks.
		waitPricingCreationRotationChain(t, f.db, blocker.Conn().PgConn().PID(), true)
		if err = blocker.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, creation), 201)
		expect(t, awaitBalanceResponse(t, done), f.success)
		expect(t, awaitBalanceResponse(t, rotation), 200)
		f.assertCommittedMutation(t)
		if priorReceipts == 1 {
			f.assertSnapshot(t, before)
		}
		committed := f.snapshot(t)
		expect(t, f.mutate(secret), 401)
		f.assertSnapshot(t, committed)
		assertPricingCreationRotationFacts(t, f.adminBalanceFixture, "pricing-lock-created", "pricing-authority-customer-create", f.group, rotationCookie, "rotated-pricing-password")
	})
}

func TestAdminPricingMutationCredentialExpiry(t *testing.T) {
	for _, scenario := range []string{"pricing_revoked", "pricing_expiry", "credential_row_expiry", "receipt_expiry", "audit_expiry"} {
		t.Run(scenario, func(t *testing.T) {
			pricingAuthorityCases(t, scenario != "audit_expiry", func(t *testing.T, f adminPricingAuthorityFixture, secret string) {
				before := f.snapshot(t)
				blocker, err := f.db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				switch scenario {
				case "receipt_expiry":
					_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", fmt.Sprintf("mutation:%d:%s:%s", f.actor, f.scope, f.key))
				case "audit_expiry":
					_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('pricing-authority-audit-expiry',0))")
					if err == nil {
						_, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_pricing_authority_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+f.action+`' THEN PERFORM pg_advisory_xact_lock(hashtextextended('pricing-authority-audit-expiry',0)); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_pricing_authority_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_pricing_authority_audit()`)
					}
				default:
					_, err = blocker.Exec(t.Context(), "SELECT pg_advisory_xact_lock(70108206)")
				}
				if err != nil {
					t.Fatal(err)
				}
				if scenario == "receipt_expiry" || scenario == "audit_expiry" {
					// The writer holds its credential by this late wait, so set
					// a future deadline before authentication, then let time pass.
					f.expireSoon(t, secret)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- f.mutate(secret) }()
				waitUploadBlockedBy(t, f.db, blocker.Conn().PgConn().PID())
				table, column, hash := f.credential(secret)
				if scenario == "pricing_revoked" {
					if secret == "" {
						_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", hash)
					} else {
						_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=clock_timestamp() WHERE secret_hash=$1", hash)
					}
					if err != nil {
						t.Fatal(err)
					}
				} else if scenario == "pricing_expiry" || scenario == "credential_row_expiry" {
					f.expireSoon(t, secret)
				}
				if scenario == "credential_row_expiry" {
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
				} else {
					if scenario != "pricing_revoked" {
						waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
					}
					if err = blocker.Commit(t.Context()); err != nil {
						t.Fatal(err)
					}
				}
				expect(t, awaitBalanceResponse(t, done), 401)
				f.assertSnapshot(t, before)
			})
		})
	}
}
