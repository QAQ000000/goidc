package httpapi

import (
	"net/http"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

// Each endpoint retains its request hash, receipt scope, authorization and lock
// order. Only replay, audit and commit belong to the common transaction flow.
type settingsUpdate struct {
	action, entity, operation string
	request                   any
	begin                     func() (pgx.Tx, bool)
	beforeReplay              func(pgx.Tx) error
	apply                     func(pgx.Tx) (result, audit any, err error)
}

func (s *Server) updateSingletonSettings(w http.ResponseWriter, r *http.Request, update settingsUpdate) {
	key, ok := mutationKey(w, r, "", true)
	if !ok {
		return
	}
	var tx pgx.Tx
	if update.begin != nil {
		tx, ok = update.begin()
		if !ok {
			return
		}
	} else {
		var err error
		tx, err = s.db.Pool.Begin(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	defer tx.Rollback(r.Context())
	if update.beforeReplay != nil {
		if err := update.beforeReplay(tx); err != nil {
			s.fail(w, r, err)
			return
		}
	}
	actor := identityFrom(r).ID
	hash := settingMutationHash(update.request)
	result, replayed, err := loadSettingReplayTx(r, tx, actor, update.operation, key, hash)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	var out any = result
	if !replayed {
		var audit any
		out, audit, err = update.apply(tx)
		if err == nil {
			err = store.Audit(r.Context(), tx, actor, update.action, update.entity, 1, audit)
		}
		if err == nil {
			err = store.SaveOperation(r.Context(), tx, actor, "admin:"+update.operation, key, hash, out)
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, out)
}

func lockSettingsVersion(r *http.Request, tx pgx.Tx, entity string, expected int64, message string) (int64, error) {
	return store.LockVersion(r.Context(), tx, entity, 1, expected, message)
}

func updateSettingsVersion(r *http.Request, tx pgx.Tx, entity string, expected int64, message, query string, args ...any) (int64, error) {
	if _, err := lockSettingsVersion(r, tx, entity, expected, message); err != nil {
		return 0, err
	}
	var next int64
	err := tx.QueryRow(r.Context(), query, args...).Scan(&next)
	return next, err
}
