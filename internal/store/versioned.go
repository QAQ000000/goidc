package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// LockVersion preserves the caller's authorization and lock ordering. Tables
// are application constants; identifiers are quoted and values stay bound.
func LockVersion(ctx context.Context, tx pgx.Tx, table string, id any, expected int64, message string) (int64, error) {
	var current int64
	if err := tx.QueryRow(ctx, "SELECT version FROM "+pgx.Identifier{table}.Sanitize()+" WHERE id=$1 FOR UPDATE", id).Scan(&current); err != nil {
		return 0, err
	}
	if current != expected {
		return 0, conflict("version_conflict", message)
	}
	return current, nil
}

// UpdateVersioned performs the common id/version compare-and-swap and returns
// the new version. set is trusted application SQL with $1..$N for args, never
// request text. Domain guards, permissions, audit and replay stay with callers.
// Callers that already lock the row keep that lock and its domain error message.
func UpdateVersioned(ctx context.Context, tx pgx.Tx, table, set string, id, expected int64, args ...any) (int64, error) {
	values := make([]any, len(args), len(args)+2)
	copy(values, args)
	values = append(values, id, expected)
	query := fmt.Sprintf("UPDATE %s SET %s,version=version+1,updated_at=now() WHERE id=$%d AND version=$%d RETURNING version", pgx.Identifier{table}.Sanitize(), set, len(args)+1, len(args)+2)
	var next int64
	err := tx.QueryRow(ctx, query, values...).Scan(&next)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, conflict("version_conflict", "记录已变更，请重新加载")
	}
	return next, err
}
