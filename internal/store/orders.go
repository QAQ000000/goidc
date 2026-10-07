package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type CancelOrderResult struct {
	OrderID    int64  `json:"order_id"`
	Status     string `json:"status"`
	Replayed   bool   `json:"replayed"`
	StockUnits int64  `json:"stock_units_released"`
}

type BulkCancelOrderItem struct {
	OrderID int64  `json:"order_id"`
	Status  string `json:"status"`
	Error   string `json:"error,omitempty"`
}

type BulkCancelOrdersResult struct {
	Items     []BulkCancelOrderItem `json:"items"`
	Succeeded int                   `json:"succeeded"`
	Failed    int                   `json:"failed"`
}

// BulkCancelOrders deliberately keeps each order in its own transaction. A stale
// or already-paid order must not prevent an operator from cancelling the rest.
func (s *Store) BulkCancelOrders(ctx context.Context, actorID int64, orderIDs []int64, key string) (BulkCancelOrdersResult, error) {
	result := BulkCancelOrdersResult{Items: make([]BulkCancelOrderItem, 0, len(orderIDs))}
	if actorID < 1 || len(orderIDs) == 0 || len(orderIDs) > 100 || len(key) < 8 || len(key) > 100 {
		return result, conflict("invalid_bulk_cancel", "订单数量或幂等键无效")
	}
	seen := make(map[int64]struct{}, len(orderIDs))
	for _, orderID := range orderIDs {
		if orderID < 1 {
			return result, conflict("invalid_bulk_cancel", "订单编号无效")
		}
		if _, ok := seen[orderID]; ok {
			return result, conflict("invalid_bulk_cancel", "订单编号不能重复")
		}
		seen[orderID] = struct{}{}
		item := BulkCancelOrderItem{OrderID: orderID}
		cancelled, err := s.CancelOrder(ctx, actorID, orderID, true, fmt.Sprintf("%s:%d", key, orderID))
		if err != nil {
			item.Status = "failed"
			item.Error = bulkCancelError(err)
			result.Failed++
		} else {
			item.Status = cancelled.Status
			result.Succeeded++
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func bulkCancelError(err error) string {
	var c *Conflict
	if errors.As(err, &c) {
		return c.Message
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return "订单不存在或无权操作"
	}
	return "订单处理失败"
}

func (s *Store) CancelOrder(ctx context.Context, actorID, orderID int64, admin bool, key string) (CancelOrderResult, error) {
	var out CancelOrderResult
	if actorID < 1 || orderID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_order_cancel", "订单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "order-cancel", fmt.Sprintf("%d:%s", orderID, key)); err != nil {
		return out, err
	}
	var ownerID int64
	var status string
	if admin {
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "order", orderID); err != nil {
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT user_id,status FROM orders WHERE id=$1", orderID).Scan(&ownerID, &status); err != nil {
		return out, err
	}
	if !admin && ownerID != actorID {
		return out, pgx.ErrNoRows
	}
	// Payment settlement locks the invoice before the order. Use the same
	// order here so a provider notification cannot deadlock with cancellation.
	var invoiceStatus string
	if err = tx.QueryRow(ctx, "SELECT status FROM invoices WHERE order_id=$1 FOR UPDATE", orderID).Scan(&invoiceStatus); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT status FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&status); err != nil {
		return out, err
	}
	out.OrderID, out.Status = orderID, status
	if status == "cancelled" {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if status != "pending" {
		return out, conflict("order_not_cancellable", "只有待支付订单可以取消")
	}
	if invoiceStatus != "unpaid" {
		return out, conflict("order_not_cancellable", "账单当前状态不可取消订单")
	}
	var pending bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE i.order_id=$1 AND p.status IN ('pending','expired') AND p.provider<>'mock')", orderID).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, conflict("invoice_payment_pending", "订单已有在线支付，请先完成原支付单")
	}
	if _, err = tx.Exec(ctx, "UPDATE orders SET status='cancelled',updated_at=now() WHERE id=$1", orderID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET status='cancelled' WHERE order_id=$1 AND status='unpaid'", orderID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='cancelled',completed_at=now(),updated_at=now(),last_error='订单取消'
WHERE service_id IN (SELECT id FROM services WHERE order_id=$1) AND status IN ('pending','running','waiting_manual')`, orderID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE services SET status='cancelled',version=version+1,updated_at=now() WHERE order_id=$1 AND status='pending_payment'", orderID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `WITH released AS (
UPDATE services SET inventory_released=true WHERE order_id=$1 AND NOT inventory_released RETURNING product_id
), counts AS (SELECT product_id,count(*)::bigint AS n FROM released GROUP BY product_id),
stock_updates AS (UPDATE products p SET stock=p.stock+c.n,updated_at=now() FROM counts c WHERE p.id=c.product_id AND p.stock IS NOT NULL RETURNING c.n)
SELECT coalesce(sum(n),0) FROM stock_updates`, orderID).Scan(&out.StockUnits); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			out.StockUnits = 0
		} else {
			return out, err
		}
	}
	if err = Audit(ctx, tx, actorID, "order.cancelled", "order", orderID, map[string]any{"stock_units_released": out.StockUnits}); err != nil {
		return out, err
	}
	out.Status = "cancelled"
	return out, tx.Commit(ctx)
}
