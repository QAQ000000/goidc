package store

import (
	"context"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"
)

type OrderReviewResult struct {
	OrderID        int64  `json:"order_id"`
	ReviewStatus   string `json:"review_status"`
	OrderStatus    string `json:"order_status"`
	QueuedServices int64  `json:"queued_services"`
	Version        int64  `json:"version"`
	Replayed       bool   `json:"replayed"`
}

func (s *Store) ReviewOrder(ctx context.Context, actorID, orderID int64, decision, note, key string, expectedVersion ...int64) (OrderReviewResult, error) {
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
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "order", orderID); err != nil {
		return out, err
	}
	if err = requireAdminOrdersManager(ctx, tx, actorID); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "order", orderID); err != nil {
		return out, err
	}
	hash, replay, err := orderOperationStart(ctx, tx, actorID, orderID, "review", key, struct {
		Decision, Note string
		Expected       []int64
	}{decision, note, expectedVersion}, &out)
	if err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	var currentReview, orderStatus, invoiceStatus string
	err = tx.QueryRow(ctx, "SELECT status FROM invoices WHERE order_id=$1 FOR UPDATE", orderID).Scan(&invoiceStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var held bool
	if err = tx.QueryRow(ctx, `SELECT external_refund_order_blocked($1)`, orderID).Scan(&held); err != nil {
		return out, err
	}
	if held {
		return out, orderRefundConflict("external_refund_order_hold")
	}
	var deleted bool
	if err = tx.QueryRow(ctx, "SELECT review_status,status,version,deleted_at IS NOT NULL FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&currentReview, &orderStatus, &out.Version, &deleted); err != nil {
		return out, err
	}
	if err = checkOrderVersion(out.Version, expectedVersion); err != nil {
		return out, err
	}
	if deleted {
		return out, conflict("order_deleted", "订单已删除")
	}
	out.OrderID, out.ReviewStatus, out.OrderStatus = orderID, currentReview, orderStatus
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
		return s.finishOrderReview(ctx, tx, actorID, orderID, key, hash, out)
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
	return s.finishOrderReview(ctx, tx, actorID, orderID, key, hash, out)
}

func (s *Store) finishOrderReview(ctx context.Context, tx pgx.Tx, actor, id int64, key, hash string, out OrderReviewResult) (OrderReviewResult, error) {
	if err := tx.QueryRow(ctx, "SELECT version FROM orders WHERE id=$1", id).Scan(&out.Version); err != nil {
		return out, err
	}
	if err := saveOrderOperation(ctx, tx, actor, id, "review", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
