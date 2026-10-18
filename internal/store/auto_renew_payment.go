package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

type autoRenewalPayment struct {
	serviceID  int64
	due        time.Time
	generation int64
}

// PayAutoRenewalInvoice rechecks an automatic decision in the transaction that
// debits the customer's balance. Its immutable source is created with the
// invoice; caller-controlled idempotency keys never authorize automatic payment.
// Existing unpaid invoices are still left for manual payment, without retrying
// a failed or interrupted automatic collection on a later scheduler pass.
func (s *Store) PayAutoRenewalInvoice(ctx context.Context, userID, serviceID, invoiceID int64, due time.Time, generation int64, key string) (PaymentResult, error) {
	if serviceID < 1 || due.IsZero() || generation < 0 {
		return PaymentResult{}, conflict("invalid_auto_renewal", "自动续费服务、期次或代次无效")
	}
	automatic := &autoRenewalPayment{serviceID: serviceID, due: due, generation: generation}
	return s.payInvoice(ctx, userID, invoiceID, key, false, automatic)
}

func requireAutoRenewalInvoiceSource(ctx context.Context, tx pgx.Tx, userID, invoiceID int64, automatic autoRenewalPayment) error {
	var valid bool
	err := tx.QueryRow(ctx, `SELECT service_id=$2 AND user_id=$3 AND expected_due=$4 AND generation=$5
FROM auto_renewal_invoice_sources WHERE invoice_id=$1`, invoiceID, automatic.serviceID, userID, automatic.due, automatic.generation).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !valid {
		return conflict("auto_renewal_invoice_source_conflict", "账单缺少匹配的自动续费来源，请人工处理原账单")
	}
	return err
}

// Invoice -> service is the existing finance lock order. Cancellation admission
// has already locked every renewal service with NOWAIT; repeat that defensive
// lock here so this helper also remains safe if its caller is reorganized.
func (s *Store) requireAutoRenewalPayment(ctx context.Context, tx pgx.Tx, userID, invoiceID int64, automatic autoRenewalPayment) error {
	var associated bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices i
WHERE i.id=$1 AND i.user_id=$2 AND i.order_id IS NULL AND i.invoice_kind='automatic'
AND (SELECT count(*) FROM invoice_items WHERE invoice_id=i.id)=1
AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.item_type='renew'
 AND ii.service_id=$3 AND ii.amount_cents=i.amount_cents))`, invoiceID, userID, automatic.serviceID).Scan(&associated); err != nil {
		return err
	}
	if !associated {
		return conflict("auto_renewal_invoice_source_conflict", "自动续费账单关联已变化，请人工处理原账单")
	}
	var ownerID, generation int64
	var enabled, inWindow bool
	var status, cycle, mode string
	var due *time.Time
	if err := tx.QueryRow(ctx, `SELECT user_id,auto_renew,next_due_at,renewal_generation,status,billing_cycle,provision_mode,
coalesce(next_due_at<=clock_timestamp()+interval '24 hours',false)
FROM services WHERE id=$1 FOR UPDATE NOWAIT`, automatic.serviceID).
		Scan(&ownerID, &enabled, &due, &generation, &status, &cycle, &mode, &inWindow); err != nil {
		return cancellationServiceLockError(err)
	}
	if ownerID != userID || !enabled || due == nil || !due.Equal(automatic.due) || generation != automatic.generation ||
		!inWindow || cycle == "once" || status != "active" && status != "suspended" {
		return conflict("auto_renewal_race", "自动续费开关、代次、期限或服务状态已变化，原账单保留供人工处理")
	}
	if err := requireServiceReviewed(ctx, tx, automatic.serviceID); err != nil {
		return err
	}
	if err := requireNoLocalRenewalHold(ctx, tx, automatic.serviceID); err != nil {
		return err
	}
	if err := requireServiceCancellationOpen(ctx, tx, automatic.serviceID); err != nil {
		return err
	}
	if mode == "supplier" {
		return s.requireAutomaticSupplierRenewalPayment(ctx, tx, invoiceID, automatic.serviceID)
	}
	return nil
}
