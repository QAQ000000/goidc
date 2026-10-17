package store

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

type PaymentCheck struct {
	ID, PaymentID, InvoiceID, UserID, ActorID, AmountCents int64
	Provider, ProviderPaymentID                            string
	Admin                                                  bool
	automatic                                              bool
}

func authorizePaymentCheck(ctx context.Context, tx pgx.Tx, check *PaymentCheck) error {
	if check.automatic {
		var valid bool
		err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_reconciliation_checks c
 JOIN payment_transactions p ON p.id=c.payment_id JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id
 WHERE c.id=$1 AND c.payment_id=$2 AND c.source='worker' AND c.actor_id IS NULL
 AND c.status='checking' AND c.lease_until>clock_timestamp()
 AND p.invoice_id=$3 AND p.user_id=$4 AND p.provider=$5 AND p.provider_payment_id=$6 AND p.amount_cents=$7)`,
			check.ID, check.PaymentID, check.InvoiceID, check.UserID, check.Provider, check.ProviderPaymentID, check.AmountCents).Scan(&valid)
		if err != nil {
			return err
		}
		if !valid {
			return conflict("payment_check_expired", "自动支付查询租约已失效")
		}
		return nil
	}
	if check.Admin {
		if err := LockAdminResourceCustomer(ctx, tx, check.ActorID, "invoice", check.InvoiceID); err != nil {
			return err
		}
	} else if check.ActorID != check.UserID {
		return pgx.ErrNoRows
	}
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", check.ActorID).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "账户不可执行支付查询")
	}
	if !check.Admin || role == "super_admin" {
		return nil
	}
	var active bool
	var permissions []string
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "缺少支付查询权限")
	}
	if err := tx.QueryRow(ctx, "SELECT active,permissions FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&active, &permissions); err != nil || !active || !slices.Contains(permissions, "payments.reconcile") {
		return conflict("permission_denied", "缺少支付查询权限")
	}
	return nil
}

func (s *Store) BeginPaymentCheck(ctx context.Context, actorID, invoiceID, paymentID int64, admin bool) (PaymentCheck, error) {
	check := PaymentCheck{ActorID: actorID, InvoiceID: invoiceID, PaymentID: paymentID, Admin: admin}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return check, err
	}
	defer tx.Rollback(ctx)
	if err = lockPaymentConfiguration(ctx, tx); err != nil {
		return check, err
	}
	if err = tx.QueryRow(ctx, `SELECT p.user_id,p.provider,p.provider_payment_id,p.amount_cents FROM payment_transactions p
 JOIN invoices i ON i.id=p.invoice_id AND i.user_id=p.user_id WHERE p.id=$1 AND p.invoice_id=$2 AND ($3::boolean OR p.user_id=$4)`, paymentID, invoiceID, admin, actorID).Scan(&check.UserID, &check.Provider, &check.ProviderPaymentID, &check.AmountCents); err != nil {
		return check, err
	}
	if err = authorizePaymentCheck(ctx, tx, &check); err != nil {
		return check, err
	}
	var state string
	if err = tx.QueryRow(ctx, "SELECT status FROM payment_transactions WHERE id=$1 FOR UPDATE", paymentID).Scan(&state); err != nil {
		return check, err
	}
	var closing bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_close_operations WHERE payment_id=$1 AND status='checking' AND lease_until>clock_timestamp())`, paymentID).Scan(&closing); err != nil {
		return check, err
	}
	if closing {
		return check, conflict("payment_close_in_progress", "支付单正在核验关单，请稍后重试")
	}
	if (state != "pending" && state != "expired") || !strings.HasPrefix(check.Provider, "epay_") {
		return check, conflict("payment_not_queryable", "只有待处理的在线支付可以查询")
	}
	if _, err = tx.Exec(ctx, "UPDATE payment_reconciliation_checks SET status='error',result_code='lease_expired',completed_at=clock_timestamp() WHERE payment_id=$1 AND status='checking' AND lease_until<=clock_timestamp()", paymentID); err != nil {
		return check, err
	}
	var limited bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_reconciliation_checks WHERE payment_id=$1 AND (status='checking' OR started_at>clock_timestamp()-interval '30 seconds'))", paymentID).Scan(&limited); err != nil {
		return check, err
	}
	if limited {
		return check, conflict("payment_check_cooldown", "查询处理中或刚刚完成，请30秒后重试")
	}
	if err = tx.QueryRow(ctx, "INSERT INTO payment_reconciliation_checks(payment_id,actor_id) VALUES($1,$2) RETURNING id", paymentID, actorID).Scan(&check.ID); err != nil {
		return check, err
	}
	if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET auto_reconcile_attempts=0,next_reconcile_at=clock_timestamp()+interval '2 minutes' WHERE id=$1", paymentID); err != nil {
		return check, err
	}
	if err = Audit(ctx, tx, actorID, "payment.reconciliation.requested", "invoice", invoiceID, map[string]any{"payment_id": paymentID, "check_id": check.ID}); err != nil {
		return check, err
	}
	return check, tx.Commit(ctx)
}

