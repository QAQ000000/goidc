package store

import (
	"context"
	"encoding/json"
	"github.com/jackc/pgx/v5"
)

type SchedulerConfig struct {
	ExpiryDays       []int `json:"expiry_days"`
	BillingGraceDays *int  `json:"billing_grace_days"`
}
type SchedulerSettingsInput struct {
	Version  int64            `json:"expected_version"`
	Enabled  *bool            `json:"enabled"`
	Settings *SchedulerConfig `json:"settings"`
}

type CertificationSettingsInput struct {
	Enabled            *bool   `json:"enabled"`
	RequireForPurchase *bool   `json:"require_for_purchase"`
	DefaultProviderID  string  `json:"default_provider_id"`
	DefaultInstanceID  *string `json:"default_instance_id"`
	AdvancedEnabled    *bool   `json:"advanced_enabled"`
	AutoUpdateName     *bool   `json:"auto_update_name"`
	RequirePhoneMatch  *bool   `json:"require_phone_match"`
	NotifyOnApproval   *bool   `json:"notify_on_approval"`
	SuspendUnverified  *bool   `json:"suspend_unverified"`
	AllowFileUpload    *bool   `json:"allow_file_upload"`
	ExpectedVersion    int64   `json:"expected_version"`
}

func PrepareSchedulerSettings(ctx context.Context, tx pgx.Tx, actor int64) (current int64, err error) {
	if err = tx.QueryRow(ctx, "SELECT version FROM scheduler_task_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
		return
	}
	if err = LockFileCredential(ctx, tx, actor); err != nil {
		return
	}
	var allowed bool
	if err = tx.QueryRow(ctx, "SELECT role='super_admin' AND status='active' FROM administrators WHERE id=$1 FOR SHARE", actor).Scan(&allowed); err != nil {
		return
	}
	if !allowed {
		err = conflict("permission_denied", "没有管理任务配置的权限")
	}
	return
}
func ApplySchedulerSettings(ctx context.Context, tx pgx.Tx, current int64, in SchedulerSettingsInput) (any, any, error) {
	if current != in.Version {
		return nil, nil, conflict("version_conflict", "任务配置已变更，请重新加载")
	}
	raw, _ := json.Marshal(in.Settings)
	var next int64
	err := tx.QueryRow(ctx, "UPDATE scheduler_task_settings SET enabled=$1,settings=$2::jsonb,version=version+1,updated_at=now() WHERE id=1 RETURNING version", *in.Enabled, raw).Scan(&next)
	return map[string]any{"version": next}, in, err
}
func ApplyCertificationSettings(ctx context.Context, tx pgx.Tx, db *Store, in CertificationSettingsInput) (any, any, error) {
	if _, err := LockVersion(ctx, tx, "certification_settings", 1, in.ExpectedVersion, "实名认证设置已变更，请重新加载"); err != nil {
		return nil, nil, err
	}
	instance := *in.DefaultInstanceID
	if err := db.ValidateCertificationSelectionTx(ctx, tx, in.DefaultProviderID, instance); err != nil {
		return nil, nil, err
	}
	var next int64
	err := tx.QueryRow(ctx, `UPDATE certification_settings SET enabled=$1,require_for_purchase=$2,default_provider_id=$3,advanced_enabled=$4,auto_update_name=$5,require_phone_match=$6,notify_on_approval=$7,suspend_unverified=$8,allow_file_upload=$9,default_instance_id=$10,version=version+1,updated_at=now() WHERE id=1 RETURNING version`, *in.Enabled, *in.RequireForPurchase, in.DefaultProviderID, *in.AdvancedEnabled, *in.AutoUpdateName, *in.RequirePhoneMatch, *in.NotifyOnApproval, *in.SuspendUnverified, *in.AllowFileUpload, instance).Scan(&next)
	return map[string]any{"version": next}, map[string]any{"enabled": *in.Enabled, "require_for_purchase": *in.RequireForPurchase, "default_provider_id": in.DefaultProviderID, "default_instance_id": instance, "advanced_enabled": *in.AdvancedEnabled, "auto_update_name": *in.AutoUpdateName, "require_phone_match": *in.RequirePhoneMatch, "notify_on_approval": *in.NotifyOnApproval, "suspend_unverified": *in.SuspendUnverified, "allow_file_upload": *in.AllowFileUpload, "previous_version": in.ExpectedVersion, "version": next}, err
}

func (s *Store) UpdateSchedulerAccountSettings(ctx context.Context, actor int64, key string, in SchedulerSettingsInput) (any, error) {
	var out any
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		current, err := PrepareSchedulerSettings(ctx, tx, actor)
		if err != nil {
			return err
		}
		hash := accountBusinessHash(in)
		result, replayed, err := loadAccountBusinessReplay(ctx, tx, actor, "scheduler.settings", key, hash)
		if err != nil {
			return err
		}
		if replayed {
			out = result
			return nil
		}
		var audit any
		out, audit, err = ApplySchedulerSettings(ctx, tx, current, in)
		if err != nil {
			return err
		}
		if err = Audit(ctx, tx, actor, "scheduler.settings_updated", "scheduler_task_settings", 1, audit); err != nil {
			return err
		}
		return SaveOperation(ctx, tx, actor, "admin:scheduler.settings", key, hash, out)
	})
	return out, err
}
func (s *Store) UpdateCertificationAccountSettings(ctx context.Context, actor int64, key string, in CertificationSettingsInput) (any, error) {
	var out any
	err := s.WithTx(ctx, func(tx pgx.Tx) error {
		if err := RequireAdministratorPermission(ctx, tx, actor, "certifications.manage"); err != nil {
			return err
		}
		if err := LockFileCredential(ctx, tx, actor); err != nil {
			return err
		}
		hash := accountBusinessHash(in)
		result, replayed, err := loadAccountBusinessReplay(ctx, tx, actor, "certification.settings", key, hash)
		if err != nil {
			return err
		}
		if replayed {
			out = result
			return nil
		}
		var audit any
		out, audit, err = ApplyCertificationSettings(ctx, tx, s, in)
		if err != nil {
			return err
		}
		if err = Audit(ctx, tx, actor, "certification.settings_updated", "certification_settings", 1, audit); err != nil {
			return err
		}
		return SaveOperation(ctx, tx, actor, "admin:certification.settings", key, hash, out)
	})
	return out, err
}
