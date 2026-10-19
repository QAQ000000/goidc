package store

import (
	"context"
	"time"

	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ConfigurePoolCorrelation scopes the diagnostic origin to a pool lease. Existing
// direct Pool.Begin/Query/Exec callers are covered without changing their SQL.
// Transactions keep their acquisition origin; switching origin requires a new
// lease. Every acquisition, including one with no origin, sets the session value.
// Call once when creating a configuration; copies inherit the installed hooks.
func ConfigurePoolCorrelation(cfg *pgxpool.Config) {
	before, after := cfg.BeforeAcquire, cfg.AfterRelease
	cfg.BeforeAcquire = func(ctx context.Context, conn *pgx.Conn) bool {
		if before != nil && !before(ctx, conn) {
			return false
		}
		setupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		_, err := conn.Exec(setupCtx, `SELECT set_config('goidc.correlation_id',$1,false)`, correlation.ID(ctx))
		return err == nil
	}
	cfg.AfterRelease = func(conn *pgx.Conn) bool {
		// The request may already be cancelled. Never return a connection with
		// its previous request's value still installed. pgxpool destroys a
		// connection if either cleanup or an existing hook rejects its reuse.
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := conn.Exec(cleanupCtx, `SELECT set_config('goidc.correlation_id','',false)`); err != nil {
			return false
		}
		return after == nil || after(conn)
	}
}
