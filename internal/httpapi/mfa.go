package httpapi

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

const mfaStep = 30 * time.Second

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, id identity) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	if _, err := s.db.Pool.Exec(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')", tokenHash(token), id.ID); err != nil {
		return err
	}
	s.authAudit(r.Context(), id.ID, "auth.login_succeeded", map[string]any{"remote_addr": r.RemoteAddr})
	http.SetCookie(w, &http.Cookie{Name: "zjmfgo_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	return nil
}

func (s *Server) createMFAChallenge(ctx context.Context, userID int64, method string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	challenge := hex.EncodeToString(raw)
	_, err := s.db.Pool.Exec(ctx, "DELETE FROM mfa_challenges WHERE user_id=$1 OR expires_at<=now()", userID)
	if err != nil {
		return "", err
	}
	_, err = s.db.Pool.Exec(ctx, "INSERT INTO mfa_challenges(token_hash,user_id,login_method,expires_at) VALUES($1,$2,$3,now()+interval '5 minutes')", tokenHash(challenge), userID, method)
	return challenge, err
}

func (s *Server) loginMFA(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "login-mfa", 30) {
		return
	}
	var input struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Challenge = strings.TrimSpace(input.Challenge)
	input.Code = strings.TrimSpace(input.Code)
	if len(input.Challenge) != 64 || (!validMFACode(input.Code) && !validRecoveryCode(input.Code)) {
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码或登录挑战无效")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var userID int64
	var secretEnc []byte
	var username, email, name, role, method string
	var attempts int
	err = tx.QueryRow(r.Context(), `SELECT c.user_id,c.attempts,u.username,u.email,u.display_name,u.role,u.mfa_secret_enc,coalesce(c.login_method,'')
		FROM mfa_challenges c JOIN users u ON u.id=c.user_id
		WHERE c.token_hash=$1 AND c.expires_at>now() AND u.status='active' AND u.mfa_enabled=true
		FOR UPDATE`, tokenHash(input.Challenge)).Scan(&userID, &attempts, &username, &email, &name, &role, &secretEnc, &method)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码或登录挑战无效")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var loginEnabled, methodEnabled bool
	if err = tx.QueryRow(r.Context(), `SELECT login_enabled,CASE $1 WHEN 'username' THEN login_username_enabled WHEN 'email' THEN login_email_enabled WHEN 'phone' THEN login_phone_enabled ELSE false END FROM auth_settings WHERE id=1 FOR SHARE`, method).Scan(&loginEnabled, &methodEnabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if role == "customer" && !loginEnabled {
		writeError(w, http.StatusForbidden, "login_disabled", "本站暂未开放客户登录")
		return
	}
	if method == "" || (role == "customer" && !methodEnabled) {
		writeError(w, http.StatusForbidden, "login_method_disabled", "此登录方式暂未开放，请重新登录")
		return
	}
	if attempts >= 5 {
		_, _ = tx.Exec(r.Context(), "DELETE FROM mfa_challenges WHERE token_hash=$1", tokenHash(input.Challenge))
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码尝试次数过多，请重新登录")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE mfa_challenges SET attempts=attempts+1 WHERE token_hash=$1", tokenHash(input.Challenge)); err != nil {
		s.fail(w, r, err)
		return
	}
	secret, err := s.decryptMFASecret(secretEnc)
	validCode := err == nil && verifyTOTP(secret, input.Code, time.Now())
	if !validCode && validRecoveryCode(input.Code) {
		var usedHash string
		recoveryErr := tx.QueryRow(r.Context(), "UPDATE mfa_recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL RETURNING code_hash", userID, tokenHash(strings.ToUpper(input.Code))).Scan(&usedHash)
		validCode = recoveryErr == nil
	}
	if !validCode {
		if attempts+1 >= 5 {
			_, _ = tx.Exec(r.Context(), "DELETE FROM mfa_challenges WHERE token_hash=$1", tokenHash(input.Challenge))
		}
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		s.authAudit(r.Context(), userID, "auth.mfa_failed", map[string]any{"remote_addr": r.RemoteAddr})
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码错误")
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM mfa_challenges WHERE token_hash=$1", tokenHash(input.Challenge)); err != nil {
		s.fail(w, r, err)
		return
	}
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	token := hex.EncodeToString(raw)
	if _, err = tx.Exec(r.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')", tokenHash(token), userID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.authAudit(r.Context(), userID, "auth.login_succeeded", map[string]any{"remote_addr": r.RemoteAddr})
	http.SetCookie(w, &http.Cookie{Name: "zjmfgo_session", Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	write(w, http.StatusOK, identity{ID: userID, Username: username, Email: email, Name: name, Role: role})
}

func (s *Server) mfaStatus(w http.ResponseWriter, r *http.Request) {
	var enabled bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT mfa_enabled FROM users WHERE id=$1", identityFrom(r).ID).Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

func (s *Server) mfaEnroll(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.MFAEncryptionKey) != 32 {
		writeError(w, http.StatusServiceUnavailable, "mfa_unavailable", "服务器未配置 MFA 加密密钥")
		return
	}
	user := identityFrom(r)
	raw := make([]byte, 20)
	if _, err := rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	secret := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(raw)
	enc, err := s.encryptMFASecret(secret)
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
	var enabled, allowed bool
	if err = tx.QueryRow(r.Context(), "SELECT mfa_enabled FROM users WHERE id=$1 AND status='active' FOR UPDATE", user.ID).Scan(&enabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT CASE WHEN $1='customer' THEN customer_enabled ELSE admin_enabled END FROM mfa_settings WHERE id=1 FOR SHARE", user.Role).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "mfa_enrollment_disabled", "当前账户类型暂未开放二次验证设置")
		return
	}
	if enabled {
		writeError(w, http.StatusConflict, "mfa_already_enabled", "MFA 已启用")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET mfa_enabled=false,mfa_secret_enc=$2,mfa_confirmed_at=NULL,updated_at=now() WHERE id=$1", user.ID, enc); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM mfa_recovery_codes WHERE user_id=$1", user.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// Enrollment only stages a secret until mfaConfirm activates it, so API
	// keys are deliberately not revoked here: nothing is enforced yet and a
	// hijacked session must not be able to invalidate the owner's credentials
	// through an enrollment that is never confirmed. Revocation happens once in
	// mfaConfirm, at the moment the factor becomes effective.
	recoveryCodes := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		codeBytes := make([]byte, 5)
		if _, err = rand.Read(codeBytes); err != nil {
			s.fail(w, r, err)
			return
		}
		code := strings.ToUpper(hex.EncodeToString(codeBytes))
		if _, err = tx.Exec(r.Context(), "INSERT INTO mfa_recovery_codes(code_hash,user_id) VALUES($1,$2)", tokenHash(code), user.ID); err != nil {
			s.fail(w, r, err)
			return
		}
		recoveryCodes = append(recoveryCodes, code)
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	issuer := url.QueryEscape("ZJMF")
	account := url.QueryEscape(user.Email)
	otpauth := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30", issuer, account, secret, issuer)
	s.authAudit(r.Context(), user.ID, "auth.mfa_enrollment_started", map[string]any{})
	write(w, http.StatusOK, map[string]any{"secret": secret, "otpauth_uri": otpauth, "recovery_codes": recoveryCodes})
}

func (s *Server) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !validMFACode(strings.TrimSpace(input.Code)) {
		writeError(w, http.StatusBadRequest, "invalid_mfa", "请输入 6 位验证码")
		return
	}
	user := identityFrom(r)
	var enabled, allowed bool
	var role string
	var enc []byte
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = tx.QueryRow(r.Context(), "SELECT mfa_enabled,mfa_secret_enc,role FROM users WHERE id=$1 AND status='active' FOR UPDATE", user.ID).Scan(&enabled, &enc, &role); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT CASE WHEN $1='customer' THEN customer_enabled ELSE admin_enabled END FROM mfa_settings WHERE id=1 FOR SHARE", role).Scan(&allowed); err != nil {
		s.fail(w, r, err)
		return
	}
	if !allowed {
		writeError(w, http.StatusForbidden, "mfa_enrollment_disabled", "当前账户类型暂未开放二次验证设置")
		return
	}
	if enabled || len(enc) == 0 {
		writeError(w, http.StatusConflict, "mfa_not_pending", "没有待确认的 MFA 设置")
		return
	}
	secret, err := s.decryptMFASecret(enc)
	if err != nil || !verifyTOTP(secret, strings.TrimSpace(input.Code), time.Now()) {
		writeError(w, http.StatusBadRequest, "invalid_mfa", "验证码错误")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET mfa_enabled=true,mfa_confirmed_at=now(),updated_at=now() WHERE id=$1", user.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// Confirming enrollment is the moment the account actually gets hardened,
	// and API keys authenticate without MFA (see the bearer path of
	// Server.authWith). Keys issued while MFA was off would silently bypass the
	// factor this request just enabled, so every active key is revoked in the
	// same transaction and the owner reissues keys from a session that passed
	// MFA. Enrollment itself deliberately does not revoke: nothing is
	// protected yet and an unconfirmed enrollment must not destroy the user's
	// credentials. See mfaDisable for the symmetric decision.
	if _, err = tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", user.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.authAudit(r.Context(), user.ID, "auth.mfa_enabled", map[string]any{"api_keys_revoked": true})
	write(w, http.StatusOK, map[string]bool{"enabled": true})
}

func (s *Server) mfaDisable(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Password string `json:"password"`
		Code     string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	if (!validMFACode(strings.TrimSpace(input.Code)) && !validRecoveryCode(input.Code)) || len(input.Password) < 8 || len(input.Password) > 72 {
		writeError(w, http.StatusBadRequest, "invalid_mfa", "密码或验证码无效")
		return
	}
	user := identityFrom(r)
	var hash string
	var enc []byte
	var enabled bool
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = tx.QueryRow(r.Context(), "SELECT password_hash,mfa_enabled,mfa_secret_enc FROM users WHERE id=$1 AND status='active' FOR UPDATE", user.ID).Scan(&hash, &enabled, &enc); err != nil {
		s.fail(w, r, err)
		return
	}
	secret, decryptErr := s.decryptMFASecret(enc)
	validCode := decryptErr == nil && verifyTOTP(secret, strings.TrimSpace(input.Code), time.Now())
	passwordValid := bcrypt.CompareHashAndPassword([]byte(hash), []byte(input.Password)) == nil
	if !enabled || !passwordValid {
		writeError(w, http.StatusBadRequest, "invalid_mfa", "密码或验证码错误")
		return
	}
	if !validCode && validRecoveryCode(input.Code) {
		var usedHash string
		if err := tx.QueryRow(r.Context(), "UPDATE mfa_recovery_codes SET used_at=now() WHERE user_id=$1 AND code_hash=$2 AND used_at IS NULL RETURNING code_hash", user.ID, tokenHash(strings.ToUpper(input.Code))).Scan(&usedHash); err == nil {
			validCode = true
		}
	}
	if !validCode {
		writeError(w, http.StatusBadRequest, "invalid_mfa", "密码或验证码错误")
		return
	}
	if _, err := tx.Exec(r.Context(), "UPDATE users SET mfa_enabled=false,mfa_secret_enc=NULL,mfa_confirmed_at=NULL,updated_at=now() WHERE id=$1", user.ID); err != nil {
		s.fail(w, r, err)
		return
	}
	// API keys are intentionally left untouched when MFA is switched off. This
	// path already demands the password plus a TOTP/recovery code, so an actor
	// who reaches it is fully authenticated; disabling the factor does not make
	// surviving keys bypass anything (they were issued while MFA was on and the
	// account has no factor to bypass afterwards). Revoking them here would
	// only punish the legitimate owner and is asymmetric with the enable/disable
	// contract, unlike mfaConfirm where revocation is what makes the new factor
	// effective. Owners who want a clean slate can revoke keys explicitly.
	if cookie, err := r.Cookie("zjmfgo_session"); err == nil {
		if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1 AND token_hash<>$2", user.ID, tokenHash(cookie.Value)); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.authAudit(r.Context(), user.ID, "auth.mfa_disabled", map[string]any{})
	write(w, http.StatusOK, map[string]bool{"enabled": false})
}

func (s *Server) encryptMFASecret(secret string) ([]byte, error) {
	block, err := aes.NewCipher(s.cfg.MFAEncryptionKey)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, []byte(secret), nil), nil
}

func (s *Server) decryptMFASecret(payload []byte) (string, error) {
	block, err := aes.NewCipher(s.cfg.MFAEncryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(payload) < gcm.NonceSize() {
		return "", errors.New("invalid MFA secret")
	}
	plain, err := gcm.Open(nil, payload[:gcm.NonceSize()], payload[gcm.NonceSize():], nil)
	return string(plain), err
}

func validMFACode(code string) bool {
	if len(code) != 6 {
		return false
	}
	for _, c := range code {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func validRecoveryCode(code string) bool {
	code = strings.TrimSpace(code)
	if len(code) != 10 {
		return false
	}
	for _, c := range code {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') && !(c >= 'A' && c <= 'F') {
			return false
		}
	}
	return true
}

func verifyTOTP(secret, code string, now time.Time) bool {
	if secret == "" || !validMFACode(code) {
		return false
	}
	decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimSpace(secret)))
	if err != nil {
		return false
	}
	for offset := int64(-1); offset <= 1; offset++ {
		counter := (now.Unix() / int64(mfaStep/time.Second)) + offset
		if counter < 0 {
			continue
		}
		mac := hmac.New(sha1.New, decoded)
		var buf [8]byte
		binary.BigEndian.PutUint64(buf[:], uint64(counter))
		_, _ = mac.Write(buf[:])
		sum := mac.Sum(nil)
		idx := sum[len(sum)-1] & 0x0f
		value := (uint32(sum[idx])&0x7f)<<24 | uint32(sum[idx+1])<<16 | uint32(sum[idx+2])<<8 | uint32(sum[idx+3])
		candidate := fmt.Sprintf("%06d", value%1000000)
		if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 {
			return true
		}
	}
	return false
}
