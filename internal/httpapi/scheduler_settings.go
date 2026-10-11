package httpapi

import (
	"encoding/json"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
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
	key, ok := settingMutationKey(w, r)
	if !ok {
		return
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(in)
	tx, e := s.db.Pool.Begin(r.Context())
	if e != nil {
		s.fail(w, r, e)
		return
	}
	defer tx.Rollback(r.Context())
	var cur int64
	if e = tx.QueryRow(r.Context(), "SELECT version FROM scheduler_task_settings WHERE id=1 FOR UPDATE").Scan(&cur); e != nil {
		s.fail(w, r, e)
		return
	}
	ctx := requestFileContext(r)
	if e = store.LockFileCredential(ctx, tx, actor); e != nil {
		s.fail(w, r, e)
		return
	}
	var allowed bool
	if e = tx.QueryRow(ctx, "SELECT role='super_admin' AND status='active' FROM users WHERE id=$1 FOR SHARE", actor).Scan(&allowed); e != nil {
		s.fail(w, r, e)
		return
	}
	if !allowed {
		writeError(w, 403, "permission_denied", "没有管理任务配置的权限")
		return
	}
	if replay, replayed, e := loadSettingReplayTx(r, tx, actor, "scheduler.settings", key, hash); e != nil {
		s.fail(w, r, e)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	if cur != in.Version {
		writeError(w, 409, "version_conflict", "任务配置已变更，请重新加载")
		return
	}
	raw, _ := json.Marshal(in.Settings)
	var next int64
	if e = tx.QueryRow(r.Context(), "UPDATE scheduler_task_settings SET enabled=$1,settings=$2::jsonb,version=version+1,updated_at=now() WHERE id=1 RETURNING version", *in.Enabled, raw).Scan(&next); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, actor, "scheduler.settings_updated", "scheduler_task_settings", 1, in); e != nil {
		s.fail(w, r, e)
		return
	}
	result := map[string]any{"version": next}
	if e = saveSettingOperationTx(r, tx, actor, "scheduler.settings", key, hash, result); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, result)
}
