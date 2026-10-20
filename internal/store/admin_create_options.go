package store

import (
	"context"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
)

// AdminCustomerCreateOptions returns only the customer fields required by the
// originating action. Its route keeps that action's permission requirement.
func (s *Store) AdminCustomerCreateOptions(ctx context.Context, actor int64, filters url.Values, query, kind string, limit, offset int) (map[string]any, error) {
	projection, active := "id,display_name AS name,email,status", " AND status='active'"
	scope := "admin_customer_allowed($1,id)"
	switch kind {
	case "order":
	case "invoice":
		projection, active = "id,username,display_name,email,status", ""
	case "ticket":
		projection, scope = "id,display_name AS name,email", "admin_ticket_customer_allowed($1,id)"
	default:
		return nil, &AccountBusinessError{400, "invalid_customer_options", "客户选项类型无效"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	customer, err := s.AdminCustomerFilterTx(ctx, tx, actor, filters)
	if err != nil {
		return nil, err
	}
	result, err := customerAccountReadPage(ctx, tx, "SELECT "+projection+` FROM users WHERE role='customer' AND `+scope+active+`
 AND ($2='' OR username ILIKE '%'||$2||'%' OR display_name ILIKE '%'||$2||'%' OR email ILIKE '%'||$2||'%')
 AND ($3::bigint IS NULL OR id=$3)`, "id DESC", []any{actor, query, customer}, limit, offset)
	if err != nil {
		return nil, err
	}
	return result, tx.Commit(ctx)
}

func (s *Store) AdminOrderMailEnabled(ctx context.Context) (bool, error) {
	var enabled bool
	err := s.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM channel_instances WHERE category='mail' AND enabled)").Scan(&enabled)
	return enabled, err
}

func (s *Store) AdminOrderProductOptions(ctx context.Context, filters url.Values, query string, limit, offset int) (map[string]any, error) {
	var productID int64
	if raw := filters.Get("product_id"); raw != "" {
		var err error
		productID, err = strconv.ParseInt(raw, 10, 64)
		if err != nil || productID < 1 {
			return nil, &AccountBusinessError{400, "invalid_product", "商品标识无效"}
		}
	}
	// The public purchase projection omits credentials and internal option notes.
	// Arbitrary metadata is also excluded from administrator create options.
	where := ` FROM products WHERE active AND catalog_category_visible(category_id)
 AND ($1='' OR name ILIKE '%'||$1||'%') AND ($2=0 OR id=$2)`
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	// Keep expensive configuration projections behind LIMIT. Materializing all
	// matching products just to count them would expand every option tree.
	var total int64
	if err = tx.QueryRow(ctx, "SELECT count(*)"+where, query, productID).Scan(&total); err != nil {
		return nil, err
	}
	items, err := collectMaps(tx.Query(ctx, "SELECT "+CatalogProductProjection(false)+where+" ORDER BY sort_order,id LIMIT $3 OFFSET $4", query, productID, limit, offset))
	if err != nil {
		return nil, err
	}
	for _, item := range items {
		delete(item, "metadata")
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]any{"items": items, "total": total, "limit": limit, "offset": offset}, nil
}
