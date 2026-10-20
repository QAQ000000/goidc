package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

const customerListSelect = `SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,
 u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id AND s.status='active')::bigint AS active_services_count,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id)::bigint AS total_services_count,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents > 0 THEN l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS income_cents,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents < 0 THEN -l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS expense_cents,
 coalesce(p.company_name,(SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name != '' ORDER BY c.id ASC LIMIT 1)) AS company_name,
 coalesce(p.phone,(SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number != '' ORDER BY c.id ASC LIMIT 1)) AS phone_number
 FROM users u
 LEFT JOIN administrators owner ON owner.id=u.sales_owner_id
 LEFT JOIN customer_group_members gm ON gm.customer_id=u.id
 LEFT JOIN customer_groups g ON g.id=gm.group_id
 LEFT JOIN customer_profiles p ON p.customer_id=u.id`

func (s *Store) AdminCustomerDirectory(ctx context.Context, actor int64, q string, limit, offset int) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, customerListSelect+`
 WHERE u.role='customer' AND admin_customer_allowed($4,u.id) AND (u.display_name ILIKE $1 OR u.email ILIKE $1 OR u.username ILIKE $1)
 ORDER BY u.id DESC LIMIT $2 OFFSET $3`, "%"+q+"%", limit, offset, actor))
}
func (s *Store) AdminCustomerDetail(ctx context.Context, actor, id int64) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, `SELECT u.id,u.username,u.email,u.display_name,u.status,u.balance_cents,u.created_at,
 u.sales_owner_id,owner.display_name AS sales_owner_name,u.sales_version,g.id AS group_id,g.name AS group_name,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id AND s.status='active')::bigint AS active_services_count,
 (SELECT count(*) FROM services s WHERE s.user_id=u.id)::bigint AS total_services_count,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents > 0 THEN l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS income_cents,
 (SELECT coalesce(sum(CASE WHEN l.delta_cents < 0 THEN -l.delta_cents ELSE 0 END), 0) FROM balance_ledger l WHERE l.user_id=u.id)::bigint AS expense_cents,
 COALESCE(p.company_name,(SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name != '' ORDER BY c.id ASC LIMIT 1)) AS company_name,
 COALESCE(p.phone,(SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number != '' ORDER BY c.id ASC LIMIT 1)) AS phone_number,
 COALESCE(p.qq,'') AS qq,COALESCE(p.gender,'unknown') AS gender,
 COALESCE(p.country,'') AS country,COALESCE(p.province,'') AS province,COALESCE(p.address,'') AS address,
 COALESCE(p.postcode,'') AS postcode,COALESCE(p.source_channel,'') AS source_channel,
 COALESCE(p.payment_method,'bank') AS payment_method,COALESCE(p.language,'zh_CN') AS language,
 COALESCE(p.admin_note,'') AS admin_note,COALESCE(p.receive_marketing,true) AS receive_marketing,COALESCE(p.custom_fields,'{}'::jsonb) AS custom_fields,COALESCE(p.version,1) AS profile_version
 FROM users u
 LEFT JOIN administrators owner ON owner.id=u.sales_owner_id
 LEFT JOIN customer_group_members gm ON gm.customer_id=u.id
 LEFT JOIN customer_groups g ON g.id=gm.group_id
 LEFT JOIN customer_profiles p ON p.customer_id=u.id
 WHERE u.role='customer' AND u.id=$1 AND admin_customer_allowed($2,u.id)`, id, actor))
}
func (s *Store) AdminAuditEvents(ctx context.Context, limit, offset int) ([]map[string]any, error) {
	return collectMaps(s.Pool.Query(ctx, "SELECT a.id,a.actor_id,u.display_name AS actor_name,a.action,a.entity_type,a.entity_id,a.payload,a.created_at FROM audit_events a LEFT JOIN account_identities u ON u.id=a.actor_id ORDER BY a.id DESC LIMIT $1 OFFSET $2", limit, offset))
}
func customerAccountReadPage(ctx context.Context, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, query, order string, args []any, limit, offset int) (map[string]any, error) {
	limitArg, offsetArg := len(args)+1, len(args)+2
	rows, err := q.Query(ctx, `WITH filtered AS (`+query+`) SELECT (SELECT count(*) FROM filtered) AS total,
 coalesce((SELECT jsonb_agg(row_to_json(page)) FROM (SELECT * FROM filtered ORDER BY `+order+fmt.Sprintf(" LIMIT $%d OFFSET $%d", limitArg, offsetArg)+`) page),'[]'::jsonb) AS items`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	page, err := pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	if err != nil {
		return nil, err
	}
	page["limit"], page["offset"] = limit, offset
	return page, nil
}
func (s *Store) AdminAuditLogPage(ctx context.Context, query string, limit, offset int) (map[string]any, error) {
	args := []any{}
	where := "TRUE"
	if query != "" {
		args = append(args, "%"+query+"%")
		where = "(a.action ILIKE $1 OR a.entity_type ILIKE $1 OR a.entity_id::text ILIKE $1 OR u.display_name ILIKE $1)"
	}
	return customerAccountReadPage(ctx, s.Pool, "SELECT a.id,a.created_at,a.action,a.entity_type,a.entity_id,a.actor_id,u.display_name AS actor_name FROM audit_events a LEFT JOIN account_identities u ON u.id=a.actor_id WHERE "+where, "id DESC", args, limit, offset)
}
func (s *Store) AdminCustomerDirectoryPage(ctx context.Context, actor int64, q string, group, owner *int64, status string, limit, offset int) (map[string]any, error) {
	query := customerListSelect + ` WHERE u.role='customer' AND admin_customer_allowed($1,u.id)
 AND (u.username ILIKE $2 OR u.display_name ILIKE $2 OR u.email ILIKE $2)
 AND ($3::bigint IS NULL OR coalesce(g.id,0)=$3)
 AND ($4::bigint IS NULL OR coalesce(u.sales_owner_id,0)=$4)
 AND ($5='' OR u.status=$5)`
	return customerAccountReadPage(ctx, s.Pool, query, "id DESC", []any{actor, "%" + q + "%", group, owner, status}, limit, offset)
}
