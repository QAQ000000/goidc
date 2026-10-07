package httpapi

import (
	_ "github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"time"
)

func (s *Server) smsSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "forbidden", "需要超级管理员权限")
		return
	}
	out, e := s.db.SMSSettings(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, out)
}
func (s *Server) updateSMSSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "forbidden", "需要超级管理员权限")
		return
	}
	var in struct {
		store.SMSSettings
		Enabled         *bool   `json:"enabled"`
		ExpectedVersion int64   `json:"expected_version"`
		Token           *string `json:"token"`
		ClearToken      bool    `json:"clear_token"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_sms", "请提供短信开关和有效版本")
		return
	}
	in.SMSSettings.Enabled = *in.Enabled
	cookie, e := r.Cookie("zjmfgo_session")
	if e != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	out, e := s.db.UpdateSMSSettings(r.Context(), identityFrom(r).ID, tokenHash(cookie.Value), in.SMSSettings, in.Token, in.ClearToken, in.ExpectedVersion)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, out)
}
func (s *Server) testSMSSettings(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if identityFrom(r).Role != "super_admin" {
		writeError(w, 403, "forbidden", "需要超级管理员权限")
		return
	}
	var in struct {
		Recipient       string `json:"recipient"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_sms", "请提供有效配置版本")
		return
	}
	cookie, e := r.Cookie("zjmfgo_session")
	if e != nil {
		writeError(w, 401, "unauthorized", "请先登录")
		return
	}
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	if e = store.AuthorizeSMSTest(r.Context(), tx, identityFrom(r).ID, tokenHash(cookie.Value), in.ExpectedVersion); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = s.db.QueueSMS(r.Context(), tx, in.Recipient, "短信渠道测试消息", "test", "", time.Now().Add(5*time.Minute)); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "sms.test_queued", "sms_settings", 1, map[string]any{"version": in.ExpectedVersion}); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 202, map[string]any{"queued": true})
}
