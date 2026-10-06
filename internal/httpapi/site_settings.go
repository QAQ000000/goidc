package httpapi

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/codex/zjmfgo/internal/store"
)

func (s *Server) publicSite(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.SiteSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, settings.SiteProfile)
}

func (s *Server) siteSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.db.SiteSettings(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, settings)
}

func (s *Server) updateSiteSettings(w http.ResponseWriter, r *http.Request) {
	var input struct {
		store.SiteProfile
		ExpectedVersion     int64 `json:"expected_version"`
		RegistrationEnabled *bool `json:"registration_enabled"`
		MaintenanceEnabled  *bool `json:"maintenance_enabled"`
		AuthCaptchaEnabled  *bool `json:"auth_captcha_enabled"`
	}
	if !decode(w, r, &input) {
		return
	}
	if input.RegistrationEnabled == nil {
		writeError(w, 400, "invalid_site_settings", "请明确指定是否开放注册")
		return
	}
	input.SiteProfile.RegistrationEnabled = *input.RegistrationEnabled
	if input.MaintenanceEnabled == nil {
		writeError(w, 400, "invalid_site_settings", "请明确指定是否启用维护模式")
		return
	}
	input.SiteProfile.MaintenanceEnabled = *input.MaintenanceEnabled
	if input.AuthCaptchaEnabled != nil {
		input.SiteProfile.AuthCaptchaEnabled = *input.AuthCaptchaEnabled
	}
	if target, err := url.Parse(strings.TrimSpace(input.MaintenanceURL)); err == nil && target.Hostname() != "" {
		origin, _ := url.Parse(s.cfg.PublicOrigin)
		if origin != nil && strings.EqualFold(strings.TrimRight(target.Hostname(), "."), strings.TrimRight(origin.Hostname(), ".")) {
			writeError(w, 409, "invalid_site_settings", "维护跳转不能指向本站")
			return
		}
	}
	settings, err := s.db.UpdateSiteSettings(r.Context(), identityFrom(r).ID, input.SiteProfile, input.ExpectedVersion, input.AuthCaptchaEnabled)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, settings)
}
