package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) schedulerSettings(w http.ResponseWriter, r *http.Request) {
	var version int64
	var enabled bool
	var settings []byte
	var updated any
	err := s.db.Pool.QueryRow(r.Context(), "SELECT version,enabled,settings,updated_at FROM scheduler_task_settings WHERE id=1").Scan(&version, &enabled, &settings, &updated)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"version": version, "enabled": enabled, "settings": settings, "updated_at": updated})
}
func (s *Server) updateSchedulerSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Version  int64          `json:"expected_version"`
		Enabled  *bool          `json:"enabled"`
		Settings map[string]any `json:"settings"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Version < 1 || in.Enabled == nil || in.Settings == nil {
		writeError(w, 400, "invalid_scheduler_settings", "配置不完整")
		return
	}
	tx, err := s.db.Pool.Begin(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	defer tx.Rollback(r.Context())
	var current int64
	if err = tx.QueryRow(r.Context(), "SELECT version FROM scheduler_task_settings WHERE id=1 FOR UPDATE").Scan(&current); err != nil {
		s.fail(w, r, err)
		return
	}
	if current != in.Version {
		writeError(w, 409, "version_conflict", "任务配置已变更，请重新加载")
		return
	}
	var next int64
	err = tx.QueryRow(r.Context(), "UPDATE scheduler_task_settings SET enabled=$1,settings=$2::jsonb,version=version+1,updated_at=now() WHERE id=1 AND version=$3 RETURNING version", *in.Enabled, in.Settings, in.Version).Scan(&next)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err = store.Audit(r.Context(), tx, identityFrom(r).ID, "scheduler.settings_updated", "scheduler_task_settings", 1, map[string]any{"version": next, "enabled": *in.Enabled}); err != nil {
		s.fail(w, r, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, 200, map[string]any{"version": next})
}
