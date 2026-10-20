package httpapi

import (
	"context"
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

	"github.com/codex/zjmfgo/internal/secure"
)

const mfaStep = 30 * time.Second

func (s *Server) completeLogin(w http.ResponseWriter, r *http.Request, id identity, passwordHash string) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := hex.EncodeToString(raw)
	if err := s.db.CreateAuthenticationSession(r.Context(), id.ID, id.Role, passwordHash, tokenHash(token)); err != nil {
		return err
	}
	s.authAudit(r.Context(), id.ID, "auth.login_succeeded", map[string]any{"remote_addr": s.clientIP(r)})
	http.SetCookie(w, &http.Cookie{Name: sessionCookieForRole(id.Role), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	return nil
}

func (s *Server) createMFAChallenge(ctx context.Context, userID int64, method, portal, passwordHash string) (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	challenge := hex.EncodeToString(raw)
	err := s.db.CreateAuthenticationMFAChallenge(ctx, userID, method, portal, passwordHash, tokenHash(challenge))
	return challenge, err
}

func (s *Server) loginMFA(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "login-mfa", 30) {
		return
	}
	var input struct {
		Challenge string `json:"challenge"`
		Code      string `json:"code"`
		Portal    string `json:"portal"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Challenge = strings.TrimSpace(input.Challenge)
	input.Code = strings.TrimSpace(input.Code)
	if len(input.Challenge) != 64 || (!validMFACode(input.Code) && !validRecoveryCode(input.Code)) || input.Portal != "" && input.Portal != "customer" && input.Portal != "admin" {
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码或登录挑战无效")
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		s.fail(w, r, err)
		return
	}
	token := hex.EncodeToString(raw)
	recoveryHash := ""
	if validRecoveryCode(input.Code) {
		recoveryHash = tokenHash(strings.ToUpper(input.Code))
	}
	result, err := s.db.CompleteAuthenticationMFA(r.Context(), tokenHash(input.Challenge), input.Portal, recoveryHash, tokenHash(token), func(encrypted []byte) bool {
		secret, decryptErr := s.decryptMFASecret(encrypted)
		return decryptErr == nil && verifyTOTP(secret, input.Code, time.Now())
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch result.Denial {
	case "login_disabled":
		writeError(w, http.StatusForbidden, "login_disabled", "本站暂未开放客户登录")
		return
	case "login_method_disabled":
		writeError(w, http.StatusForbidden, "login_method_disabled", "此登录方式暂未开放，请重新登录")
		return
	case "invalid_mfa":
		if result.FailedCode {
			s.authAudit(r.Context(), result.User.ID, "auth.mfa_failed", map[string]any{"remote_addr": s.clientIP(r)})
		}
		writeError(w, http.StatusUnauthorized, "invalid_mfa", "验证码或登录挑战无效")
		return
	}
	s.authAudit(r.Context(), result.User.ID, "auth.login_succeeded", map[string]any{"remote_addr": s.clientIP(r)})
	http.SetCookie(w, &http.Cookie{Name: sessionCookieForRole(result.User.Role), Value: token, Path: "/", HttpOnly: true, Secure: s.cfg.SecureCookie, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	u := result.User
	write(w, http.StatusOK, identity{ID: u.ID, Username: u.Username, Email: u.Email, Name: u.Name, Role: u.Role})
}

func (s *Server) mfaStatus(w http.ResponseWriter, r *http.Request) {
	enabled, err := s.db.AuthenticationMFAEnabled(r.Context(), identityFrom(r).ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]bool{"enabled": enabled})
}

func (s *Server) mfaEnroll(w http.ResponseWriter, r *http.Request) {
	if len(s.cfg.MFAEncryptionKey) != 32 {
		writeError(w, 503, "mfa_unavailable", "服务器未配置 MFA 加密密钥")
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
	recoveryCodes := make([]string, 0, 8)
	recoveryHashes := make([]string, 0, 8)
	for i := 0; i < 8; i++ {
		codeBytes := make([]byte, 5)
		if _, err = rand.Read(codeBytes); err != nil {
			s.fail(w, r, err)
			return
		}
		code := strings.ToUpper(hex.EncodeToString(codeBytes))
		recoveryCodes = append(recoveryCodes, code)
		recoveryHashes = append(recoveryHashes, tokenHash(code))
	}
	denial, err := s.db.EnrollAuthenticationMFA(r.Context(), user.ID, accountSessionHash(r), enc, recoveryHashes)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if denial == "mfa_enrollment_disabled" {
		writeError(w, 403, denial, "当前账户类型暂未开放二次验证设置")
		return
	}
	if denial == "mfa_already_enabled" {
		writeError(w, 409, denial, "MFA 已启用")
		return
	}
	issuer := url.QueryEscape("ZJMF")
	account := url.QueryEscape(user.Email)
	otpauth := fmt.Sprintf("otpauth://totp/%s:%s?secret=%s&issuer=%s&algorithm=SHA1&digits=6&period=30", issuer, account, secret, issuer)
	s.authAudit(r.Context(), user.ID, "auth.mfa_enrollment_started", map[string]any{})
	write(w, 200, map[string]any{"secret": secret, "otpauth_uri": otpauth, "recovery_codes": recoveryCodes})
}

func (s *Server) mfaConfirm(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Code string `json:"code"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !validMFACode(strings.TrimSpace(input.Code)) {
		writeError(w, 400, "invalid_mfa", "请输入 6 位验证码")
		return
	}
	user := identityFrom(r)
	denial, err := s.db.ConfirmAuthenticationMFA(r.Context(), user.ID, accountSessionHash(r), func(enc []byte) bool {
		secret, e := s.decryptMFASecret(enc)
		return e == nil && verifyTOTP(secret, strings.TrimSpace(input.Code), time.Now())
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch denial {
	case "mfa_enrollment_disabled":
		writeError(w, 403, denial, "当前账户类型暂未开放二次验证设置")
		return
	case "mfa_not_pending":
		writeError(w, 409, denial, "没有待确认的 MFA 设置")
		return
	case "invalid_mfa":
		writeError(w, 400, denial, "验证码错误")
		return
	}
	s.authAudit(r.Context(), user.ID, "auth.mfa_enabled", map[string]any{"api_keys_revoked": true})
	write(w, 200, map[string]bool{"enabled": true})
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
		writeError(w, 400, "invalid_mfa", "密码或验证码无效")
		return
	}
	user := identityFrom(r)
	recoveryHash := ""
	if validRecoveryCode(input.Code) {
		recoveryHash = tokenHash(strings.ToUpper(input.Code))
	}
	sessionHash := accountSessionHash(r)
	valid, err := s.db.DisableAuthenticationMFA(r.Context(), user.ID, input.Password, recoveryHash, sessionHash, func(enc []byte) bool {
		secret, e := s.decryptMFASecret(enc)
		return e == nil && verifyTOTP(secret, strings.TrimSpace(input.Code), time.Now())
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid {
		writeError(w, 400, "invalid_mfa", "密码或验证码错误")
		return
	}
	s.authAudit(r.Context(), user.ID, "auth.mfa_disabled", map[string]any{})
	write(w, 200, map[string]bool{"enabled": false})
}

func (s *Server) encryptMFASecret(secret string) ([]byte, error) {
	return secure.Seal(s.cfg.MFAEncryptionKey, nil, []byte(secret))
}

func (s *Server) decryptMFASecret(payload []byte) (string, error) {
	plain, err := secure.Open(s.cfg.MFAEncryptionKey, nil, payload)
	if errors.Is(err, secure.ErrInvalidCiphertext) {
		return "", errors.New("invalid MFA secret")
	}
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
