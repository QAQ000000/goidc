package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
)

type schedulerConfig = store.SchedulerConfig
type schedulerSettingsInput = store.SchedulerSettingsInput

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
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	out, err := s.db.UpdateSchedulerAccountSettings(requestFileContext(r), identityFrom(r).ID, key, in)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, out)
}
