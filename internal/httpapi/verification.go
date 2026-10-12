package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

func (s *Server) verificationChannels(w http.ResponseWriter, r *http.Request) {
	channels := []string{}
	var emailEnabled, smsEnabled bool
	if err := s.db.Pool.QueryRow(r.Context(), "SELECT verification_email_enabled,verification_sms_enabled FROM auth_settings WHERE id=1").Scan(&emailEnabled, &smsEnabled); err != nil {
		s.fail(w, r, err)
		return
	}
	if emailEnabled && s.verificationAvailable(r.Context(), "email") == nil {
		channels = append(channels, "email")
	}
	if smsEnabled && s.verificationAvailable(r.Context(), "sms") == nil {
		channels = append(channels, "sms")
	}
	write(w, 200, map[string]any{"channels": channels})
}

func (s *Server) verificationAvailable(ctx context.Context, channel string) error {
	var enabled bool
	if err := s.db.Pool.QueryRow(ctx, "SELECT CASE WHEN $1='sms' THEN verification_sms_enabled ELSE verification_email_enabled END FROM auth_settings WHERE id=1", channel).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return storeError("notification_channel_unavailable", "通知渠道未启用")
	}
	if len(s.db.ProvisionEncryptionKey) != 32 {
		return storeError("verification_unconfigured", "验证码加密尚未配置")
	}
	if channel == "sms" {
		cfg, err := s.db.ResolveSMSConfig(ctx)
		if err == nil && cfg.Enabled {
			return nil
		}
	} else if channel == "email" {
		cfg, err := s.db.ResolveSMTPConfig(ctx, s.smtpFallback())
		if err == nil && cfg.Host != "" && cfg.From != "" {
			return nil
		}
	}
	return storeError("notification_channel_unavailable", "通知渠道尚未配置或未启用")
}

func storeError(code, message string) error { return &store.Conflict{Code: code, Message: message} }

type verificationInput struct {
	Purpose       string `json:"purpose"`
	Channel       string `json:"channel"`
	Recipient     string `json:"recipient"`
	Identifier    string `json:"identifier"`
	CaptchaID     string `json:"captcha_id"`
	CaptchaAnswer string `json:"captcha_answer"`
}

func validVerificationRecipient(channel, value string) bool {
	if channel == "sms" {
		return store.ValidSMSRecipient(value)
	}
	email, err := mail.ParseAddress(value)
	return channel == "email" && err == nil && email.Address == value && len(value) <= 254
}

func verificationResponse(w http.ResponseWriter, id string) {
	write(w, 202, map[string]any{"queued": true, "challenge_id": id, "expires_in": 600, "resend_after": 60})
}

func (s *Server) requestPasswordReset(w http.ResponseWriter, r *http.Request) {
	s.requestVerification(w, r, "password_reset")
}
func (s *Server) requestRegistrationVerification(w http.ResponseWriter, r *http.Request) {
	s.requestVerification(w, r, "register")
}
func (s *Server) requestAccountVerification(w http.ResponseWriter, r *http.Request) {
	s.requestVerification(w, r, "account")
}

