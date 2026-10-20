package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/codex/zjmfgo/internal/captcha"
	"github.com/jackc/pgx/v5"
)

// LoginPortalForRole is the authentication audience associated with a role.
// Unspecified legacy requests still resolve to this audience, never to a
// customer identity for an administrator.
func LoginPortalForRole(role string) string {
	switch role {
	case "customer":
		return "customer"
	case "admin", "super_admin":
		return "admin"
	}
	return ""
}

type LoginCredential struct {
	ID                                                int64
	Username, Email, Name, Role, PasswordHash, Status string
	ResetRequired, MFAEnabled                         bool
}

type LoginSettings struct{ Enabled, EmailEnabled, PhoneEnabled bool }

func (s *Store) AuthenticationLoginSettings(ctx context.Context) (out LoginSettings, err error) {
	err = s.Pool.QueryRow(ctx, "SELECT login_enabled,login_email_enabled,login_phone_enabled FROM auth_settings WHERE id=1").Scan(&out.Enabled, &out.EmailEnabled, &out.PhoneEnabled)
	return
}

func (s *Store) AuthenticationCredential(ctx context.Context, identifier, method, portal string) (out LoginCredential, err error) {
	read := func(table string) (credential LoginCredential, readErr error) {
		readErr = s.Pool.QueryRow(ctx, "SELECT id,username,email,display_name,role,password_hash,status,password_reset_required,mfa_enabled FROM "+table+" WHERE ($2='username' AND username=$1) OR ($2='email' AND email=$1) OR ($2='phone' AND phone=$1 AND phone_verified)", identifier, method).Scan(&credential.ID, &credential.Username, &credential.Email, &credential.Name, &credential.Role, &credential.PasswordHash, &credential.Status, &credential.ResetRequired, &credential.MFAEnabled)
		return
	}
	if portal != "" {
		table, tableErr := authenticationTableForPortal(portal)
		if tableErr != nil {
			return out, tableErr
		}
		return read(table)
	}
	// Legacy callers may omit the portal only when the identifier names one
	// physical account. Never resolve cross-table collisions by query order.
	customer, customerErr := read("users")
	administrator, administratorErr := read("administrators")
	if customerErr != nil && !errors.Is(customerErr, pgx.ErrNoRows) {
		return out, customerErr
	}
	if administratorErr != nil && !errors.Is(administratorErr, pgx.ErrNoRows) {
		return out, administratorErr
	}
	if customerErr == nil && administratorErr == nil {
		return out, pgx.ErrNoRows
	}
	if customerErr == nil {
		return customer, nil
	}
	return administrator, administratorErr
}

func (s *Store) UpgradeAuthenticationPassword(ctx context.Context, userID int64, role, original, upgraded string) (bool, error) {
	table, err := authenticationTableForPortal(LoginPortalForRole(role))
	if err != nil {
		return false, err
	}
	tag, err := s.Pool.Exec(ctx, `UPDATE `+table+` SET password_hash=$2,password_reset_required=false,updated_at=now() WHERE id=$1 AND password_hash=$3 AND role=$4 AND status='active'`, userID, upgraded, original, role)
	return err == nil && tag.RowsAffected() == 1, err
}

func (s *Store) RecordAuthenticationAudit(ctx context.Context, actorID int64, action string, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	_, _ = s.Pool.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES(NULLIF($1,0),$2,'user',$3,$4)", actorID, action, strconv.FormatInt(actorID, 10), b)
}

func (s *Store) AuthenticationRateAttempt(ctx context.Context, key string) (count int, err error) {
	err = s.Pool.QueryRow(ctx, `INSERT INTO auth_rate_limits(key,attempts) VALUES($1,1)
 ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN auth_rate_limits.window_start < now()-interval '1 minute' THEN 1 ELSE auth_rate_limits.attempts+1 END,
 window_start=CASE WHEN auth_rate_limits.window_start < now()-interval '1 minute' THEN now() ELSE auth_rate_limits.window_start END RETURNING attempts`, key).Scan(&count)
	return
}

