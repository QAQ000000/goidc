package httpapi

import (
	"net/http"
	"time"

	_ "github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
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
		Recipient         string `json:"recipient"`
		ExpectedVersion   int64  `json:"expected_version"`
		ChannelInstanceID string `json:"channel_instance_id"`
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
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {

		if e = store.AuthorizeSMSTest(r.Context(), tx, identityFrom(r).ID, tokenHash(cookie.Value), in.ExpectedVersion, in.ChannelInstanceID); e != nil {
			return txResponse{}, e
		}
		if e = s.db.QueueSMS(r.Context(), tx, in.Recipient, "短信渠道测试消息", "test", "", time.Now().Add(5*time.Minute), in.ChannelInstanceID); e != nil {
			return txResponse{}, e
		}
		if e = store.Audit(r.Context(), tx, identityFrom(r).ID, "sms.test_queued", "sms_settings", 1, map[string]any{"version": in.ExpectedVersion}); e != nil {
			return txResponse{}, e
		}

		return txResponse{202, map[string]any{"queued": true}}, nil
	})
}
