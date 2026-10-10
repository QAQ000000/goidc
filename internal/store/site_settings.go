package store

import (
	"context"
	"encoding/json"
	"net/mail"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

type SiteProfile struct {
	Name                      string `json:"site_name"`
	Description               string `json:"description"`
	CompanyName               string `json:"company_name"`
	LogoURL                   string `json:"logo_url"`
	SupportEmail              string `json:"support_email"`
	SupportPhone              string `json:"support_phone"`
	Address                   string `json:"address"`
	SupportURL                string `json:"support_url"`
	TermsURL                  string `json:"terms_url"`
	PrivacyURL                string `json:"privacy_url"`
	RecordNumber              string `json:"record_number"`
	Copyright                 string `json:"copyright"`
	RegistrationEnabled       bool   `json:"registration_enabled"`
	MaintenanceEnabled        bool   `json:"maintenance_enabled"`
	MaintenanceMessage        string `json:"maintenance_message"`
	MaintenanceURL            string `json:"maintenance_url"`
	AuthCaptchaEnabled        bool   `json:"auth_captcha_enabled"`
	EmailVerificationRequired bool   `json:"email_verification_required"`
}

type SiteAdminSettings struct {
	DefaultLanguage         string `json:"default_language"`
	LanguageMenuEnabled     bool   `json:"language_menu_enabled"`
	RecordsPerPage          int    `json:"records_per_page"`
	FrontendCreditEnabled   bool   `json:"frontend_credit_enabled"`
	CustomCustomerIDEnabled bool   `json:"custom_customer_id_enabled"`
	AdminLoginDays          int    `json:"admin_login_days"`
	FrontendIPCheck         bool   `json:"frontend_ip_check"`
	AdminIPCheck            bool   `json:"admin_ip_check"`
	DebugEnabled            bool   `json:"debug_enabled"`
}

type SiteSettings struct {
	SiteProfile
	AdminSettings SiteAdminSettings `json:"admin_settings"`
	Version       int64             `json:"version"`
	UpdatedAt     time.Time         `json:"updated_at"`
}

const siteColumns = `site_name,description,company_name,logo_url,support_email,support_phone,address,support_url,terms_url,privacy_url,record_number,copyright,registration_enabled,maintenance_enabled,maintenance_message,maintenance_url,auth_captcha_enabled,email_verification_required,admin_settings,version,updated_at`

func scanSite(row pgx.Row) (SiteSettings, error) {
	var out SiteSettings
	var rawPreferences []byte
	err := row.Scan(&out.Name, &out.Description, &out.CompanyName, &out.LogoURL, &out.SupportEmail, &out.SupportPhone, &out.Address, &out.SupportURL, &out.TermsURL, &out.PrivacyURL, &out.RecordNumber, &out.Copyright, &out.RegistrationEnabled, &out.MaintenanceEnabled, &out.MaintenanceMessage, &out.MaintenanceURL, &out.AuthCaptchaEnabled, &out.EmailVerificationRequired, &rawPreferences, &out.Version, &out.UpdatedAt)
	if err == nil {
		if unmarshalErr := json.Unmarshal(rawPreferences, &out.AdminSettings); unmarshalErr != nil {
			err = unmarshalErr
		}
	}
	return out, err
}

func (s *Store) SiteSettings(ctx context.Context) (SiteSettings, error) {
	return scanSite(s.Pool.QueryRow(ctx, "SELECT "+siteColumns+" FROM site_settings WHERE id=1"))
}

func validateSite(in *SiteProfile) error {
	in.Description = strings.Join(strings.Fields(in.Description), " ")
	in.Address = strings.Join(strings.Fields(in.Address), " ")
	in.MaintenanceMessage = strings.ReplaceAll(strings.ReplaceAll(in.MaintenanceMessage, "\r\n", "\n"), "\r", "\n")
	in.MaintenanceMessage = strings.TrimSpace(in.MaintenanceMessage)
	if !utf8.ValidString(in.MaintenanceMessage) || utf8.RuneCountInString(in.MaintenanceMessage) > 1000 || strings.IndexFunc(in.MaintenanceMessage, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\t' }) >= 0 {
		return conflict("invalid_site_settings", "维护公告字符或长度无效")
	}
	for _, field := range []struct {
		value *string
		limit int
	}{{&in.Name, 80}, {&in.Description, 300}, {&in.CompanyName, 160}, {&in.LogoURL, 2048}, {&in.SupportEmail, 254}, {&in.SupportPhone, 80}, {&in.Address, 300}, {&in.SupportURL, 2048}, {&in.TermsURL, 2048}, {&in.PrivacyURL, 2048}, {&in.RecordNumber, 120}, {&in.Copyright, 160}, {&in.MaintenanceURL, 2048}} {
		*field.value = strings.TrimSpace(*field.value)
		if !utf8.ValidString(*field.value) || utf8.RuneCountInString(*field.value) > field.limit || strings.IndexFunc(*field.value, unicode.IsControl) >= 0 {
			return conflict("invalid_site_settings", "站点字段长度或字符无效")
		}
	}
	if in.Name == "" {
		return conflict("invalid_site_settings", "请填写站点名称")
	}
	if in.SupportEmail != "" {
		email, err := mail.ParseAddress(in.SupportEmail)
		if err != nil || email.Address != in.SupportEmail {
			return conflict("invalid_site_settings", "联系邮箱无效")
		}
	}
	for _, link := range []string{in.LogoURL, in.SupportURL, in.TermsURL, in.PrivacyURL, in.MaintenanceURL} {
		if link == "" {
			continue
		}
		parsed, err := url.Parse(link)
		if err != nil || parsed.User != nil || strings.ContainsAny(link, "\\ \t\n\r") {
			return conflict("invalid_site_settings", "链接地址无效")
		}
		if strings.HasPrefix(link, "/") && !strings.HasPrefix(link, "//") && parsed.Host == "" && parsed.Scheme == "" {
			if link == in.MaintenanceURL {
				return conflict("invalid_site_settings", "维护跳转必须使用外部 HTTPS 地址")
			}
			continue
		}
		if parsed.Scheme != "https" || parsed.Hostname() == "" {
			return conflict("invalid_site_settings", "链接必须为站内绝对路径或 HTTPS 地址")
		}
	}
	return nil
}

func normalizeAdminSettings(in *SiteAdminSettings) error {
	if in.DefaultLanguage == "" {
		in.DefaultLanguage = "zh_CN"
	}
	if in.DefaultLanguage != "zh_CN" && in.DefaultLanguage != "en_US" {
		return conflict("invalid_admin_settings", "默认语言无效")
	}
	if in.RecordsPerPage != 10 && in.RecordsPerPage != 20 && in.RecordsPerPage != 50 && in.RecordsPerPage != 100 {
		return conflict("invalid_admin_settings", "每页记录数无效")
	}
	if in.AdminLoginDays < 1 || in.AdminLoginDays > 30 {
		return conflict("invalid_admin_settings", "管理员登录时长必须在 1-30 天之间")
	}
	return nil
}

func (s *Store) UpdateSiteSettings(ctx context.Context, actorID int64, sessionHash string, in SiteProfile, adminSettings *SiteAdminSettings, expectedVersion int64, captchaEnabled *bool) (SiteSettings, error) {
	if err := validateSite(&in); err != nil {
		return SiteSettings{}, err
	}
	if expectedVersion < 1 {
		return SiteSettings{}, conflict("invalid_site_version", "请提供有效站点版本")
	}
	if adminSettings != nil {
		if err := normalizeAdminSettings(adminSettings); err != nil {
			return SiteSettings{}, err
		}
	}
	tx, err := settingsManagementTx(ctx, s, actorID)
	if err != nil {
		return SiteSettings{}, err
	}
	defer tx.Rollback(ctx)
	previous, err := scanSite(tx.QueryRow(ctx, "SELECT "+siteColumns+" FROM site_settings WHERE id=1 FOR UPDATE"))
	if err != nil {
		return SiteSettings{}, err
	}
	if err = recheckSettingsManager(ctx, tx, actorID, sessionHash); err != nil {
		return SiteSettings{}, err
	}
	if previous.Version != expectedVersion {
		return SiteSettings{}, conflict("version_conflict", "站点设置已变更，请重新加载")
	}
	if captchaEnabled == nil {
		in.AuthCaptchaEnabled = previous.AuthCaptchaEnabled
	}
	if adminSettings == nil {
		adminSettings = &previous.AdminSettings
	}
	preferences, err := json.Marshal(adminSettings)
	if err != nil {
		return SiteSettings{}, err
	}
	out, err := scanSite(tx.QueryRow(ctx, `UPDATE site_settings SET site_name=$1,description=$2,company_name=$3,logo_url=$4,support_email=$5,support_phone=$6,address=$7,support_url=$8,terms_url=$9,privacy_url=$10,record_number=$11,copyright=$12,registration_enabled=$13,maintenance_enabled=$14,maintenance_message=$15,maintenance_url=$16,auth_captcha_enabled=$17,email_verification_required=$18,admin_settings=$19,version=version+1,updated_at=now() WHERE id=1 RETURNING `+siteColumns, in.Name, in.Description, in.CompanyName, in.LogoURL, in.SupportEmail, in.SupportPhone, in.Address, in.SupportURL, in.TermsURL, in.PrivacyURL, in.RecordNumber, in.Copyright, in.RegistrationEnabled, in.MaintenanceEnabled, in.MaintenanceMessage, in.MaintenanceURL, in.AuthCaptchaEnabled, in.EmailVerificationRequired, preferences))
	if err != nil {
		return SiteSettings{}, err
	}
	if err = Audit(ctx, tx, actorID, "site.settings_updated", "site_settings", 1, map[string]any{"before": previous.SiteProfile, "after": out.SiteProfile, "previous_version": previous.Version, "version": out.Version}); err != nil {
		return SiteSettings{}, err
	}
	return out, tx.Commit(ctx)
}
