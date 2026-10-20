package store

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

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

type AdminCustomerDirectoryFilter struct {
	Q, Company, Phone, QQ, Status, CertificationStatus string
	GroupID, SalesOwnerID, LevelID                     *int64
	CustomFields                                       map[string]any
}

// Keep eligibility identical for the rule page and customer filtering. A rule
// matches all six thresholds; recent activity retains the existing 30-day window.
// This is an eligibility filter, not a new persisted level or priority engine.
const customerLevelEligibility = `u.role='customer' AND u.status='active'
 AND (SELECT coalesce(sum(i.amount_cents),0) FROM invoices i WHERE i.user_id=u.id AND i.status='paid') >= cl.min_income_cents
 AND (SELECT coalesce(sum(oi.quantity),0) FROM order_items oi JOIN orders o ON o.id=oi.order_id WHERE o.user_id=u.id AND o.status IN ('paid','active')) >= cl.min_purchase_count
 AND (SELECT count(*) FROM audit_events a WHERE a.actor_id=u.id AND a.action='auth.login_succeeded') >= cl.min_login_count
 AND (SELECT count(*) FROM audit_events a WHERE a.actor_id=u.id AND a.action='auth.login_succeeded' AND a.created_at >= now()-interval '30 days') >= cl.min_recent_login_count
 AND (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=u.id AND i.status='paid' AND ii.item_type='renew') >= cl.min_renewal_count
 AND (SELECT count(*) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE i.user_id=u.id AND i.status='paid' AND ii.item_type='renew' AND i.paid_at >= now()-interval '30 days') >= cl.min_recent_renewal_count`

