package httpapi

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestAdminAutoRenewPermissionsScopeAndAudit(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "admin-auto-renew-customer", "customer")
	_, other := user(t, db, h, "admin-auto-renew-other", "customer")
	staffID, staff := user(t, db, h, "admin-auto-renew-staff", "admin")
	sid := downloadService(t, db, h, uid, customer, product(t, db, 4), "admin-auto-renew-order")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/admin/services/%d/auto-renew", sid)
	in := map[string]bool{"enabled": true}
	expect(t, request(h, "PATCH", path, in, "", ""), 401)
	expect(t, request(h, "PATCH", path, in, customer, ""), 401)
	expect(t, request(h, "PATCH", path, in, other, ""), 401)
	contentRole(t, db, staffID, "services.view")
	expect(t, request(h, "PATCH", path, in, staff, ""), 403)
	contentRole(t, db, staffID, "services.manage")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, in, staff, ""), 404)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", uid, staffID); err != nil {
		t.Fatal(err)
	}
	w := request(h, "PATCH", path, in, staff, "")
	expect(t, w, 200)
	if data(t, w)["service_id"] != float64(sid) || data(t, w)["auto_renew"] != true {
		t.Fatal("setting response", w.Body.String())
	}
	var enabled bool
	var actor int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT auto_renew FROM services WHERE id=$1", sid).Scan(&enabled); err != nil || !enabled {
		t.Fatal("setting not committed", enabled, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT actor_id FROM audit_events WHERE action='service.auto_renew.updated' AND entity_id=$1 ORDER BY id DESC LIMIT 1", fmt.Sprint(sid)).Scan(&actor); err != nil || actor != staffID {
		t.Fatal("administrator actor was lost", actor, err)
	}
	for _, malformed := range []any{map[string]any{}, map[string]any{"enabled": nil}, map[string]any{"enabled": "false"}, map[string]any{"enabled": 0}} {
		expect(t, request(h, "PATCH", path, malformed, staff, ""), 400)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT auto_renew FROM services WHERE id=$1", sid).Scan(&enabled); err != nil || !enabled {
		t.Fatal("invalid input disabled auto-renew", enabled, err)
	}
	// The ordinary customer route keeps its owner semantics and portal split.
	customerPath := fmt.Sprintf("/api/v1/services/%d/auto-renew", sid)
	expect(t, request(h, "PATCH", customerPath, in, staff, ""), 401)
	expect(t, request(h, "PATCH", customerPath, in, other, ""), 404)
	expect(t, request(h, "PATCH", customerPath, map[string]bool{"enabled": false}, customer, ""), 200)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET billing_cycle='once' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	w = request(h, "PATCH", path, in, staff, "")
	expect(t, w, 409)
	if !strings.Contains(w.Body.String(), "nonrenewable_service") {
		t.Fatal("admin bypassed renewal business validation", w.Body.String())
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET billing_cycle='monthly' WHERE id=$1", sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_auto_renew_audit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN IF NEW.action='service.auto_renew.updated' THEN RAISE EXCEPTION 'audit unavailable'; END IF; RETURN NEW; END $$;
CREATE TRIGGER reject_auto_renew_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_auto_renew_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "PATCH", path, in, staff, ""), 500)
	if err := db.Pool.QueryRow(t.Context(), "SELECT auto_renew FROM services WHERE id=$1", sid).Scan(&enabled); err != nil || enabled {
		t.Fatal("audit failure committed unaudited setting", enabled, err)
	}
}