func (s *Store) AuthenticationLoginBlocked(ctx context.Context, username, host string) bool {
	var blocked bool
	err := s.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth_blacklist WHERE operation_type='login' AND (blocked_until IS NULL OR blocked_until>now()) AND ((subject_type='username' AND subject=$1) OR (subject_type='ip' AND subject=$2)))`, username, host).Scan(&blocked)
	return err == nil && blocked
}

func authenticationHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

// RecordAdministratorLoginFailure preserves the existing independent source
// blacklist and account delay counters. The caller applies the delay.
func (s *Store) RecordAdministratorLoginFailure(ctx context.Context, username, host string, actorID int64) (int, int) {
	var maxAttempts, lockoutMinutes int
	if err := s.Pool.QueryRow(ctx, "SELECT login_max_attempts,login_lockout_minutes FROM auth_settings WHERE id=1").Scan(&maxAttempts, &lockoutMinutes); err != nil {
		return 0, 0
	}
	const attemptSQL = `INSERT INTO auth_rate_limits(key,attempts) VALUES($1,1)
 ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN auth_rate_limits.window_start < now()-make_interval(mins=>$2) THEN 1 ELSE auth_rate_limits.attempts+1 END,
 window_start=CASE WHEN auth_rate_limits.window_start < now()-make_interval(mins=>$2) THEN now() ELSE auth_rate_limits.window_start END RETURNING attempts`
	if host != "" {
		var attempts int
		if s.Pool.QueryRow(ctx, attemptSQL, authenticationHash("admin-login:"+username+":"+host), lockoutMinutes).Scan(&attempts) == nil && attempts >= maxAttempts {
			_, _ = s.Pool.Exec(ctx, `INSERT INTO auth_blacklist(subject_type,subject,operation_type,remote_addr,reason,blocked_until) VALUES('ip',$1,'login',$1,'连续登录失败',now()+make_interval(mins=>$2)) ON CONFLICT(subject_type,subject,operation_type) DO UPDATE SET remote_addr=excluded.remote_addr,reason=excluded.reason,blocked_until=GREATEST(auth_blacklist.blocked_until,excluded.blocked_until),version=auth_blacklist.version+1,updated_at=now()`, host, lockoutMinutes)
			s.RecordAuthenticationAudit(ctx, actorID, "auth.login_blocked", map[string]any{"username": username, "subject_type": "ip", "remote_addr": host, "duration_minutes": lockoutMinutes})
		}
	}
	var attempts int
	if err := s.Pool.QueryRow(ctx, attemptSQL, authenticationHash("admin-login-account:"+username), lockoutMinutes).Scan(&attempts); err != nil {
		return 0, 0
	}
	return attempts, maxAttempts
}

func (s *Store) ClearAdministratorLoginPressure(ctx context.Context, username, host string) {
	keys := []string{authenticationHash("admin-login-account:" + username)}
	if host != "" {
		keys = append(keys, authenticationHash("admin-login:"+username+":"+host))
	}
	_, _ = s.Pool.Exec(ctx, "DELETE FROM auth_rate_limits WHERE key=ANY($1)", keys)
}

func (s *Store) AuthenticationCaptchaEnabled(ctx context.Context) (enabled bool, err error) {
	err = s.Pool.QueryRow(ctx, "SELECT auth_captcha_enabled FROM site_settings WHERE id=1").Scan(&enabled)
	return
}

type AuthenticationCaptchaClaim struct {
	Binding  captcha.Binding
	Kind     string
	State    []byte
	Expected string
	Expires  time.Time
}

func (s *Store) ClaimAuthenticationCaptcha(ctx context.Context, id, action, nonceHash string) (out AuthenticationCaptchaClaim, err error) {
	err = s.Pool.QueryRow(ctx, `UPDATE auth_captcha_challenges SET consumed_at=now() WHERE id=$1 AND action=$2 AND nonce_hash=$3 AND consumed_at IS NULL AND expires_at>now() RETURNING provider_id,profile_id,profile_fingerprint,challenge_kind,provider_state,answer_hash,expires_at`, id, action, nonceHash).Scan(&out.Binding.ImplementationID, &out.Binding.ProfileID, &out.Binding.Fingerprint, &out.Kind, &out.State, &out.Expected, &out.Expires)
	return
}

var ErrAuthenticationRoleChanged = errors.New("authentication role changed")

func lockAuthenticatedAccount(ctx context.Context, tx pgx.Tx, table string, userID int64, expectedHash, expectedRole string, expectedMFA bool) error {
	var role, hash string
	var mfa, reset bool
	err := tx.QueryRow(ctx, "SELECT role,password_hash,mfa_enabled,password_reset_required FROM "+table+" WHERE id=$1 AND status='active' FOR SHARE", userID).Scan(&role, &hash, &mfa, &reset)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrAuthenticationRoleChanged
	}
	if err != nil {
		return err
	}
	if hash != expectedHash || mfa != expectedMFA || reset || expectedRole != "" && role != expectedRole {
		return ErrAuthenticationRoleChanged
	}
	return nil
}

func (s *Store) CreateAuthenticationSession(ctx context.Context, userID int64, role, passwordHash, tokenHash string) error {
	table, err := authenticationTableForPortal(LoginPortalForRole(role))
	if err != nil {
		return ErrAuthenticationRoleChanged
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := lockAuthenticatedAccount(ctx, tx, table, userID, passwordHash, role, false); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')", tokenHash, userID)
		return err
	})
}

func (s *Store) CreateAuthenticationMFAChallenge(ctx context.Context, userID int64, method, portal, passwordHash, challengeHash string) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		table, err := authenticationTableForPortal(portal)
		if err != nil {
			return ErrAuthenticationRoleChanged
		}
		if err = lockAuthenticatedAccount(ctx, tx, table, userID, passwordHash, "", true); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE user_id=$1 OR expires_at<=now()", userID); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "INSERT INTO mfa_challenges(token_hash,user_id,login_method,login_portal,expires_at) VALUES($1,$2,$3,$4,now()+interval '5 minutes')", challengeHash, userID, method, portal)
		return err
	})
}

// AuthenticationMFAResult describes policy/verification denials without exposing
// a challenge's owner. All successful recovery/session writes share one commit.
type AuthenticationMFAResult struct {
	User       LoginCredential
	Denial     string
	FailedCode bool
}

func (s *Store) CompleteAuthenticationMFA(ctx context.Context, challengeHash, portal, recoveryHash, sessionHash string, verify func([]byte) bool) (out AuthenticationMFAResult, err error) {
	out.Denial = "invalid_mfa"
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var secretEnc []byte
	var method, frozenPortal string
	var attempts int
	if err = tx.QueryRow(ctx, "SELECT login_portal FROM mfa_challenges WHERE token_hash=$1 AND expires_at>now()", challengeHash).Scan(&frozenPortal); errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	} else if err != nil {
		return out, err
	}
	table, err := authenticationTableForPortal(frozenPortal)
	if err != nil {
		return out, nil
	}
	err = tx.QueryRow(ctx, `SELECT c.user_id,c.attempts,u.username,u.email,u.display_name,u.role,u.mfa_secret_enc,coalesce(c.login_method,''),c.login_portal FROM mfa_challenges c JOIN `+table+` u ON u.id=c.user_id WHERE c.token_hash=$1 AND c.expires_at>now() AND u.status='active' AND u.mfa_enabled=true FOR UPDATE`, challengeHash).Scan(&out.User.ID, &attempts, &out.User.Username, &out.User.Email, &out.User.Name, &out.User.Role, &secretEnc, &method, &frozenPortal)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	// Check before attempts or recovery-code writes. Omitted legacy portal is
	// accepted only for the audience frozen when the password was authenticated.
	if (portal != "" && portal != frozenPortal) || LoginPortalForRole(out.User.Role) != frozenPortal {
		return out, nil
	}
	var enabled, methodEnabled bool
	if err = tx.QueryRow(ctx, `SELECT login_enabled,CASE $1 WHEN 'username' THEN login_username_enabled WHEN 'email' THEN login_email_enabled WHEN 'phone' THEN login_phone_enabled ELSE false END FROM auth_settings WHERE id=1 FOR SHARE`, method).Scan(&enabled, &methodEnabled); err != nil {
		return out, err
	}
	if out.User.Role == "customer" && !enabled {
		out.Denial = "login_disabled"
		return out, nil
	}
	if method == "" || (out.User.Role == "customer" && (method == "username" || !methodEnabled)) {
		out.Denial = "login_method_disabled"
		return out, nil
	}
	if attempts >= 5 {
		return out, nil
	}
	if _, err = tx.Exec(ctx, "UPDATE mfa_challenges SET attempts=attempts+1 WHERE token_hash=$1", challengeHash); err != nil {
		return out, err
	}
	valid := verify(secretEnc)
	if !valid && recoveryHash != "" {
		var usedHash string
		recoveryErr := tx.QueryRow(ctx, "UPDATE mfa_recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL RETURNING code_hash", out.User.ID, recoveryHash).Scan(&usedHash)
		if recoveryErr != nil && !errors.Is(recoveryErr, pgx.ErrNoRows) {
			return out, recoveryErr
		}
		valid = recoveryErr == nil
	}
	if !valid {
		if attempts+1 >= 5 {
			if _, err = tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE token_hash=$1", challengeHash); err != nil {
				return out, err
			}
		}
		out.FailedCode = true
		return out, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "DELETE FROM mfa_challenges WHERE token_hash=$1", challengeHash); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')", sessionHash, out.User.ID); err != nil {
		return out, err
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	out.Denial = ""
	return out, nil
}

func (s *Store) DeleteAuthenticationSession(ctx context.Context, userID int64, hash string) error {
	_, err := s.Pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1 AND user_id=$2", hash, userID)
	return err
}

type HTTPMaintenanceConfiguration struct {
	Enabled bool
	Message string
	URL     string
}

func (s *Store) HTTPMaintenanceSettings(ctx context.Context) (HTTPMaintenanceConfiguration, error) {
	var settings HTTPMaintenanceConfiguration
	err := s.Pool.QueryRow(ctx, "SELECT maintenance_enabled,maintenance_message,maintenance_url FROM site_settings WHERE id=1").Scan(&settings.Enabled, &settings.Message, &settings.URL)
	return settings, err
}
