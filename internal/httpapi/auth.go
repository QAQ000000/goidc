package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"strconv"
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
func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "login", 30) {
		return
	}
	var input struct {
		Username      string `json:"username"`
		Password      string `json:"password"`
		CaptchaID     string `json:"captcha_id"`
		CaptchaAnswer string `json:"captcha_answer"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.consumeCaptcha(r.Context(), r, "login", input.CaptchaID, input.CaptchaAnswer); err != nil {
		if !errors.Is(err, errInvalidCaptcha) {
			s.captchaUnavailable(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_captcha", "验证码错误，请刷新后重试")
		return
	}
	if len(input.Username) > 254 || len(input.Password) > 72 || input.Username == "" {
		writeError(w, 400, "invalid_credentials", "登录信息无效")
		return
	}
	username := strings.ToLower(strings.TrimSpace(input.Username))
	if s.loginBlocked(r.Context(), username, requestHost(r)) {
		writeError(w, http.StatusForbidden, "login_blocked", "该登录来源已被暂时禁止，请稍后再试")
		return
	}
	var id identity
	var hash, status string
	var resetRequired, mfaEnabled bool
	err := s.db.Pool.QueryRow(r.Context(), "SELECT id,username,email,display_name,role,password_hash,status,password_reset_required,mfa_enabled FROM users WHERE username=$1 OR email=$1", username).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role, &hash, &status, &resetRequired, &mfaEnabled)
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
	if err != nil || match != nil || status != "active" {
		s.authAudit(r.Context(), id.ID, "auth.login_failed", map[string]any{"username": username, "remote_addr": r.RemoteAddr})
		if id.ID > 0 && (id.Role == "admin" || id.Role == "super_admin") {
			s.recordAdminLoginFailure(r.Context(), username, requestHost(r), id.ID)
		}
		writeError(w, 401, "invalid_credentials", "账号或密码错误")
		return
	}
	if legacyHash && match == nil {
		upgraded, upgradeErr := bcrypt.GenerateFromPassword([]byte(input.Password), 12)
		if upgradeErr != nil {
			s.fail(w, r, upgradeErr)
			return
		}
		if _, upgradeErr = s.db.Pool.Exec(r.Context(), `UPDATE users
SET password_hash=$2,password_reset_required=false,updated_at=now()
WHERE id=$1 AND password_hash=$3`, id.ID, string(upgraded), hash); upgradeErr != nil {
			s.fail(w, r, upgradeErr)
			return
		}
		resetRequired = false
		s.authAudit(r.Context(), id.ID, "auth.legacy_password_upgraded", map[string]any{})
	}
	if resetRequired {
		s.authAudit(r.Context(), id.ID, "auth.password_reset_required", map[string]any{"remote_addr": r.RemoteAddr})
		writeError(w, http.StatusForbidden, "password_reset_required", "该账户需要先重置密码")
		return
	}
	if mfaEnabled {
		challenge, challengeErr := s.createMFAChallenge(r.Context(), id.ID)
		if challengeErr != nil {
			s.fail(w, r, challengeErr)
			return
		}
		s.authAudit(r.Context(), id.ID, "auth.mfa_challenge_created", map[string]any{"remote_addr": r.RemoteAddr})
		writeErrorDetails(w, http.StatusUnauthorized, "mfa_required", "请输入身份验证器验证码", map[string]any{"challenge": challenge})
		return
	}
	if err = s.completeLogin(w, r, id); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, id)
}
func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if _, err := r.Cookie(adminSessionCookie); err == nil {
		s.exitImpersonation(w, r)
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	if _, err := s.db.Pool.Exec(r.Context(), "DELETE FROM sessions WHERE token_hash=$1", tokenHash(cookie.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	s.authAudit(r.Context(), identityFrom(r).ID, "auth.logout", map[string]any{"remote_addr": r.RemoteAddr})
	http.SetCookie(w, &http.Cookie{Name: "zjmfgo_session", Value: "", Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: -1, Expires: time.Unix(0, 0)})
	write(w, 200, map[string]bool{"logged_out": true})
}

func (s *Server) authAudit(ctx context.Context, actorID int64, action string, payload map[string]any) {
	b, err := json.Marshal(payload)
	if err != nil {
		return
	}
	entityID := strconv.FormatInt(actorID, 10)
	_, _ = s.db.Pool.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES(NULLIF($1,0),$2,'user',$3,$4)", actorID, action, entityID, b)
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
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
	var enabled bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT registration_enabled FROM site_settings WHERE id=1").Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if !enabled {
		writeError(w, 403, "registration_disabled", "本站暂未开放注册")
		return
	}
	var input struct {
		Username         string `json:"username"`
		Email            string `json:"email"`
		Password         string `json:"password"`
		Name             string `json:"display_name"`
		VerificationID   string `json:"verification_id"`
		VerificationCode string `json:"verification_code"`
		AffiliateCode    string `json:"affiliate_code"`
		CaptchaID        string `json:"captcha_id"`
		CaptchaAnswer    string `json:"captcha_answer"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Username = strings.ToLower(strings.TrimSpace(input.Username))
	input.Email = strings.ToLower(strings.TrimSpace(input.Email))
	input.Name = strings.TrimSpace(input.Name)
	email, err := mail.ParseAddress(input.Email)
	if err != nil || email.Address != input.Email || len(input.Email) > 254 || !validSlug(input.Username) || len(input.Username) < 3 || len(input.Username) > 48 || len(input.Password) < 8 || len(input.Password) > 72 || input.Name == "" || len(input.Name) > 120 {
		writeError(w, 400, "invalid_registration", "请检查账号、邮箱、姓名和密码长度")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var id int64
	if err = tx.QueryRow(r.Context(), "SELECT registration_enabled FROM site_settings WHERE id=1 FOR SHARE").Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if !enabled {
		writeError(w, 403, "registration_disabled", "本站暂未开放注册")
		return
	}
	if err := s.consumeCaptcha(r.Context(), r, "register", input.CaptchaID, input.CaptchaAnswer); err != nil {
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
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var exists bool
	if err = store.LockAdminCustomer(r.Context(), tx, identityFrom(r).ID, id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='customer')", id).Scan(&exists); err != nil {
		s.fail(w, r, err)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found", "客户不存在")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,password_reset_required=false,updated_at=now() WHERE id=$1", id, string(hash)); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", id); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "customer.password_reset", "user", id, map[string]string{"reason": "admin_reset"}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"user_id": id, "password_reset_required": false})
}

func (s *Server) authLimit(w http.ResponseWriter, r *http.Request, scope string, limit int) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	var count int
	err = s.db.Pool.QueryRow(r.Context(), `INSERT INTO auth_rate_limits(key,attempts) VALUES($1,1)
 ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN auth_rate_limits.window_start < now()-interval '1 minute' THEN 1 ELSE auth_rate_limits.attempts+1 END,
 window_start=CASE WHEN auth_rate_limits.window_start < now()-interval '1 minute' THEN now() ELSE auth_rate_limits.window_start END RETURNING attempts`, tokenHash(scope+":"+host)).Scan(&count)
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

func (s *Server) loginBlocked(ctx context.Context, username, host string) bool {
	var blocked bool
	err := s.db.Pool.QueryRow(ctx, `SELECT EXISTS(
		SELECT 1 FROM auth_blacklist
		WHERE operation_type='login'
		  AND (blocked_until IS NULL OR blocked_until>now())
		  AND ((subject_type='username' AND subject=$1) OR (subject_type='ip' AND subject=$2))
	)`, username, host).Scan(&blocked)
	return err == nil && blocked
}

func (s *Server) recordAdminLoginFailure(ctx context.Context, username, host string, actorID int64) {
	key := tokenHash("admin-login:" + username + ":" + host)
	var attempts int
	if err := s.db.Pool.QueryRow(ctx, `INSERT INTO auth_rate_limits(key,attempts) VALUES($1,1)
 ON CONFLICT(key) DO UPDATE SET attempts=CASE WHEN auth_rate_limits.window_start < now()-interval '3 hours' THEN 1 ELSE auth_rate_limits.attempts+1 END,
 window_start=CASE WHEN auth_rate_limits.window_start < now()-interval '3 hours' THEN now() ELSE auth_rate_limits.window_start END RETURNING attempts`, key).Scan(&attempts); err != nil || attempts < 3 {
		return
	}
	_, _ = s.db.Pool.Exec(ctx, `INSERT INTO auth_blacklist(subject_type,subject,operation_type,remote_addr,reason,blocked_until)
 VALUES('username',$1,'login',$2,'连续登录失败',now()+interval '3 hours')
 ON CONFLICT(subject_type,subject,operation_type) DO UPDATE SET remote_addr=excluded.remote_addr,reason=excluded.reason,blocked_until=GREATEST(auth_blacklist.blocked_until,excluded.blocked_until),version=auth_blacklist.version+1,updated_at=now()`, username, host)
	s.authAudit(ctx, actorID, "auth.login_blocked", map[string]any{"username": username, "remote_addr": host, "duration": "3h"})
}
