package store

import (
	"context"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AdminCatalogGroups(ctx context.Context, actor int64, kind string) ([]map[string]any, error) {
	result, err := s.adminCatalogGroupsRead(ctx, actor, kind, nil, 0, 0, false)
	if err != nil {
		return nil, err
	}
	return result.([]map[string]any), nil
}

func (s *Store) AdminCatalogGroupsPage(ctx context.Context, actor int64, kind string, filters url.Values, limit, offset int) (map[string]any, error) {
	result, err := s.adminCatalogGroupsRead(ctx, actor, kind, filters, limit, offset, true)
	if err != nil {
		return nil, err
	}
	return result.(map[string]any), nil
}

func (s *Store) adminCatalogGroupsRead(ctx context.Context, actor int64, kind string, filters url.Values, limit, offset int, paged bool) (any, error) {
	if CatalogGroupTable(kind) == "" {
		return nil, &AccountBusinessError{400, "invalid_catalog_group", "分组类型无效"}
	}
	invalid := &AccountBusinessError{400, "invalid_catalog_filter", "商品分组筛选无效"}
	for _, key := range []string{"q", "active"} {
		if values, present := filters[key]; present && len(values) != 1 {
			return nil, invalid
		}
	}
	query, active := strings.TrimSpace(filters.Get("q")), filters.Get("active")
	if !contentTextValid(query, 120, false) || (active != "" && active != "true" && active != "false") {
		return nil, invalid
	}
	var section *int64
	if values, present := filters["section_id"]; present {
		invalidSection := &AccountBusinessError{400, "invalid_section", "一级分组筛选无效"}
		if kind != "category" || len(values) != 1 || values[0] == "" {
			return nil, invalidSection
		}
		for _, char := range values[0] {
			if char < '0' || char > '9' {
				return nil, invalidSection
			}
		}
		id, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || id < 1 {
			return nil, invalidSection
		}
		section = &id
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
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'products.view'=ANY(r.permissions))))`, actor).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, &AccountBusinessError{403, "permission_denied", "没有读取商品分组的权限"}
	}
	projection := `SELECT c.*,s.name AS section_name,coalesce(s.active,true) AS section_active,
 (SELECT count(*) FROM products p WHERE p.category_id=c.id) AS member_count
 FROM product_categories c LEFT JOIN product_sections s ON s.id=c.section_id`
	where := ` WHERE ($1='' OR c.name ILIKE '%'||$1||'%' OR c.slug ILIKE '%'||$1||'%' OR c.description ILIKE '%'||$1||'%')
 AND ($2='' OR c.active=($2='true')) AND ($3::bigint IS NULL OR c.section_id=$3)`
	args := []any{query, active, section}
	if kind == "section" {
		projection = `SELECT s.*,(SELECT count(*) FROM product_categories c WHERE c.section_id=s.id) AS member_count FROM product_sections s`
		where = ` WHERE ($1='' OR s.name ILIKE '%'||$1||'%' OR s.slug ILIKE '%'||$1||'%' OR s.description ILIKE '%'||$1||'%') AND ($2='' OR s.active=($2='true'))`
		args = []any{query, active}
	}
	var result any
	if paged {
		result, err = customerAccountReadPage(ctx, tx, projection+where, "sort_order,id", args, limit, offset)
	} else {
		// Keep the legacy array complete, including inactive groups.
		order := "c.sort_order,c.id"
		if kind == "section" {
			order = "s.sort_order,s.id"
		}
		result, err = collectMaps(tx.Query(ctx, projection+" ORDER BY "+order))
	}
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return result, nil
}