func (s *Server) requestVerification(w http.ResponseWriter, r *http.Request, flow string) {
	if !s.authLimit(w, r, "verification-send", 10) {
		return
	}
	var in verificationInput
	if !decode(w, r, &in) {
		return
	}
	in.Channel = strings.TrimSpace(in.Channel)
	if in.Channel == "" {
		in.Channel = "email"
	}
	in.Recipient = strings.TrimSpace(in.Recipient)
	in.Identifier = strings.ToLower(strings.TrimSpace(in.Identifier))
	if in.Channel == "email" {
		in.Recipient = strings.ToLower(in.Recipient)
	}
	if flow == "register" && (in.Purpose != "register" || in.Channel != "email") {
		writeError(w, 400, "invalid_verification", "注册需验证邮箱")
		return
	}
	purpose := flow
	if flow == "account" {
		purpose = in.Purpose
		if purpose != "bind_email" && purpose != "bind_phone" || purpose == "bind_email" && in.Channel != "email" || purpose == "bind_phone" && in.Channel != "sms" {
			writeError(w, 400, "invalid_verification", "验证码用途无效")
			return
		}
	}
	if flow == "password_reset" {
		if in.Identifier == "" || len(in.Identifier) > 254 {
			writeError(w, 400, "invalid_password_reset", "账号或邮箱无效")
			return
		}
		if err := s.consumeCaptcha(r.Context(), r, "forgot-password", in.CaptchaID, in.CaptchaAnswer); err != nil {
			if !errors.Is(err, errInvalidCaptcha) {
				s.captchaUnavailable(w, r)
				return
			}
			writeError(w, 401, "invalid_captcha", "验证码错误，请刷新后重试")
			return
		}
	} else if flow == "register" {
		if err := s.consumeCaptcha(r.Context(), r, "register", in.CaptchaID, in.CaptchaAnswer); err != nil {
			if !errors.Is(err, errInvalidCaptcha) {
				s.captchaUnavailable(w, r)
				return
			}
			writeError(w, 401, "invalid_captcha", "验证码错误，请刷新后重试")
			return
		}
	}
	if err := s.verificationAvailable(r.Context(), in.Channel); err != nil {
		s.fail(w, r, err)
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var uid *int64
	deliver := true
	if flow == "register" {
		var enabled bool
		if err = tx.QueryRow(r.Context(), "SELECT s.registration_enabled AND a.registration_email_enabled FROM site_settings s JOIN auth_settings a ON a.id=1 WHERE s.id=1 FOR SHARE").Scan(&enabled); err != nil {
			s.fail(w, r, err)
			return
		}
		if !enabled {
			writeError(w, 403, "registration_disabled", "本站暂未开放注册")
			return
		}
	} else if flow == "password_reset" {
		var id int64
		var email, phone string
		var phoneVerified bool
		err = tx.QueryRow(r.Context(), "SELECT id,email,phone,phone_verified FROM users WHERE (lower(username)=$1 OR lower(email)=$1 OR phone=$1) AND status='active' FOR UPDATE", in.Identifier).Scan(&id, &email, &phone, &phoneVerified)
		if errors.Is(err, pgx.ErrNoRows) {
			deliver = false
			in.Recipient = "unknown:" + in.Identifier
		} else if err != nil {
			s.fail(w, r, err)
			return
		} else {
			uid = &id
			if in.Channel == "email" {
				in.Recipient = email
			} else {
				in.Recipient = phone
				deliver = phoneVerified && phone != ""
				if !deliver {
					in.Recipient = "unknown:" + in.Identifier
				}
			}
		}
	} else {
		id := identityFrom(r).ID
		uid = &id
		var active bool
		if err = tx.QueryRow(r.Context(), "SELECT status='active' FROM users WHERE id=$1 FOR UPDATE", id).Scan(&active); err != nil {
			s.fail(w, r, err)
			return
		}
		if !active {
			writeError(w, 401, "unauthorized", "请重新登录")
			return
		}
		if !s.recheckVerificationSession(w, r, tx, id) {
			return
		}
	}
	if deliver && !validVerificationRecipient(in.Channel, in.Recipient) {
		writeError(w, 400, "invalid_verification_recipient", "邮箱或手机号无效")
		return
	}
	id, err := s.db.IssueVerification(r.Context(), tx, purpose, in.Channel, in.Recipient, uid, deliver)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if uid != nil {
		action := "auth.verification_requested"
		if purpose == "password_reset" {
			action = "auth.password_reset_requested"
		}
		if err = store.Audit(r.Context(), tx, *uid, action, "user", *uid, map[string]any{"purpose": purpose, "channel": in.Channel}); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	verificationResponse(w, id)
}

type verificationConfirmInput struct {
	ChallengeID string `json:"challenge_id"`
	Code        string `json:"code"`
	Password    string `json:"password"`
}

func (s *Server) confirmPasswordReset(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "verification-confirm", 30) {
		return
	}
	var in verificationConfirmInput
	if !decode(w, r, &in) {
		return
	}
	if len(in.ChallengeID) != 64 || len(in.Password) < 8 || len(in.Password) > 72 {
		writeError(w, 400, "invalid_password_reset", "验证码或密码无效")
		return
	}
	// Lock account before its challenges, matching password and account updates.
	var uid *int64
	var recipient, channel string
	err := s.db.Pool.QueryRow(r.Context(), "SELECT user_id,recipient,channel FROM verification_challenges WHERE id=$1 AND purpose='password_reset'", store.VerificationID(in.ChallengeID)).Scan(&uid, &recipient, &channel)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if uid == nil {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), 12)
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
	var status, email, phone string
	var phoneVerified bool
	if err = tx.QueryRow(r.Context(), "SELECT status,email,phone,phone_verified FROM users WHERE id=$1 FOR UPDATE", *uid).Scan(&status, &email, &phone, &phoneVerified); err != nil {
		s.fail(w, r, err)
		return
	}
	_, valid, err := s.db.ConsumeVerification(r.Context(), tx, in.ChallengeID, in.Code, "password_reset", uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid || status != "active" || channel == "email" && email != recipient || channel == "sms" && (!phoneVerified || phone != recipient) {
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE users SET password_hash=$2,password_reset_required=false,admin_version=admin_version+1,updated_at=now() WHERE id=$1", *uid, string(hash)); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM sessions WHERE user_id=$1", *uid); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "DELETE FROM mfa_challenges WHERE user_id=$1", *uid); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE api_keys SET revoked_at=now() WHERE user_id=$1 AND revoked_at IS NULL", *uid); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, *uid, "auth.password_reset_completed", "user", *uid, map[string]any{"channel": channel}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"changed": true})
}

