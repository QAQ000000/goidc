package httpapi

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"strings"

	"github.com/codex/zjmfgo/internal/captcha"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
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
	enabled, err := s.db.AccountVerificationChannelEnabled(ctx, channel)
	if err != nil {
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
		implementation, cfg, err := s.db.ResolveMailConfigForInstance(ctx, "", s.smtpFallback())
		if err == nil {
			provider, factoryErr := notification.NewMailProvider(implementation, cfg)
			if factoryErr == nil && provider != nil && provider.Enabled() {
				return nil
			}
		}
	}
	return storeError("notification_channel_unavailable", "通知渠道尚未配置或未启用")
}

func storeError(code, message string) error { return &store.Conflict{Code: code, Message: message} }

type verificationInput struct {
	Portal        string         `json:"portal"`
	Purpose       string         `json:"purpose"`
	Channel       string         `json:"channel"`
	Recipient     string         `json:"recipient"`
	Identifier    string         `json:"identifier"`
	CaptchaID     string         `json:"captcha_id"`
	CaptchaAnswer string         `json:"captcha_answer"`
	CaptchaProof  *captcha.Proof `json:"captcha_proof"`
}

func validVerificationRecipient(channel, value string) bool {
	if channel == "sms" {
		return store.ValidSMSRecipient(value)
	}
	email, err := mail.ParseAddress(value)
	return channel == "email" && err == nil && email.Address == value && len(value) <= 254
}

func verificationResponse(w http.ResponseWriter, id, channel string) {
	expiresIn := 600
	if channel == "sms" {
		expiresIn = 300
	}
	write(w, 202, map[string]any{"queued": true, "challenge_id": id, "expires_in": expiresIn, "resend_after": 60})
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
	in.Identifier, _ = normalizeLoginIdentifier(in.Identifier)
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
			writeError(w, 400, "invalid_password_reset", "邮箱或手机号无效")
			return
		}
		if err := s.consumeCaptcha(r.Context(), r, "forgot-password", in.CaptchaID, in.CaptchaAnswer, in.CaptchaProof); err != nil {
			if !errors.Is(err, errInvalidCaptcha) {
				s.captchaUnavailable(w, r)
				return
			}
			writeError(w, 401, "invalid_captcha", "验证码错误，请刷新后重试")
			return
		}
	} else if flow == "register" {
		if err := s.consumeCaptcha(r.Context(), r, "register", in.CaptchaID, in.CaptchaAnswer, in.CaptchaProof); err != nil {
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
	if in.Portal != "" && in.Portal != "customer" && in.Portal != "admin" {
		writeError(w, 400, "invalid_verification", "验证入口无效")
		return
	}
	actorID := int64(0)
	sessionHash := ""
	if flow == "account" {
		actorID = identityFrom(r).ID
		if cookie, err := r.Cookie(customerSessionCookie); err == nil {
			sessionHash = tokenHash(cookie.Value)
		}
	}
	result, err := s.db.IssueAccountVerification(r.Context(), store.AccountVerificationInput{Flow: flow, Purpose: purpose, Channel: in.Channel, Recipient: in.Recipient, Identifier: in.Identifier, Portal: in.Portal, ActorID: actorID, SessionHash: sessionHash})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	switch result.Denial {
	case "registration_disabled":
		writeError(w, 403, result.Denial, "本站暂未开放注册")
		return
	case "unauthorized":
		writeError(w, 401, result.Denial, "请重新登录")
		return
	case "invalid_verification_recipient":
		writeError(w, 400, result.Denial, "邮箱或手机号无效")
		return
	}
	verificationResponse(w, result.ID, in.Channel)
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
	valid, err := s.db.ConfirmAccountPasswordReset(r.Context(), in.ChallengeID, in.Code, in.Password)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
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
	sessionHash := ""
	if cookie, err := r.Cookie(customerSessionCookie); err == nil {
		sessionHash = tokenHash(cookie.Value)
	}
	valid, err := s.db.ConfirmAccountContactVerification(r.Context(), identityFrom(r).ID, sessionHash, in.ChallengeID, in.Code)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if !valid {
		writeError(w, 409, "invalid_verification_code", "验证码无效或已过期")
		return
	}
	write(w, 200, map[string]bool{"verified": true})
}
