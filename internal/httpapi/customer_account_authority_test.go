package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type customerAccountAuthorityFixture struct {
	adminBalanceFixture
	method, path, operation, scope, action string
	body                                   any
	key                                    string
	success                                int
}

func newCustomerAccountAuthorityFixture(t *testing.T, operation string) customerAccountAuthorityFixture {
	t.Helper()
	f := customerAccountAuthorityFixture{adminBalanceFixture: newAdminBalanceFixture(t), operation: operation, key: "customer-account-authority", success: 200}
	switch operation {
	case "create":
		f.method, f.path, f.scope, f.action, f.success = "POST", "/api/v1/admin/customers", "admin:customer.create", "customer.created", 201
		f.body = map[string]any{"username": "authority-created", "email": "authority-created@example.test", "password": "original-creation-password", "display_name": "Original customer", "company_name": "Original company", "phone": "13800138000", "admin_note": "Original private note"}
	case "update":
		f.method, f.path, f.scope, f.action = "PATCH", fmt.Sprintf("/api/v1/admin/customers/%d", f.customer), fmt.Sprintf("admin:customer.update:%d", f.customer), "customer.updated"
		f.body = map[string]any{"expected_version": 1, "display_name": "Authority changed customer", "status": "disabled", "profile": map[string]any{"admin_note": "Changed private note"}}
	default:
		t.Fatalf("unknown customer mutation %q", operation)
	}
	return f
}

func (f customerAccountAuthorityFixture) mutate(secret string) *httptest.ResponseRecorder {
	cookie := f.cookie
	headers := map[string]string{"Idempotency-Key": f.key}
	if secret != "" {
		cookie = ""
		headers["Authorization"] = "Bearer " + secret
	}
	return requestFrom(f.h, f.method, f.path, f.body, "192.0.2.86:5678", cookie, headers)
}

