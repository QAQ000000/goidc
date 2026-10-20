package httpapi

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestPhysicalAccountSecurityMutationsRejectRevokedSessionAfterHTTPAuth(t *testing.T) {
	for _, portal := range []string{"customer", "admin"} {
		for _, action := range []string{"password", "mfa_enroll", "mfa_confirm", "mfa_disable", "api_key"} {
			t.Run(portal+"/"+action, func(t *testing.T) {
				db, h := testServer(t)
				table, role, otherRole, otherPortal := "users", "customer", "super_admin", "admin"
				if portal == "admin" {
					table, role, otherRole, otherPortal = "administrators", "super_admin", "customer", "customer"
				}
				owner, cookie := user(t, db, h, "security-wait-owner", role)
				_, other := user(t, db, h, "security-wait-other", otherRole)
				dual := cookie + "; " + other
				method, path := "POST", "/api/v1/account/mfa/enroll"
				var body any = map[string]any{}
				query := "SELECT mfa_enabled,role FROM " + table + " WHERE id=$1 AND status='active' FOR UPDATE"
				if action == "mfa_confirm" || action == "mfa_disable" {
					enrolled := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", body, dual, portal)
					expect(t, enrolled, 200)
					secret := data(t, enrolled)["secret"].(string)
					body = map[string]string{"code": testTOTP(secret, time.Now())}
					if action == "mfa_confirm" {
						path = "/api/v1/account/mfa/confirm"
						query = "SELECT mfa_enabled,mfa_secret_enc,role FROM " + table + " WHERE id=$1 AND status='active' FOR UPDATE"
					} else {
						expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", body, dual, portal), 200)
						path = "/api/v1/account/mfa/disable"
						body = map[string]string{"password": "integration-password", "code": testTOTP(secret, time.Now())}
						query = "SELECT password_hash,mfa_enabled,mfa_secret_enc FROM " + table + " WHERE id=$1 AND status='active' FOR UPDATE"
					}
				}
				if action == "password" {
					method, path = "PUT", "/api/v1/account/password"
					body = map[string]string{"current_password": "integration-password", "new_password": "revoked-session-password"}
					query = "SELECT password_hash FROM " + table + " WHERE id=$1 AND status='active' FOR UPDATE"
				} else if action == "api_key" {
					path, body = "/api/v1/api-keys", map[string]string{"name": "revoked-session-key"}
					query = "SELECT role FROM " + table + " WHERE id=$1 AND status='active' FOR UPDATE"
				}
				snapshot := func() string {
					t.Helper()
					var value string
					if err := db.Pool.QueryRow(t.Context(), "SELECT jsonb_build_object('profile',to_jsonb(a),'recovery',(SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY code_hash),'[]'::jsonb) FROM mfa_recovery_codes c WHERE c.user_id=a.id),'keys',(SELECT count(*) FROM api_keys k WHERE k.user_id=a.id))::text FROM "+table+" a WHERE a.id=$1", owner).Scan(&value); err != nil {
						t.Fatal(err)
					}
					return value
				}
				before := snapshot()
				blocker, err := db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), "SELECT id FROM "+table+" WHERE id=$1 FOR UPDATE", owner); err != nil {
					t.Fatal(err)
				}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- portalRequest(h, method, path, body, dual, portal) }()
				waitQueryLock(t, db, query)
				if _, err = db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1 AND token_hash=$2", owner, tokenHash(cookieValue(cookie))); err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					expect(t, result, 401)
				case <-time.After(7 * time.Second):
					t.Fatal("security mutation stalled after session revocation")
				}
				if after := snapshot(); before != after {
					t.Fatal("revoked security mutation changed account/factor/API keys")
				}
				expect(t, portalRequest(h, "GET", "/api/v1/me", nil, dual, otherPortal), 200)
			})
		}
	}
}