func finishPaymentCheck(ctx context.Context, tx pgx.Tx, check *PaymentCheck, status string) error {
	if err := finishPaymentCheckInTx(ctx, tx, check, status); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func finishPaymentCheckInTx(ctx context.Context, tx pgx.Tx, check *PaymentCheck, status string) error {
	if check == nil {
		return nil
	}
	// A replay may reach this point without having locked the payment yet.
	// Keep payment-before-check ordering consistent with reservations.
	var paymentID int64
	if err := tx.QueryRow(ctx, "SELECT id FROM payment_transactions WHERE id=$1 FOR UPDATE", check.PaymentID).Scan(&paymentID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, "UPDATE payment_reconciliation_checks SET status=$2,result_code='verified',completed_at=clock_timestamp() WHERE id=$1 AND payment_id=$3 AND actor_id IS NOT DISTINCT FROM nullif($4::bigint,0) AND source=$5 AND status='checking' AND lease_until>clock_timestamp()", check.ID, status, check.PaymentID, check.ActorID, check.source())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return conflict("payment_check_expired", "支付查询已过期，请重新查询")
	}
	if status == "succeeded" {
		if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET next_reconcile_at=NULL WHERE id=$1", check.PaymentID); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, check.ActorID, "payment.reconciliation.completed", "invoice", check.InvoiceID, map[string]any{"payment_id": check.PaymentID, "check_id": check.ID, "status": status, "source": check.source()}); err != nil {
		return err
	}
	return nil
}

func (s *Store) CompletePaymentCheck(ctx context.Context, check PaymentCheck, result payment.QueryResult) (WebhookResult, error) {
	if result.ProviderPaymentID != check.ProviderPaymentID || result.AmountCents != check.AmountCents || (result.Status != "pending" && result.Status != "succeeded") || result.ExternalTransactionID == "" {
		return WebhookResult{}, conflict("invalid_payment_query", "支付查询结果不匹配")
	}
	if result.Status == "succeeded" {
		raw, _ := json.Marshal(result)
		return s.processPaymentWebhook(ctx, check.Provider, "query:"+result.ExternalTransactionID, check.ProviderPaymentID, check.InvoiceID, result.AmountCents, "succeeded", raw, []string{result.ExternalTransactionID}, &check)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return WebhookResult{}, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentCheck(ctx, tx, &check); err != nil {
		return WebhookResult{}, err
	}
	var state, external string
	if err = tx.QueryRow(ctx, "SELECT status,external_transaction_id FROM payment_transactions WHERE id=$1 FOR UPDATE", check.PaymentID).Scan(&state, &external); err != nil {
		return WebhookResult{}, err
	}
	// A callback may have committed while the remote query was in flight.
	if state == "succeeded" && external != result.ExternalTransactionID {
		return WebhookResult{}, conflict("payment_transaction_mismatch", "渠道流水号不一致")
	}
	if state != "pending" && state != "expired" && state != "succeeded" {
		return WebhookResult{}, conflict("payment_state_conflict", "支付状态已改变，请刷新")
	}
	out := WebhookResult{Provider: check.Provider, ProviderPaymentID: check.ProviderPaymentID, InvoiceID: check.InvoiceID, Status: state}
	checkStatus := state
	if checkStatus == "expired" {
		checkStatus = "pending"
	}
	return out, finishPaymentCheck(ctx, tx, &check, checkStatus)
}

func (s *Store) FailPaymentCheck(ctx context.Context, check PaymentCheck, code string) error {
	// Only internal fixed codes are accepted. Never persist provider error text.
	if !slices.Contains([]string{"provider_unavailable", "provider_invalid", "settlement_failed"}, code) {
		return fmt.Errorf("invalid payment check failure code")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	result, err := tx.Exec(ctx, "UPDATE payment_reconciliation_checks SET status='error',result_code=$2,completed_at=clock_timestamp() WHERE id=$1 AND payment_id=$3 AND actor_id IS NOT DISTINCT FROM nullif($4::bigint,0) AND source=$5 AND status='checking'", check.ID, code, check.PaymentID, check.ActorID, check.source())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		if err = Audit(ctx, tx, check.ActorID, "payment.reconciliation.failed", "invoice", check.InvoiceID, map[string]any{"payment_id": check.PaymentID, "check_id": check.ID, "code": code, "source": check.source()}); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (check PaymentCheck) source() string {
	if check.automatic {
		return "worker"
	}
	return "manual"
}
