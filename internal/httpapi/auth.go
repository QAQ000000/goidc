package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	var id identity
	var hash, status string
	var resetRequired, mfaEnabled bool
	err := s.db.Pool.QueryRow(r.Context(), "SELECT id,username,email,display_name,role,password_hash,status,password_reset_required,mfa_enabled FROM users WHERE username=$1 OR email=$1", strings.ToLower(strings.TrimSpace(input.Username))).Scan(&id.ID, &id.Username, &id.Email, &id.Name, &id.Role, &hash, &status, &resetRequired, &mfaEnabled)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = "$2b$12$IaaLuziNj3ry6N5MGpfILOWgfDw.czQWlhSIXXp68bPxDJiIX1.3a"
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	match := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password))
	if err != nil || match != nil || status != "active" {
		s.authAudit(r.Context(), id.ID, "auth.login_failed", map[string]any{"username": strings.ToLower(strings.TrimSpace(input.Username)), "remote_addr": r.RemoteAddr})
		writeError(w, 401, "invalid_credentials", "账号或密码错误")
		return
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
		Username      string `json:"username"`
		Email         string `json:"email"`
		Password      string `json:"password"`
		Name          string `json:"display_name"`
		CaptchaID     string `json:"captcha_id"`
		CaptchaAnswer string `json:"captcha_answer"`
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

func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "password-reset", 5) {
		return
	}
	var input struct {
		Identifier    string `json:"identifier"`
		CaptchaID     string `json:"captcha_id"`
		CaptchaAnswer string `json:"captcha_answer"`
	}
	if !decode(w, r, &input) {
		return
	}
	if err := s.consumeCaptcha(r.Context(), r, "forgot-password", input.CaptchaID, input.CaptchaAnswer); err != nil {
		if !errors.Is(err, errInvalidCaptcha) {
			s.captchaUnavailable(w, r)
			return
		}
		writeError(w, http.StatusUnauthorized, "invalid_captcha", "验证码错误，请刷新后重试")
		return
	}
	input.Identifier = strings.ToLower(strings.TrimSpace(input.Identifier))
	if input.Identifier == "" || len(input.Identifier) > 254 {
		writeError(w, http.StatusBadRequest, "invalid_password_reset", "账号或邮箱无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var userID int64
	var email string
	err = tx.QueryRow(r.Context(), "SELECT id,email FROM users WHERE (lower(username)=lower($1) OR lower(email)=lower($1)) AND status='active'", input.Identifier).Scan(&userID, &email)
	if errors.Is(err, pgx.ErrNoRows) {
		// Keep the response identical for unknown accounts to prevent enumeration.
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, http.StatusAccepted, map[string]any{"queued": true})
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	token := hex.EncodeToString(raw)
	if _, err = tx.Exec(r.Context(), "UPDATE password_reset_tokens SET consumed_at=now() WHERE user_id=$1 AND consumed_at IS NULL", userID); err != nil {
		s.fail(w, r, err)
		return
	}
	requestedIP := r.RemoteAddr
	if host, _, splitErr := net.SplitHostPort(r.RemoteAddr); splitErr == nil {
		requestedIP = host
	}
	if _, err = tx.Exec(r.Context(), "INSERT INTO password_reset_tokens(user_id,token_hash,expires_at,requested_ip) VALUES($1,$2,now()+interval '30 minutes',$3)", userID, tokenHash(token), requestedIP); err != nil {
		s.fail(w, r, err)
		return
	}
	link := strings.TrimRight(s.cfg.PublicOrigin, "/") + "/reset-password?token=" + url.QueryEscape(token)
	subject := "重置 ZJMF 密码"
	templateBody := "您好，\n\n请打开以下链接重置您的 ZJMF 密码（30 分钟内有效）：\n{{reset_link}}\n\n如果不是您本人操作，请忽略此邮件。"
	var templateActive bool
	templateErr := tx.QueryRow(r.Context(), "SELECT subject,body,active FROM notification_templates WHERE kind='password_reset'").Scan(&subject, &templateBody, &templateActive)
	if errors.Is(templateErr, pgx.ErrNoRows) {
		templateActive = true
	} else if templateErr != nil {
		s.fail(w, r, templateErr)
		return
	}
	if templateActive {
		body := strings.ReplaceAll(templateBody, "{{reset_link}}", link)
		if _, err = tx.Exec(r.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES($1,$2,$3,'password_reset')`, email, subject, body); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = store.Audit(r.Context(), tx, userID, "auth.password_reset_requested", "user", userID, map[string]any{"requested_ip": requestedIP}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusAccepted, map[string]any{"queued": true})
}

func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Token = strings.TrimSpace(input.Token)
	if len(input.Token) != 64 || len(input.Password) < 8 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_password_reset", "重置令牌或密码无效")
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
	var userID int64
	if err = tx.QueryRow(r.Context(), "SELECT user_id FROM password_reset_tokens WHERE token_hash=$1 AND consumed_at IS NULL AND expires_at>now() FOR UPDATE", tokenHash(input.Token)).Scan(&userID); errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusConflict, "invalid_password_reset", "重置令牌无效或已过期")
		return
	} else if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1", userID, string(hash)); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", userID); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE password_reset_tokens SET consumed_at=now() WHERE token_hash=$1", tokenHash(input.Token)); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, userID, "auth.password_reset_completed", "user", userID, map[string]any{}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{"user_id": userID, "reset": true})
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