func TestPhysicalPasswordChangeRejectsBearerWithOtherPortalCookie(t *testing.T) {
	db, h := testServer(t)
	_, admin := user(t, db, h, "password-mixed-admin", "super_admin")
	_, customer := user(t, db, h, "password-mixed-customer", "customer")
	created := portalRequest(h, "POST", "/api/v1/api-keys", map[string]string{"name": "mixed-credential"}, admin, "admin")
	expect(t, created, 201)
	secret := data(t, created)["secret"].(string)
	result := requestFrom(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "mixed-credential-password"}, "192.0.2.10:1234", customer, map[string]string{"Authorization": "Bearer " + secret})
	expect(t, result, 401)
	expect(t, requestBearer(h, "GET", "/api/v1/admin/dashboard", nil, secret), 200)
	expect(t, portalRequest(h, "GET", "/api/v1/me", nil, admin+"; "+customer, "admin"), 200)
	if strings.Contains(result.Body.String(), "changed") {
		t.Fatal("mixed credential changed password")
	}
}

func TestPhysicalAccountContactVerificationUsesSelectedPortal(t *testing.T) {
	db, h := testServer(t)
	aid, admin := user(t, db, h, "contact-portal-admin", "super_admin")
	cid, customer := user(t, db, h, "contact-portal-customer", "customer")
	const email = "contact-shared@example.test"
	if _, err := db.Pool.Exec(t.Context(), "UPDATE administrators SET email=$2 WHERE id=$1", aid, email); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET email=$2 WHERE id=$1", cid, email); err != nil {
		t.Fatal(err)
	}
	dual := admin + "; " + customer
	challenges := map[string]string{}
	for _, portal := range []string{"admin", "customer"} {
		issued := portalRequest(h, "POST", "/api/v1/account/verification/request", map[string]string{"purpose": "bind_email", "channel": "email", "recipient": email}, dual, portal)
		expect(t, issued, 202)
		challenges[portal] = data(t, issued)["challenge_id"].(string)
	}
	adminConfirm := map[string]string{"challenge_id": challenges["admin"], "code": verificationEmailCode(t, db, challenges["admin"])}
	expect(t, portalRequest(h, "POST", "/api/v1/account/verification/confirm", adminConfirm, dual, "customer"), 409)
	expect(t, portalRequest(h, "POST", "/api/v1/account/verification/confirm", adminConfirm, dual, "admin"), 200)
	var adminVerified, customerVerified bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT email_verified FROM administrators WHERE id=$1),(SELECT email_verified FROM users WHERE id=$2)", aid, cid).Scan(&adminVerified, &customerVerified); err != nil || !adminVerified || customerVerified {
		t.Fatal("admin contact verification crossed portal", err)
	}
	expect(t, portalRequest(h, "POST", "/api/v1/account/verification/confirm", map[string]string{"challenge_id": challenges["customer"], "code": verificationEmailCode(t, db, challenges["customer"])}, dual, "customer"), 200)
	// An administrator cookie in the customer slot never enables fallback.
	expect(t, portalRequest(h, "POST", "/api/v1/account/verification/request", map[string]string{"purpose": "bind_email", "channel": "email", "recipient": email}, customerSessionCookie+"="+cookieValue(admin), "customer"), 401)
}

