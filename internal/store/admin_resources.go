package store

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// AdminResourcesPage describes registered services, not an independent pool of
// unassigned machines. Upstream identities come from immutable delivery data.
func (s *Store) AdminResourcesPage(ctx context.Context, actor int64, filters url.Values, limit, offset int) (map[string]any, error) {
	var product *int64
	if values, present := filters["product_id"]; present {
		invalid := &AccountBusinessError{400, "invalid_product", "商品编号必须是正整数"}
		if len(values) != 1 || values[0] == "" {
			return nil, invalid
		}
		for _, char := range values[0] {
			if char < '0' || char > '9' {
				return nil, invalid
			}
		}
		id, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || id < 1 {
			return nil, invalid
		}
		product = &id
	}
	supplier, status := strings.TrimSpace(filters.Get("supplier_id")), strings.TrimSpace(filters.Get("status"))
	if supplier != "" {
		if id, err := strconv.ParseInt(supplier, 10, 64); err != nil || id < 1 {
			return nil, &AccountBusinessError{400, "invalid_supplier", "供应商筛选无效"}
		}
	}
	if status != "" {
		switch status {
		case "pending_payment", "queued", "provisioning", "active", "suspended", "terminated", "failed", "cancelled", "import_review_required":
		default:
			return nil, &AccountBusinessError{400, "invalid_status", "资源状态无效"}
		}
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'resources.view'=ANY(r.permissions))))`, actor).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, &AccountBusinessError{403, "permission_denied", "没有读取资源的权限"}
	}
	customer, err := s.AdminCustomerFilterTx(ctx, tx, actor, filters)
	if err != nil {
		return nil, err
	}
	query := `SELECT s.id,s.name,s.domain,s.status,s.username,s.upstream_id,s.product_id,p.name AS product_name,s.next_due_at,
 jsonb_strip_nulls(jsonb_build_object('ips',s.resource_info->'ips','dedicated_ip',s.resource_info->'dedicated_ip','assigned_ips',s.resource_info->'assigned_ips','name',s.resource_info->'name','power',s.resource_info->'power')) AS resource_info,
 s.user_id,u.display_name AS customer_name,a.id AS supplier_id,a.name AS supplier_name
 FROM services s JOIN users u ON u.id=s.user_id JOIN products p ON p.id=s.product_id
 LEFT JOIN supplier_accounts a ON a.id::text=s.delivery_snapshot->>'supplier_id'
 WHERE admin_customer_allowed($1,s.user_id) AND ($2::bigint IS NULL OR s.user_id=$2) AND ($3::bigint IS NULL OR s.product_id=$3)
 AND (s.name ILIKE $4 OR s.domain ILIKE $4 OR u.display_name ILIKE $4 OR s.upstream_id ILIKE $4)
 AND (coalesce(s.resource_info->>'dedicated_ip','') ILIKE $5 OR coalesce(s.resource_info->>'assigned_ips','') ILIKE $5
 OR EXISTS(SELECT 1 FROM jsonb_array_elements_text(CASE WHEN jsonb_typeof(s.resource_info->'ips')='array' THEN s.resource_info->'ips' ELSE '[]'::jsonb END) ip WHERE ip ILIKE $5) OR $5='%%')
 AND ($6='' OR a.id::text=$6)
 AND (CASE WHEN $7='import_review_required' THEN s.import_review_required ELSE ($7='' OR s.status=$7) END)`
	page, err := customerAccountReadPage(ctx, tx, query, "id DESC", []any{actor, customer, product, "%" + strings.TrimSpace(filters.Get("q")) + "%", "%" + strings.TrimSpace(filters.Get("ip")) + "%", supplier, status}, limit, offset)
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT a.id,a.name FROM supplier_accounts a
 WHERE EXISTS(SELECT 1 FROM services s WHERE s.delivery_snapshot->>'supplier_id'=a.id::text
 AND admin_customer_allowed($1,s.user_id) AND ($2::bigint IS NULL OR s.user_id=$2) AND ($3::bigint IS NULL OR s.product_id=$3)) ORDER BY a.name,a.id`, actor, customer, product)
	suppliers, err := collectMaps(rows, err)
	if err != nil {
		return nil, err
	}
	page["suppliers"] = suppliers
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return page, nil
}
