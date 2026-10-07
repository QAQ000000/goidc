package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) smtpSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "forbidden", "需要超级管理员权限")
		return
	}
	out, e := s.db.SMTPSettings(r.Context(), s.smtpFallback())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, out)
}
func (s *Server) updateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "forbidden", "需要超级管理员权限")
		return
	}
	var in struct {
		store.SMTPSettings
		Enabled         *bool   `json:"enabled"`
		ExpectedVersion int64   `json:"expected_version"`
		Password        *string `json:"password"`
		ClearPassword   bool    `json:"clear_password"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_smtp", "请提供邮件发送开关和有效版本")
		return
	}
	in.SMTPSettings.Enabled = *in.Enabled
	cookie, e := r.Cookie("zjmfgo_session")
	if e != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, e := s.db.UpdateSMTPSettings(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), in.SMTPSettings, in.Password, in.ClearPassword, in.ExpectedVersion, s.smtpFallback())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, out)
}
func (s *Server) smtpFallback() store.SMTPRuntimeConfig {
	return store.SMTPRuntimeConfig{Host: s.cfg.SMTPHost, Port: s.cfg.SMTPPort, Username: s.cfg.SMTPUsername, Password: s.cfg.SMTPPassword, From: s.cfg.SMTPFrom}
}
