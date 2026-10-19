package httpapi

import (
	"crypto/md5"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
)

func TestLoginPortalRejectsCrossRoleAndRetainsLegacyClients(t *testing.T) {
	db, h := testServer(t)
	for _, role := range []string{"customer", "admin", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			name := "portal-" + strings.ReplaceAll(role, "_", "-")
			id, _ := user(t, db, h, name, role)
			portal := store.LoginPortalForRole(role)
			wrong := "admin"
			if portal == "admin" {
				wrong = "customer"
			}
			field, identifier := "identifier", name+"@example.test"
			if portal == "admin" {
				field, identifier = "username", name
			}
			body := map[string]string{field: identifier, "password": "integration-password", "portal": wrong}
			var before int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE user_id=$1", id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			rejected := request(h, http.MethodPost, "/api/v1/auth/login", body, "", "")
			expect(t, rejected, 401)
			if errorData(t, rejected)["code"] != "invalid_credentials" || len(rejected.Result().Cookies()) != 0 {
				t.Fatal("cross-portal login exposed identity or issued cookie", rejected.Body.String())
			}
			var after, challenges int
			if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM sessions WHERE user_id=$1),(SELECT count(*) FROM mfa_challenges WHERE user_id=$1)", id).Scan(&after, &challenges); err != nil || after != before || challenges != 0 {
				t.Fatal("cross-portal login issued authentication state", before, after, challenges, err)
			}
			body["portal"] = portal
			accepted := request(h, http.MethodPost, "/api/v1/auth/login", body, "", "")
			expect(t, accepted, 200)
			if data(t, accepted)["role"] != role {
				t.Fatal("portal changed authenticated role", accepted.Body.String())
			}
			delete(body, "portal")
			accepted = request(h, http.MethodPost, "/api/v1/auth/login", body, "", "")
			expect(t, accepted, 200)
			if data(t, accepted)["role"] != role {
				t.Fatal("legacy login changed authenticated role", accepted.Body.String())
			}
		})
	}
}