func (s *Server) confirmAccountVerification(w http.ResponseWriter, r *http.Request) {
	if !s.authLimit(w, r, "verification-confirm", 30) {
		return
	}
	var in verificationConfirmInput
	if !decode(w, r, &in) {
		return
	}
	uid := identityFrom(r).ID
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var email, status string
	if err = tx.QueryRow(r.Context(), "SELECT email,status FROM users WHERE id=$1 FOR UPDATE", uid).Scan(&email, &status); err != nil {
		s.fail(w, r, err)
		return
	}
	if !s.recheckVerificationSession(w, r, tx, uid) {
		return
	}
	var purpose string
	err = tx.QueryRow(r.Context(), "SELECT purpose FROM verification_challenges WHERE id=$1", store.VerificationID(in.ChallengeID)).Scan(&purpose)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if purpose != "bind_email" && purpose != "bind_phone" {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	challenge, valid, err := s.db.ConsumeVerification(r.Context(), tx, in.ChallengeID, in.Code, purpose, &uid)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid || status != "active" || purpose == "bind_email" && challenge.Recipient != email {
		if err = tx.Commit(r.Context()); err != nil {
			s.fail(w, r, err)
			return
		}
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	if purpose == "bind_email" {
		_, err = tx.Exec(r.Context(), "UPDATE users SET email_verified=true WHERE id=$1", uid)
	} else {
		_, err = tx.Exec(r.Context(), "UPDATE users SET phone=$2 WHERE id=$1", uid, challenge.Recipient)
		if err == nil {
			_, err = tx.Exec(r.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", uid)
		}
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, uid, "auth.contact_verified", "user", uid, map[string]any{"channel": challenge.Channel}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]bool{"verified": true})
}

func (s *Server) recheckVerificationSession(w http.ResponseWriter, r *http.Request, tx pgx.Tx, uid int64) bool {
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请重新登录")
		return false
	}
	var token string
	err = tx.QueryRow(r.Context(), "SELECT token_hash FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>now() FOR SHARE", uid, tokenHash(cookie.Value)).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		writeError(w, 401, "unauthorized", "请重新登录")
		return false
	}
	if err != nil {
		s.fail(w, r, err)
		return false
	}
	return true
}
