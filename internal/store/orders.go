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
	Version    int64  `json:"version"`
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
func (s *Store) BulkCancelOrders(ctx context.Context, actorID int64, orderIDs []int64, key string, versions map[int64]int64) (BulkCancelOrdersResult, error) {
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
	}
	if err := s.BindOrderBatch(ctx, actorID, "cancel", key, struct {
		IDs      []int64
		Versions map[int64]int64
	}{orderIDs, versions}); err != nil {
		return result, err
	}
	for _, orderID := range orderIDs {
		item := BulkCancelOrderItem{OrderID: orderID}
		cancelled, err := s.CancelOrder(ctx, actorID, orderID, true, fmt.Sprintf("%s:%d", key, orderID), versions[orderID])
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

func (s *Store) CancelOrder(ctx context.Context, actorID, orderID int64, admin bool, key string, expectedVersion ...int64) (CancelOrderResult, error) {
	var out CancelOrderResult
	if actorID < 1 || orderID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_order_cancel", "订单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if admin {
		if err = requireAdminOrdersManager(ctx, tx, actorID); err != nil {
			return out, err
		}
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "order", orderID); err != nil {
			return out, err
		}
	}
	var owner int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM orders WHERE id=$1", orderID).Scan(&owner); err != nil {
		return out, err
	}
	if !admin && owner != actorID {
		return out, pgx.ErrNoRows
	}
	hash, replay, err := orderOperationStart(ctx, tx, actorID, orderID, "cancel", key, expectedVersion, &out)
	if err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM invoices WHERE order_id=$1 FOR UPDATE", orderID); err != nil {
		return out, err
	}
	var status string
	var deleted bool
	if err = tx.QueryRow(ctx, "SELECT status,version,deleted_at IS NOT NULL FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&status, &out.Version, &deleted); err != nil {
		return out, err
	}
	if admin {
		if err = checkOrderVersion(out.Version, expectedVersion); err != nil {
			return out, err
		}
	} else if len(expectedVersion) > 0 {
		if err = checkOrderVersion(out.Version, expectedVersion); err != nil {
			return out, err
		}
	}
	if deleted {
		return out, conflict("order_deleted", "订单已删除")
	}
	out.OrderID = orderID
	if out.StockUnits, err = s.cancelUnpaidOrder(ctx, tx, actorID, orderID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT version FROM orders WHERE id=$1", orderID).Scan(&out.Version); err != nil {
		return out, err
	}
	out.Status = "cancelled"
	if err = saveOrderOperation(ctx, tx, actorID, orderID, "cancel", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) cancelUnpaidOrder(ctx context.Context, tx pgx.Tx, actorID, orderID int64) (int64, error) {
	var invoiceStatus string
	err := tx.QueryRow(ctx, "SELECT status FROM invoices WHERE order_id=$1 FOR UPDATE", orderID).Scan(&invoiceStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, err
	}
	var status string
	if err = tx.QueryRow(ctx, "SELECT status FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&status); err != nil {
		return 0, err
	}
	// Deferred admin orders have no invoice yet and cannot have payment intents.
	if invoiceStatus == "" {
		invoiceStatus = "unpaid"
	}
	var stockUnits int64
	if status != "pending" {
		return 0, conflict("order_not_cancellable", "只有待支付订单可以取消")
	}
	if invoiceStatus != "unpaid" {
		return 0, conflict("order_not_cancellable", "账单当前状态不可取消订单")
	}
	var anomaly bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_close_anomalies a JOIN invoices i ON i.id=a.invoice_id WHERE i.order_id=$1 AND payment_close_anomaly_unresolved(a.id))`, orderID).Scan(&anomaly); err != nil {
		return 0, err
	}
	if anomaly {
		return 0, conflict("payment_close_anomaly", "订单存在关闭后收款异常，需财务核验")
	}
	var pending bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE i.order_id=$1 AND p.status IN ('pending','expired') AND p.provider<>'mock') OR EXISTS(SELECT 1 FROM payment_close_operations c JOIN invoices i ON i.id=c.invoice_id WHERE i.order_id=$1 AND c.status IN ('checking','unknown'))", orderID).Scan(&pending); err != nil {
		return 0, err
	}
	if pending {
		return 0, conflict("invoice_payment_pending", "订单已有在线支付，请先完成原支付单")
	}
	rows, err := tx.Query(ctx, "SELECT id FROM services WHERE order_id=$1 ORDER BY id FOR UPDATE", orderID)
	if err != nil {
		return 0, err
	}
	for rows.Next() {
		var sid int64
		if err = rows.Scan(&sid); err != nil {
			rows.Close()
			return 0, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	var advanced bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE order_id=$1 AND status<>'pending_payment') OR EXISTS(SELECT 1 FROM provision_jobs WHERE service_id IN (SELECT id FROM services WHERE order_id=$1) AND status IN ('running','waiting_verification'))`, orderID).Scan(&advanced); err != nil {
		return 0, err
	}
	if advanced {
		return 0, conflict("order_resources_active", "订单资源已开始交付，请核实资源状态后处理")
	}
	if _, err = tx.Exec(ctx, "UPDATE orders SET status='cancelled',updated_at=now() WHERE id=$1", orderID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET status='cancelled' WHERE order_id=$1 AND status='unpaid'", orderID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='cancelled',completed_at=now(),updated_at=now(),last_error='订单取消'
WHERE service_id IN (SELECT id FROM services WHERE order_id=$1) AND status IN ('pending','running','waiting_manual')`, orderID); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(ctx, "UPDATE services SET status='cancelled',version=version+1,updated_at=now() WHERE order_id=$1 AND status='pending_payment'", orderID); err != nil {
		return 0, err
	}
	if err = tx.QueryRow(ctx, `WITH released AS (
UPDATE services SET inventory_released=true WHERE order_id=$1 AND NOT inventory_released RETURNING product_id
), counts AS (SELECT product_id,count(*)::bigint AS n FROM released GROUP BY product_id),
stock_updates AS (UPDATE products p SET stock=p.stock+c.n,updated_at=now() FROM counts c WHERE p.id=c.product_id AND p.stock IS NOT NULL RETURNING c.n)
SELECT coalesce(sum(n),0) FROM stock_updates`, orderID).Scan(&stockUnits); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			stockUnits = 0
		} else {
			return 0, err
		}
	}
	if err = Audit(ctx, tx, actorID, "order.cancelled", "order", orderID, map[string]any{"stock_units_released": stockUnits}); err != nil {
		return 0, err
	}
	var affiliateInvoiceID int64
	if err = tx.QueryRow(ctx, "SELECT coalesce((SELECT id FROM invoices WHERE order_id=$1),0)", orderID).Scan(&affiliateInvoiceID); err != nil {
		return 0, err
	}
	if affiliateInvoiceID > 0 {
		if err = s.ReverseAffiliateCommissions(ctx, tx, affiliateInvoiceID, actorID, "unpaid order cancelled"); err != nil {
			return 0, err
		}
	}

	return stockUnits, nil
}
