package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type adminPasswordResetFixture struct {
	db                          *store.Store
	h                           http.Handler
	actor, customer             int64
	adminCookie, customerCookie string
	apiKey, secret, recovery    string
	challenge                   string
}

func newAdminPasswordResetFixture(t *testing.T) adminPasswordResetFixture {
	t.Helper()
	db, h := testServer(t)
	f := adminPasswordResetFixture{db: db, h: h}
	f.actor, f.adminCookie = user(t, db, h, "reset-administrator", "admin")
	f.customer, f.customerCookie = user(t, db, h, "reset-customer", "customer")
	enrolled := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, f.customerCookie, "customer")
	expect(t, enrolled, 200)
	f.secret = data(t, enrolled)["secret"].(string)
	f.recovery = data(t, enrolled)["recovery_codes"].([]any)[0].(string)
	expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(f.secret, time.Now())}, f.customerCookie, "customer"), 200)
	created := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "customer-before-admin-reset"}, f.customerCookie, "customer")
	expect(t, created, 201)
	f.apiKey = data(t, created)["secret"].(string)
	login := portalRequest(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "reset-customer@example.test", "password": "integration-password", "portal": "customer"}, "", "customer")
	expect(t, login, 401)
	f.challenge = errorData(t, login)["challenge"].(string)
	return f
}

func (f adminPasswordResetFixture) path() string {
	return fmt.Sprintf("/api/v1/admin/customers/%d/password", f.customer)
}

func (f adminPasswordResetFixture) securitySnapshot(t *testing.T) string {
	t.Helper()
	var snapshot string
	err := f.db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'account',to_jsonb(u),
 'sessions',(SELECT coalesce(jsonb_agg(to_jsonb(s) ORDER BY token_hash),'[]'::jsonb) FROM sessions s WHERE s.user_id=u.id),
 'keys',(SELECT coalesce(jsonb_agg(to_jsonb(k) ORDER BY id),'[]'::jsonb) FROM api_keys k WHERE k.user_id=u.id),
 'challenges',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY token_hash),'[]'::jsonb) FROM mfa_challenges c WHERE c.user_id=u.id),
 'recovery',(SELECT coalesce(jsonb_agg(to_jsonb(r) ORDER BY code_hash),'[]'::jsonb) FROM mfa_recovery_codes r WHERE r.user_id=u.id)
 )::text FROM users u WHERE u.id=$1`, f.customer).Scan(&snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestAdminPasswordResetRevokesAllCustomerCredentials(t *testing.T) {
	for _, actorCredential := range []string{"session", "api_key"} {
		t.Run(actorCredential, func(t *testing.T) {
			f := newAdminPasswordResetFixture(t)
			_, unrelated := user(t, f.db, f.h, "reset-unrelated-customer", "customer")
			var factor []byte
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT mfa_secret_enc FROM users WHERE id=$1", f.customer).Scan(&factor); err != nil {
				t.Fatal(err)
			}
			body := map[string]string{"password": "administrator-reset-password"}
			var reset *httptest.ResponseRecorder
			if actorCredential == "api_key" {
				key := portalRequest(f.h, "POST", "/api/v1/api-keys", map[string]string{"name": "admin-password-reset"}, f.adminCookie, "admin")
				expect(t, key, 201)
				reset = requestBearer(f.h, "POST", f.path(), body, data(t, key)["secret"].(string))
			} else {
				reset = portalRequest(f.h, "POST", f.path(), body, f.adminCookie+"; "+f.customerCookie, "admin")
			}
			expect(t, reset, 200)
			if data(t, reset)["user_id"] != float64(f.customer) || data(t, reset)["password_reset_required"] != false {
				t.Fatal("administrator reset response changed", reset.Body.String())
			}
			var sessions, challenges, activeKeys int
			var enabled, recoveryUnused, factorUnchanged bool
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM sessions WHERE user_id=u.id),
 (SELECT count(*) FROM mfa_challenges WHERE user_id=u.id),
 (SELECT count(*) FROM api_keys WHERE user_id=u.id AND revoked_at IS NULL),
 u.mfa_enabled,u.mfa_secret_enc=$2,
 (SELECT bool_and(used_at IS NULL) FROM mfa_recovery_codes WHERE user_id=u.id)
 FROM users u WHERE id=$1`, f.customer, factor).Scan(&sessions, &challenges, &activeKeys, &enabled, &factorUnchanged, &recoveryUnused); err != nil || sessions != 0 || challenges != 0 || activeKeys != 0 || !enabled || !factorUnchanged || !recoveryUnused {
				t.Fatal("reset did not invalidate credentials and preserve MFA", sessions, challenges, activeKeys, enabled, factorUnchanged, recoveryUnused, err)
			}
			expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, f.customerCookie, "customer"), 401)
			expect(t, requestBearer(f.h, "GET", "/api/v1/me", nil, f.apiKey), 401)
			expect(t, portalRequest(f.h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": "customer", "challenge": f.challenge, "code": f.recovery}, "", "customer"), 401)
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT used_at IS NULL FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$2", f.customer, tokenHash(f.recovery)).Scan(&recoveryUnused); err != nil || !recoveryUnused {
				t.Fatal("stale challenge consumed a recovery code", err)
			}
			expect(t, portalRequest(f.h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "reset-customer@example.test", "password": "integration-password", "portal": "customer"}, "", "customer"), 401)
			login := portalRequest(f.h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "reset-customer@example.test", "password": body["password"], "portal": "customer"}, "", "customer")
			expect(t, login, 401)
			if errorData(t, login)["code"] != "mfa_required" {
				t.Fatal("new password did not retain MFA", login.Body.String())
			}
			expect(t, portalRequest(f.h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": "customer", "challenge": errorData(t, login)["challenge"].(string), "code": f.recovery}, "", "customer"), 200)
			expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, unrelated, "customer"), 200)
			expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, f.adminCookie, "admin"), 200)
		})
	}
}

