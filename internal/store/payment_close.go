package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

type PaymentCloseOperation struct {
	ID, PaymentID, InvoiceID, UserID, ActorID, AmountCents, ExpectedVersion int64
	Provider, ProviderPaymentID, OperationID, Status, ResultCode            string
	ExternalTransactionID                                                   string
	Version                                                                 int64
	Replayed                                                                bool
	Resolution                                                              *PaymentCloseResolution
}

func closeOperationHash(invoiceID, paymentID, expectedVersion int64) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("invoice=%d\x00payment=%d\x00version=%d", invoiceID, paymentID, expectedVersion)))
	return hex.EncodeToString(sum[:])
}

func newCloseOperationID() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "close_" + hex.EncodeToString(raw), nil
}

func authorizePaymentClose(ctx context.Context, tx pgx.Tx, actorID int64, sessionHash string, invoiceID int64, required ...string) error {
	permission := "payments.close"
	if len(required) > 0 {
		permission = required[0]
	}
	if strings.TrimSpace(sessionHash) == "" {
		return conflict("unauthorized", "关单操作需要有效浏览器会话")
	}
	if err := LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
		return err
	}
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "没有支付关单权限")
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", sessionHash, actorID).Scan(&sessionUser); err != nil {
		return conflict("unauthorized", "登录已失效")
	}
	if role == "super_admin" {
		return LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID)
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有支付关单权限")
	}
	var active bool
	var permissions []string
	if err := tx.QueryRow(ctx, "SELECT active,permissions FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&active, &permissions); err != nil || !active || (!slices.Contains(permissions, permission) || !slices.Contains(permissions, "invoices.view")) {
		return conflict("permission_denied", "没有支付关单权限")
	}
	// Employee scope may have changed while the employee row lock was awaited.
	return LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID)
}

func scanPaymentCloseOperation(row pgx.Row, out *PaymentCloseOperation) error {
	return row.Scan(&out.ID, &out.PaymentID, &out.InvoiceID, &out.UserID, &out.ActorID, &out.Provider, &out.ProviderPaymentID, &out.AmountCents, &out.ExpectedVersion, &out.OperationID, &out.Status, &out.ResultCode, &out.ExternalTransactionID)
}

const paymentCloseOperationColumns = `id,payment_id,invoice_id,user_id,actor_id,provider,provider_payment_id,amount_cents,expected_version,operation_id,status,result_code,external_transaction_id`

