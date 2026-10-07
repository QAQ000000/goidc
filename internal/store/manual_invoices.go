package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const manualInvoiceLimit int64 = 10000000000

type ManualInvoiceItem struct {
	Description string `json:"description"`
	AmountCents int64  `json:"amount_cents"`
}

type ManualInvoiceInput struct {
	UserID          int64               `json:"user_id"`
	DueAt           time.Time           `json:"due_at"`
	Items           []ManualInvoiceItem `json:"items"`
	ExpectedVersion int64               `json:"expected_version"`
}

type ManualInvoiceCancel struct {
	ExpectedVersion int64  `json:"expected_version"`
	Reason          string `json:"reason"`
}

type ManualInvoiceResult struct {
	InvoiceID   int64  `json:"invoice_id"`
	InvoiceNo   string `json:"invoice_no"`
	AmountCents int64  `json:"amount_cents"`
	Status      string `json:"status"`
	Version     int64  `json:"version"`
	Replayed    bool   `json:"replayed"`
}

type ManualInvoiceMergeSource struct {
	InvoiceID       int64 `json:"invoice_id"`
	ExpectedVersion int64 `json:"expected_version"`
}

type ManualInvoiceMergeInput struct {
	Sources []ManualInvoiceMergeSource `json:"sources"`
}

