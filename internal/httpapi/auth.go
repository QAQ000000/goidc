package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/codex/zjmfgo/internal/captcha"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/legacy"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func tokenHash(token string) string {
	v := sha256.Sum256([]byte(token))
	return hex.EncodeToString(v[:])
}

// adminFailureMaxDelay caps the exponential backoff applied to repeated failed
// administrator logins for one account. Successful logins are never delayed,
// so the cap only bounds how long a brute-force attempt is stalled per request.
const adminFailureMaxDelay = 15 * time.Second

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "login", 30) {
		return
	}
	var input struct {
		Portal        string         `json:"portal"`
		Identifier    string         `json:"identifier"`
		Username      string         `json:"username"`
		Password      string         `json:"password"`
		CaptchaID     string         `json:"captcha_id"`
		CaptchaAnswer string         `json:"captcha_answer"`
		CaptchaProof  *captcha.Proof `json:"captcha_proof"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.Portal != "" && input.Portal != "customer" && input.Portal != "admin" {
		writeError(w, http.StatusBadRequest, "invalid_credentials", "登录信息无效")
		return
	}
	if err := s.consumeCaptcha(r.Context(), r, "login", input.CaptchaID, input.CaptchaAnswer, input.CaptchaProof); err != nil {
		if !errors.Is(err, errInvalidCaptcha) {
			s.captchaUnavailable(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_captcha", "验证码错误，请刷新后重试")
		return
	}
	identifier := strings.TrimSpace(input.Identifier)
	if identifier == "" {
		identifier = strings.TrimSpace(input.Username) // Compatibility for existing API clients.
	} else if input.Username != "" && strings.TrimSpace(input.Username) != identifier {
		writeError(w, 400, "invalid_credentials", "请只提供一个登录邮箱或手机号")
		return
	}
	if len(identifier) > 254 || len(input.Password) > 72 || identifier == "" {
		writeError(w, 400, "invalid_credentials", "登录信息无效")
		return
	}
	username, method := normalizeLoginIdentifier(identifier)
	// The administrator form retains the username field, including numeric
	// administrator names. Customer forms send identifier for phone inference.
	if input.Identifier == "" && validSlug(strings.ToLower(identifier)) {
		username, method = strings.ToLower(identifier), "username"
	}
	settings, err := s.db.AuthenticationLoginSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	clientAddr := s.clientIP(r)
	if s.loginBlocked(r.Context(), username, clientAddr) {
		writeError(w, http.StatusForbidden, "login_blocked", "该登录来源已被暂时禁止，请稍后再试")
		return
	}
	credential, err := s.db.AuthenticationCredential(r.Context(), username, method, input.Portal)
	id := identity{ID: credential.ID, Username: credential.Username, Email: credential.Email, Name: credential.Name, Role: credential.Role}
	hash, status := credential.PasswordHash, credential.Status
	resetRequired, mfaEnabled := credential.ResetRequired, credential.MFAEnabled
	if errors.Is(err, pgx.ErrNoRows) {
		hash = "$2b$12$IaaLuziNj3ry6N5MGpfILOWgfDw.czQWlhSIXXp68bPxDJiIX1.3a"
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	legacyHash := legacy.IsCMFPasswordHash(hash)
	match := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password))
	if match != nil && legacyHash {
		if legacy.VerifyCMFPassword(input.Password, hash, s.cfg.LegacyCMFAuthCode) {
			match = nil
		}
	}
	if err != nil || match != nil || status != "active" || store.LoginPortalForRole(id.Role) == "" || input.Portal != "" && input.Portal != store.LoginPortalForRole(id.Role) {
		s.authAudit(r.Context(), id.ID, "auth.login_failed", map[string]any{"username": username, "remote_addr": clientAddr})
		if id.ID > 0 && (id.Role == "admin" || id.Role == "super_admin") {
			if delay := s.recordAdminLoginFailure(r.Context(), id.Username, clientAddr, id.ID); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-r.Context().Done():
					timer.Stop()
				}
			}
		}
		writeError(w, 401, "invalid_credentials", "账号或密码错误")
		return
	}
	if id.Role == "admin" || id.Role == "super_admin" {
		// A verified administrator password clears the per-address failure
		// pressure so shared egress is not pushed towards an automatic block.
		s.clearAdminLoginPressure(r.Context(), id.Username, clientAddr)
	}
	if !settings.Enabled && id.Role == "customer" {
		writeError(w, http.StatusForbidden, "login_disabled", "本站暂未开放客户登录")
		return
	}
	if id.Role == "customer" && (method == "username" || method == "email" && !settings.EmailEnabled || method == "phone" && !settings.PhoneEnabled) {
		writeError(w, http.StatusForbidden, "login_method_disabled", "此登录方式暂未开放")
		return
	}
	if legacyHash && match == nil {
		upgraded, upgradeErr := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
		if upgradeErr != nil {
			s.fail(w, r, upgradeErr)
			return
		}
		changed, upgradeErr := s.db.UpgradeAuthenticationPassword(r.Context(), id.ID, id.Role, hash, string(upgraded))
		if upgradeErr != nil {
			s.fail(w, r, upgradeErr)
			return
		}
		if !changed {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
			return
		}
		resetRequired = false
		hash = string(upgraded)
		s.authAudit(r.Context(), id.ID, "auth.legacy_password_upgraded", map[string]any{})
	}
	if resetRequired {
		s.authAudit(r.Context(), id.ID, "auth.password_reset_required", map[string]any{"remote_addr": s.clientIP(r)})
		writeError(w, http.StatusForbidden, "password_reset_required", "该账户需要先重置密码")
		return
	}
	if mfaEnabled {
		challenge, challengeErr := s.createMFAChallenge(r.Context(), id.ID, method, store.LoginPortalForRole(id.Role), hash)
		if errors.Is(challengeErr, store.ErrAuthenticationRoleChanged) {
			writeError(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
			return
		}
		if challengeErr != nil {
			s.fail(w, r, challengeErr)
			return
		}
		s.authAudit(r.Context(), id.ID, "auth.mfa_challenge_created", map[string]any{"remote_addr": s.clientIP(r)})
		writeErrorDetails(w, http.StatusUnauthorized, "mfa_required", "请输入身份验证器验证码", map[string]any{"challenge": challenge})
		return
	}
	if err = s.completeLogin(w, r, id, hash); errors.Is(err, store.ErrAuthenticationRoleChanged) {
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "账号或密码错误")
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, id)
}

// Domestic mobile numbers use China's calling code. International numbers must
// include + or 00; normalization never changes a name into an email address.
func normalizeLoginIdentifier(raw string) (string, string) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if strings.Contains(value, "@") {
		return value, "email"
	}
	phone := strings.NewReplacer(" ", "", "-", "", "(", "", ")", "").Replace(value)
	if strings.HasPrefix(phone, "00") {
		phone = "+" + phone[2:]
	} else if len(phone) == 11 && phone[0] == '1' && phone[1] >= '3' && phone[1] <= '9' {
		phone = "+86" + phone
	}
	if store.ValidSMSRecipient(phone) {
		return phone, "phone"
	}
	return value, "username" // Administrator accounts only.
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if apiKeyRequest(r) {
		writeError(w, http.StatusForbidden, "session_required", "该操作需要浏览器会话")
		return
	}
	cookie, err := originalSessionCookie(r, requestPortal(r))
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	if err = s.db.DeleteAuthenticationSession(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.authAudit(r.Context(), identityFrom(r).ID, "auth.logout", map[string]any{"remote_addr": s.clientIP(r)})
	s.clearSessionCookie(w, sessionCookieForRole(identityFrom(r).Role))
	write(w, 200, map[string]bool{"logged_out": true})
}

func (s *Server) authAudit(ctx context.Context, actorID int64, action string, payload map[string]any) {
	s.db.RecordAuthenticationAudit(ctx, actorID, action, payload)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	if apiKeyRequest(r) {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if !decode(w, r, &input) {
		return
	}
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, http.StatusUnauthorized, "unauthorized", "请先登录")
		return
	}
	if err = s.db.ChangePassword(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), input.CurrentPassword, input.NewPassword); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"changed": true})
}
func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "register", 10) {
		return
	}
	var enabled, registrationEmail bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT s.registration_enabled,a.registration_email_enabled FROM site_settings s JOIN auth_settings a ON a.id=1 WHERE s.id=1").Scan(&enabled, &registrationEmail); err != nil {
		s.fail(w, r, err)
		return
	}
	if !enabled || !registrationEmail {
		writeError(w, 403, "registration_disabled", "本站暂未开放注册")
		return
	}
	var input struct {
		Username            string         `json:"username"`
		Email               string         `json:"email"`
		Password            string         `json:"password"`
		Name                string         `json:"display_name"`
		VerificationID      string         `json:"verification_id"`
		VerificationCode    string         `json:"verification_code"`
		AffiliateCode       string         `json:"affiliate_code"`
		CaptchaID           string         `json:"captcha_id"`
		CaptchaAnswer       string         `json:"captcha_answer"`
		CaptchaProof        *captcha.Proof `json:"captcha_proof"`
		CustomFields        map[string]any `json:"custom_fields"`
		CustomFieldsVersion string         `json:"custom_fields_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	if input.Username == "" {
		var accountID [16]byte
		if _, err := rand.Read(accountID[:]); err != nil {
			s.fail(w, r, err)
			return
		}
		input.Username = "customer-" + hex.EncodeToString(accountID[:])
	}
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Name = strings.TrimSpace(input.Name)
	email, err := mail.ParseAddress(input.Email)
	if err != nil || email.Address != input.Email || len(input.Email) > 254 || !validSlug(input.Username) || len(input.Username) < 3 || len(input.Username) > 48 || len(input.Password) < 8 || len(input.Password) > 72 || input.Name == "" || len(input.Name) > 120 {
		writeError(w, 400, "invalid_registration", "请检查邮箱、姓名和密码长度")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	schema, allowed, err := registrationFieldsTx(r.Context(), tx)
	if err != nil {
		s.registrationFieldsError(w, r, err)
		return
	}
	if (len(allowed) > 0 || input.CustomFieldsVersion != "") && input.CustomFieldsVersion != schema.Version {
		writeError(w, 409, "registration_fields_changed", "注册字段已变化，请加载最新字段后重试")
		return
	}
	custom, fieldErr := validateCustomFieldValues(allowed, input.CustomFields, nil)
	if fieldErr != nil {
		writeError(w, 400, "invalid_custom_fields", "请检查注册自定义字段")
		return
	}
	if err := s.consumeCaptcha(r.Context(), r, "register", input.CaptchaID, input.CaptchaAnswer, input.CaptchaProof); err != nil {
		if !errors.Is(err, errInvalidCaptcha) {
			s.captchaUnavailable(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_captcha", "验证码错误，请刷新后重试")
		return
	}
	var requireVerification bool
	if err = tx.QueryRow(r.Context(), "SELECT email_verification_required FROM site_settings WHERE id=1").Scan(&requireVerification); err != nil {
		s.fail(w, r, err)
		return
	}
	verified := false
	if requireVerification || input.VerificationID != "" {
		challenge, valid, verifyErr := s.db.ConsumeVerification(r.Context(), tx, input.VerificationID, input.VerificationCode, "register", nil)
		if verifyErr != nil {
			s.fail(w, r, verifyErr)
			return
		}
		if !valid || challenge.Channel != "email" || challenge.Recipient != input.Email {
			if err = tx.Commit(r.Context()); err != nil {
				s.fail(w, r, err)
				return
			}
			writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
			return
		}
		verified = true
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "INSERT INTO users(username,email,password_hash,display_name) VALUES($1,$2,$3,$4) RETURNING id", input.Username, input.Email, string(hash), input.Name).Scan(&id); err != nil {
		s.fail(w, r, err)
		return
	}
	customRaw, err := json.Marshal(custom)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO customer_profiles(customer_id,custom_fields) VALUES($1,$2)", id, customRaw); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO carts(user_id) VALUES($1)", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET email_verified=$2 WHERE id=$1", id, verified); err != nil {
		s.fail(w, r, err)
		return
	}
	affiliateCode := strings.TrimSpace(input.AffiliateCode)
	if affiliateCode == "" {
		if cookie, cookieErr := r.Cookie("zjmfgo_affiliate_code"); cookieErr == nil {
			affiliateCode = cookie.Value
		}
	}
	if session, cookieErr := r.Cookie("zjmfgo_affiliate_session"); cookieErr == nil && affiliateCode != "" && verified {
		if err = s.db.ConvertAffiliateClickTx(r.Context(), tx, id, affiliateCode, tokenHash(session.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = store.Audit(r.Context(), tx, id, "customer.registered", "user", id, map[string]string{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 201, map[string]int64{"id": id})
}

func (s *Server) adminSetCustomerPassword(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.Password) < 8 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_password", "密码长度必须为 8 到 72 个字符")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		var err error
		var exists bool
		if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, id); err != nil {
			return txResponse{}, err
		}
		if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='customer')", id).Scan(&exists); err != nil {
			return txResponse{}, err
		}
		if !exists {
			return txResponse{}, &txHTTPError{http.StatusNotFound, "not_found", "客户不存在"}
		}
		if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,password_reset_required=false,updated_at=now() WHERE id=$1", id, string(hash)); err != nil {
			return txResponse{}, err
		}
		if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.password_reset", "user", id, map[string]string{"reason": "admin_reset"}); err != nil {
			return txResponse{}, err
		}

		return txResponse{http.StatusOK, map[string]any{"user_id": id, "password_reset_required": false}}, nil
	})
}

