package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Only a verified callback/query may bypass the customer topup prelock for an
// existing administrator-owned invoice. Customer-initiated payment paths retain
// lockOnlineTopupCustomer and their original customer/session requirements.
func lockPaymentWebhookCustomer(ctx context.Context, tx pgx.Tx, invoice int64) (bool, error) {
	var kind string
	if err := tx.QueryRow(ctx, `SELECT a.kind FROM invoices i JOIN account_principals a ON a.id=i.user_id WHERE i.id=$1`, invoice).Scan(&kind); err != nil {
		return false, err
	}
	if kind == "administrator" {
		return false, nil
	}
	return lockOnlineTopupCustomer(ctx, tx, invoice)
}

// Preserve an already verified provider observation about a pre-split payment
// without granting its administrator owner any customer business capability.
// Financial rows and their raw_result/credential bytes remain unchanged; only
// the existing event/audit evidence and automatic-query scheduling are written.
func reviewHistoricalAdministratorPayment(ctx context.Context, tx pgx.Tx, provider, eventID, reference string, invoice, amount int64, status string, raw []byte, external []string, check *PaymentCheck) (WebhookResult, bool, error) {
	var historical bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions p JOIN account_principals a ON a.id=p.user_id WHERE p.provider=$1 AND p.provider_payment_id=$2 AND a.kind='administrator')`, provider, reference).Scan(&historical); err != nil || !historical {
		return WebhookResult{}, false, err
	}
	if err := lockOperationKey(ctx, tx, "payment-webhook", provider+":"+eventID); err != nil {
		return WebhookResult{}, true, err
	}
	hash := sha256.Sum256(raw)
	payloadHash := hex.EncodeToString(hash[:])
	var savedHash string
	err := tx.QueryRow(ctx, `SELECT payload_hash FROM payment_webhook_events WHERE provider=$1 AND event_id=$2`, provider, eventID).Scan(&savedHash)
	replayed := err == nil
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return WebhookResult{}, true, err
	}
	if replayed && savedHash != payloadHash {
		return WebhookResult{}, true, conflict("webhook_replay_mismatch", "重复回调内容不一致")
	}
	var paymentID, owner, savedInvoice, savedAmount, invoiceOwner int64
	var paymentStatus, savedExternal string
	if err = tx.QueryRow(ctx, `SELECT p.id,p.user_id,p.invoice_id,p.amount_cents,p.status,p.external_transaction_id,i.user_id FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id JOIN account_principals a ON a.id=p.user_id WHERE p.provider=$1 AND p.provider_payment_id=$2 AND a.kind='administrator' FOR UPDATE OF p`, provider, reference).
		Scan(&paymentID, &owner, &savedInvoice, &savedAmount, &paymentStatus, &savedExternal, &invoiceOwner); err != nil {
		return WebhookResult{}, true, err
	}
	if savedInvoice != invoice || savedAmount != amount || invoiceOwner != owner {
		return WebhookResult{}, true, conflict("payment_amount_mismatch", "支付金额或账单不匹配")
	}
	if len(external) > 1 || (len(external) == 1 && (external[0] == "" || len(external[0]) > 160)) {
		return WebhookResult{}, true, conflict("invalid_payment_webhook", "渠道流水号无效")
	}
	externalID := ""
	if len(external) == 1 {
		externalID = external[0]
		if paymentStatus == "succeeded" && externalID != savedExternal {
			return WebhookResult{}, true, conflict("payment_transaction_mismatch", "渠道流水号不一致")
		}
	}
	if !replayed {
		if _, err = tx.Exec(ctx, `INSERT INTO payment_webhook_events(provider,event_id,payload_hash,status) VALUES($1,$2,$3,'ignored')`, provider, eventID, payloadHash); err != nil {
			return WebhookResult{}, true, err
		}
		if err = Audit(ctx, tx, 0, "payment.legacy_administrator.review_required", "invoice", invoice, map[string]any{
			"payment_id": paymentID, "account_id": owner, "provider": provider, "provider_payment_id": reference,
			"event_id": eventID, "amount_cents": amount, "provider_status": status,
			"external_transaction_id": externalID, "payload_sha256": payloadHash, "manual_review": true,
		}); err != nil {
			return WebhookResult{}, true, err
		}
	}
	if err = finishAdministratorPaymentReview(ctx, tx, paymentID, check); err != nil {
		return WebhookResult{}, true, err
	}
	return WebhookResult{EventID: eventID, Provider: provider, ProviderPaymentID: reference, InvoiceID: invoice, Status: "review_required", Replayed: replayed}, true, nil
}

func finishAdministratorPaymentReview(ctx context.Context, tx pgx.Tx, paymentID int64, check *PaymentCheck) error {
	if check != nil {
		result, err := tx.Exec(ctx, `UPDATE payment_reconciliation_checks SET status='error',result_code='manual_review_required',completed_at=clock_timestamp()
 WHERE id=$1 AND payment_id=$2 AND actor_id IS NOT DISTINCT FROM nullif($3::bigint,0) AND source=$4 AND status='checking' AND lease_until>clock_timestamp()`, check.ID, paymentID, check.ActorID, check.source())
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return conflict("payment_check_expired", "支付查询已过期，请重新查询")
		}
		if err = Audit(ctx, tx, check.ActorID, "payment.reconciliation.completed", "invoice", check.InvoiceID, map[string]any{
			"payment_id": paymentID, "check_id": check.ID, "status": "error", "result_code": "manual_review_required", "source": check.source(), "manual_review": true,
		}); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `UPDATE payment_transactions SET next_reconcile_at=NULL WHERE id=$1 AND next_reconcile_at IS NOT NULL`, paymentID)
	return err
}
