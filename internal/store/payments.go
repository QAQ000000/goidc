package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type InvoicePaymentDetails struct {
	InvoiceID   int64
	InvoiceNo   string
	UserID      int64
	AmountCents int64
	Status      string
	Version     int64
	InvoiceKind string
}

type PaymentIntentResult struct {
	PaymentID         int64  `json:"payment_id"`
	Provider          string `json:"provider"`
	ProviderPaymentID string `json:"provider_payment_id"`
	InvoiceID         int64  `json:"invoice_id"`
	InvoiceNo         string `json:"invoice_no"`
	AmountCents       int64  `json:"amount_cents"`
	CheckoutURL       string `json:"checkout_url"`
	Status            string `json:"status"`
	Replayed          bool   `json:"replayed"`
}

type WebhookResult struct {
	EventID           string `json:"event_id"`
	Provider          string `json:"provider"`
	ProviderPaymentID string `json:"provider_payment_id"`
	InvoiceID         int64  `json:"invoice_id"`
	Status            string `json:"status"`
	Replayed          bool   `json:"replayed"`
}

func (s *Store) InvoicePaymentDetails(ctx context.Context, userID, invoiceID int64) (InvoicePaymentDetails, error) {
	var out InvoicePaymentDetails
	err := s.Pool.QueryRow(ctx, `SELECT id,invoice_no,user_id,amount_cents,status,version,invoice_kind
FROM invoices WHERE id=$1 AND user_id=$2`, invoiceID, userID).Scan(&out.InvoiceID, &out.InvoiceNo, &out.UserID, &out.AmountCents, &out.Status, &out.Version, &out.InvoiceKind)
	return out, err
}

func (s *Store) CreatePaymentIntent(ctx context.Context, userID, invoiceID int64, provider, providerPaymentID, checkoutURL, key string, expectedVersion ...int64) (PaymentIntentResult, error) {
	var out PaymentIntentResult
	if userID < 1 || invoiceID < 1 || provider == "" || providerPaymentID == "" || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_payment_intent", "支付参数或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if _, err = lockOnlineTopupCustomer(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if err = checkPaymentConfigurationVersion(ctx, tx, provider); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "payment-intent", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT pt.id,pt.provider,pt.provider_payment_id,pt.invoice_id,i.invoice_no,pt.amount_cents,pt.status,pt.checkout_url
FROM payment_transactions pt JOIN invoices i ON i.id=pt.invoice_id
WHERE pt.user_id=$1 AND pt.idempotency_key=$2`, userID, key).
		Scan(&out.PaymentID, &out.Provider, &out.ProviderPaymentID, &out.InvoiceID, &out.InvoiceNo, &out.AmountCents, &out.Status, &out.CheckoutURL)
	if err == nil {
		if out.InvoiceID != invoiceID || out.Provider != provider {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同支付")
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var invoiceStatus, kind string
	var version int64
	if err = tx.QueryRow(ctx, "SELECT invoice_no,amount_cents,status,invoice_kind,version FROM invoices WHERE id=$1 AND user_id=$2 FOR UPDATE", invoiceID, userID).
		Scan(&out.InvoiceNo, &out.AmountCents, &invoiceStatus, &kind, &version); err != nil {
		return out, err
	}
	if invoiceStatus != "unpaid" {
		return out, conflict("invoice_not_payable", "账单当前状态不可支付")
	}
	if kind == "manual" && (len(expectedVersion) == 0 || expectedVersion[0] < 1) {
		return out, conflict("invoice_version_required", "人工账单付款必须提交当前账单版本")
	}
	if len(expectedVersion) > 2 || (len(expectedVersion) >= 1 && expectedVersion[0] != version) || (len(expectedVersion) == 2 && expectedVersion[1] != version) {
		return out, conflict("stale_invoice", "账单已更新，请刷新后重新支付")
	}
	if kind == "manual" {
		if err = requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	}
	if err = requireInvoiceReviewed(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired') AND (provider=$2 OR provider<>'mock' OR $2<>'mock'))", invoiceID, provider).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, conflict("invoice_payment_pending", "账单已有待处理支付，请继续原支付单")
	}
	if kind == "topup" {
		if !realPaymentProvider(provider) {
			return out, conflict("topup_external_payment_required", "充值仅允许真实在线支付渠道")
		}
		if err = acceptOnlineTopup(ctx, tx, userID, invoiceID); err != nil {
			return out, err
		}
	}
	var sealed []byte
	if realPaymentProvider(provider) {
		cfg, ok := ctx.Value(paymentConfigurationKey{}).(PaymentConfiguration)
		if !ok {
			cfg, _, err = s.paymentConfiguration(tx.QueryRow(ctx, "SELECT "+paymentSettingsColumns+" FROM payment_settings WHERE id=1"), PaymentConfiguration{})
			if err != nil {
				return out, err
			}
		}
		if sealed, err = s.snapshotPaymentCredentials(providerPaymentID, cfg, false); err != nil {
			return out, err
		}
	}
	instanceID := paymentChannelInstance(ctx)
	if err = tx.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,channel_instance_id,invoice_id,user_id,amount_cents,idempotency_key,checkout_url,credential_snapshot)
VALUES($1,$2,COALESCE(NULLIF($9,''),(SELECT id FROM channel_instances WHERE category='payment' AND implementation_id=CASE WHEN $1='mock' THEN 'mock' ELSE 'epay' END AND enabled=true ORDER BY sort_order,id LIMIT 1)),$3,$4,$5,$6,$7,$8) RETURNING id`, provider, providerPaymentID, invoiceID, userID, out.AmountCents, key, checkoutURL, append([]byte{}, sealed...), instanceID).Scan(&out.PaymentID); err != nil {
		return out, err
	}
	out.Provider, out.ProviderPaymentID, out.InvoiceID, out.CheckoutURL, out.Status = provider, providerPaymentID, invoiceID, checkoutURL, "pending"
	if err = Audit(ctx, tx, userID, "payment.intent.created", "invoice", invoiceID, map[string]any{"provider": provider, "payment_id": providerPaymentID, "amount_cents": out.AmountCents}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// Called while holding the invoice row lock. Local expiry cannot prove that
// a real provider will no longer collect payment or retry its notification.
func requireNoRealPaymentPending(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var pending bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status IN ('pending','expired') AND provider<>'mock')", invoiceID).Scan(&pending); err != nil {
		return err
	}
	if pending {
		return conflict("invoice_payment_pending", "账单已有在线支付，请先完成原支付单")
	}
	return nil
}