func (s *Store) MergeManualInvoices(ctx context.Context, actorID int64, in ManualInvoiceMergeInput, key string) (ManualInvoiceResult, error) {
	var out ManualInvoiceResult
	if len(in.Sources) < 2 || len(in.Sources) > 20 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_invoice_merge", "至少选择两张、最多二十张账单")
	}
	seen := map[int64]bool{}
	for _, source := range in.Sources {
		if source.InvoiceID < 1 || source.ExpectedVersion < 1 || seen[source.InvoiceID] {
			return out, conflict("invalid_invoice_merge", "账单编号或版本重复")
		}
		seen[source.InvoiceID] = true
	}
	tx, err := s.manualInvoiceTx(ctx, actorID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "manual-invoice-merge", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, err
	}
	var savedHash string
	var saved []byte
	canonical, err := json.Marshal(in)
	if err != nil {
		return out, err
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM manual_invoice_operations WHERE actor_id=$1 AND idempotency_key=$2", actorID, key).Scan(&savedHash, &saved)
	if err == nil {
		if savedHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同合并操作")
		}
		if err = json.Unmarshal(saved, &out); err != nil {
			return out, err
		}
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", out.InvoiceID); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var customerID int64
	var due time.Time
	var total int64
	items := make([]ManualInvoiceItem, 0, len(in.Sources))
	for index, source := range in.Sources {
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", source.InvoiceID); err != nil {
			return out, err
		}
		var owner, version int64
		var kind, status string
		if err = tx.QueryRow(ctx, "SELECT user_id,invoice_kind,status,version,due_at FROM invoices WHERE id=$1 FOR UPDATE", source.InvoiceID).Scan(&owner, &kind, &status, &version, &due); err != nil {
			return out, err
		}
		if index == 0 {
			customerID = owner
		} else if owner != customerID {
			return out, conflict("invoice_customer_mismatch", "只能合并同一客户的账单")
		}
		if kind != "manual" || status != "unpaid" || version != source.ExpectedVersion {
			return out, conflict("invoice_not_mergeable", "账单必须是当前版本的未支付人工账单")
		}
		if err = requireManualInvoiceValid(ctx, tx, source.InvoiceID); err != nil {
			return out, err
		}
		var pending bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired','succeeded'))", source.InvoiceID).Scan(&pending); err != nil {
			return out, err
		}
		if pending {
			return out, conflict("invoice_payment_pending", "已有支付意图的账单不可合并")
		}
		rows, queryErr := tx.Query(ctx, "SELECT description,amount_cents FROM invoice_items WHERE invoice_id=$1 ORDER BY id", source.InvoiceID)
		if queryErr != nil {
			return out, queryErr
		}
		for rows.Next() {
			var item ManualInvoiceItem
			if err = rows.Scan(&item.Description, &item.AmountCents); err != nil {
				rows.Close()
				return out, err
			}
			items = append(items, item)
			total += item.AmountCents
		}
		rows.Close()
	}
	if len(items) > 100 || total < 0 || total > manualInvoiceLimit {
		return out, conflict("invalid_invoice_merge", "合并后项目或金额超出范围")
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,invoice_kind,amount_cents,due_at) VALUES('M-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'manual',$2,$3) RETURNING id,invoice_no,status,amount_cents,version`, customerID, total, due).Scan(&out.InvoiceID, &out.InvoiceNo, &out.Status, &out.AmountCents, &out.Version); err != nil {
		return out, err
	}
	for _, item := range items {
		if _, err = tx.Exec(ctx, "INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,$2,'manual',$3)", out.InvoiceID, item.Description, item.AmountCents); err != nil {
			return out, err
		}
	}
	for _, source := range in.Sources {
		if _, err = tx.Exec(ctx, "UPDATE invoices SET status='cancelled' WHERE id=$1", source.InvoiceID); err != nil {
			return out, err
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO manual_invoice_operations(actor_id,idempotency_key,invoice_id,action,request_hash,result) VALUES($1,$2,$3,'merge',$4,$5)`, actorID, key, out.InvoiceID, hash, encoded); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "invoice.manual.merge", "invoice", out.InvoiceID, map[string]any{"source_invoice_ids": in.Sources, "user_id": customerID, "amount_cents": total}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func validateManualInvoice(in *ManualInvoiceInput) (int64, error) {
	if in.UserID < 1 || in.DueAt.IsZero() || in.DueAt.Year() < 2000 || in.DueAt.Year() > 2200 || len(in.Items) < 1 || len(in.Items) > 100 {
		return 0, conflict("invalid_invoice", "客户、到期时间或账单项目无效")
	}
	in.DueAt = in.DueAt.UTC().Truncate(time.Microsecond)
	var total int64
	for i := range in.Items {
		item := &in.Items[i]
		item.Description = strings.TrimSpace(item.Description)
		if item.Description == "" || !contentTextValid(item.Description, 500, true) || item.AmountCents < -manualInvoiceLimit || item.AmountCents > manualInvoiceLimit {
			return 0, conflict("invalid_invoice_item", "项目描述或金额无效")
		}
		total += item.AmountCents
	}
	if total < 0 || total > manualInvoiceLimit {
		return 0, conflict("invalid_invoice_total", "账单总额必须在 0 到 100000000 元之间")
	}
	return total, nil
}

func (s *Store) manualInvoiceTx(ctx context.Context, actorID int64) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108190)"); err == nil {
		var allowed bool
		err = tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active
 AND 'invoices.view'=ANY(r.permissions) AND 'invoices.manage'=ANY(r.permissions)),false)
 FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed)
		if err == nil && !allowed {
			err = conflict("permission_denied", "没有维护账单的权限")
		}
	}
	if err != nil {
		tx.Rollback(ctx)
		return nil, err
	}
	return tx, nil
}

func manualInvoiceReplay(ctx context.Context, tx pgx.Tx, actorID, invoiceID int64, action, key string, payload any) (ManualInvoiceResult, string, error) {
	var out ManualInvoiceResult
	if len(key) < 8 || len(key) > 128 {
		return out, "", conflict("invalid_idempotency_key", "幂等键长度必须在 8 到 128 之间")
	}
	if err := lockOperationKey(ctx, tx, "manual-invoice", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, "", err
	}
	canonical, err := json.Marshal(struct {
		InvoiceID int64  `json:"invoice_id"`
		Action    string `json:"action"`
		Payload   any    `json:"payload"`
	}{invoiceID, action, payload})
	if err != nil {
		return out, "", err
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	var savedHash string
	var saved []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM manual_invoice_operations WHERE actor_id=$1 AND idempotency_key=$2", actorID, key).Scan(&savedHash, &saved)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, hash, nil
	}
	if err != nil {
		return out, "", err
	}
	if savedHash != hash {
		return out, "", conflict("idempotency_conflict", "相同幂等键对应不同账单操作")
	}
	if err = json.Unmarshal(saved, &out); err != nil {
		return out, "", err
	}
	// Replays must respect the invoice's current customer scope, including transfers.
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", out.InvoiceID); err != nil {
		return out, "", err
	}
	out.Replayed = true
	return out, hash, nil
}

func finishManualInvoice(ctx context.Context, tx pgx.Tx, actorID int64, action, key, hash string, out ManualInvoiceResult, details map[string]any) (ManualInvoiceResult, error) {
	encoded, err := json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO manual_invoice_operations(actor_id,idempotency_key,invoice_id,action,request_hash,result)
 VALUES($1,$2,$3,$4,$5,$6)`, actorID, key, out.InvoiceID, action, hash, encoded); err != nil {
		return out, err
	}
	details["amount_cents"], details["version"] = out.AmountCents, out.Version
	if err = Audit(ctx, tx, actorID, "invoice.manual."+action, "invoice", out.InvoiceID, details); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) SaveManualInvoice(ctx context.Context, actorID, invoiceID int64, in ManualInvoiceInput, key string) (ManualInvoiceResult, error) {
	var out ManualInvoiceResult
	total, err := validateManualInvoice(&in)
	if err != nil {
		return out, err
	}
	if invoiceID < 0 || (invoiceID > 0 && in.ExpectedVersion < 1) || (invoiceID == 0 && in.ExpectedVersion != 0) {
		return out, conflict("invalid_invoice_version", "账单版本无效")
	}
	tx, err := s.manualInvoiceTx(ctx, actorID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	action := "create"
	if invoiceID > 0 {
		action = "update"
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
			return out, err
		}
	} else if err = LockAdminCustomer(ctx, tx, actorID, in.UserID); err != nil {
		return out, err
	}
	out, hash, err := manualInvoiceReplay(ctx, tx, actorID, invoiceID, action, key, in)
	if err != nil {
		return out, err
	}
	if out.Replayed {
		return out, tx.Commit(ctx)
	}
	if invoiceID == 0 {
		err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,invoice_kind,amount_cents,due_at)
 VALUES('M-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'manual',$2,$3)
 RETURNING id,invoice_no,status,amount_cents,version`, in.UserID, total, in.DueAt).
			Scan(&out.InvoiceID, &out.InvoiceNo, &out.Status, &out.AmountCents, &out.Version)
	} else {
		var owner int64
		var kind string
		err = tx.QueryRow(ctx, "SELECT id,invoice_no,status,amount_cents,version,user_id,invoice_kind FROM invoices WHERE id=$1 FOR UPDATE", invoiceID).
			Scan(&out.InvoiceID, &out.InvoiceNo, &out.Status, &out.AmountCents, &out.Version, &owner, &kind)
		if err != nil {
			return out, err
		}
		if owner != in.UserID {
			return out, conflict("invoice_owner_immutable", "账单客户不可变更")
		}
		if err = requireManualInvoiceEditable(ctx, tx, invoiceID, kind, out.Status, out.Version, in.ExpectedVersion); err != nil {
			return out, err
		}
		err = tx.QueryRow(ctx, "UPDATE invoices SET amount_cents=$2,due_at=$3 WHERE id=$1 RETURNING amount_cents,version", invoiceID, total, in.DueAt).
			Scan(&out.AmountCents, &out.Version)
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM invoice_items WHERE invoice_id=$1", invoiceID)
		}
	}
	if err != nil {
		return out, err
	}
	for _, item := range in.Items {
		if _, err = tx.Exec(ctx, "INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents) VALUES($1,$2,'manual',$3)", out.InvoiceID, item.Description, item.AmountCents); err != nil {
			return out, err
		}
	}
	return finishManualInvoice(ctx, tx, actorID, action, key, hash, out, map[string]any{"user_id": in.UserID, "due_at": in.DueAt, "items": in.Items})
}

func requireManualInvoiceEditable(ctx context.Context, tx pgx.Tx, invoiceID int64, kind, status string, version, expected int64) error {
	if kind != "manual" {
		return conflict("invoice_automatic", "订单、续费和升级账单需通过原业务流程处理")
	}
	if status != "unpaid" {
		return conflict("invoice_not_editable", "只有未付款的人工账单可维护")
	}
	if version != expected {
		return conflict("stale_invoice", "账单已更新，请刷新后操作")
	}
	if err := requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
		return err
	}
	var pending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired','succeeded'))", invoiceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("invoice_payment_pending", "账单已有待处理支付，不可编辑或作废")
	}
	return nil
}

func requireManualInvoiceValid(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT i.invoice_kind='manual' AND i.order_id IS NULL AND i.legacy_source='' AND i.legacy_id IS NULL AND i.amount_cents<=$2
 AND NOT i.import_review_required AND NOT EXISTS(SELECT 1 FROM service_configuration_changes WHERE invoice_id=i.id)
 AND (SELECT count(*) BETWEEN 1 AND 100 AND coalesce(sum(amount_cents)=i.amount_cents,false)
	 AND bool_and(item_type='manual' AND service_id IS NULL AND legacy_source='' AND legacy_id IS NULL
 AND amount_cents BETWEEN -$2 AND $2 AND char_length(trim(description)) BETWEEN 1 AND 500)
 FROM invoice_items WHERE invoice_id=i.id) FROM invoices i WHERE i.id=$1`, invoiceID, manualInvoiceLimit).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return conflict("invalid_manual_invoice", "人工账单项目或金额关系无效")
	}
	return nil
}

