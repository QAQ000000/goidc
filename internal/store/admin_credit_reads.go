package store

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func (s *Store) AdminCreditAccountPage(ctx context.Context, actor int64, filters url.Values, limit, offset int) (map[string]any, error) {
	return s.adminCreditReadPage(ctx, actor, "accounts", filters, limit, offset)
}

func (s *Store) AdminCreditConsumptionsPage(ctx context.Context, actor int64, filters url.Values, limit, offset int) (map[string]any, error) {
	return s.adminCreditReadPage(ctx, actor, "consumptions", filters, limit, offset)
}

func (s *Store) AdminCreditRepaymentsPage(ctx context.Context, actor int64, filters url.Values, limit, offset int) (map[string]any, error) {
	return s.adminCreditReadPage(ctx, actor, "repayments", filters, limit, offset)
}

// Keep exact customer validation and the final count/rows in the same read
// snapshot. Every final query retains the customer's current resource scope.
func (s *Store) adminCreditReadPage(ctx context.Context, actor int64, kind string, filters url.Values, limit, offset int) (map[string]any, error) {
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'credit.view'=ANY(r.permissions))))`, actor).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, &AccountBusinessError{403, "permission_denied", "没有读取信用账户的权限"}
	}
	customer, err := s.AdminCustomerFilterTx(ctx, tx, actor, filters)
	if err != nil {
		return nil, err
	}
	if limit < 1 || limit > 100 {
		limit = 25
	}
	if offset < 0 {
		offset = 0
	}
	var query, order string
	var args []any
	status := filters.Get("status")
	if kind == "accounts" {
		if status != "" && status != "active" && status != "disabled" {
			return nil, &AccountBusinessError{400, "invalid_status", "客户状态无效"}
		}
		query = `SELECT u.id,u.username,u.display_name,u.email,u.status,a.limit_cents,a.used_cents,
 GREATEST(a.limit_cents-a.used_cents,0)::bigint AS available_cents,a.version,a.updated_at,a.frozen
 FROM users u JOIN customer_credit_accounts a ON a.user_id=u.id
 WHERE u.role='customer' AND admin_customer_allowed($1,u.id)
 AND ($2='' OR u.status=$2) AND ($3='' OR u.username ILIKE $3 OR u.display_name ILIKE $3 OR u.email ILIKE $3 OR u.id::text ILIKE $3)
 AND ($4::bigint IS NULL OR u.id=$4)`
		order = "id DESC"
		args = []any{actor, status, "%" + strings.TrimSpace(filters.Get("q")) + "%", customer}
	} else {
		from, to, periodErr := adminCreditReadPeriod(filters)
		if periodErr != nil {
			return nil, periodErr
		}
		if status != "" && status != "outstanding" && status != "repaid" && status != "refunded" {
			return nil, &AccountBusinessError{400, "invalid_credit_status", "信用状态无效"}
		}
		args = []any{actor, "%" + filters.Get("q") + "%", customer, from, to}
		if kind == "consumptions" {
			query = `SELECT c.invoice_id AS id,c.user_id,u.display_name AS customer_name,i.invoice_no,c.amount_cents,c.remaining_cents,c.status,c.version,c.created_at
 FROM credit_invoice_payments c JOIN users u ON u.id=c.user_id JOIN invoices i ON i.id=c.invoice_id
 WHERE admin_customer_allowed($1,c.user_id) AND (u.display_name ILIKE $2 OR i.invoice_no ILIKE $2 OR i.id::text ILIKE $2)
 AND ($3::bigint IS NULL OR c.user_id=$3) AND ($4::timestamptz IS NULL OR c.created_at>=$4)
 AND ($5::timestamptz IS NULL OR c.created_at<$5) AND ($6='' OR c.status=$6)`
			args = append(args, status)
			order = "created_at DESC,id DESC"
		} else {
			if status != "" {
				return nil, &AccountBusinessError{400, "invalid_repayment_status", "还款记录仅包含已确认到账记录"}
			}
			query = `SELECT p.id,p.invoice_id,p.user_id,u.display_name AS customer_name,i.invoice_no,p.amount_cents,p.receipt,p.received_at,p.note,p.created_at
 FROM credit_repayments p JOIN users u ON u.id=p.user_id JOIN invoices i ON i.id=p.invoice_id
 WHERE admin_customer_allowed($1,p.user_id) AND (u.display_name ILIKE $2 OR i.invoice_no ILIKE $2 OR p.receipt ILIKE $2)
 AND ($3::bigint IS NULL OR p.user_id=$3) AND ($4::timestamptz IS NULL OR p.received_at>=$4)
 AND ($5::timestamptz IS NULL OR p.received_at<$5)`
			order = "received_at DESC,id DESC"
		}
	}
	page, err := customerAccountReadPage(ctx, tx, query, order, args, limit, offset)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return page, nil
}

func adminCreditReadPeriod(filters url.Values) (*time.Time, *time.Time, error) {
	var from, to *time.Time
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	for _, field := range []string{"date_from", "date_to"} {
		raw := filters.Get(field)
		if raw == "" {
			continue
		}
		value, err := time.ParseInLocation("2006-01-02", raw, zone)
		if err != nil {
			return nil, nil, &AccountBusinessError{400, "invalid_date", "日期无效"}
		}
		if field == "date_from" {
			from = &value
		} else {
			value = value.AddDate(0, 0, 1)
			to = &value
		}
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, nil, &AccountBusinessError{400, "invalid_date_range", "日期范围无效"}
	}
	return from, to, nil
}