func settleExternalInvoice(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var orderID int64
	if err := tx.QueryRow(ctx, "SELECT coalesce(order_id,0) FROM invoices WHERE id=$1 FOR UPDATE", invoiceID).Scan(&orderID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1 AND status='unpaid'", invoiceID); err != nil {
		return err
	}
	if orderID > 0 {
		var reviewStatus string
		if err := tx.QueryRow(ctx, "SELECT review_status FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&reviewStatus); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, "UPDATE orders SET status='paid',updated_at=now() WHERE id=$1", orderID); err != nil {
			return err
		}
		if reviewStatus == "pending" {
			return nil
		}
		_, err := tx.Exec(ctx, `WITH queued AS (UPDATE services SET status='queued',version=version+1,updated_at=now() WHERE order_id=$1 AND status='pending_payment' RETURNING id)
INSERT INTO provision_jobs(service_id,action,idempotency_key) SELECT id,'create','service-create-'||id FROM queued ON CONFLICT(idempotency_key) DO NOTHING`, orderID)
		return err
	}
	return settleServiceInvoice(ctx, tx, invoiceID)
}

func (s *Store) ProcessPaymentWebhook(ctx context.Context, provider, eventID, providerPaymentID string, invoiceID, amountCents int64, status string, raw []byte, externalTransaction ...string) (WebhookResult, error) {
	return s.processPaymentWebhook(ctx, provider, eventID, providerPaymentID, invoiceID, amountCents, status, raw, externalTransaction, nil)
}