func TestLoginPortalMismatchDoesNotUpgradeLegacyPasswordOrClearPressure(t *testing.T) {
	db, h := testServerConfig(t, func(cfg *config.Config) { cfg.LegacyCMFAuthCode = "auth-code" })
	inner := md5.Sum([]byte("auth-codelegacy-portal-password"))
	outer := md5.Sum([]byte(hex.EncodeToString(inner[:])))
	hash := legacy.WrapCMFPasswordHash("###" + hex.EncodeToString(outer[:]))
	var id int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role,password_reset_required) VALUES('portal-legacy','portal-legacy@example.test',$1,'Portal Legacy','super_admin',true) RETURNING id`, hash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	key := tokenHash("admin-login-account:portal-legacy")
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO auth_rate_limits(key,attempts) VALUES($1,1)", key); err != nil {
		t.Fatal(err)
	}
	rejected := request(h, "POST", "/api/v1/auth/login", map[string]string{"identifier": "portal-legacy@example.test", "password": "legacy-portal-password", "portal": "customer"}, "", "")
	expect(t, rejected, 401)
	if errorData(t, rejected)["code"] != "invalid_credentials" {
		t.Fatal(rejected.Body.String())
	}
	var actual string
	var reset bool
	var attempts, sessions, challenges int
	if err := db.Pool.QueryRow(t.Context(), `SELECT password_hash,password_reset_required,(SELECT attempts FROM auth_rate_limits WHERE key=$2),(SELECT count(*) FROM sessions WHERE user_id=$1),(SELECT count(*) FROM mfa_challenges WHERE user_id=$1) FROM users WHERE id=$1`, id, key).Scan(&actual, &reset, &attempts, &sessions, &challenges); err != nil || actual != hash || !reset || attempts < 1 || sessions != 0 || challenges != 0 {
		t.Fatal("wrong portal changed legacy authentication state", actual == hash, reset, attempts, sessions, challenges, err)
	}
	accepted := request(h, "POST", "/api/v1/auth/login", map[string]string{"username": "portal-legacy", "password": "legacy-portal-password", "portal": "admin"}, "", "")
	expect(t, accepted, 200)
	if err := db.Pool.QueryRow(t.Context(), "SELECT password_hash,password_reset_required FROM users WHERE id=$1", id).Scan(&actual, &reset); err != nil || !strings.HasPrefix(actual, "$2") || reset {
		t.Fatal("correct portal lost legacy upgrade compatibility", reset, err)
	}
}

func TestLoginMFAPortalMismatchPreservesRecoveryAndChallenge(t *testing.T) {
	db, h := testServer(t)
	for _, role := range []string{"customer", "super_admin"} {
		t.Run(role, func(t *testing.T) {
			name := "portal-mfa-" + strings.ReplaceAll(role, "_", "-")
			id, cookie := user(t, db, h, name, role)
			enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
			expect(t, enroll, 200)
			enrolled := data(t, enroll)
			secret := enrolled["secret"].(string)
			recovery := enrolled["recovery_codes"].([]any)[0].(string)
			expect(t, request(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, ""), 200)
			portal := store.LoginPortalForRole(role)
			wrong := "admin"
			if portal == "admin" {
				wrong = "customer"
			}
			field, identifier := "identifier", name+"@example.test"
			if portal == "admin" {
				field, identifier = "username", name
			}
			wrongPasswordStep := request(h, "POST", "/api/v1/auth/login", map[string]string{field: identifier, "password": "integration-password", "portal": wrong}, "", "")
			expect(t, wrongPasswordStep, http.StatusUnauthorized)
			if errorData(t, wrongPasswordStep)["code"] != "invalid_credentials" || len(wrongPasswordStep.Result().Cookies()) != 0 {
				t.Fatal("wrong portal reached the MFA challenge stage", wrongPasswordStep.Body.String())
			}
			var wrongChallenges int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mfa_challenges WHERE user_id=$1", id).Scan(&wrongChallenges); err != nil || wrongChallenges != 0 {
				t.Fatal("wrong portal persisted an MFA challenge", wrongChallenges, err)
			}
			login := request(h, "POST", "/api/v1/auth/login", map[string]string{field: identifier, "password": "integration-password", "portal": portal}, "", "")
			expect(t, login, 401)
			challenge := errorData(t, login)["challenge"].(string)
			var frozen string
			var before int
			if err := db.Pool.QueryRow(t.Context(), "SELECT login_portal,(SELECT count(*) FROM sessions WHERE user_id=$1) FROM mfa_challenges WHERE user_id=$1", id).Scan(&frozen, &before); err != nil || frozen != portal {
				t.Fatal("challenge audience was not frozen", frozen, err)
			}
			rejected := request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": recovery, "portal": wrong}, "", "")
			expect(t, rejected, 401)
			assertPortalMFAUntouched(t, db, id, challenge, recovery, before)
			accepted := request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": recovery, "portal": portal}, "", "")
			expect(t, accepted, 200)
			if data(t, accepted)["role"] != role || len(accepted.Result().Cookies()) != 1 {
				t.Fatal("correct portal did not finish MFA", accepted.Body.String())
			}
			// Legacy password and MFA requests remain compatible, but their audience is
			// still the actual role frozen at password authentication.
			login = request(h, "POST", "/api/v1/auth/login", map[string]string{field: identifier, "password": "integration-password"}, "", "")
			expect(t, login, 401)
			challenge = errorData(t, login)["challenge"].(string)
			accepted = request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": testTOTP(secret, time.Now())}, "", "")
			expect(t, accepted, 200)
		})
	}
}

func assertPortalMFAUntouched(t *testing.T, db *store.Store, id int64, challenge, recovery string, sessions int) {
	t.Helper()
	var attempts, after int
	var unused bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT c.attempts,(SELECT count(*) FROM sessions WHERE user_id=$1),(SELECT used_at IS NULL FROM mfa_recovery_codes WHERE user_id=$1 AND code_hash=$3) FROM mfa_challenges c WHERE c.token_hash=$2`, id, tokenHash(challenge), tokenHash(recovery)).Scan(&attempts, &after, &unused); err != nil || attempts != 0 || after != sessions || !unused {
		t.Fatal("portal mismatch consumed challenge, recovery or issued session", attempts, after, sessions, unused, err)
	}
}

