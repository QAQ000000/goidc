package httpapi

import (
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

type adminOrderCommissionItem struct {
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

// adminOrderCommission returns a current-rule preview for selected orders.
// It is deliberately read-only: commission configuration is not a settlement
// record, and the preview must never credit an employee or customer balance.
func (s *Server) adminOrderCommission(w http.ResponseWriter, r *http.Request) {
	actor := identityFrom(r)
	if !actor.can("sales.view") {
		writeError(w, http.StatusForbidden, "permission_denied", "没有查看销售提成的权限")
		return
	}
	var input struct {
		OrderIDs []int64 `json:"order_ids"`
	}
	if !decode(w, r, &input) {
		return
	}
	if len(input.OrderIDs) > 100 {
		writeError(w, http.StatusBadRequest, "invalid_order_ids", "一次最多计算100个订单")
		return
	}
	seen := make(map[int64]struct{}, len(input.OrderIDs))
	for _, id := range input.OrderIDs {
		if id < 1 {
			writeError(w, http.StatusBadRequest, "invalid_order_ids", "订单编号无效")
			return
		}
		if _, ok := seen[id]; ok {
			writeError(w, http.StatusBadRequest, "invalid_order_ids", "订单编号不能重复")
			return
		}
		seen[id] = struct{}{}
	}
	if len(input.OrderIDs) == 0 {
		write(w, http.StatusOK, map[string]any{"items": []adminOrderCommissionItem{}, "total_commission_cents": int64(0)})
		return
	}

	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Now().In(zone)
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, zone)
	rows, err := s.db.Pool.Query(r.Context(), `
WITH requested AS MATERIALIZED (
  SELECT unnest($1::bigint[]) AS order_id
), scoped_orders AS MATERIALIZED (
  SELECT o.id,o.user_id,o.total_cents,COALESCE(i.id,0) AS invoice_id,
         COALESCE(i.status,'') AS invoice_status,
         CASE WHEN i.status='paid' THEN LEAST(o.total_cents,i.amount_cents) ELSE 0 END::bigint AS basis_cents,
         owner.id AS sales_owner_id,COALESCE(owner.display_name,'') AS sales_owner_name,
         COALESCE(owner.sales_enabled,false) AS sales_enabled,
         COALESCE(owner.status='active',false) AS sales_active,
         active_group.id AS group_id,COALESCE(active_group.purchase_rate_bps,0) AS purchase_rate_bps
  FROM requested req
  JOIN orders o ON o.id=req.order_id AND o.deleted_at IS NULL
  JOIN users customer ON customer.id=o.user_id AND customer.role='customer'
  LEFT JOIN invoices i ON i.order_id=o.id AND i.user_id=o.user_id
  LEFT JOIN users owner ON owner.id=customer.sales_owner_id
  LEFT JOIN sales_commission_groups active_group ON active_group.id=owner.sales_commission_group_id AND active_group.active
  WHERE admin_customer_allowed($4,o.user_id)
), monthly_turnover AS (
  SELECT customer.sales_owner_id,SUM(i.amount_cents)::bigint AS amount_cents
  FROM invoices i
  JOIN users customer ON customer.id=i.user_id AND customer.role='customer'
  WHERE i.status='paid' AND i.paid_at >= $2 AND i.paid_at < $3
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
            WHEN o.invoice_status<>'paid' THEN 'not_paid'
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
ORDER BY o.id`, input.OrderIDs, monthStart, now, actor.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (adminOrderCommissionItem, error) {
		var item adminOrderCommissionItem
		err := row.Scan(&item.OrderID, &item.SalesOwnerID, &item.SalesOwnerName, &item.InvoiceStatus, &item.BasisCents,
			&item.PurchaseRateBPS, &item.TierExtraRateBPS, &item.MonthlyTurnoverCents, &item.CommissionCents, &item.Status)
		return item, err
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if len(items) != len(input.OrderIDs) {
		writeError(w, http.StatusNotFound, "not_found", "订单不存在或无权查看")
		return
	}
	var total int64
	for _, item := range items {
		total += item.CommissionCents
	}
	write(w, http.StatusOK, map[string]any{"items": items, "total_commission_cents": total})
}
