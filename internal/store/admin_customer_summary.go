package store

import (
	"context"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
)

// AdminCustomerSummarySection distinguishes inaccessible data from a real zero.
type AdminCustomerSummarySection struct {
	Available bool           `json:"available"`
	Reason    *string        `json:"reason"`
	Data      map[string]any `json:"data"`
}

type AdminCustomerSummaryResult struct {
	Customer map[string]any                         `json:"customer"`
	AsOf     time.Time                              `json:"as_of"`
	Currency string                                 `json:"currency"`
	Sections map[string]AdminCustomerSummarySection `json:"sections"`
}

// AdminCustomerSummary reads one snapshot and authorizes each section separately.
// It deliberately does not call the legacy detail projection, whose financial
// counters represent wallet movements rather than external cash receipts.
func (s *Store) AdminCustomerSummary(ctx context.Context, actor, customer int64) (AdminCustomerSummaryResult, error) {
	result := AdminCustomerSummaryResult{Currency: "CNY", Sections: map[string]AdminCustomerSummarySection{}}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var role string
	var permissions []string
	if err = tx.QueryRow(ctx, `SELECT a.role,coalesce(r.permissions,'{}'::text[]),now()
 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id AND r.active
 WHERE a.id=$1 AND a.status='active'`, actor).Scan(&role, &permissions, &result.AsOf); err != nil {
		if err == pgx.ErrNoRows {
			return result, &AccountBusinessError{403, "permission_denied", "没有读取客户的权限"}
		}
		return result, err
	}
	can := func(permission string) bool {
		return role == "super_admin" || (role == "admin" && slices.Contains(permissions, permission))
	}
	if !can("customers.view") {
		return result, &AccountBusinessError{403, "permission_denied", "没有读取客户的权限"}
	}
	read := func(sql string, args ...any) (map[string]any, error) {
		rows, e := tx.Query(ctx, sql, args...)
		if e != nil {
			return nil, e
		}
		return pgx.CollectExactlyOneRow(rows, pgx.RowToMap)
	}
	result.Customer, err = read(`SELECT u.id,u.username,u.email,u.display_name,u.status,u.created_at,
 u.email_verified,u.phone AS login_phone,u.phone_verified,coalesce(p.version,1) AS profile_version,
 u.sales_owner_id,o.display_name AS sales_owner_name,u.sales_version,
 g.id AS group_id,g.name AS group_name,
 (SELECT max(a.created_at) FROM audit_events a WHERE a.actor_id=u.id AND a.action='auth.login_succeeded') AS last_login_at
 FROM users u LEFT JOIN administrators o ON o.id=u.sales_owner_id
 LEFT JOIN customer_profiles p ON p.customer_id=u.id
 LEFT JOIN customer_group_members gm ON gm.customer_id=u.id LEFT JOIN customer_groups g ON g.id=gm.group_id
 WHERE u.id=$2 AND u.role='customer' AND admin_customer_allowed($1,u.id)`, actor, customer)
	if err != nil {
		return result, err
	}
	type section struct {
		name    string
		allowed bool
		sql     string
	}
	for _, part := range []section{
		{"services", can("services.view"), `SELECT count(*)::bigint AS total,
 count(*) FILTER (WHERE status='active')::bigint AS active,
 count(*) FILTER (WHERE status='suspended')::bigint AS suspended,
 count(*) FILTER (WHERE status IN ('active','suspended') AND next_due_at<now())::bigint AS overdue,
 count(*) FILTER (WHERE status IN ('active','suspended') AND next_due_at>=now() AND next_due_at<now()+interval '30 days')::bigint AS expiring_30_days,
 coalesce((SELECT jsonb_agg(g ORDER BY g.group_id NULLS LAST) FROM (
 SELECT p.service_group_id AS group_id,n.name AS group_name,count(*)::bigint AS total,
 count(*) FILTER (WHERE s.status='active')::bigint AS active
 FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN service_navigation_groups n ON n.id=p.service_group_id
 WHERE s.user_id=$2 GROUP BY p.service_group_id,n.name) g),'[]'::jsonb) AS groups
 FROM services WHERE user_id=$2 AND admin_customer_allowed($1,user_id)`},
		{"invoices", can("invoices.view"), `SELECT count(*) FILTER (WHERE status='unpaid')::bigint AS unpaid_count,
 coalesce(sum(amount_cents) FILTER (WHERE status='unpaid'),0)::bigint AS unpaid_cents,
 count(*) FILTER (WHERE status='unpaid' AND due_at<now())::bigint AS overdue_count,
 coalesce(sum(amount_cents) FILTER (WHERE status='unpaid' AND due_at<now()),0)::bigint AS overdue_cents
 FROM invoices WHERE user_id=$2 AND admin_customer_allowed($1,user_id)`},
		{"tickets", can("tickets.view"), `SELECT count(*)::bigint AS total,
 count(*) FILTER (WHERE merged_ticket_id IS NOT NULL)::bigint AS merged_sources,
 count(*) FILTER (WHERE status='open' AND merged_ticket_id IS NULL)::bigint AS open,
 count(*) FILTER (WHERE status='pending' AND merged_ticket_id IS NULL)::bigint AS pending,
 count(*) FILTER (WHERE status='resolved' AND merged_ticket_id IS NULL)::bigint AS resolved,
 count(*) FILTER (WHERE status='closed' AND merged_ticket_id IS NULL)::bigint AS closed
 FROM tickets WHERE user_id=$2 AND admin_customer_allowed($1,user_id) AND admin_ticket_department_allowed($1,department_id)`},
		{"wallet", can("invoices.view") || can("balance.manage"), `SELECT u.balance_cents,
 (SELECT coalesce(sum(delta_cents) FILTER (WHERE delta_cents>0),0)::bigint FROM balance_ledger WHERE user_id=u.id) AS credits_cents,
 (SELECT coalesce(sum(-delta_cents) FILTER (WHERE delta_cents<0),0)::bigint FROM balance_ledger WHERE user_id=u.id) AS debits_cents
 FROM users u WHERE u.id=$2 AND admin_customer_allowed($1,u.id)`},
		{"cash", can("invoices.view"), `WITH totals AS (SELECT
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM payment_transactions WHERE user_id=$2 AND status='succeeded') AS gateway_receipts_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM credit_repayments WHERE user_id=$2) AS credit_repayment_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM external_refunds WHERE user_id=$2 AND status='confirmed') AS external_refunds_cents)
 SELECT *,gateway_receipts_cents+credit_repayment_cents AS receipts_cents,
 gateway_receipts_cents+credit_repayment_cents-external_refunds_cents AS net_receipts_cents
 FROM totals WHERE admin_customer_allowed($1,$2)`},
		{"certification", can("certifications.view"), `SELECT c.id AS profile_id,coalesce(c.status,'unverified') AS status,
 c.certification_type,c.version,c.updated_at
 FROM users u LEFT JOIN certification_profiles c ON c.user_id=u.id WHERE u.id=$2 AND admin_customer_allowed($1,u.id)`},
		{"referral", can("affiliates.view"), `SELECT at.id IS NOT NULL AS attributed,at.status,
 CASE WHEN admin_customer_allowed($1,a.user_id) THEN a.user_id END AS referrer_customer_id,
 CASE WHEN admin_customer_allowed($1,a.user_id) THEN ref.display_name END AS referrer_name,
 at.registered_at
 FROM users u LEFT JOIN affiliate_attributions at ON at.user_id=u.id AND at.status='active'
 LEFT JOIN affiliate_accounts a ON a.id=at.account_id LEFT JOIN users ref ON ref.id=a.user_id
 WHERE u.id=$2 AND admin_customer_allowed($1,u.id)`},
	} {
		if !part.allowed {
			reason := "permission_denied"
			result.Sections[part.name] = AdminCustomerSummarySection{Reason: &reason}
			continue
		}
		value, e := read(part.sql, actor, customer)
		if e != nil {
			return result, e
		}
		result.Sections[part.name] = AdminCustomerSummarySection{Available: true, Data: value}
	}
	return result, tx.Commit(ctx)
}