func TestLoginMFARoleChangeCannotSwitchFrozenPortal(t *testing.T) {
	db, h := testServer(t)
	for _, originalRole := range []string{"customer", "super_admin"} {
		t.Run(originalRole, func(t *testing.T) {
			name := "portal-role-" + strings.ReplaceAll(originalRole, "_", "-")
			id, cookie := user(t, db, h, name, originalRole)
			enroll := request(h, "POST", "/api/v1/account/mfa/enroll", map[string]any{}, cookie, "")
			expect(t, enroll, 200)
			enrolled := data(t, enroll)
			secret := enrolled["secret"].(string)
			recovery := enrolled["recovery_codes"].([]any)[0].(string)
			expect(t, request(h, "POST", "/api/v1/account/mfa/confirm", map[string]string{"code": testTOTP(secret, time.Now())}, cookie, ""), 200)
			field, identifier := "identifier", name+"@example.test"
			if originalRole != "customer" {
				field, identifier = "username", name
			}
			login := request(h, "POST", "/api/v1/auth/login", map[string]string{field: identifier, "password": "integration-password"}, "", "")
			expect(t, login, 401)
			challenge := errorData(t, login)["challenge"].(string)
			var before int
			if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM sessions WHERE user_id=$1", id).Scan(&before); err != nil {
				t.Fatal(err)
			}
			newRole := "super_admin"
			if originalRole == "super_admin" {
				newRole = "customer"
			}
			if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET role=$2 WHERE id=$1", id, newRole); err != nil {
				t.Fatal(err)
			}
			for _, portal := range []string{"", store.LoginPortalForRole(originalRole), store.LoginPortalForRole(newRole)} {
				rejected := request(h, "POST", "/api/v1/auth/login/mfa", map[string]string{"challenge": challenge, "code": recovery, "portal": portal}, "", "")
				expect(t, rejected, 401)
				assertPortalMFAUntouched(t, db, id, challenge, recovery, before)
			}
		})
	}
}

func TestLoginPortalStillRequiresAndConsumesCaptcha(t *testing.T) {
	db, h := testServer(t)
	id, _ := user(t, db, h, "portal-captcha", "customer")
	if _, err := db.Pool.Exec(t.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET auth_captcha_enabled=true"); err != nil {
		t.Fatal(err)
	}
	body := map[string]string{"identifier": "portal-captcha@example.test", "password": "integration-password", "portal": "customer"}
	missing := request(h, "POST", "/api/v1/auth/login", body, "", "")
	expect(t, missing, 401)
	if errorData(t, missing)["code"] != "invalid_captcha" {
		t.Fatal(missing.Body.String())
	}
	nonce, challenge := strings.Repeat("a", 64), strings.Repeat("b", 64)
	seedCaptcha(t, db, challenge, "login", "ABC234", nonce, 5*time.Minute)
	body["captcha_id"], body["captcha_answer"] = challenge, "ABC234"
	accepted := request(h, "POST", "/api/v1/auth/login", body, captchaCookie+"="+nonce, "")
	expect(t, accepted, 200)
	reused := request(h, "POST", "/api/v1/auth/login", body, captchaCookie+"="+nonce, "")
	expect(t, reused, 401)
	if errorData(t, reused)["code"] != "invalid_captcha" {
		t.Fatal("portal bypassed one-time captcha", reused.Body.String())
	}
}
