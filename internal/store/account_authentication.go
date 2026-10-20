package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

type accountQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Call after locking the concrete account row so a security mutation cannot
// outlive revocation or expiration while waiting for another account change.
func lockAccountSession(ctx context.Context, tx pgx.Tx, id int64, hash string) error {
	var token string
	err := tx.QueryRow(ctx, "SELECT token_hash FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR SHARE", id, hash).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) {
		return conflict("unauthorized", "登录已失效，请重新登录")
	}
	return err
}

// Authentication tables are selected from immutable principal kinds, never
// from a request-provided table name or the display-only identity projection.
func authenticationTable(ctx context.Context, q accountQuerier, id int64) (string, error) {
	var kind string
	if err := q.QueryRow(ctx, "SELECT kind FROM account_principals WHERE id=$1", id).Scan(&kind); err != nil {
		return "", err
	}
	switch kind {
	case "customer":
		return "users", nil
	case "administrator":
		return "administrators", nil
	default:
		return "", pgx.ErrNoRows
	}
}

func authenticationTableForPortal(portal string) (string, error) {
	switch portal {
	case "customer":
		return "users", nil
	case "admin":
		return "administrators", nil
	default:
		return "", pgx.ErrNoRows
	}
}