func (s *Store) CancelManualInvoice(ctx context.Context, actorID, invoiceID int64, in ManualInvoiceCancel, key string) (ManualInvoiceResult, error) {
	var out ManualInvoiceResult
	in.Reason = strings.TrimSpace(in.Reason)
	if invoiceID < 1 || in.ExpectedVersion < 1 || len(in.Reason) < 3 || !contentTextValid(in.Reason, 500, true) {
		return out, conflict("invalid_invoice_cancellation", "账单、版本或作废原因无效")
	}
	tx, err := s.manualInvoiceTx(ctx, actorID)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
		return out, err
	}
	out, hash, err := manualInvoiceReplay(ctx, tx, actorID, invoiceID, "cancel", key, in)
	if err != nil {
		return out, err
	}
	if out.Replayed {
		return out, tx.Commit(ctx)
	}
	var kind string
	if err = tx.QueryRow(ctx, "SELECT id,invoice_no,status,amount_cents,version,invoice_kind FROM invoices WHERE id=$1 FOR UPDATE", invoiceID).
		Scan(&out.InvoiceID, &out.InvoiceNo, &out.Status, &out.AmountCents, &out.Version, &kind); err != nil {
		return out, err
	}
	if err = requireManualInvoiceEditable(ctx, tx, invoiceID, kind, out.Status, out.Version, in.ExpectedVersion); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "UPDATE invoices SET status='cancelled' WHERE id=$1 RETURNING status,version", invoiceID).Scan(&out.Status, &out.Version); err != nil {
		return out, err
	}
	return finishManualInvoice(ctx, tx, actorID, "cancel", key, hash, out, map[string]any{"reason": in.Reason})
}
