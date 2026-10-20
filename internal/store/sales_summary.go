package store

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

type SalesReportItem struct {
	SalesOwnerID   int64  `json:"sales_owner_id"`
	SalesOwnerName string `json:"sales_owner_name"`
	OrderCount     int64  `json:"order_count"`
	IncomeCents    int64  `json:"income_cents"`
}

func (s *Store) SalesSummary(ctx context.Context, start, end time.Time, actor int64) ([]SalesReportItem, error) {
	rows, e := s.Pool.Query(ctx, `
WITH cash AS (
 SELECT i.user_id,i.amount_cents,i.paid_at received_at FROM invoices i WHERE (i.status='paid' OR (i.status='refunded' AND EXISTS(SELECT 1 FROM external_refunds er WHERE er.invoice_id=i.id AND er.status='confirmed'))) AND i.invoice_kind<>'topup' AND NOT EXISTS(SELECT 1 FROM credit_invoice_payments c WHERE c.invoice_id=i.id)
 UNION ALL SELECT r.user_id,r.amount_cents,r.received_at FROM credit_repayments r
), customer_orders AS (
 SELECT user_id,count(*)::bigint AS order_count FROM orders
 WHERE created_at >= $1 AND created_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY user_id
), customer_income AS (
 SELECT user_id,sum(amount_cents)::bigint AS income_cents FROM cash
 WHERE received_at >= $1 AND received_at < $2 AND admin_customer_allowed($3,user_id) GROUP BY user_id
)
SELECT coalesce(u.sales_owner_id,0) AS sales_owner_id,
       coalesce(o.display_name,'未分配') AS sales_owner_name,
       coalesce(sum(c.order_count),0)::bigint AS order_count,
       coalesce(sum(i.income_cents),0)::bigint AS income_cents
FROM customer_orders c FULL JOIN customer_income i ON i.user_id=c.user_id
JOIN users u ON u.id=coalesce(c.user_id,i.user_id)
LEFT JOIN administrators o ON o.id=u.sales_owner_id
GROUP BY 1,2 ORDER BY income_cents DESC,sales_owner_id`, start, end, actor)
	if e != nil {
		return nil, e
	}
	items, e := pgx.CollectRows(rows, pgx.RowToStructByName[SalesReportItem])
	return items, e
}
