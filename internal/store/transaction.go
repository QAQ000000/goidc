package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// WithTx commits only a successful callback. pgx handles rollback on errors and
// panics, and returns commit failures to the caller. Do not write HTTP responses
// or perform external side effects inside the callback before commit succeeds.
func (s *Store) WithTx(ctx context.Context, fn func(pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, s.Pool, fn)
}

func withTx[T any](ctx context.Context, s *Store, fn func(pgx.Tx) (T, error)) (result T, err error) {
	err = s.WithTx(ctx, func(tx pgx.Tx) error {
		result, err = fn(tx)
		return err
	})
	return result, err
}
