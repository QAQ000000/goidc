package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// DashboardRanking describes business receipts, including balance consumption.
// Its amounts are neither external cash receipts nor commission or profit.
type DashboardRanking[T any] struct {
	Items            []T    `json:"items"`
	Total            int    `json:"total"`
	Month            string `json:"month"`
	Limit            int    `json:"limit"`
	Currency         string `json:"currency"`
	Timezone         string `json:"timezone"`
	AmountBasis      string `json:"amount_basis"`
	ActivityBasis    string `json:"activity_basis"`
	RefundBasis      string `json:"refund_basis"`
	AttributionBasis string `json:"attribution_basis,omitempty"`
}

type StaffRankingItem struct {
	SalesOwnerID        int64  `json:"sales_owner_id"`
	SalesOwnerName      string `json:"sales_owner_name"`
	AmountCents         int64  `json:"amount_cents"`
	InvoiceCount        int64  `json:"invoice_count"`
	CustomerCount       int64  `json:"customer_count"`
	PurchaseAmountCents int64  `json:"purchase_amount_cents"`
	RenewalAmountCents  int64  `json:"renewal_amount_cents"`
	UpgradeAmountCents  int64  `json:"upgrade_amount_cents"`
	OtherAmountCents    int64  `json:"other_amount_cents"`
}

func dashboardRankingSnapshot(ctx context.Context, s *Store, actor int64, start, end time.Time, limit int) (pgx.Tx, error) {
	zone := time.FixedZone("Asia/Shanghai", 8*3600)
	month := start.In(zone)
	if limit < 1 || limit > 20 || month.Year() < 2000 || month.Year() > time.Now().In(zone).Year()+1 ||
		!start.Equal(time.Date(month.Year(), month.Month(), 1, 0, 0, 0, 0, zone)) || !end.Equal(month.AddDate(0, 1, 0)) {
		return nil, &AccountBusinessError{400, "invalid_report_period", "排名期间或条数无效"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, err
	}
	var allowed bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'reports.view'=ANY(r.permissions))))`, actor).Scan(&allowed)
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	if !allowed {
		tx.Rollback(ctx)
		return nil, &AccountBusinessError{403, "permission_denied", "没有查看经营报表的权限"}
	}
	return tx, nil
}

func dashboardRankingResult[T any](items []T, start time.Time, limit int) DashboardRanking[T] {
	result := DashboardRanking[T]{
		Items: items, Total: len(items), Month: start.In(time.FixedZone("Asia/Shanghai", 8*3600)).Format("2006-01"), Limit: limit,
		Currency: "CNY", Timezone: "Asia/Shanghai",
		AmountBasis: "business_receipts_including_balance_and_actual_credit_repayments",
		RefundBasis: "confirmed_external_refunds_not_deducted_balance_refunded_invoices_excluded",
	}
	if len(result.Items) > limit {
		result.Items = result.Items[:limit]
	}
	return result
}

// ReportProductRanking reuses complete invoice-line allocation before ranking.
// Authority, current customer scope and returned rows share one read snapshot.
func (s *Store) ReportProductRanking(ctx context.Context, actor int64, start, end time.Time, limit int) (DashboardRanking[ProductIncomeRow], error) {
	tx, err := dashboardRankingSnapshot(ctx, s, actor, start, end, limit)
	if err != nil {
		return DashboardRanking[ProductIncomeRow]{}, err
	}
	defer tx.Rollback(ctx)
	items, err := productIncomeRowsFrom(ctx, tx, start, end, actor)
	if err != nil {
		return DashboardRanking[ProductIncomeRow]{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return DashboardRanking[ProductIncomeRow]{}, err
	}
	result := dashboardRankingResult(items, start, limit)
	result.ActivityBasis = "new_purchase_and_renewal_separate_units_and_distinct_orders"
	return result, nil
}

// ReportStaffRanking uses the existing line-level receipt facts, before any
// commission estimation. Unassigned customers and staff without receipts do not
// participate. Reassigning a customer moves that customer's historical receipts.
func (s *Store) ReportStaffRanking(ctx context.Context, actor int64, start, end time.Time, limit int) (DashboardRanking[StaffRankingItem], error) {
	tx, err := dashboardRankingSnapshot(ctx, s, actor, start, end, limit)
	if err != nil {
		return DashboardRanking[StaffRankingItem]{}, err
	}
	defer tx.Rollback(ctx)
	args := []any{actor, start, end}
	rows, err := tx.Query(ctx, salesReceiptFactsCTE+`SELECT sales_owner_id,max(sales_owner_name),sum(amount)::bigint,
 count(DISTINCT id)::bigint,count(DISTINCT user_id)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='purchase'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='renew'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='upgrade'),0)::bigint,
 coalesce(sum(amount) FILTER(WHERE kind='other'),0)::bigint
 FROM amounts a WHERE received_at >= $2 AND received_at < $3 AND sales_owner_id IS NOT NULL AND amount>0
 AND (credit OR status='paid' OR (status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=a.id AND er.status='confirmed')))
 GROUP BY sales_owner_id ORDER BY sum(amount) DESC,sales_owner_id`, args...)
	if err != nil {
		return DashboardRanking[StaffRankingItem]{}, err
	}
	items, err := pgx.CollectRows(rows, pgx.RowToStructByPos[StaffRankingItem])
	if err != nil {
		return DashboardRanking[StaffRankingItem]{}, err
	}
	if items == nil {
		items = []StaffRankingItem{}
	}
	if err = tx.Commit(ctx); err != nil {
		return DashboardRanking[StaffRankingItem]{}, err
	}
	result := dashboardRankingResult(items, start, limit)
	result.ActivityBasis = "purchase_renewal_upgrade_and_other_business"
	result.AttributionBasis = "current_customer_sales_owner_unassigned_excluded"
	return result, nil
}