func (f customerAccountAuthorityFixture) snapshot(t *testing.T) string {
	t.Helper()
	var value string
	err := f.db.Pool.QueryRow(t.Context(), `WITH owners AS (SELECT id FROM users WHERE id=$1 OR username='authority-created')
 SELECT jsonb_build_object(
 'users',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]'::jsonb) FROM users x WHERE id IN (SELECT id FROM owners)),
 'profiles',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY customer_id),'[]'::jsonb) FROM customer_profiles x WHERE customer_id IN (SELECT id FROM owners)),
 'carts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY user_id),'[]'::jsonb) FROM carts x WHERE user_id IN (SELECT id FROM owners)),
 'contacts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]'::jsonb) FROM customer_contacts x WHERE customer_id IN (SELECT id FROM owners)),
 'groups',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY customer_id),'[]'::jsonb) FROM customer_group_members x WHERE customer_id IN (SELECT id FROM owners)),
 'membership_versions',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY customer_id),'[]'::jsonb) FROM customer_group_membership_versions x WHERE customer_id IN (SELECT id FROM owners)),
 'audits',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY id),'[]'::jsonb) FROM audit_events x WHERE action=$2),
 'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY idempotency_key),'[]'::jsonb) FROM mutation_operations x WHERE actor_id=$3 AND scope=$4))::text`, f.customer, f.action, f.actor, f.scope).Scan(&value)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func (f customerAccountAuthorityFixture) assertSnapshot(t *testing.T, before string) {
	t.Helper()
	if after := f.snapshot(t); after != before {
		t.Fatal("rejected customer mutation changed committed account/profile/cart/contact/audit/receipt facts")
	}
}

func (f customerAccountAuthorityFixture) expireSoon(t *testing.T, secret string) {
	t.Helper()
	table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
	if secret != "" {
		table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
	}
	seconds := 1
	if f.operation == "create" {
		// The HTTP create path intentionally hashes with bcrypt cost 12 before
		// entering Store. Under -race this can take several seconds. The
		// credential must remain valid until the actual business lock is seen.
		seconds = 15
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE "+table+" SET expires_at=clock_timestamp()+$2::int*interval '1 second' WHERE "+column+"=$1", hash, seconds); err != nil {
		t.Fatal(err)
	}
}

func (f customerAccountAuthorityFixture) waitBlockedThenNaturallyExpired(t *testing.T, secret string, blocker uint32, done <-chan *httptest.ResponseRecorder) {
	t.Helper()
	if f.operation != "create" {
		waitUploadBlockedBy(t, f.db, blocker)
		waitBalanceCredentialExpiry(t, f.adminBalanceFixture, secret)
		return
	}
	// Observe both the real fixture blocker and the database clock with a
	// bounded wait. Updating expires_at after acquisition would block on the
	// credential's SHARE lock and would not prove natural expiry.
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
	if secret != "" {
		table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
	}
	observed := false
	for {
		var blocked, expired bool
		if err := f.db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity a WHERE datname=current_database() AND $1::integer=ANY(pg_blocking_pids(a.pid))), expires_at<=clock_timestamp() FROM "+table+" WHERE "+column+"=$2", blocker, hash).Scan(&blocked, &expired); err != nil {
			t.Fatal(err)
		}
		if blocked && !observed {
			if expired {
				t.Fatal("create credential expired before its actual business wait was observed")
			}
			observed = true
		}
		if observed && expired {
			if !blocked {
				t.Fatal("create stopped waiting before natural credential expiry")
			}
			return
		}
		select {
		case response := <-done:
			t.Fatalf("create returned before observed business-wait expiry: status=%d body=%s", response.Code, response.Body.String())
		case <-ctx.Done():
			t.Fatalf("create did not reach its fixture lock and naturally expire: observed_block=%t", observed)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func customerAccountAuthorityCases(t *testing.T, replayCases bool, run func(*testing.T, customerAccountAuthorityFixture, string, bool)) {
	t.Helper()
	for _, operation := range []string{"create", "update"} {
		for _, credential := range []string{"session", "api_key"} {
			modes := []bool{false}
			if replayCases {
				modes = append(modes, true)
			}
			for _, replay := range modes {
				t.Run(fmt.Sprintf("%s/%s/replay_%t", operation, credential, replay), func(t *testing.T) {
					f := newCustomerAccountAuthorityFixture(t, operation)
					if replay {
						expect(t, f.mutate(""), f.success)
					}
					secret := ""
					if credential == "api_key" {
						secret = f.apiKey(t)
					}
					run(t, f, secret, replay)
				})
			}
		}
	}
}

func TestCustomerAccountMutationCredentialExpiryAfterReceiptWait(t *testing.T) {
	customerAccountAuthorityCases(t, true, func(t *testing.T, f customerAccountAuthorityFixture, secret string, _ bool) {
		before := f.snapshot(t)
		gate, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background())
		// Block the actual unified receipt, after the original credential has
		// been checked. New requests and historical replays take this same lock.
		lock := fmt.Sprintf("mutation:%d:%s:%s", f.actor, f.scope, f.key)
		if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended($1,0))", lock); err != nil {
			t.Fatal(err)
		}
		f.expireSoon(t, secret)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		f.waitBlockedThenNaturallyExpired(t, secret, gate.Conn().PgConn().PID(), done)
		if err = gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
}

func TestCustomerAccountMutationCredentialExpiryAfterBusinessWait(t *testing.T) {
	customerAccountAuthorityCases(t, false, func(t *testing.T, f customerAccountAuthorityFixture, secret string, _ bool) {
		before := f.snapshot(t)
		gate, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background())
		if f.operation == "create" {
			// Creation can insert the account/cart before it waits for the
			// authoritative custom-field catalogue; expiry must roll them back.
			_, err = gate.Exec(t.Context(), "SELECT version FROM customer_field_catalogue WHERE id=1 FOR UPDATE")
		} else {
			_, err = gate.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.customer)
		}
		if err != nil {
			t.Fatal(err)
		}
		f.expireSoon(t, secret)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		f.waitBlockedThenNaturallyExpired(t, secret, gate.Conn().PgConn().PID(), done)
		if err = gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
	for _, credential := range []string{"session", "api_key"} {
		t.Run("create_group_pricing/"+credential, func(t *testing.T) {
			f := newCustomerAccountAuthorityFixture(t, "create")
			contentRole(t, f.db, f.actor, "customers.view", "customers.manage", "customer_groups.view", "customer_groups.manage", "api_keys.manage")
			var group int64
			if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name,discount_percent) VALUES('customer-create-pricing-expiry',20) RETURNING id").Scan(&group); err != nil {
				t.Fatal(err)
			}
			f.body.(map[string]any)["group_id"] = group
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			before := f.snapshot(t)
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if err := store.GroupPricingWriteLock(t.Context(), gate); err != nil {
				t.Fatal(err)
			}
			f.expireSoon(t, secret)
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.mutate(secret) }()
			f.waitBlockedThenNaturallyExpired(t, secret, gate.Conn().PgConn().PID(), done)
			var pricingWait bool
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_stat_activity a
 WHERE datname=current_database() AND a.query='SELECT pg_advisory_xact_lock(70108206)'
 AND $1::integer=ANY(pg_blocking_pids(a.pid)))`, gate.Conn().PgConn().PID()).Scan(&pricingWait); err != nil || !pricingWait {
				t.Fatal("customer creation did not naturally expire at its explicit pre-group pricing lock", pricingWait, err)
			}
			if err := gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			expect(t, awaitBalanceResponse(t, done), 401)
			f.assertSnapshot(t, before)
		})
	}
}

func TestCustomerAccountMutationCredentialExpiryBeforeCommit(t *testing.T) {
	customerAccountAuthorityCases(t, false, func(t *testing.T, f customerAccountAuthorityFixture, secret string, _ bool) {
		before := f.snapshot(t)
		gate, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background())
		if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(741098542)"); err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Pool.Exec(t.Context(), `CREATE FUNCTION wait_customer_account_authority_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action IN ('customer.created','customer.updated') THEN PERFORM pg_advisory_xact_lock(741098542); END IF; RETURN NEW; END $$; CREATE TRIGGER wait_customer_account_authority_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION wait_customer_account_authority_audit()`); err != nil {
			t.Fatal(err)
		}
		f.expireSoon(t, secret)
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		f.waitBlockedThenNaturallyExpired(t, secret, gate.Conn().PgConn().PID(), done)
		if err = gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
}

func TestCustomerAccountMutationCredentialRowWaitExpiry(t *testing.T) {
	customerAccountAuthorityCases(t, false, func(t *testing.T, f customerAccountAuthorityFixture, secret string, _ bool) {
		before := f.snapshot(t)
		gate, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background())
		f.expireSoon(t, secret)
		table, column, hash := "sessions", "token_hash", tokenHash(cookieValue(f.cookie))
		if secret != "" {
			table, column, hash = "api_keys", "secret_hash", tokenHash(secret)
		}
		if _, err = gate.Exec(t.Context(), "SELECT user_id FROM "+table+" WHERE "+column+"=$1 FOR UPDATE", hash); err != nil {
			t.Fatal(err)
		}
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		f.waitBlockedThenNaturallyExpired(t, secret, gate.Conn().PgConn().PID(), done)
		if err = gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
}

func TestCustomerAccountMutationPasswordRotationLockOrder(t *testing.T) {
	customerAccountAuthorityCases(t, true, func(t *testing.T, f customerAccountAuthorityFixture, secret string, _ bool) {
		before := f.snapshot(t)
		login := request(f.h, "POST", "/api/v1/auth/login", map[string]string{"username": "balance-operation-admin", "password": "integration-password", "portal": "admin"}, "", "")
		expect(t, login, 200)
		cookie := login.Result().Cookies()[0]
		rotationCookie := cookie.Name + "=" + cookie.Value
		gate, err := f.db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer gate.Rollback(context.Background())
		if _, err = gate.Exec(t.Context(), "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE", tokenHash(cookie.Value)); err != nil {
			t.Fatal(err)
		}
		rotation := make(chan *httptest.ResponseRecorder, 1)
		go func() {
			rotation <- portalRequest(f.h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "rotated-customer-authority-password"}, rotationCookie, "admin")
		}()
		waitUploadBlockedBy(t, f.db, gate.Conn().PgConn().PID())
		done := make(chan *httptest.ResponseRecorder, 1)
		go func() { done <- f.mutate(secret) }()
		waitQueryLock(t, f.db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
		if err = gate.Commit(t.Context()); err != nil {
			t.Fatal(err)
		}
		expect(t, awaitBalanceResponse(t, rotation), 200)
		expect(t, awaitBalanceResponse(t, done), 401)
		f.assertSnapshot(t, before)
	})
}

func TestCustomerAccountMutationRequiresOriginalCredential(t *testing.T) {
	f := newCustomerAccountAuthorityFixture(t, "create")
	before := f.snapshot(t)
	_, err := f.db.AdminCreateCustomer(t.Context(), f.actor, store.AdminCreateCustomerInput{}, "customer-unbound-context", strings.Repeat("1", 64))
	if failure, ok := err.(*store.Conflict); !ok || failure.Code != "unauthorized" {
		t.Fatalf("unbound customer creation inherited actor authority: %T %v", err, err)
	}
	_, err = f.db.AdminUpdateCustomer(t.Context(), f.actor, f.customer, store.AdminUpdateCustomerInput{}, "customer-unbound-context")
	if failure, ok := err.(*store.Conflict); !ok || failure.Code != "unauthorized" {
		t.Fatalf("unbound customer update inherited actor authority: %T %v", err, err)
	}
	f.assertSnapshot(t, before)
	for _, credential := range []string{"session", "api_key"} {
		t.Run("deleted_group_historical_replay/"+credential, func(t *testing.T) {
			f := newCustomerAccountAuthorityFixture(t, "create")
			contentRole(t, f.db, f.actor, "customers.view", "customers.manage", "customer_groups.view", "customer_groups.manage", "api_keys.manage")
			var group int64
			if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO customer_groups(name,discount_percent) VALUES('customer-create-deleted-group',20) RETURNING id").Scan(&group); err != nil {
				t.Fatal(err)
			}
			f.body.(map[string]any)["group_id"] = group
			secret := ""
			if credential == "api_key" {
				secret = f.apiKey(t)
			}
			first := f.mutate(secret)
			expect(t, first, 201)
			created := data(t, first)
			id := int64(created["id"].(float64))
			// Use the public management APIs to remove the membership and then
			// its now-unused group. Neither action invalidates an earlier create.
			groupPath := fmt.Sprintf("/api/v1/admin/customers/%d/group", id)
			membership := request(f.h, "GET", groupPath, nil, f.cookie, "")
			expect(t, membership, 200)
			if data(t, membership)["group_id"] != float64(group) {
				t.Fatal("customer creation omitted its requested group", membership.Body.String())
			}
			expect(t, request(f.h, "PUT", groupPath, map[string]any{"group_id": nil, "expected_version": data(t, membership)["group_membership_version"]}, f.cookie, "customer-create-clear-group"), 200)
			expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/admin/customer-groups/%d", group), map[string]any{"expected_version": 1}, f.cookie, "customer-create-delete-group"), 200)
			before := f.snapshot(t)
			gate, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if err := store.GroupPricingWriteLock(t.Context(), gate); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() { done <- f.mutate(secret) }()
			var replay *httptest.ResponseRecorder
			select {
			case replay = <-done:
			case <-time.After(20 * time.Second):
				t.Fatal("committed customer replay waited for mutable pricing state")
			}
			expect(t, replay, 200)
			got := data(t, replay)
			if got["id"] != created["id"] || got["profile_version"] != created["profile_version"] || got["replayed"] != true {
				t.Fatal("deleted group changed the committed customer creation result", replay.Body.String())
			}
			f.assertSnapshot(t, before)
			if err := gate.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
}
