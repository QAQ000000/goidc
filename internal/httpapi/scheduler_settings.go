package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

type schedulerConfig struct {
	ExpiryDays       []int `json:"expiry_days"`
	BillingGraceDays *int  `json:"billing_grace_days"`
}
type schedulerSettingsInput struct {
	Version  int64            `json:"expected_version"`
	Enabled  *bool            `json:"enabled"`
	Settings *schedulerConfig `json:"settings"`
}

func (s *Server) schedulerSettings(w http.ResponseWriter, r *http.Request) {
	var v int64
	var en bool
	var raw []byte
	var up any
	if e := s.db.Pool.QueryRow(r.Context(), "SELECT version,enabled,settings,updated_at FROM scheduler_task_settings WHERE id=1").Scan(&v, &en, &raw, &up); e != nil {
		s.fail(w, r, e)
		return
	}
	var cfg map[string]any
	_ = json.Unmarshal(raw, &cfg)
	write(w, 200, map[string]any{"version": v, "schema_version": 1, "enabled": en, "settings": cfg, "updated_at": up})
}

func validateScheduler(in *schedulerSettingsInput) error {
	if in.Version < 1 || in.Enabled == nil || in.Settings == nil || in.Settings.ExpiryDays == nil || in.Settings.BillingGraceDays == nil {
		return &store.Conflict{Code: "invalid_scheduler_settings", Message: "配置不完整"}
	}
	seen := map[int]bool{}
	for _, n := range in.Settings.ExpiryDays {
		if n < 1 || n > 365 || seen[n] {
			return &store.Conflict{Code: "invalid_scheduler_settings", Message: "到期提醒天数必须为 1 到 365 的不重复整数"}
		}
		seen[n] = true
	}
	if *in.Settings.BillingGraceDays < 0 || *in.Settings.BillingGraceDays > 365 {
		return &store.Conflict{Code: "invalid_scheduler_settings", Message: "账单宽限天数必须为 0 到 365 的整数"}
	}
	return nil
}

func (s *Server) updateSchedulerSettings(w http.ResponseWriter, r *http.Request) {
	var in schedulerSettingsInput
	if !decode(w, r, &in) {
		return
	}
	if e := validateScheduler(&in); e != nil {
		writeError(w, 400, "invalid_scheduler_settings", e.Error())
		return
	}
	var current int64
	s.updateSingletonSettings(w, r, settingsUpdate{
		action: "scheduler.settings_updated", entity: "scheduler_task_settings", operation: "scheduler.settings", request: in,
		beforeReplay: func(tx pgx.Tx) error {
			// The singleton lock must precede credential and actor locks, even on replay.
			if err := tx.QueryRow(r.Context(), "SELECT version FROM scheduler_task_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
				return err
			}
			ctx := requestFileContext(r)
			actor := identityFrom(r).ID
			if err := store.LockFileCredential(ctx, tx, actor); err != nil {
				return err
			}
			var allowed bool
			if err := tx.QueryRow(ctx, "SELECT role='super_admin' AND status='active' FROM users WHERE id=$1 FOR SHARE", actor).Scan(&allowed); err != nil {
				return err
			}
			if !allowed {
				return &store.Conflict{Code: "permission_denied", Message: "没有管理任务配置的权限"}
			}
			return nil
		},
		apply: func(tx pgx.Tx) (any, any, error) {
			if current != in.Version {
				return nil, nil, &store.Conflict{Code: "version_conflict", Message: "任务配置已变更，请重新加载"}
			}
			raw, _ := json.Marshal(in.Settings)
			var next int64
			err := tx.QueryRow(r.Context(), "UPDATE scheduler_task_settings SET enabled=$1,settings=$2::jsonb,version=version+1,updated_at=now() WHERE id=1 RETURNING version", *in.Enabled, raw).Scan(&next)
			return map[string]any{"version": next}, in, err
		},
	})
}