func TestPhysicalLoginFinalizationRechecksCredentialAfterLockWait(t *testing.T) {
	for _, portal := range []string{"customer", "admin"} {
		for _, scenario := range []string{"session_password", "session_mfa", "session_reset", "mfa_password", "mfa_factor"} {
			t.Run(portal+"/"+scenario, func(t *testing.T) {
				db, h := testServer(t)
				table, role := "users", "customer"
				if portal == "admin" {
					table, role = "administrators", "super_admin"
				}
				owner, cookie := user(t, db, h, "login-wait-owner", role)
				if strings.HasPrefix(scenario, "mfa_") || scenario == "session_mfa" {
					enrolled := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, portal)
					expect(t, enrolled, 200)
					if strings.HasPrefix(scenario, "mfa_") {
						secret := data(t, enrolled)["secret"].(string)
						expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, portal), 200)
					}
				}
				changedHash, err := bcrypt.GenerateFromPassword([]byte("login-wait-new-password"), bcrypt.MinCost)
				if err != nil {
					t.Fatal(err)
				}
				blocker, err := db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer blocker.Rollback(context.Background())
				if _, err = blocker.Exec(t.Context(), "SELECT id FROM "+table+" WHERE id=$1 FOR UPDATE", owner); err != nil {
					t.Fatal(err)
				}
				input := map[string]string{"identifier": "login-wait-owner@example.test", "password": "integration-password", "portal": portal}
				done := make(chan *httptest.ResponseRecorder, 1)
				go func() { done <- portalRequest(h, "POST", "/api/v1/auth/login", input, "", portal) }()
				waitQueryLock(t, db, "SELECT role,password_hash,mfa_enabled,password_reset_required FROM "+table+" WHERE id=$1 AND status='active' FOR SHARE")
				switch scenario {
				case "session_password", "mfa_password":
					_, err = blocker.Exec(t.Context(), "UPDATE "+table+" SET password_hash=$2 WHERE id=$1", owner, string(changedHash))
				case "session_mfa":
					_, err = blocker.Exec(t.Context(), "UPDATE "+table+" SET mfa_enabled=true,mfa_confirmed_at=now() WHERE id=$1", owner)
				case "session_reset":
					_, err = blocker.Exec(t.Context(), "UPDATE "+table+" SET password_reset_required=true WHERE id=$1", owner)
				case "mfa_factor":
					_, err = blocker.Exec(t.Context(), "UPDATE "+table+" SET mfa_enabled=false WHERE id=$1", owner)
				}
				if err != nil {
					t.Fatal(err)
				}
				if err = blocker.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				select {
				case result := <-done:
					expect(t, result, 401)
					if len(result.Result().Cookies()) != 0 || errorData(t, result)["code"] != "invalid_credentials" {
						t.Fatal("changed credential finalized a login", result.Body.String())
					}
				case <-time.After(7 * time.Second):
					t.Fatal("login stalled after credential changed")
				}
				var sessions, challenges int
				if err = db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM sessions WHERE user_id=$1),(SELECT count(*) FROM mfa_challenges WHERE user_id=$1)", owner).Scan(&sessions, &challenges); err != nil || sessions != 1 || challenges != 0 {
					t.Fatal("stale password created credential", sessions, challenges, err)
				}
			})
		}
	}
}

func TestPhysicalPasswordChangeInvalidatesPendingMFAChallenge(t *testing.T) {
	for _, portal := range []string{"customer", "admin"} {
		t.Run(portal, func(t *testing.T) {
			db, h := testServer(t)
			role := "customer"
			if portal == "admin" {
				role = "super_admin"
			}
			owner, cookie := user(t, db, h, "challenge-rotation-owner", role)
			enrolled := portalRequest(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, portal)
			expect(t, enrolled, 200)
			secret := data(t, enrolled)["secret"].(string)
			recovery := data(t, enrolled)["recovery_codes"].([]any)[0].(string)
			expect(t, portalRequest(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, portal), 200)
			input := map[string]string{"identifier": "challenge-rotation-owner@example.test", "password": "integration-password", "portal": portal}
			login := portalRequest(h, "POST", "/api/v1/auth/login", input, "", portal)
			expect(t, login, 401)
			challenge := errorData(t, login)["challenge"].(string)
			expect(t, portalRequest(h, "PUT", "/api/v1/account/password", map[string]string{"current_password": "integration-password", "new_password": "challenge-rotation-password"}, cookie, portal), 200)
			expect(t, portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": portal, "challenge": challenge, "code": recovery}, "", portal), 401)
			var unused bool
			if err := db.Pool.QueryRow(t.Context(), "SELECT used_at IS NULL FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$2", owner, tokenHash(recovery)).Scan(&unused); err != nil || !unused {
				t.Fatal("invalidated challenge consumed recovery code", err)
			}
			input["password"] = "challenge-rotation-password"
			login = portalRequest(h, "POST", "/api/v1/auth/login", input, "", portal)
			expect(t, login, 401)
			challenge = errorData(t, login)["challenge"].(string)
			expect(t, portalRequest(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"portal": portal, "challenge": challenge, "code": recovery}, "", portal), 200)
		})
	}
}
