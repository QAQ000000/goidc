package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
)

func (s *Server) creditSettings(w http.ResponseWriter, r *http.Request) {
	var id int
	var enabled, auto bool
	var limit, version int64
	var updated any
	row := s.db.Pool.QueryRow(r.Context(), `SELECT id,enabled,limit_cents,auto_recharge,version,updated_at FROM credit_settings WHERE id=1`)
	if e := row.Scan(&id, &enabled, &limit, &auto, &version, &updated); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, map[string]any{"id": id, "enabled": enabled, "limit_cents": limit, "auto_recharge": auto, "version": version, "updated_at": updated})
}
func (s *Server) updateCreditSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled         *bool `json:"enabled"`
		LimitCents      int64 `json:"limit_cents"`
		AutoRecharge    *bool `json:"auto_recharge"`
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) || in.Enabled == nil || in.AutoRecharge == nil || in.LimitCents < 0 || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_credit_settings", "信用额设置无效")
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
	if replay, replayed, err := loadSettingReplayTx(r, tx, actor, "credit.settings", key, hash); err != nil {
		s.fail(w, r, err)
		return
	} else if replayed {
		_ = tx.Commit(r.Context())
		write(w, 200, replay)
		return
	}
	var v int64
	if e = tx.QueryRow(r.Context(), `SELECT version FROM credit_settings WHERE id=1 FOR UPDATE`).Scan(&v); e != nil {
		s.fail(w, r, e)
		return
	}
	if v != in.ExpectedVersion {
		writeError(w, 409, "version_conflict", "信用额设置已变更，请重新加载")
		return
	}
	var next int64
	if e = tx.QueryRow(r.Context(), `UPDATE credit_settings SET enabled=$1,limit_cents=$2,auto_recharge=$3,version=version+1,updated_at=now() WHERE id=1 RETURNING version`, *in.Enabled, in.LimitCents, *in.AutoRecharge).Scan(&next); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = store.Audit(r.Context(), tx, actor, "credit.settings_updated", "credit_settings", 1, in); e != nil {
		s.fail(w, r, e)
		return
	}
	result := map[string]any{"version": next}
	if e = saveSettingOperationTx(r, tx, actor, "credit.settings", key, hash, result); e != nil {
		s.fail(w, r, e)
		return
	}
	if e = tx.Commit(r.Context()); e != nil {
		s.fail(w, r, e)
		return
	}
	write(w, 200, result)
}
