package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

type AdminOrderCommissionItem struct {
	OrderID              int64  `json:"order_id"`
	SalesOwnerID         *int64 `json:"sales_owner_id"`
	SalesOwnerName       string `json:"sales_owner_name"`
	InvoiceStatus        string `json:"invoice_status"`
	BasisCents           int64  `json:"basis_cents"`
	PurchaseRateBPS      int    `json:"purchase_rate_bps"`
	TierExtraRateBPS     int    `json:"tier_extra_rate_bps"`
	MonthlyTurnoverCents int64  `json:"monthly_turnover_cents"`
	CommissionCents      int64  `json:"commission_cents"`
	Status               string `json:"status"`
}

func (s *Store) AdminOrderCommission(ctx context.Context, ids []int64, monthStart, now time.Time, actor int64) ([]AdminOrderCommissionItem, error) {
	rows, err := s.Pool.Query(ctx, `
WITH requested AS MATERIALIZED (
  SELECT unnest($1::bigint[]) AS order_id
), scoped_orders AS MATERIALIZED (
  SELECT o.id,o.user_id,o.total_cents,COALESCE(i.id,0) AS invoice_id,
         COALESCE(i.status,'') AS invoice_status,
         CASE WHEN i.status='paid' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id AND cp.status<>'repaid') THEN LEAST(o.total_cents,i.amount_cents) ELSE 0 END::bigint AS basis_cents,
         owner.id AS sales_owner_id,COALESCE(owner.display_name,'') AS sales_owner_name,
         COALESCE(owner.sales_enabled,false) AS sales_enabled,
         COALESCE(owner.status='active',false) AS sales_active,
         active_group.id AS group_id,COALESCE(active_group.purchase_rate_bps,0) AS purchase_rate_bps
  FROM requested req
  JOIN orders o ON o.id=req.order_id AND o.deleted_at IS NULL
  JOIN users customer ON customer.id=o.user_id AND customer.role='customer'
  LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id
  LEFT JOIN administrators owner ON owner.id=customer.sales_owner_id
  LEFT JOIN sales_commission_groups active_group ON active_group.id=owner.sales_commission_group_id AND active_group.active
  WHERE admin_customer_allowed($4,o.user_id)
), monthly_turnover AS (
  SELECT customer.sales_owner_id,SUM(i.amount_cents)::bigint AS amount_cents
  FROM invoices i
  JOIN users customer ON customer.id=i.user_id AND customer.role='customer'
  WHERE i.status='paid' AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id AND cp.status<>'repaid') AND COALESCE((SELECT max(received_at) FROM credit_repayments cr WHERE cr.invoice_id=i.id),i.paid_at) >= $2 AND COALESCE((SELECT max(received_at) FROM credit_repayments cr WHERE cr.invoice_id=i.id),i.paid_at) < $3
    AND admin_customer_allowed($4,customer.id)
    AND customer.sales_owner_id IN (SELECT DISTINCT sales_owner_id FROM scoped_orders WHERE sales_owner_id IS NOT NULL)
  GROUP BY customer.sales_owner_id
)
SELECT o.id,o.sales_owner_id,o.sales_owner_name,o.invoice_status,o.basis_cents,o.purchase_rate_bps,
       COALESCE(tier.extra_rate_bps,0)::integer,COALESCE(turnover.amount_cents,0)::bigint,
       CASE WHEN o.invoice_status='paid' AND o.sales_enabled AND o.sales_active AND o.group_id IS NOT NULL
            THEN round(o.basis_cents::numeric*(o.purchase_rate_bps+COALESCE(tier.extra_rate_bps,0))/10000)::bigint
            ELSE 0::bigint END AS commission_cents,
       CASE WHEN o.invoice_id=0 THEN 'not_invoiced'
            WHEN o.invoice_status<>'paid' OR o.basis_cents=0 THEN 'not_paid'
            WHEN o.sales_owner_id IS NULL THEN 'unassigned'
            WHEN NOT o.sales_enabled OR NOT o.sales_active THEN 'sales_inactive'
            WHEN o.group_id IS NULL THEN 'commission_group_unavailable'
            ELSE 'calculated' END AS status
FROM scoped_orders o
LEFT JOIN monthly_turnover turnover ON turnover.sales_owner_id=o.sales_owner_id
LEFT JOIN LATERAL (
  SELECT extra_rate_bps FROM sales_commission_tiers
  WHERE active AND threshold_cents<=COALESCE(turnover.amount_cents,0)
  ORDER BY threshold_cents DESC,id DESC LIMIT 1
) tier ON true
ORDER BY o.id`, ids, monthStart, now, actor)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AdminOrderCommissionItem, error) {
		var item AdminOrderCommissionItem
		err := row.Scan(&item.OrderID, &item.SalesOwnerID, &item.SalesOwnerName, &item.InvoiceStatus, &item.BasisCents,
			&item.PurchaseRateBPS, &item.TierExtraRateBPS, &item.MonthlyTurnoverCents, &item.CommissionCents, &item.Status)
		return item, err
	})
	return items, err
}