func (s *Store) customerDirectoryReadSnapshot(ctx context.Context, actor int64, permission string) (pgx.Tx, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND $2=ANY(r.permissions))))`, actor, permission).Scan(&allowed)
	if err != nil || !allowed {
		tx.Rollback(ctx)
		if err != nil {
			return nil, err
		}
		return nil, &AccountBusinessError{403, "permission_denied", "没有查看客户的权限"}
	}
	return tx, nil
}

func invalidCustomerDirectoryFilter() error {
	return &AccountBusinessError{400, "invalid_customer_filter", "客户高级筛选条件无效"}
}

func customerDirectoryContains(value string) string {
	if value == "" {
		return ""
	}
	return "%" + strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(value) + "%"
}

func validateCustomerDirectoryFilter(in AdminCustomerDirectoryFilter, limit, offset int) error {
	for _, field := range []struct {
		value string
		max   int
	}{{in.Q, 120}, {in.Company, 200}, {in.Phone, 80}, {in.QQ, 40}} {
		if !utf8.ValidString(field.value) || utf8.RuneCountInString(field.value) > field.max || strings.ContainsRune(field.value, 0) {
			return invalidCustomerDirectoryFilter()
		}
	}
	if limit < 1 || limit > 100 || offset < 0 || len(in.CustomFields) > 20 ||
		(in.GroupID != nil && *in.GroupID < 0) || (in.SalesOwnerID != nil && *in.SalesOwnerID < 0) || (in.LevelID != nil && *in.LevelID < 1) ||
		(in.Status != "" && in.Status != "active" && in.Status != "disabled") ||
		(in.CertificationStatus != "" && in.CertificationStatus != "unverified" && in.CertificationStatus != "pending" && in.CertificationStatus != "approved" && in.CertificationStatus != "rejected") {
		return invalidCustomerDirectoryFilter()
	}
	return nil
}

func (s *Store) AdminCustomerDirectoryPage(ctx context.Context, actor int64, in AdminCustomerDirectoryFilter, limit, offset int) (map[string]any, error) {
	if err := validateCustomerDirectoryFilter(in, limit, offset); err != nil {
		return nil, err
	}
	tx, err := s.customerDirectoryReadSnapshot(ctx, actor, "customers.view")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	query := customerListSelect + ` WHERE u.role='customer' AND admin_customer_allowed($1,u.id)
 AND (u.username ILIKE $2 OR u.display_name ILIKE $2 OR u.email ILIKE $2)
 AND ($3::bigint IS NULL OR coalesce(g.id,0)=$3)
 AND ($4::bigint IS NULL OR coalesce(u.sales_owner_id,0)=$4)
 AND ($5='' OR u.status=$5)
 AND ($6='' OR coalesce(p.company_name,(SELECT c.company_name FROM customer_contacts c WHERE c.customer_id=u.id AND c.company_name!='' ORDER BY c.id ASC LIMIT 1)) ILIKE $6 ESCAPE E'\\')
 AND ($7='' OR coalesce(p.phone,(SELECT c.phone_number FROM customer_contacts c WHERE c.customer_id=u.id AND c.phone_number!='' ORDER BY c.id ASC LIMIT 1)) ILIKE $7 ESCAPE E'\\')
 AND ($8='' OR p.qq ILIKE $8 ESCAPE E'\\')
 AND ($9='' OR coalesce((SELECT cp.status FROM certification_profiles cp WHERE cp.user_id=u.id),'unverified')=$9)`
	args := []any{actor, "%" + in.Q + "%", in.GroupID, in.SalesOwnerID, in.Status, customerDirectoryContains(in.Company), customerDirectoryContains(in.Phone), customerDirectoryContains(in.QQ), in.CertificationStatus}
	if in.LevelID != nil {
		var active bool
		if err = tx.QueryRow(ctx, "SELECT active FROM customer_levels WHERE id=$1", *in.LevelID).Scan(&active); err != nil {
			if err == pgx.ErrNoRows {
				return nil, invalidCustomerDirectoryFilter()
			}
			return nil, err
		}
		if !active {
			return nil, invalidCustomerDirectoryFilter()
		}
		args = append(args, *in.LevelID)
		query += fmt.Sprintf(" AND EXISTS(SELECT 1 FROM customer_levels cl WHERE cl.id=$%d AND cl.active AND ", len(args)) + customerLevelEligibility + ")"
	}
	keys := make([]string, 0, len(in.CustomFields))
	for key := range in.CustomFields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		// Field definitions and actual customer values share the same read snapshot.
		// Filters can inspect only current active fields exposed by the existing
		// administrator definition permission; never arbitrary JSON paths.
		var def CustomerCustomFieldDefinition
		if err = tx.QueryRow(ctx, "SELECT field_type,choices FROM customer_custom_fields WHERE field_key=$1 AND active", key).Scan(&def.Type, &def.Choices); err != nil {
			if err == pgx.ErrNoRows {
				return nil, invalidCustomerDirectoryFilter()
			}
			return nil, err
		}
		value := in.CustomFields[key]
		if value == nil {
			return nil, invalidCustomerDirectoryFilter()
		}
		if text, ok := value.(string); ok && strings.TrimSpace(text) == "" {
			return nil, invalidCustomerDirectoryFilter()
		}
		// Search text need not itself match the field's full-value regex. Other
		// typed filters reuse the profile validator, including real dates/choices.
		if _, err = ValidateCustomerCustomFieldValues(map[string]CustomerCustomFieldDefinition{key: def}, map[string]any{key: value}, nil); err != nil {
			return nil, invalidCustomerDirectoryFilter()
		}
		args = append(args, key)
		keyArg := len(args)
		if def.Type == "text" || def.Type == "textarea" {
			args = append(args, customerDirectoryContains(value.(string)))
			query += fmt.Sprintf(" AND jsonb_typeof(p.custom_fields->$%d::text)='string' AND (p.custom_fields->>$%d::text) ILIKE $%d ESCAPE E'\\\\'", keyArg, keyArg, len(args))
		} else {
			raw, e := json.Marshal(value)
			if e != nil {
				return nil, invalidCustomerDirectoryFilter()
			}
			args = append(args, string(raw))
			query += fmt.Sprintf(" AND p.custom_fields->$%d::text=$%d::jsonb", keyArg, len(args))
		}
	}
	page, err := customerAccountReadPage(ctx, tx, query, "id DESC", args, limit, offset)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return page, nil
}

func (s *Store) AdminCustomerLevels(ctx context.Context, actor int64, q, active string, limit, offset int) (map[string]any, error) {
	if limit < 1 || limit > 100 || offset < 0 || (active != "" && active != "true" && active != "false") {
		return nil, invalidCustomerDirectoryFilter()
	}
	tx, err := s.customerDirectoryReadSnapshot(ctx, actor, "customer_levels.view")
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	query := `SELECT cl.id,cl.name,cl.min_income_cents,cl.min_purchase_count,cl.min_login_count,cl.min_recent_login_count,
 cl.min_renewal_count,cl.min_recent_renewal_count,cl.sort_order,cl.active,cl.version,cl.created_at,cl.updated_at,
 (SELECT count(*) FROM users u WHERE admin_customer_allowed($1,u.id) AND ` + customerLevelEligibility + `) AS matching_customer_count
 FROM customer_levels cl WHERE ($2='' OR cl.name ILIKE '%'||$2||'%') AND ($3='' OR cl.active=($3='true'))`
	page, err := customerAccountReadPage(ctx, tx, query, "sort_order,id", []any{actor, q, active}, limit, offset)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	page["recent_window_days"] = 30
	return page, nil
}
