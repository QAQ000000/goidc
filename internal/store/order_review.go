package store

import (
	"context"
	"fmt"
	"strings"
)

type OrderReviewResult struct {
	OrderID        int64  `json:"order_id"`
	ReviewStatus   string `json:"review_status"`
	OrderStatus    string `json:"order_status"`
	QueuedServices int64  `json:"queued_services"`
	Replayed       bool   `json:"replayed"`
}

func (s *Store) ReviewOrder(ctx context.Context, actorID, orderID int64, decision, note, key string) (OrderReviewResult, error) {
	var out OrderReviewResult
	decision = strings.ToLower(strings.TrimSpace(decision))
	note = strings.TrimSpace(note)
	if actorID < 1 || orderID < 1 || (decision != "approved" && decision != "rejected") || len(note) > 2000 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_order_review", "审核决定、备注或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "order-review", fmt.Sprintf("%d:%s", orderID, key)); err != nil {
		return out, err
	}
	var currentReview, orderStatus, invoiceStatus string
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "order", orderID); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT o.review_status,o.status,i.status
FROM orders o JOIN invoices i ON i.order_id=o.id WHERE o.id=$1 FOR UPDATE`, orderID).
		Scan(&currentReview, &orderStatus, &invoiceStatus); err != nil {
		return out, err
	}
	out.OrderID, out.ReviewStatus, out.OrderStatus = orderID, currentReview, orderStatus
	if currentReview == decision {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if currentReview != "pending" {
		return out, conflict("order_review_not_pending", "订单当前不需要审核")
	}
	if invoiceStatus != "paid" {
		return out, conflict("order_review_not_paid", "只有已支付订单可以审核")
	}
	if decision == "rejected" {
		if _, err = tx.Exec(ctx, "UPDATE orders SET review_status='rejected',notes=CASE WHEN $2='' THEN notes ELSE $2 END,updated_at=now() WHERE id=$1", orderID, note); err != nil {
			return out, err
		}
		if err = Audit(ctx, tx, actorID, "order.review.rejected", "order", orderID, map[string]any{"note": note}); err != nil {
			return out, err
		}
		out.ReviewStatus = "rejected"
		return out, tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, "UPDATE orders SET review_status='approved',status='active',notes=CASE WHEN $2='' THEN notes ELSE $2 END,updated_at=now() WHERE id=$1", orderID, note); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `WITH queued AS (
UPDATE services SET status='queued',version=version+1,updated_at=now()
WHERE order_id=$1 AND status='pending_payment' RETURNING id
), inserted AS (
INSERT INTO provision_jobs(service_id,action,idempotency_key)
SELECT id,'create','service-create-'||id FROM queued ON CONFLICT(idempotency_key) DO NOTHING
RETURNING service_id
)
SELECT count(*) FROM inserted`, orderID).Scan(&out.QueuedServices); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "order.review.approved", "order", orderID, map[string]any{"note": note, "queued_services": out.QueuedServices}); err != nil {
		return out, err
	}
	out.ReviewStatus, out.OrderStatus = "approved", "active"
	return out, tx.Commit(ctx)
}
