package store

import (
	"context"
	"net/url"

	"github.com/jackc/pgx/v5"
)

// AdminCustomerFilter resolves an optional exact customer context. An explicit
// invalid or inaccessible ID must never fall back to an unfiltered query.
// Callers must also enforce current customer scope in their final data query.
func (s *Store) AdminCustomerFilter(ctx context.Context, actor int64, filters url.Values) (*int64, error) {
	return accountLogCustomerQuery(ctx, s.Pool, filters, actor)
}

// AdminCustomerFilterTx keeps context validation and a paginated read in the
// caller's snapshot. The same strict semantics apply to all admin lists.
func (s *Store) AdminCustomerFilterTx(ctx context.Context, tx pgx.Tx, actor int64, filters url.Values) (*int64, error) {
	return accountLogCustomerQuery(ctx, tx, filters, actor)
}
