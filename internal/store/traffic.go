package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// settleTrafficInvoice records payment only. Resource delivery remains an
// explicit, audited operator verification until an upstream adapter exists.
func settleTrafficInvoice(ctx context.Context, tx pgx.Tx, invoiceID int64) (bool, error) {
	var id int64
	var status string
	err := tx.QueryRow(ctx, `SELECT t.id,t.status FROM traffic_orders t JOIN invoices i ON i.id=t.invoice_id
 WHERE t.invoice_id=$1 AND t.user_id=i.user_id AND t.amount_cents=i.amount_cents
 AND (SELECT count(*)=1 AND bool_and(ii.item_type='traffic' AND ii.service_id=t.service_id AND ii.amount_cents=t.amount_cents) FROM invoice_items ii WHERE ii.invoice_id=i.id)
 FOR UPDATE OF t`, invoiceID).Scan(&id, &status)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return true, err
	}
	if status != "pending" {
		return true, conflict("traffic_order_not_payable", "流量包订单当前状态不可支付")
	}
	if _, err = tx.Exec(ctx, `UPDATE traffic_orders SET status='paid',payment_method='balance',fulfillment_status='waiting_manual',version=version+1,updated_at=now() WHERE id=$1`, id); err != nil {
		return true, err
	}
	return true, Audit(ctx, tx, 0, "traffic_order.paid", "traffic_order", id, map[string]any{"invoice_id": invoiceID, "fulfillment_status": "waiting_manual"})
}

// ReleaseTrafficInvoice is called while the invoice row is already locked.
// It returns reserved inventory exactly once and never changes fulfilled
// resources. Payment/refund callers use the same transaction boundary.
func ReleaseTrafficInvoice(ctx context.Context, tx pgx.Tx, invoiceID int64, target string) error {
	var orderID, productID int64
	var status, fulfillment string
	if err := tx.QueryRow(ctx, `SELECT id,traffic_product_id,status,fulfillment_status FROM traffic_orders WHERE invoice_id=$1 FOR UPDATE`, invoiceID).Scan(&orderID, &productID, &status, &fulfillment); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if fulfillment == "fulfilled" {
		return conflict("traffic_order_fulfilled", "已核验交付的流量包不能自动回收")
	}
	if target != "cancelled" && target != "refunded" {
		return conflict("invalid_traffic_release", "流量包释放状态无效")
	}
	if status == target || status == "cancelled" || status == "refunded" {
		return nil
	}
	if (target == "cancelled" && status != "pending") || (target == "refunded" && status != "paid") {
		return conflict("traffic_order_not_releasable", "流量包订单当前状态不能释放")
	}
	if _, err := tx.Exec(ctx, "SELECT id FROM traffic_products WHERE id=$1 FOR UPDATE", productID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE traffic_products SET stock=CASE WHEN stock IS NULL THEN NULL ELSE stock+1 END,sold_count=GREATEST(0,sold_count-1),version=version+1,updated_at=now() WHERE id=$1`, productID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `UPDATE traffic_orders SET status=$2,fulfillment_status='cancelled',version=version+1,updated_at=now() WHERE id=$1`, orderID, target)
	return err
}