func (s *Store) processPaymentWebhook(ctx context.Context, provider, eventID, providerPaymentID string, invoiceID, amountCents int64, status string, raw []byte, externalTransaction []string, check *PaymentCheck) (WebhookResult, error) {
	var out WebhookResult
	if provider == "" || eventID == "" || providerPaymentID == "" || invoiceID < 1 || amountCents < 0 || (status != "succeeded" && status != "failed") || len(raw) == 0 {
		return out, conflict("invalid_payment_webhook", "支付回调参数无效")
	}
	hash := sha256.Sum256(raw)
	payloadHash := hex.EncodeToString(hash[:])
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	topup, err := lockOnlineTopupCustomer(ctx, tx, invoiceID)
	if err != nil {
		return out, err
	}
	if check != nil {
		if err = authorizePaymentCheck(ctx, tx, check); err != nil {
			return out, err
		}
	}
	if err = lockOperationKey(ctx, tx, "payment-webhook", provider+":"+eventID); err != nil {
		return out, err
	}
	var savedHash string
	err = tx.QueryRow(ctx, "SELECT payload_hash FROM payment_webhook_events WHERE provider=$1 AND event_id=$2", provider, eventID).Scan(&savedHash)
	if err == nil {
		if savedHash != payloadHash {
			return out, conflict("webhook_replay_mismatch", "重复回调内容不一致")
		}
		out = WebhookResult{EventID: eventID, Provider: provider, ProviderPaymentID: providerPaymentID, InvoiceID: invoiceID, Status: "processed", Replayed: true}
		return out, finishPaymentCheck(ctx, tx, check, "succeeded")
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO payment_webhook_events(provider,event_id,payload_hash,status) VALUES($1,$2,$3,'processed')", provider, eventID, payloadHash); err != nil {
		return out, err
	}
	var txID, savedInvoice, savedAmount int64
	var txStatus string
	if err = tx.QueryRow(ctx, `SELECT id,invoice_id,amount_cents,status FROM payment_transactions WHERE provider=$1 AND provider_payment_id=$2 FOR UPDATE`, provider, providerPaymentID).
		Scan(&txID, &savedInvoice, &savedAmount, &txStatus); err != nil {
		return out, err
	}
	if savedInvoice != invoiceID || savedAmount != amountCents {
		return out, conflict("payment_amount_mismatch", "支付金额或账单不匹配")
	}
	if txStatus == "succeeded" {
		if len(externalTransaction) > 0 {
			var current string
			if err = tx.QueryRow(ctx, "SELECT external_transaction_id FROM payment_transactions WHERE id=$1", txID).Scan(&current); err != nil {
				return out, err
			}
			if current != externalTransaction[0] {
				return out, conflict("payment_transaction_mismatch", "渠道流水号不一致")
			}
		}
		out = WebhookResult{EventID: eventID, Provider: provider, ProviderPaymentID: providerPaymentID, InvoiceID: invoiceID, Status: "succeeded", Replayed: true}
		return out, finishPaymentCheck(ctx, tx, check, "succeeded")
	}
	if txStatus == "failed" && status == "succeeded" && !topup {
		return out, conflict("payment_state_conflict", "失败支付不能直接标记成功")
	}
	if len(externalTransaction) > 0 {
		if len(externalTransaction) != 1 || externalTransaction[0] == "" || len(externalTransaction[0]) > 160 {
			return out, conflict("invalid_payment_webhook", "渠道流水号无效")
		}
		if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET external_transaction_id=$2 WHERE id=$1", txID, externalTransaction[0]); err != nil {
			return out, err
		}
	}
	if status == "succeeded" {
		var invoiceStatus string
		if err = tx.QueryRow(ctx, "SELECT status FROM invoices WHERE id=$1 FOR UPDATE", invoiceID).Scan(&invoiceStatus); err != nil {
			return out, err
		}
		if invoiceStatus != "unpaid" {
			return out, conflict("invoice_not_payable", "账单当前状态不可支付")
		}
		if err = requireInvoiceReviewed(ctx, tx, invoiceID); err != nil {
			return out, err
		}
		if topup {
			err = settleOnlineTopup(ctx, tx, invoiceID, txID)
		} else {
			err = settleExternalInvoice(ctx, tx, invoiceID)
		}
		if err != nil {
			return out, err
		}
		if err = s.QueuePaymentSuccessNotifications(ctx, tx, invoiceID); err != nil {
			return out, err
		}
		if !topup {
			if err = s.QueueAffiliateCommissions(ctx, tx, invoiceID, txID); err != nil {
				return out, err
			}
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET status=$2,completed_at=CASE WHEN $2 IN ('succeeded','failed') THEN now() ELSE completed_at END,raw_result=$3,next_reconcile_at=NULL,updated_at=now() WHERE id=$1", txID, status, json.RawMessage(raw)); err != nil {
		return out, err
	}
	if status == "succeeded" {
		if _, err = tx.Exec(ctx, "UPDATE traffic_orders SET payment_method=$2 WHERE invoice_id=$1 AND status='paid'", invoiceID, provider); err != nil {
			return out, err
		}
	}
	if err = Audit(ctx, tx, 0, "payment.webhook."+status, "invoice", invoiceID, map[string]any{"provider": provider, "event_id": eventID, "payment_id": providerPaymentID, "amount_cents": amountCents}); err != nil {
		return out, err
	}
	out = WebhookResult{EventID: eventID, Provider: provider, ProviderPaymentID: providerPaymentID, InvoiceID: invoiceID, Status: status}
	return out, finishPaymentCheck(ctx, tx, check, "succeeded")
}