func (s *Server) authLimit(w http.ResponseWriter, r *http.Request, scope string, limit int) bool {
	host := s.clientIP(r)
	count, err := s.db.AuthenticationRateAttempt(r.Context(), tokenHash(scope+":"+host))
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	if count > limit {
		w.Header().Set("Retry-After", "60")
		writeError(w, 429, "rate_limited", "操作频繁，请稍后重试")
		return false
	}
	return true
}

// loginBlocked applies manual and automatic auth_blacklist rows. Manual
// username entries stay global on purpose; IP entries only cover the client
// address they name.
func (s *Server) loginBlocked(ctx context.Context, username, host string) bool {
	return s.db.AuthenticationLoginBlocked(ctx, username, host)
}

// adminFailureDelay converts the number of recorded failures for one
// administrator account into an exponentially growing delay applied to each
// further failed attempt. The first maxAttempts failures run at full speed;
// afterwards every additional failure doubles the wait, capped at
// adminFailureMaxDelay. Only failed attempts are delayed, so a correct
// password always authenticates immediately and the delay cannot be used to
// lock the account out.
func adminFailureDelay(attempts, maxAttempts int) time.Duration {
	if attempts <= maxAttempts {
		return 0
	}
	delay := time.Second << uint(min(attempts-maxAttempts-1, 10))
	if delay > adminFailureMaxDelay {
		return adminFailureMaxDelay
	}
	return delay
}

// recordAdminLoginFailure records one failed administrator login. Per-source
// pressure keeps the historical strength: after login_max_attempts failures
// from the same address the address is blacklisted for login_lockout_minutes,
// exactly as before but scoped to an 'ip' subject so the attacker only locks
// the source they actually used. Failures from many addresses against one
// account only accumulate an exponentially growing delay returned to the
// caller, never a global account block; administrators retain that decision
// through the manual username blacklist.
func (s *Server) recordAdminLoginFailure(ctx context.Context, username, host string, actorID int64) time.Duration {
	attempts, maxAttempts := s.db.RecordAdministratorLoginFailure(ctx, username, host, actorID)
	return adminFailureDelay(attempts, maxAttempts)
}

// clearAdminLoginPressure forgets the failure history a verified administrator
// login accumulated, so normal use of a shared address never drifts towards an
// automatic block.
func (s *Server) clearAdminLoginPressure(ctx context.Context, username, host string) {
	s.db.ClearAdministratorLoginPressure(ctx, username, host)
}
