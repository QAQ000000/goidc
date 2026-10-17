package httpapi

import (
	"net/http"

	"github.com/jackc/pgx/v5"
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
	s.updateSingletonSettings(w, r, settingsUpdate{
		action: "credit.settings_updated", entity: "credit_settings", operation: "credit.settings", request: in,
		apply: func(tx pgx.Tx) (any, any, error) {
			next, err := updateSettingsVersion(r, tx, "credit_settings", in.ExpectedVersion, "信用额设置已变更，请重新加载",
				`UPDATE credit_settings SET enabled=$1,limit_cents=$2,auto_recharge=$3,version=version+1,updated_at=now() WHERE id=1 RETURNING version`, *in.Enabled, in.LimitCents, *in.AutoRecharge)
			return map[string]any{"version": next}, in, err
		},
	})
}
