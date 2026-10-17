package httpapi

import (
	"context"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"
)

type pageQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Queries are application SQL, never request fragments. Keeping both projections
// explicit supports joins, grouped rows and permission filters without exposing
// extra columns or assuming that a count can be derived from a table name.
// selectSQL includes ordering but no LIMIT/OFFSET; args belong to both queries.
// The caller chooses a pool or an existing transaction and its isolation level.
func listPage[T any](ctx context.Context, db pageQuerier, countSQL, selectSQL string, args []any, limit, offset int, collect pgx.RowToFunc[T]) (map[string]any, error) {
	var total int64
	if err := db.QueryRow(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, err
	}
	// Do not append into the caller's backing array: filters may be reused.
	pageArgs := make([]any, len(args), len(args)+2)
	copy(pageArgs, args)
	pageArgs = append(pageArgs, limit, offset)
	rows, err := db.Query(ctx, selectSQL+fmt.Sprintf(" LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), pageArgs...)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, collect)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []T{}
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}

func (s *Server) writeListPage(w http.ResponseWriter, r *http.Request, countSQL, selectSQL string, args []any, limit, offset int) {
	page, err := listPage(r.Context(), s.db.Pool, countSQL, selectSQL, args, limit, offset, pgx.RowToMap)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	write(w, http.StatusOK, page)
}