func TestAdminPasswordResetAuditFailureRollsBackCredentials(t *testing.T) {
	f := newAdminPasswordResetFixture(t)
	before := f.securitySnapshot(t)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_admin_password_reset_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.action='customer.password_reset' THEN RAISE EXCEPTION 'injected reset audit failure'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_admin_password_reset_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_admin_password_reset_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, portalRequest(f.h, "POST", f.path(), map[string]string{"password": "administrator-reset-password"}, f.adminCookie, "admin"), 500)
	if after := f.securitySnapshot(t); after != before {
		t.Fatal("audit failure changed password, sessions, keys, or MFA")
	}
	expect(t, requestBearer(f.h, "GET", "/api/v1/me", nil, f.apiKey), 200)
	expect(t, portalRequest(f.h, "GET", "/api/v1/me", nil, f.customerCookie, "customer"), 200)
	expect(t, portalRequest(f.h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": "customer", "challenge": f.challenge, "code": f.recovery}, "", "customer"), 200)
}

func TestAdminPasswordResetRechecksAuthorizationAfterCustomerLock(t *testing.T) {
	for _, scenario := range []string{"session_revoked", "key_revoked", "permission_revoked", "role_disabled", "administrator_disabled", "scope_revoked"} {
		t.Run(scenario, func(t *testing.T) {
			f := newAdminPasswordResetFixture(t)
			before := f.securitySnapshot(t)
			bearer := ""
			if scenario == "key_revoked" {
				key := portalRequest(f.h, "POST", "/api/v1/api-keys", map[string]string{"name": "admin-reset-revoked"}, f.adminCookie, "admin")
				expect(t, key, 201)
				bearer = data(t, key)["secret"].(string)
			}
			blocker, err := f.db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer blocker.Rollback(context.Background())
			if _, err = blocker.Exec(t.Context(), "SELECT id FROM users WHERE id=$1 FOR UPDATE", f.customer); err != nil {
				t.Fatal(err)
			}
			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				body := map[string]string{"password": "administrator-reset-password"}
				if bearer != "" {
					done <- requestBearer(f.h, "POST", f.path(), body, bearer)
				} else {
					done <- portalRequest(f.h, "POST", f.path(), body, f.adminCookie+"; "+f.customerCookie, "admin")
				}
			}()
			waitQueryLock(t, f.db, "SELECT id FROM users WHERE id=$1 AND role='customer' FOR UPDATE")
			status := 401
			switch scenario {
			case "session_revoked":
				_, err = f.db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookieValue(f.adminCookie)))
			case "key_revoked":
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE secret_hash=$1", tokenHash(bearer))
			case "permission_revoked":
				status = 403
				_, err = f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=array_remove(permissions,'customers.manage') WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", f.actor)
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
			select {
			case result := <-done:
				expect(t, result, status)
			case <-time.After(7 * time.Second):
				t.Fatal("administrator password reset stalled after revocation")
			}
			if after := f.securitySnapshot(t); after != before {
				t.Fatal("revoked administrator reset changed customer credentials")
			}
		})
	}
}

func TestAdminPasswordResetRechecksCustomerAfterSalesTransfer(t *testing.T) {
	f := newAdminPasswordResetFixture(t)
	other, _ := user(t, f.db, f.h, "reset-next-owner", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", f.actor); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$1 WHERE id=$2", f.actor, f.customer); err != nil {
		t.Fatal(err)
	}
	before := f.securitySnapshot(t)
	transfer, err := f.db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer transfer.Rollback(context.Background())
	if _, err = transfer.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", f.customer); err != nil {
		t.Fatal(err)
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- portalRequest(f.h, "POST", f.path(), map[string]string{"password": "administrator-reset-password"}, f.adminCookie, "admin")
	}()
	waitQueryLock(t, f.db, "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))")
	if _, err = transfer.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.customer, other); err != nil {
		t.Fatal(err)
	}
	if err = transfer.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		expect(t, result, 404)
	case <-time.After(7 * time.Second):
		t.Fatal("administrator reset stalled after sales transfer")
	}
	// Compare security material while allowing the authorized ownership change.
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", f.customer, f.actor); err != nil {
		t.Fatal(err)
	}
	if after := f.securitySnapshot(t); after != before {
		t.Fatal("sales transfer let the previous owner reset customer credentials")
	}
}