func TestAdminAutoRenewCredentialLockOrder(t *testing.T) {
	for _, credential := range []string{"session", "api_key"} {
		t.Run(credential, func(t *testing.T) {
			db, h := testServer(t)
			uid, customer := user(t, db, h, "auto-renew-lock-customer", "customer")
			staffID, retained := user(t, db, h, "auto-renew-lock-staff", "admin")
			sid := downloadService(t, db, h, uid, customer, product(t, db, 4), "auto-renew-lock-order")
			if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
				t.Fatal(err)
			}
			login := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "auto-renew-lock-staff", "password": "integration-password"}, "", "")
			expect(t, login, 200)
			cookies := login.Result().Cookies()
			if len(cookies) != 1 {
				t.Fatal("second session not created")
			}
			second := cookies[0].Name + "=" + cookies[0].Value
			secret := "zjmfgo_" + strings.Repeat("c", 64)
			if credential == "api_key" {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'auto-renew-lock-test','zjmfgo_1234567890123456',$2)`, staffID, tokenHash(secret)); err != nil {
					t.Fatal(err)
				}
			}
			// Pause the real password change after the administrator row is
			// locked but before it invalidates the other session and API keys.
			if _, err := db.Pool.Exec(t.Context(), `CREATE FUNCTION gate_auto_renew_password() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
PERFORM pg_advisory_xact_lock(hashtextextended(TG_TABLE_SCHEMA||':auto-renew-password-gate',0)); RETURN NEW; END $$;
CREATE TRIGGER gate_auto_renew_password BEFORE UPDATE OF password_hash ON administrators FOR EACH ROW EXECUTE FUNCTION gate_auto_renew_password()`); err != nil {
				t.Fatal(err)
			}
			gate, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer gate.Rollback(context.Background())
			if _, err = gate.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended(current_schema()||':auto-renew-password-gate',0))"); err != nil {
				t.Fatal(err)
			}
			rotated := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				rotated <- portalRequest(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "auto-renew-updated-password"}, retained, "admin")
			}()
			waitQueryLock(t, db, "UPDATE administrators SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1")
			completed := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				path := fmt.Sprintf("/api/v1/admin/services/%d/auto-renew", sid)
				in := map[string]bool{"enabled": true}
				if credential == "api_key" {
					completed <- requestBearer(h, "PATCH", path, in, secret)
				} else {
					completed <- request(h, "PATCH", path, in, second, "")
				}
			}()
			waitQueryLock(t, db, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE")
			probe, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer probe.Rollback(context.Background())
			query, hash := "SELECT user_id FROM sessions WHERE token_hash=$1 FOR UPDATE NOWAIT", tokenHash(cookies[0].Value)
			if credential == "api_key" {
				query, hash = "SELECT user_id FROM api_keys WHERE secret_hash=$1 FOR UPDATE NOWAIT", tokenHash(secret)
			}
			var owner int64
			if err = probe.QueryRow(t.Context(), query, hash).Scan(&owner); err != nil || owner != staffID {
				t.Fatal("auto-renew waiting on administrator prematurely locked credential", owner, err)
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
				t.Fatal("password change deadlocked with auto-renew")
			}
			select {
			case response := <-completed:
				expect(t, response, 401)
			case <-time.After(7 * time.Second):
				t.Fatal("auto-renew deadlocked after password change")
			}
			var enabled bool
			var audits int64
			if err = db.Pool.QueryRow(t.Context(), "SELECT auto_renew,(SELECT count(*) FROM audit_events WHERE action='service.auto_renew.updated' AND entity_id=$2) FROM services WHERE id=$1", sid, fmt.Sprint(sid)).Scan(&enabled, &audits); err != nil || enabled || audits != 0 {
				t.Fatal("password-invalidated credential changed service", enabled, audits, err)
			}
		})
	}
}

func TestAdminAutoRenewWaitingRechecksAuthority(t *testing.T) {
	for _, revocation := range []string{"session", "api_key", "permission", "account", "scope_after_service", "scope_lock"} {
		t.Run(revocation, func(t *testing.T) {
			db, h := testServer(t)
			uid, customer := user(t, db, h, "admin-auto-renew-wait-customer", "customer")
			staffID, staff := user(t, db, h, "admin-auto-renew-wait-staff", "admin")
			sid := downloadService(t, db, h, uid, customer, product(t, db, 4), "admin-auto-renew-wait-order")
			contentRole(t, db, staffID, "services.manage")
			if _, err := db.Pool.Exec(t.Context(), "UPDATE services SET status='active' WHERE id=$1", sid); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET sales_owner_id=$2 WHERE id=$1", uid, staffID); err != nil {
				t.Fatal(err)
			}
			secret := "zjmfgo_" + strings.Repeat("b", 64)
			if revocation == "api_key" {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'auto-renew-test','zjmfgo_1234567890123456',$2)`, staffID, tokenHash(secret)); err != nil {
					t.Fatal(err)
				}
			}
			block, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer block.Rollback(t.Context())
			waitQuery := "SELECT user_id,status,billing_cycle FROM services WHERE id=$1 FOR UPDATE"
			if revocation == "scope_lock" {
				_, err = block.Exec(t.Context(), "SELECT pg_advisory_xact_lock(hashtextextended('customer-sales:'||$1::bigint::text,0))", uid)
				waitQuery = "SELECT pg_advisory_xact_lock_shared(hashtextextended('customer-sales:'||$1::bigint::text,0))"
			} else {
				_, err = block.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", sid)
			}
			if err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/admin/services/%d/auto-renew", sid)
			done := make(chan int, 1)
			go func() {
				in := map[string]bool{"enabled": true}
				if revocation == "api_key" {
					done <- requestBearer(h, "PATCH", path, in, secret).Code
				} else {
					done <- request(h, "PATCH", path, in, staff, "").Code
				}
			}()
			waitQueryLock(t, db, waitQuery)
			want := 401
			switch revocation {
			case "api_key":
				_, err = db.Pool.Exec(t.Context(), "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1", staffID)
			case "permission":
				_, err = db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions='{}' WHERE id=(SELECT admin_role_id FROM administrators WHERE id=$1)", staffID)
				want = 403
			case "account":
				_, err = db.Pool.Exec(t.Context(), "UPDATE administrators SET status='disabled' WHERE id=$1", staffID)
			case "scope_after_service", "scope_lock":
				_, err = block.Exec(t.Context(), "UPDATE users SET sales_owner_id=NULL WHERE id=$1", uid)
				want = 404
			default:
				_, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", staffID)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = block.Commit(t.Context()); err != nil {
				t.Fatal(err)
			}
			select {
			case status := <-done:
				if status != want {
					t.Fatalf("revoked request returned %d want %d", status, want)
				}
			case <-time.After(7 * time.Second):
				t.Fatal("request did not finish after releasing lock")
			}
			var enabled bool
			var audits int64
			if err = db.Pool.QueryRow(t.Context(), "SELECT auto_renew,(SELECT count(*) FROM audit_events WHERE action='service.auto_renew.updated' AND entity_id=$2) FROM services WHERE id=$1", sid, fmt.Sprint(sid)).Scan(&enabled, &audits); err != nil || enabled || audits != 0 {
				t.Fatal("denied mutation changed service or audit", enabled, audits, err)
			}
		})
	}
}
