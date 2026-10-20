package store

import (
	"context"
	"fmt"
)

const invoiceAccountTopupRetryProjection = `i.status='unpaid' AND EXISTS(SELECT 1 FROM balance_topups t WHERE t.invoice_id=i.id AND t.status='accepted') AND EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status='closed') AND NOT EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.status<>'closed') AND NOT EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AND NOT EXISTS(SELECT 1 FROM balance_topup_settlements s WHERE s.invoice_id=i.id)`
const invoiceAccountPendingProjection = `EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_close_anomaly,
 ` + invoiceAccountTopupRetryProjection + ` AS topup_can_retry,
(SELECT t.status FROM balance_topups t WHERE t.invoice_id=i.id) AS topup_status,
 (SELECT s.amount_cents FROM balance_topup_settlements s WHERE s.invoice_id=i.id AND s.status='pending_liability' AND NOT EXISTS(SELECT 1 FROM balance_topup_liability_resolutions r WHERE r.topup_id=s.topup_id)) AS topup_pending_liability_cents,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired'))) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS payment_pending,
 EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status='succeeded') AS external_payment_paid,
 (i.status='unpaid' AND EXISTS(SELECT 1 FROM payment_transactions pt WHERE pt.invoice_id=i.id AND pt.status IN ('pending','expired') AND pt.provider<>'mock')) OR EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)) AS real_payment_pending, (SELECT jsonb_build_object('remaining_cents',cp.remaining_cents,'status',cp.status,'amount_cents',cp.amount_cents) FROM credit_invoice_payments cp WHERE cp.invoice_id=i.id) AS credit_payment`

type InvoiceAccountListFilter struct {
	Actor         int64
	Admin, Page   bool
	CustomerID    *int64
	Status, Query string
	Limit, Offset int
}

// InvoiceAccountList keeps customer scope in the final query, even when a
// caller already resolved an explicit customer before reaching the store.
func (s *Store) InvoiceAccountList(ctx context.Context, in InvoiceAccountListFilter) (any, error) {
	args := []any{in.Admin, in.Actor}
	where := "(($1::boolean AND admin_customer_allowed($2,i.user_id)) OR (NOT $1::boolean AND i.user_id=$2))"
	if in.Admin && in.CustomerID != nil {
		args = append(args, *in.CustomerID)
		where += fmt.Sprintf(" AND i.user_id=$%d", len(args))
	}
	if in.Status != "" {
		args = append(args, in.Status)
		where += fmt.Sprintf(" AND i.status=$%d", len(args))
	}
	if in.Query != "" {
		args = append(args, "%"+in.Query+"%")
		where += fmt.Sprintf(" AND (i.invoice_no ILIKE $%d OR o.order_no ILIKE $%d OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.description ILIKE $%d) OR EXISTS(SELECT 1 FROM order_items oi WHERE oi.order_id=o.id AND oi.product_name ILIKE $%d) OR ($1::boolean AND (u.display_name ILIKE $%d OR u.email ILIKE $%d)))", len(args), len(args), len(args), len(args), len(args), len(args))
	}
	query := "SELECT i.id,i.invoice_no,o.id AS order_id,o.order_no,i.user_id,u.display_name AS customer_name,i.status,i.amount_cents,i.due_at,i.paid_at,i.created_at,i.invoice_kind,i.version,i.import_review_required," + invoiceAccountPendingProjection + " FROM invoices i JOIN users u ON u.id=i.user_id LEFT JOIN orders o ON o.id=i.order_id AND o.user_id=i.user_id WHERE " + where
	if in.Page {
		return customerAccountReadPage(ctx, s.Pool, query, "id DESC", args, in.Limit, in.Offset)
	}
	rows, err := s.Pool.Query(ctx, query+fmt.Sprintf(" ORDER BY i.id DESC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, in.Limit, in.Offset)...)
	return collectMaps(rows, err)
}
