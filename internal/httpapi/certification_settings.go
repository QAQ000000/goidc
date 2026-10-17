package httpapi

import (
	"net/http"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type certificationSettingsInput struct {
	Enabled            *bool  `json:"enabled"`
	RequireForPurchase *bool  `json:"require_for_purchase"`
	DefaultProviderID  string `json:"default_provider_id"`
	AdvancedEnabled    *bool  `json:"advanced_enabled"`
	AutoUpdateName     *bool  `json:"auto_update_name"`
	RequirePhoneMatch  *bool  `json:"require_phone_match"`
	NotifyOnApproval   *bool  `json:"notify_on_approval"`
	SuspendUnverified  *bool  `json:"suspend_unverified"`
	AllowFileUpload    *bool  `json:"allow_file_upload"`
	ExpectedVersion    int64  `json:"expected_version"`
}

func (s *Server) certificationSettings(w http.ResponseWriter, r *http.Request) {
	// Keep the field list explicit so the response remains stable and secret-free.
	var id int16
	var enabled, requireForPurchase, advanced, autoName, phoneMatch, notify, suspend, upload bool
	var provider string
	var version int64
	var updated any
	if err := s.db.Pool.QueryRow(r.Context(), `
		SELECT id,enabled,require_for_purchase,default_provider_id,advanced_enabled,
			auto_update_name,require_phone_match,notify_on_approval,suspend_unverified,
			allow_file_upload,version,updated_at
		FROM certification_settings WHERE id=1`).Scan(&id, &enabled, &requireForPurchase, &provider, &advanced, &autoName, &phoneMatch, &notify, &suspend, &upload, &version, &updated); err != nil {
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
		"default_provider_id": provider, "advanced_enabled": advanced,
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
	s.updateSingletonSettings(w, r, settingsUpdate{
		action: "certification.settings_updated", entity: "certification_settings", operation: "certification.settings", request: in,
		beforeReplay: func(tx pgx.Tx) error {
			var allowed bool
			if err := tx.QueryRow(r.Context(), `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND ar.active AND 'certifications.manage'=ANY(ar.permissions)),false) FROM users u LEFT JOIN admin_roles ar ON ar.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, identityFrom(r).ID).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return &store.Conflict{Code: "permission_denied", Message: "没有管理实名认证设置的权限"}
			}
			return nil
		},
		apply: func(tx pgx.Tx) (any, any, error) {
			next, err := updateSettingsVersion(r, tx, "certification_settings", in.ExpectedVersion, "实名认证设置已变更，请重新加载",
				`UPDATE certification_settings SET enabled=$1,require_for_purchase=$2,default_provider_id=$3,advanced_enabled=$4,auto_update_name=$5,require_phone_match=$6,notify_on_approval=$7,suspend_unverified=$8,allow_file_upload=$9,version=version+1,updated_at=now() WHERE id=1 RETURNING version`, *in.Enabled, *in.RequireForPurchase, in.DefaultProviderID, *in.AdvancedEnabled, *in.AutoUpdateName, *in.RequirePhoneMatch, *in.NotifyOnApproval, *in.SuspendUnverified, *in.AllowFileUpload)
			return map[string]any{"version": next}, map[string]any{
				"enabled": *in.Enabled, "require_for_purchase": *in.RequireForPurchase, "default_provider_id": in.DefaultProviderID,
				"advanced_enabled": *in.AdvancedEnabled, "auto_update_name": *in.AutoUpdateName, "require_phone_match": *in.RequirePhoneMatch,
				"notify_on_approval": *in.NotifyOnApproval, "suspend_unverified": *in.SuspendUnverified, "allow_file_upload": *in.AllowFileUpload,
				"previous_version": in.ExpectedVersion, "version": next,
			}, err
		},
	})
}
