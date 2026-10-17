package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type authSettingsView struct {
	LoginEnabled              bool      `json:"login_enabled"`
	RegistrationEnabled       bool      `json:"registration_enabled"`
	RegistrationEmailEnabled  bool      `json:"registration_email_enabled"`
	RegistrationPhoneEnabled  bool      `json:"registration_phone_enabled"`
	LoginUsernameEnabled      bool      `json:"login_username_enabled"`
	LoginEmailEnabled         bool      `json:"login_email_enabled"`
	LoginPhoneEnabled         bool      `json:"login_phone_enabled"`
	EmailVerificationRequired bool      `json:"email_verification_required"`
	PhoneVerificationRequired bool      `json:"phone_verification_required"`
	VerificationEmailEnabled  bool      `json:"verification_email_enabled"`
	VerificationSMSEnabled    bool      `json:"verification_sms_enabled"`
	AuthCaptchaEnabled        bool      `json:"auth_captcha_enabled"`
	CaptchaAutoEnabled        bool      `json:"captcha_auto_enabled"`
	LoginMaxAttempts          int       `json:"login_max_attempts"`
	LoginLockoutMinutes       int       `json:"login_lockout_minutes"`
	EmailChannelReady         bool      `json:"email_channel_ready"`
	SMSChannelReady           bool      `json:"sms_channel_ready"`
	Version                   int64     `json:"version"`
	UpdatedAt                 time.Time `json:"updated_at"`
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func readAuthSettings(ctx context.Context, q rowQuerier) (authSettingsView, error) {
	var out authSettingsView
	err := q.QueryRow(ctx, `SELECT a.login_enabled,s.registration_enabled,a.registration_email_enabled,a.registration_phone_enabled,
 a.login_username_enabled,a.login_email_enabled,a.login_phone_enabled,s.email_verification_required,a.phone_verification_required,
 a.verification_email_enabled,a.verification_sms_enabled,s.auth_captcha_enabled,a.captcha_auto_enabled,a.login_max_attempts,a.login_lockout_minutes,
 EXISTS(SELECT 1 FROM channel_instances c WHERE c.category='mail' AND c.implementation_id='smtp' AND c.enabled),
 EXISTS(SELECT 1 FROM channel_instances c WHERE c.category='sms' AND c.enabled),a.version,a.updated_at
 FROM auth_settings a JOIN site_settings s ON s.id=1 WHERE a.id=1`).Scan(
		&out.LoginEnabled, &out.RegistrationEnabled, &out.RegistrationEmailEnabled, &out.RegistrationPhoneEnabled,
		&out.LoginUsernameEnabled, &out.LoginEmailEnabled, &out.LoginPhoneEnabled, &out.EmailVerificationRequired, &out.PhoneVerificationRequired,
		&out.VerificationEmailEnabled, &out.VerificationSMSEnabled, &out.AuthCaptchaEnabled, &out.CaptchaAutoEnabled, &out.LoginMaxAttempts, &out.LoginLockoutMinutes,
		&out.EmailChannelReady, &out.SMSChannelReady, &out.Version, &out.UpdatedAt)
	return out, err
}

func (s *Server) adminAuthSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		out, err := readAuthSettings(r.Context(), s.db.Pool)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, out)
		return
	}
	var in struct {
		LoginEnabled              *bool `json:"login_enabled"`
		RegistrationEnabled       *bool `json:"registration_enabled"`
		RegistrationEmailEnabled  *bool `json:"registration_email_enabled"`
		RegistrationPhoneEnabled  *bool `json:"registration_phone_enabled"`
		LoginUsernameEnabled      *bool `json:"login_username_enabled"`
		LoginEmailEnabled         *bool `json:"login_email_enabled"`
		LoginPhoneEnabled         *bool `json:"login_phone_enabled"`
		EmailVerificationRequired *bool `json:"email_verification_required"`
		PhoneVerificationRequired *bool `json:"phone_verification_required"`
		VerificationEmailEnabled  *bool `json:"verification_email_enabled"`
		VerificationSMSEnabled    *bool `json:"verification_sms_enabled"`
		AuthCaptchaEnabled        *bool `json:"auth_captcha_enabled"`
		CaptchaAutoEnabled        *bool `json:"captcha_auto_enabled"`
		LoginMaxAttempts          int   `json:"login_max_attempts"`
		LoginLockoutMinutes       int   `json:"login_lockout_minutes"`
		ExpectedVersion           int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.LoginEnabled == nil || in.RegistrationEnabled == nil || in.RegistrationEmailEnabled == nil || in.RegistrationPhoneEnabled == nil || in.LoginUsernameEnabled == nil || in.LoginEmailEnabled == nil || in.LoginPhoneEnabled == nil || in.EmailVerificationRequired == nil || in.PhoneVerificationRequired == nil || in.VerificationEmailEnabled == nil || in.VerificationSMSEnabled == nil || in.AuthCaptchaEnabled == nil || in.CaptchaAutoEnabled == nil || in.LoginMaxAttempts < 1 || in.LoginMaxAttempts > 20 || in.LoginLockoutMinutes < 1 || in.LoginLockoutMinutes > 1440 || in.ExpectedVersion < 1 || !*in.RegistrationEmailEnabled || *in.RegistrationPhoneEnabled || *in.PhoneVerificationRequired || *in.CaptchaAutoEnabled || (*in.LoginUsernameEnabled || (!*in.LoginEmailEnabled && !*in.LoginPhoneEnabled)) || (*in.EmailVerificationRequired && !*in.VerificationEmailEnabled) {
		writeError(w, 400, "invalid_auth_settings", "注册登录安全设置无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = store.RecheckSettingsManager(r.Context(), tx, actor, tokenHash(cookie.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "auth.settings", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var current int64
	if _, err = tx.Exec(r.Context(), "SELECT id FROM site_settings WHERE id=1 FOR UPDATE"); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.QueryRow(r.Context(), "SELECT version FROM auth_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "注册登录设置已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE auth_settings SET login_enabled=$1,registration_email_enabled=$2,registration_phone_enabled=$3,login_username_enabled=$4,login_email_enabled=$5,login_phone_enabled=$6,phone_verification_required=$7,verification_email_enabled=$8,verification_sms_enabled=$9,captcha_auto_enabled=$10,login_max_attempts=$11,login_lockout_minutes=$12,version=version+1,updated_at=now() WHERE id=1`, *in.LoginEnabled, *in.RegistrationEmailEnabled, *in.RegistrationPhoneEnabled, *in.LoginUsernameEnabled, *in.LoginEmailEnabled, *in.LoginPhoneEnabled, *in.PhoneVerificationRequired, *in.VerificationEmailEnabled, *in.VerificationSMSEnabled, *in.CaptchaAutoEnabled, in.LoginMaxAttempts, in.LoginLockoutMinutes); err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `UPDATE site_settings SET registration_enabled=$1,email_verification_required=$2,auth_captcha_enabled=$3,version=version+1,updated_at=now() WHERE id=1`, *in.RegistrationEnabled, *in.EmailVerificationRequired, *in.AuthCaptchaEnabled); err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := readAuthSettings(r.Context(), tx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, actor, "auth.settings_updated", "auth_settings", 1, map[string]any{"before_version": current, "after": out}); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{}
	raw, _ := json.Marshal(out)
	_ = json.Unmarshal(raw, &result)
	if err = saveSettingOperationTx(r, tx, actor, "auth.settings", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}

type mfaSettingsView struct {
	CustomerEnabled bool      `json:"customer_enabled"`
	AdminEnabled    bool      `json:"admin_enabled"`
	Version         int64     `json:"version"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func readMFASettings(ctx context.Context, q rowQuerier) (mfaSettingsView, error) {
	var out mfaSettingsView
	err := q.QueryRow(ctx, "SELECT customer_enabled,admin_enabled,version,updated_at FROM mfa_settings WHERE id=1").Scan(&out.CustomerEnabled, &out.AdminEnabled, &out.Version, &out.UpdatedAt)
	return out, err
}

func (s *Server) adminMFASettings(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		out, err := readMFASettings(r.Context(), s.db.Pool)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, out)
		return
	}
	var in struct {
		CustomerEnabled *bool `json:"customer_enabled"`
		AdminEnabled    *bool `json:"admin_enabled"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.CustomerEnabled == nil || in.AdminEnabled == nil || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_mfa_settings", "MFA 设置无效")
		return
	}
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	cookie, err := r.Cookie("zjmfgo_session")
	if err != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = store.RecheckSettingsManager(r.Context(), tx, actor, tokenHash(cookie.Value)); err != nil {
		s.fail(w, r, err)
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "mfa.settings", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM mfa_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
		s.fail(w, r, err)
		return
	}
	if current != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "MFA 策略已变更，请重新加载")
		return
	}
	if _, err = tx.Exec(r.Context(), "UPDATE mfa_settings SET customer_enabled=$1,admin_enabled=$2,version=version+1,updated_at=now() WHERE id=1", *in.CustomerEnabled, *in.AdminEnabled); err != nil {
		s.fail(w, r, err)
		return
	}
	out, err := readMFASettings(r.Context(), tx)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, actor, "mfa.settings_updated", "mfa_settings", 1, map[string]any{"before_version": current, "after": out}); err != nil {
		s.fail(w, r, err)
		return
	}
	result := map[string]any{}
	raw, _ := json.Marshal(out)
	_ = json.Unmarshal(raw, &result)
	if err = saveSettingOperationTx(r, tx, actor, "mfa.settings", key, hash, result); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
