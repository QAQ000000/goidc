package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

func orderOperationStart(ctx context.Context, tx pgx.Tx, actor, orderID int64, action, key string, payload any, result any) (string, bool, error) {
	if len(key) < 8 || len(key) > 128 {
		return "", false, conflict("invalid_order_operation", "请提供8至128字符的幂等键")
	}
	if err := lockOperationKey(ctx, tx, "order-"+action, fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return "", false, err
	}
	body, err := json.Marshal(struct {
		OrderID int64
		Payload any
	}{orderID, payload})
	if err != nil {
		return "", false, err
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	var previous string
	var savedID int64
	var raw []byte
	err = tx.QueryRow(ctx, "SELECT order_id,request_hash,result FROM order_operation_receipts WHERE actor_id=$1 AND action=$2 AND idempotency_key=$3", actor, action, key).Scan(&savedID, &previous, &raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return hash, false, nil
	}
	if err != nil {
		return "", false, err
	}
	if previous != hash || savedID != orderID {
		return "", false, conflict("idempotency_conflict", "相同幂等键对应不同订单操作")
	}
	return hash, true, json.Unmarshal(raw, result)
}
func saveOrderOperation(ctx context.Context, tx pgx.Tx, actor, id int64, action, key, hash string, result any) error {
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO order_operation_receipts(actor_id,action,idempotency_key,order_id,request_hash,result) VALUES($1,$2,$3,$4,$5,$6)", actor, action, key, id, hash, raw)
	return err
}

// BindOrderBatch prevents reusing one batch key with a different selection or
// decision. Individual order receipts still make retries safe after a partial
// response or network interruption.
func (s *Store) BindOrderBatch(ctx context.Context, actor int64, action, key string, payload any) error {
	if actor < 1 || len(key) < 8 || len(key) > 100 {
		return conflict("invalid_bulk_order_operation", "幂等键无效")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(body)
	hash := hex.EncodeToString(digest[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_batch_requests(actor_id,action,idempotency_key,request_hash) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, actor, action, key, hash); err != nil {
		return err
	}
	var stored string
	if err = tx.QueryRow(ctx, `SELECT request_hash FROM order_batch_requests WHERE actor_id=$1 AND action=$2 AND idempotency_key=$3`, actor, action, key).Scan(&stored); err != nil {
		return err
	}
	if stored != hash {
		return conflict("idempotency_conflict", "相同幂等键对应不同批量订单操作")
	}
	return tx.Commit(ctx)
}
func checkOrderVersion(version int64, expected []int64) error {
	if len(expected) != 1 || expected[0] < 1 {
		return conflict("order_version_required", "请提交当前订单版本")
	}
	if version != expected[0] {
		return conflict("stale_order", "订单已变化，请刷新后再操作")
	}
	return nil
}

type OrderMutationResult struct {
	OrderID  int64  `json:"order_id"`
	Version  int64  `json:"version"`
	Notes    string `json:"notes,omitempty"`
	Deleted  bool   `json:"deleted,omitempty"`
	Replayed bool   `json:"replayed"`
}

func (s *Store) UpdateOrderNotes(ctx context.Context, actor, id, expected int64, notes, key string) (OrderMutationResult, error) {
	var out OrderMutationResult
	notes = strings.TrimSpace(notes)
	if len(notes) > 5000 {
		return out, conflict("invalid_order_notes", "订单备注过长")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "order", id); err != nil {
		return out, err
	}
	in := struct {
		Expected int64
		Notes    string
	}{expected, notes}
	hash, replay, err := orderOperationStart(ctx, tx, actor, id, "notes", key, in, &out)
	if err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	var version int64
	var deleted bool
	var previous string
	if err = tx.QueryRow(ctx, "SELECT version,deleted_at IS NOT NULL,notes FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&version, &deleted, &previous); err != nil {
		return out, err
	}
	if err = checkOrderVersion(version, []int64{expected}); err != nil {
		return out, err
	}
	if deleted {
		return out, conflict("order_deleted", "已删除订单不能修改")
	}
	if err = tx.QueryRow(ctx, "UPDATE orders SET notes=$2 WHERE id=$1 RETURNING version", id, notes).Scan(&out.Version); err != nil {
		return out, err
	}
	out.OrderID, out.Notes = id, notes
	if err = Audit(ctx, tx, actor, "order.notes.updated", "order", id, map[string]any{"previous": previous, "notes": notes, "expected_version": expected}); err != nil {
		return out, err
	}
	if err = saveOrderOperation(ctx, tx, actor, id, "notes", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Deletion removes an order from normal lists, while retaining the immutable
// financial and resource history. Pending inventory release is atomic with it.
func (s *Store) DeleteOrder(ctx context.Context, actor, id, expected int64, reason, key string) (OrderMutationResult, error) {
	var out OrderMutationResult
	reason = strings.TrimSpace(reason)
	if len([]rune(reason)) < 3 || len([]rune(reason)) > 500 {
		return out, conflict("invalid_order_deletion", "请填写3至500字删除原因")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireAdminOrdersManager(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "order", id); err != nil {
		return out, err
	}
	hash, replay, err := orderOperationStart(ctx, tx, actor, id, "delete", key, struct {
		Expected int64
		Reason   string
	}{expected, reason}, &out)
	if err != nil {
		return out, err
	}
	if replay {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	var invoiceStatus string
	if err = tx.QueryRow(ctx, "SELECT status FROM invoices WHERE order_id=$1 FOR UPDATE", id).Scan(&invoiceStatus); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var version int64
	var status string
	var deleted bool
	if err = tx.QueryRow(ctx, "SELECT version,status,deleted_at IS NOT NULL FROM orders WHERE id=$1 FOR UPDATE", id).Scan(&version, &status, &deleted); err != nil {
		return out, err
	}
	if err = checkOrderVersion(version, []int64{expected}); err != nil {
		return out, err
	}
	if deleted {
		return out, conflict("order_deleted", "订单已删除，请刷新列表")
	}
	if status == "pending" {
		if _, err = s.cancelUnpaidOrder(ctx, tx, actor, id); err != nil {
			return out, err
		}
		status, invoiceStatus = "cancelled", "cancelled"
	}
	if (status != "cancelled" && status != "refunded") || (invoiceStatus != "" && invoiceStatus != "cancelled" && invoiceStatus != "refunded") {
		return out, conflict("order_not_deletable", "请先取消或退款并终止资源，再删除订单")
	}
	// Lock the services before checking resource/job state, preventing a worker
	// from beginning delivery while the deletion decision is made.
	rows, err := tx.Query(ctx, "SELECT id FROM services WHERE order_id=$1 ORDER BY id FOR UPDATE", id)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var sid int64
		if err = rows.Scan(&sid); err != nil {
			rows.Close()
			return out, err
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return out, err
	}
	var live bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services WHERE order_id=$1 AND (status NOT IN ('cancelled','terminated') OR NOT inventory_released)) OR EXISTS(SELECT 1 FROM provision_jobs WHERE service_id IN(SELECT id FROM services WHERE order_id=$1) AND status IN ('pending','running','waiting_manual','waiting_verification')) OR EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id IN(SELECT id FROM invoices WHERE order_id=$1) AND provider<>'mock' AND status IN ('pending','expired')) OR EXISTS(SELECT 1 FROM payment_close_anomalies WHERE invoice_id IN(SELECT id FROM invoices WHERE order_id=$1) AND payment_close_anomaly_unresolved(id)) OR EXISTS(SELECT 1 FROM payment_close_operations WHERE invoice_id IN(SELECT id FROM invoices WHERE order_id=$1) AND status IN ('checking','unknown'))`, id).Scan(&live); err != nil {
		return out, err
	}
	if live {
		return out, conflict("order_resources_active", "订单仍有资源、库存或支付处理中，请完成后再删除")
	}
	if err = tx.QueryRow(ctx, "UPDATE orders SET deleted_at=now(),deleted_by=$2,deletion_reason=$3 WHERE id=$1 RETURNING version", id, actor, reason).Scan(&out.Version); err != nil {
		return out, err
	}
	out.OrderID, out.Deleted = id, true
	if err = Audit(ctx, tx, actor, "order.deleted", "order", id, map[string]any{"reason": reason, "expected_version": expected, "financial_records_retained": true}); err != nil {
		return out, err
	}
	if err = saveOrderOperation(ctx, tx, actor, id, "delete", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