func TestMFALoginSerializesPasswordRotation(t *testing.T) {
	for _, scenario := range []string{"admin_reset_customer", "personal_customer", "personal_admin"} {
		t.Run(scenario, func(t *testing.T) {
			db, h := testServer(t)
			portal, role, table := "customer", "customer", "users"
			if scenario == "personal_admin" {
				portal, role, table = "admin", "super_admin", "administrators"
			}
			owner, cookie := user(t, db, h, "mfa-rotation-owner", role)
			method, path, rotationPortal, rotationCookie := "PUT", "/api/v1/account/password", portal, cookie
			body := map[string]string{"current_password": "integration-password", "new_password": "mfa-rotation-new-password"}
			if scenario == "admin_reset_customer" {
				_, rotationCookie = user(t, db, h, "mfa-rotation-administrator", "super_admin")
				method, path, rotationPortal = "POST", fmt.Sprintf("/api/v1/admin/customers/%d/password", owner), "admin"
				body = map[string]string{"password": "mfa-rotation-new-password"}
			}
			enrolled := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, portal)
			expect(t, enrolled, 200)
			secret := data(t, enrolled)["secret"].(string)
			recovery := data(t, enrolled)["recovery_codes"].([]any)[0].(string)
			expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, portal), 200)
			loginInput := map[string]string{"identifier": "mfa-rotation-owner@example.test", "password": "integration-password", "portal": portal}
			login := portalRequest(h, "POST", "/api/v1/auth/login", loginInput, "", portal)
			expect(t, login, 401)
			challenge := errorData(t, login)["challenge"].(string)

			// Pause the real password mutation after it owns the account row and
			// before it invalidates challenges. The gate is unique to this schema.
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION gate_password_rotation() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 PERFORM pg_advisory_xact_lock(hashtextextended(TG_TABLE_SCHEMA||':password-rotation-gate',0)); RETURN NEW; END $$;
 CREATE TRIGGER gate_password_rotation BEFORE UPDATE OF password_hash ON `+table+` FOR EACH ROW EXECUTE FUNCTION gate_password_rotation()`); err != nil {
				t.Fatal(err)
			}
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':password-rotation-gate',0))"); err != nil {
				t.Fatal(err)
			}
			rotated := make(chan *httptest.ResponseRecorder, 1)
			go func() { rotated <- portalRequest(h, method, path, body, rotationCookie, rotationPortal) }()
			waitQueryLock(t, db, "UPDATE "+table+" SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1")
			completed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				completed <- portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": portal, "challenge": challenge, "code": recovery}, "", portal)
			}()
			waitQueryLock(t, db, "SELECT id FROM "+table+" WHERE id=$1 AND status='active' AND mfa_enabled=true FOR UPDATE")

			// A completion waiting on its account must not hold the challenge
			// needed by password invalidation, or the two writers deadlock.
			probe, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Rollback(context.Background())
			var challengeOwner int64
			err = probe.QueryRow(t.Context(), "SELECT user_id FROM mfa_challenges WHERE token_hash=$1 FOR UPDATE NOWAIT", tokenHash(challenge)).Scan(&challengeOwner)
			if err != nil || challengeOwner != owner {
				t.Fatal("waiting MFA completion locked or changed the challenge", challengeOwner, err)
			}
			if err = probe.Rollback(t.Context()); err != nil {
				t.Fatal(err)
			}
			if err = gate.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-rotated:
				expect(t, response, 200)
			case <-time.After(7 * time.Second):
				t.Fatal("password rotation stalled with a concurrent MFA completion")
			}
			select {
			case response := <-completed:
				expect(t, response, 401)
				if len(response.Result().Cookies()) != 0 {
					t.Fatal("invalidated MFA challenge issued a session")
				}
			case <-time.After(7 * time.Second):
				t.Fatal("MFA completion stalled after password rotation")
			}
			var unused bool
			var sessions, challenges int
			if err = db.Pool.QueryRow(t.Context(), `SELECT used_at IS NULL,
 (SELECT count(*) FROM sessions WHERE user_id=$1),
 (SELECT count(*) FROM mfa_challenges WHERE user_id=$1)
 FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$2`, owner, tokenHash(recovery)).Scan(&unused, &sessions, &challenges); err != nil || !unused || challenges != 0 {
				t.Fatal("invalidated MFA consumed recovery or retained a challenge", unused, sessions, challenges, err)
			}
			wantSessions := 1
			if scenario == "admin_reset_customer" {
				wantSessions = 0
			}
			if sessions != wantSessions {
				t.Fatal("concurrent MFA created an extra session", sessions, wantSessions)
			}
			loginInput["password"] = "mfa-rotation-new-password"
			login = portalRequest(h, "POST", "/api/v1/auth/login", loginInput, "", portal)
			expect(t, login, 401)
			expect(t, portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": portal, "challenge": errorData(t, login)["challenge"].(string), "code": recovery}, "", portal), 200)
		})
	}
}
