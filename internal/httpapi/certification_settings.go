package httpapi

import (
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
)

type certificationSettingsInput = store.CertificationSettingsInput

func (s *Server) certificationSettings(w http.ResponseWriter, r *http.Request) {
	// Keep the field list explicit so the response remains stable and secret-free.
	var id int16
	var enabled, requireForPurchase, advanced, autoName, phoneMatch, notify, suspend, upload bool
	var provider, instance string
	var version int64
	var updated any
	if err := s.db.Pool.QueryRow(r.Context(), `
		SELECT id,enabled,require_for_purchase,default_provider_id,default_instance_id,advanced_enabled,
			auto_update_name,require_phone_match,notify_on_approval,suspend_unverified,
			allow_file_upload,version,updated_at
		FROM certification_settings WHERE id=1`).Scan(&id, &enabled, &requireForPurchase, &provider, &instance, &advanced, &autoName, &phoneMatch, &notify, &suspend, &upload, &version, &updated); err != nil {
		s.fail(w, r, err)
		return
	}
	instances, err := s.db.ListChannelInstances(r.Context(), string(channel.Certification))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, map[string]any{
		"id": id, "enabled": enabled, "require_for_purchase": requireForPurchase,
		"default_provider_id": provider, "default_instance_id": instance, "advanced_enabled": advanced,
		"auto_update_name": autoName, "require_phone_match": phoneMatch,
		"notify_on_approval": notify, "suspend_unverified": suspend,
		"allow_file_upload": upload, "version": version, "updated_at": updated,
		"providers": channel.List(channel.Certification), "instances": instances,
	})
}

func (s *Server) updateCertificationSettings(w http.ResponseWriter, r *http.Request) {
	var in certificationSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.RequireForPurchase == nil || in.AdvancedEnabled == nil || in.AutoUpdateName == nil || in.RequirePhoneMatch == nil || in.NotifyOnApproval == nil || in.SuspendUnverified == nil || in.AllowFileUpload == nil || in.ExpectedVersion < 1 {
		writeError(w, http.StatusBadRequest, "invalid_certification_settings", "认证设置字段不完整")
		return
	}
	in.DefaultProviderID = strings.TrimSpace(in.DefaultProviderID)
	if in.DefaultProviderID == "" || len(in.DefaultProviderID) > 80 {
		writeError(w, http.StatusBadRequest, "invalid_certification_settings", "认证接口无效")
		return
	}
	if _, ok := channel.Lookup(channel.Certification, in.DefaultProviderID); !ok {
		writeError(w, http.StatusBadRequest, "invalid_certification_provider", "认证接口未注册")
		return
	}
	if in.DefaultInstanceID == nil {
		if in.DefaultProviderID != "manual" {
			writeError(w, http.StatusBadRequest, "certification_instance_required", "请选择具体认证实例，不能仅按认证实现自动选择")
			return
		}
		legacyInstance := "manual-default"
		in.DefaultInstanceID = &legacyInstance
	}
	instance := strings.TrimSpace(*in.DefaultInstanceID)
	if instance == "" || len(instance) > 64 || strings.ContainsAny(instance, " \t\r\n") {
		writeError(w, http.StatusBadRequest, "invalid_certification_instance", "认证实例标识无效")
		return
	}
	in.DefaultInstanceID = &instance
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.UpdateCertificationAccountSettings(requestFileContext(r), identityFrom(r).ID, key, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