// BeginPaymentClose reserves one final-close attempt. The provider is called
// after commit so no database lock is held across the network request.
func (s *Store) BeginPaymentClose(ctx context.Context, actorID, invoiceID, paymentID, expectedVersion int64, key, sessionHash string) (PaymentCloseOperation, error) {
	var out PaymentCloseOperation
	if actorID < 1 || invoiceID < 1 || paymentID < 1 || expectedVersion < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_payment_close", "关单参数或幂等键无效")
	}
	hash := closeOperationHash(invoiceID, paymentID, expectedVersion)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, actorID, sessionHash, invoiceID); err != nil {
		return out, err
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "payment-close", fmt.Sprintf("%d:%s", actorID, key)); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT `+paymentCloseOperationColumns+` FROM payment_close_operations WHERE actor_id=$1 AND idempotency_key=$2`, actorID, key).Scan(&out.ID, &out.PaymentID, &out.InvoiceID, &out.UserID, &out.ActorID, &out.Provider, &out.ProviderPaymentID, &out.AmountCents, &out.ExpectedVersion, &out.OperationID, &out.Status, &out.ResultCode, &out.ExternalTransactionID); err == nil {
		var saved string
		if err = tx.QueryRow(ctx, "SELECT request_hash FROM payment_close_operations WHERE id=$1", out.ID).Scan(&saved); err != nil {
			return out, err
		}
		if saved != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同关单请求")
		}
		if out.Status == "closed" {
			out.Version = out.ExpectedVersion + 1
		}
		out.Resolution, err = paymentCloseResolution(ctx, tx, out.PaymentID, out.ID)
		if err != nil {
			return out, err
		}
		if out.Resolution != nil {
			out.Status = out.Resolution.PaymentStatus
			out.Version = out.Resolution.Version
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var paymentStatus string
	var currentVersion int64
	if err = tx.QueryRow(ctx, `SELECT user_id,provider,provider_payment_id,amount_cents,status FROM payment_transactions WHERE id=$1 AND invoice_id=$2 FOR UPDATE`, paymentID, invoiceID).
		Scan(&out.UserID, &out.Provider, &out.ProviderPaymentID, &out.AmountCents, &paymentStatus); err != nil {
		return out, err
	}
	var invoiceStatus string
	if err = tx.QueryRow(ctx, "SELECT status,version FROM invoices WHERE id=$1 AND user_id=$2 FOR UPDATE", invoiceID, out.UserID).Scan(&invoiceStatus, &currentVersion); err != nil {
		return out, err
	}
	if currentVersion != expectedVersion {
		return out, conflict("stale_invoice", "账单已更新，请刷新后再关单")
	}
	out.ExpectedVersion = expectedVersion
	out.Status = paymentStatus
	if err = requireNoPaymentCloseAnomaly(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if invoiceStatus != "unpaid" {
		return out, conflict("invoice_not_unpaid", "只有未付款账单可以关单")
	}
	if out.Status != "pending" && out.Status != "expired" {
		return out, conflict("payment_not_closable", "支付单当前状态不可关单")
	}
	var busy bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_reconciliation_checks WHERE payment_id=$1 AND status='checking') OR EXISTS(SELECT 1 FROM payment_close_operations WHERE payment_id=$1 AND payment_close_operation_unresolved(id))`, paymentID).Scan(&busy); err != nil {
		return out, err
	}
	if busy {
		return out, conflict("payment_close_in_progress", "支付单已有核验或关单操作处理中")
	}
	operationID, err := newCloseOperationID()
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO payment_close_operations(payment_id,invoice_id,user_id,actor_id,provider,provider_payment_id,amount_cents,expected_version,idempotency_key,request_hash,operation_id,channel_instance_id,credential_sha256)
SELECT $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,COALESCE(channel_instance_id,''),encode(sha256(credential_snapshot),'hex') FROM payment_transactions WHERE id=$1 RETURNING `+paymentCloseOperationColumns, paymentID, invoiceID, out.UserID, actorID, out.Provider, out.ProviderPaymentID, out.AmountCents, expectedVersion, key, hash, operationID).
		Scan(&out.ID, &out.PaymentID, &out.InvoiceID, &out.UserID, &out.ActorID, &out.Provider, &out.ProviderPaymentID, &out.AmountCents, &out.ExpectedVersion, &out.OperationID, &out.Status, &out.ResultCode, &out.ExternalTransactionID)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "payment.intent.close_requested", "invoice", invoiceID, map[string]any{"payment_id": paymentID, "operation_id": operationID}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// CompletePaymentClose commits only a provider-confirmed final closed result.
// A provider-reported success is retained as a non-unlock outcome so the
// normal signed callback/query path can settle the payment.
func (s *Store) CompletePaymentClose(ctx context.Context, op PaymentCloseOperation, result payment.CloseResult, sessionHash string) (PaymentCloseOperation, error) {
	if result.ProviderPaymentID != op.ProviderPaymentID || result.OperationID != op.OperationID || result.AmountCents != op.AmountCents || len(result.ExternalTransactionID) > 160 || (result.Status != "closed" && result.Status != "succeeded") {
		return op, conflict("invalid_payment_close", "关单结果不匹配")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return op, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, op.ActorID, sessionHash, op.InvoiceID); err != nil {
		return op, err
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, op.InvoiceID); err != nil {
		return op, err
	}
	// Payment -> invoice -> operation matches callback and reservation ordering.
	var paymentStatus, invoiceStatus string
	var invoiceVersion int64
	if err = tx.QueryRow(ctx, `SELECT status FROM payment_transactions WHERE id=$1 AND invoice_id=$2 AND user_id=$3 AND provider=$4 AND provider_payment_id=$5 AND amount_cents=$6 FOR UPDATE`, op.PaymentID, op.InvoiceID, op.UserID, op.Provider, op.ProviderPaymentID, op.AmountCents).Scan(&paymentStatus); err != nil {
		return op, err
	}
	if err = tx.QueryRow(ctx, `SELECT status,version FROM invoices WHERE id=$1 AND user_id=$2 FOR UPDATE`, op.InvoiceID, op.UserID).Scan(&invoiceStatus, &invoiceVersion); err != nil {
		return op, err
	}
	var stored PaymentCloseOperation
	if err = scanPaymentCloseOperation(tx.QueryRow(ctx, "SELECT "+paymentCloseOperationColumns+" FROM payment_close_operations WHERE id=$1 AND actor_id=$2 FOR UPDATE", op.ID, op.ActorID), &stored); err != nil {
		return op, err
	}
	var leaseValid bool
	if err = tx.QueryRow(ctx, `SELECT lease_until>clock_timestamp() FROM payment_close_operations WHERE id=$1`, op.ID).Scan(&leaseValid); err != nil {
		return op, err
	}
	if stored.Status != "checking" || !leaseValid || stored.OperationID != op.OperationID || stored.PaymentID != op.PaymentID || stored.InvoiceID != op.InvoiceID || stored.UserID != op.UserID || stored.Provider != op.Provider || stored.ProviderPaymentID != op.ProviderPaymentID || stored.AmountCents != op.AmountCents {
		return op, conflict("payment_close_expired", "关单操作已失效，原支付单继续冻结")
	}
	var credentialsUnchanged bool
	if err = tx.QueryRow(ctx, `SELECT c.credential_sha256=encode(sha256(p.credential_snapshot),'hex') AND c.channel_instance_id=COALESCE(p.channel_instance_id,'') FROM payment_close_operations c JOIN payment_transactions p ON p.id=c.payment_id WHERE c.id=$1`, op.ID).Scan(&credentialsUnchanged); err != nil {
		return op, err
	}
	if !credentialsUnchanged {
		return op, conflict("payment_credentials_changed", "原支付凭据快照变化，必须重新核验")
	}
	stored.Version = invoiceVersion
	if err = requireNoPaymentCloseAnomaly(ctx, tx, op.InvoiceID); err != nil {
		return op, err
	}
	if paymentStatus == "succeeded" {
		if err = finishPaymentCloseOutcome(ctx, tx, stored, "succeeded", "already_paid", result, "支付已成功收款，不能关单"); err != nil {
			return op, err
		}
		if err = tx.Commit(ctx); err != nil {
			return op, err
		}
		return stored, conflict("payment_already_succeeded", "支付已成功收款，不能关单")
	}
	if paymentStatus == "closed" {
		if err = finishPaymentCloseOutcome(ctx, tx, stored, "error", "already_closed", result, "支付单已经关闭"); err != nil {
			return op, err
		}
		if err = tx.Commit(ctx); err != nil {
			return op, err
		}
		return stored, conflict("payment_already_closed", "支付单已经关闭")
	}
	if paymentStatus != "pending" && paymentStatus != "expired" || invoiceStatus != "unpaid" || invoiceVersion != stored.ExpectedVersion {
		if err = finishPaymentCloseOutcome(ctx, tx, stored, "unknown", "state_changed", result, "账单或支付状态已变化"); err != nil {
			return op, err
		}
		if err = tx.Commit(ctx); err != nil {
			return op, err
		}
		return stored, conflict("payment_state_conflict", "账单或支付状态已变化，请刷新后重试")
	}
	if result.Status == "succeeded" {
		if err = finishPaymentCloseOutcome(ctx, tx, stored, "unknown", "provider_paid", result, "渠道已报告付款成功，请执行查单或等待回调"); err != nil {
			return op, err
		}
		if err = tx.Commit(ctx); err != nil {
			return op, err
		}
		return stored, conflict("payment_close_paid", "渠道已报告付款成功，未解除付款保护")
	}
	resultJSON, _ := json.Marshal(map[string]any{"status": "closed", "operation_id": result.OperationID, "external_transaction_id": result.ExternalTransactionID})
	if _, err = tx.Exec(ctx, `UPDATE payment_transactions SET status='closed',completed_at=clock_timestamp(),next_reconcile_at=NULL,updated_at=clock_timestamp(),raw_result=$2 WHERE id=$1`, op.PaymentID, resultJSON); err != nil {
		return op, err
	}
	if err = tx.QueryRow(ctx, `UPDATE invoices SET updated_at=clock_timestamp() WHERE id=$1 RETURNING version`, op.InvoiceID).Scan(&stored.Version); err != nil {
		return op, err
	}
	if err = finishPaymentCloseOutcome(ctx, tx, stored, "closed", "closed", result, ""); err != nil {
		return op, err
	}
	if err = Audit(ctx, tx, op.ActorID, "payment.intent.closed", "invoice", op.InvoiceID, map[string]any{"payment_id": op.PaymentID, "operation_id": op.OperationID}); err != nil {
		return op, err
	}
	stored.Status, stored.ResultCode, stored.ExternalTransactionID = "closed", "closed", result.ExternalTransactionID
	return stored, tx.Commit(ctx)
}

func finishPaymentCloseOutcome(ctx context.Context, tx pgx.Tx, op PaymentCloseOperation, status, code string, result payment.CloseResult, _ string) error {
	resultJSON, _ := json.Marshal(map[string]any{"status": result.Status, "operation_id": result.OperationID, "amount_cents": result.AmountCents, "external_transaction_id": result.ExternalTransactionID})
	_, err := tx.Exec(ctx, `UPDATE payment_close_operations SET status=$2,result_code=$3,external_transaction_id=$4,result=$5,completed_at=clock_timestamp(),updated_at=clock_timestamp(),lease_until=clock_timestamp() WHERE id=$1 AND status='checking'`, op.ID, status, code, result.ExternalTransactionID, resultJSON)
	if err != nil {
		return err
	}
	return Audit(ctx, tx, op.ActorID, "payment.intent.close_result", "invoice", op.InvoiceID, map[string]any{"payment_id": op.PaymentID, "operation_id": op.OperationID, "status": status, "code": code})
}

// FailPaymentClose records a fixed, sanitized outcome. It never changes the
// payment state; unknown provider outcomes therefore remain frozen.
func (s *Store) FailPaymentClose(ctx context.Context, op PaymentCloseOperation, code string) error {
	if !slices.Contains([]string{"provider_unavailable", "provider_invalid", "unknown", "authorization_revoked", "settlement_failed"}, code) {
		return fmt.Errorf("invalid payment close failure code")
	}
	status := "error"
	if code != "provider_unavailable" {
		status = "unknown"
	}
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		resultJSON, _ := json.Marshal(map[string]any{"status": status, "code": code})
		result, err := tx.Exec(ctx, `UPDATE payment_close_operations SET status=$2,result_code=$3,result=$4,completed_at=clock_timestamp(),updated_at=clock_timestamp(),lease_until=clock_timestamp() WHERE id=$1 AND status='checking' AND lease_until>clock_timestamp()`, op.ID, status, code, resultJSON)
		if err != nil {
			return err
		}
		if result.RowsAffected() == 1 {
			if err = Audit(ctx, tx, op.ActorID, "payment.intent.close_failed", "invoice", op.InvoiceID, map[string]any{"payment_id": op.PaymentID, "operation_id": op.OperationID, "code": code}); err != nil {
				return err
			}
		}
		return nil
	})
}
