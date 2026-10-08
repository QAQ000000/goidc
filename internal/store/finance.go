package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

type PaymentResult struct {
	InvoiceID    int64  `json:"invoice_id"`
	InvoiceNo    string `json:"invoice_no"`
	AmountCents  int64  `json:"amount_cents"`
	BalanceCents int64  `json:"balance_cents"`
	Status       string `json:"status"`
	Replayed     bool   `json:"replayed"`
}

func (s *Store) PayInvoice(ctx context.Context, userID, invoiceID int64, key string, expectedVersion ...int64) (PaymentResult, error) {
	var out PaymentResult
	if userID < 1 || invoiceID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_payment", "账单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "payment", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	var delta, balance int64
	var replayInvoiceID int64
	err = tx.QueryRow(ctx, `SELECT l.reference_id,i.invoice_no,l.delta_cents,i.status
FROM balance_ledger l JOIN invoices i ON i.id=l.reference_id
WHERE l.user_id=$1 AND l.entry_type='payment' AND l.idempotency_key=$2`, userID, key).
		Scan(&replayInvoiceID, &out.InvoiceNo, &delta, &out.Status)
	if err == nil {
		if replayInvoiceID != invoiceID {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同账单")
		}
		out.InvoiceID = replayInvoiceID
		out.AmountCents = -delta
		if err = tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1", userID).Scan(&out.BalanceCents); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var ownerID, orderID int64
	var kind string
	var version int64
	err = tx.QueryRow(ctx, `SELECT id,user_id,invoice_no,amount_cents,status,coalesce(order_id,0),invoice_kind,version FROM invoices WHERE id=$1 FOR UPDATE`, invoiceID).
		Scan(&out.InvoiceID, &ownerID, &out.InvoiceNo, &out.AmountCents, &out.Status, &orderID, &kind, &version)
	if err != nil {
		return out, err
	}
	if ownerID != userID {
		return out, pgx.ErrNoRows
	}
	if err = requireInvoiceReviewed(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if out.Status != "unpaid" {
		return out, conflict("invoice_not_payable", "账单当前状态不可支付")
	}
	if err = requireNoRealPaymentPending(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if kind == "manual" && len(expectedVersion) != 1 {
		return out, conflict("invoice_version_required", "人工账单付款必须提交当前账单版本")
	}
	if len(expectedVersion) > 1 || (len(expectedVersion) == 1 && expectedVersion[0] != version) {
		return out, conflict("stale_invoice", "账单已更新，请刷新后重新支付")
	}
	if kind == "manual" {
		if err = requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	} else if orderID == 0 {
		var renewal bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=$1 AND item_type IN ('renew','upgrade') AND service_id IS NOT NULL)", invoiceID).Scan(&renewal); err != nil {
			return out, err
		}
		if !renewal {
			return out, conflict("invoice_not_payable", "账单没有可支付的服务关联")
		}
	}
	if err = tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&balance); err != nil {
		return out, err
	}
	if balance < out.AmountCents {
		return out, conflict("insufficient_balance", "账户余额不足")
	}
	balance -= out.AmountCents
	if _, err = tx.Exec(ctx, "UPDATE users SET balance_cents=$1,updated_at=now() WHERE id=$2", balance, userID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", invoiceID); err != nil {
		return out, err
	}
	if orderID > 0 {
		var reviewStatus string
		if err = tx.QueryRow(ctx, "SELECT review_status FROM orders WHERE id=$1 FOR UPDATE", orderID).Scan(&reviewStatus); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, "UPDATE orders SET status='paid',updated_at=now() WHERE id=$1", orderID); err != nil {
			return out, err
		}
		if reviewStatus != "pending" {
			if _, err = tx.Exec(ctx, `WITH queued AS (
UPDATE services SET status='queued',version=version+1,updated_at=now()
WHERE order_id=$1 AND status='pending_payment'
RETURNING id
)
INSERT INTO provision_jobs(service_id,action,idempotency_key)
SELECT id,'create','service-create-'||id FROM queued
		ON CONFLICT(idempotency_key) DO NOTHING`, orderID); err != nil {
				return out, err
			}
		}
	} else {
		if err = settleServiceInvoice(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
VALUES($1,'payment',$2,$3,'invoice',$4,$5,'账单支付')`, userID, -out.AmountCents, balance, invoiceID, key); err != nil {
		return out, err
	}
	if err = s.QueuePaymentSuccessNotifications(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if err = s.QueueAffiliateCommissions(ctx, tx, invoiceID, 0); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, userID, "invoice.paid", "invoice", invoiceID, map[string]any{"amount_cents": out.AmountCents, "balance_cents": balance}); err != nil {
		return out, err
	}
	out.BalanceCents = balance
	out.Status = "paid"
	return out, tx.Commit(ctx)
}

func (s *Store) TopUp(ctx context.Context, actorID, userID, amount int64, key, note string) (int64, bool, error) {
	if actorID < 1 || userID < 1 || amount < 1 || amount > 10000000000 || len(key) < 8 || len(key) > 128 {
		return 0, false, conflict("invalid_topup", "充值金额或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "topup", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return 0, false, err
	}
	var balance int64
	var previousTopUp int64
	if err = LockAdminCustomer(ctx, tx, actorID, userID); err != nil {
		return 0, false, err
	}
	if err = tx.QueryRow(ctx, `SELECT delta_cents,balance_after FROM balance_ledger WHERE user_id=$1 AND entry_type='topup' AND idempotency_key=$2`, userID, key).Scan(&previousTopUp, &balance); err == nil {
		if previousTopUp != amount {
			return 0, false, conflict("idempotency_conflict", "相同幂等键对应不同的充值金额")
		}
		return balance, true, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	if err = tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 AND role='customer' FOR UPDATE", userID).Scan(&balance); err != nil {
		return 0, false, err
	}
	if amount > 9223372036854775807-balance {
		return 0, false, conflict("balance_overflow", "余额超出上限")
	}
	balance += amount
	if _, err = tx.Exec(ctx, "UPDATE users SET balance_cents=$1,updated_at=now() WHERE id=$2", balance, userID); err != nil {
		return 0, false, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,idempotency_key,note)
VALUES($1,'topup',$2,$3,'admin_topup',$4,$5)`, userID, amount, balance, key, note); err != nil {
		return 0, false, err
	}
	if err = Audit(ctx, tx, actorID, "balance.topped_up", "user", userID, map[string]any{"amount_cents": amount, "balance_cents": balance}); err != nil {
		return 0, false, err
	}
	return balance, false, tx.Commit(ctx)
}

func (s *Store) RefundInvoice(ctx context.Context, actorID, invoiceID int64, key string) (PaymentResult, error) {
	var out PaymentResult
	if actorID < 1 || invoiceID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_refund", "账单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "refund", key); err != nil {
		return out, err
	}
	var delta, replayInvoiceID int64
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT l.reference_id,i.invoice_no,l.delta_cents,i.status
FROM balance_ledger l JOIN invoices i ON i.id=l.reference_id
WHERE l.entry_type='refund' AND l.reference_type='invoice' AND l.idempotency_key=$1`, key).
		Scan(&replayInvoiceID, &out.InvoiceNo, &delta, &out.Status)
	if err == nil {
		if replayInvoiceID != invoiceID {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同账单")
		}
		out.InvoiceID = replayInvoiceID
		out.AmountCents = delta
		if err = tx.QueryRow(ctx, "SELECT u.balance_cents FROM users u JOIN invoices i ON i.user_id=u.id WHERE i.id=$1", invoiceID).Scan(&out.BalanceCents); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var userID, orderID, balance int64
	var kind string
	err = tx.QueryRow(ctx, `SELECT i.id,i.user_id,coalesce(i.order_id,0),i.invoice_no,i.amount_cents,i.status,i.invoice_kind
FROM invoices i WHERE i.id=$1 FOR UPDATE`, invoiceID).
		Scan(&out.InvoiceID, &userID, &orderID, &out.InvoiceNo, &out.AmountCents, &out.Status, &kind)
	if err != nil {
		return out, err
	}
	if out.Status != "paid" {
		return out, conflict("invoice_not_refundable", "只有已支付账单可以退款")
	}
	var applicationActive bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=$1 AND a.status IN ('pending','issued'))`, invoiceID).Scan(&applicationActive); err != nil {
		return out, err
	}
	if applicationActive {
		return out, conflict("invoice_application_active", "账单存在有效发票申请，不能直接退款")
	}
	if err = requireInvoiceReviewed(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	var externalPayment bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE invoice_id=$1 AND status='succeeded')", invoiceID).Scan(&externalPayment); err != nil {
		return out, err
	}
	if externalPayment {
		return out, conflict("external_refund_requires_gateway", "外部支付账单必须通过原支付渠道退款")
	}
	var renewalServiceID int64
	var upgradeID int64
	if kind == "manual" {
		if err = requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	} else if orderID == 0 {
		if err = tx.QueryRow(ctx, "SELECT service_id FROM invoice_items WHERE invoice_id=$1 AND item_type IN ('renew','upgrade') AND service_id IS NOT NULL LIMIT 1", invoiceID).Scan(&renewalServiceID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, conflict("invoice_not_refundable", "账单没有可退款的服务关联")
			}
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&balance); err != nil {
		return out, err
	}
	if orderID == 0 && kind != "manual" {
		if _, err = tx.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", renewalServiceID); err != nil {
			return out, err
		}
		err = tx.QueryRow(ctx, "SELECT id FROM service_configuration_changes WHERE invoice_id=$1", invoiceID).Scan(&upgradeID)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if upgradeID > 0 {
			var cancellable bool
			if err = tx.QueryRow(ctx, `SELECT c.status='queued' AND j.status='pending' AND j.attempts=0 FROM service_configuration_changes c JOIN provision_jobs j ON j.id=c.job_id WHERE c.id=$1 FOR UPDATE OF c,j`, upgradeID).Scan(&cancellable); err != nil {
				return out, err
			}
			if !cancellable {
				return out, conflict("upgrade_delivery_started", "配置交付已经开始，不能直接退款")
			}
			if err = releaseProductChangeStock(ctx, tx, upgradeID); err != nil {
				return out, err
			}
			if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='cancelled',completed_at=now(),updated_at=now() WHERE id=(SELECT job_id FROM service_configuration_changes WHERE id=$1)", upgradeID); err != nil {
				return out, err
			}
			if _, err = tx.Exec(ctx, "UPDATE service_configuration_changes SET status='cancelled',updated_at=now() WHERE id=$1", upgradeID); err != nil {
				return out, err
			}
		} else if err = requireNoConfigurationChange(ctx, tx, renewalServiceID); err != nil {
			return out, err
		}
	}
	if balance > 9223372036854775807-out.AmountCents {
		return out, conflict("balance_overflow", "余额超出上限")
	}
	balance += out.AmountCents
	if _, err = tx.Exec(ctx, "UPDATE users SET balance_cents=$1,updated_at=now() WHERE id=$2", balance, userID); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET status='refunded',paid_at=NULL WHERE id=$1", invoiceID); err != nil {
		return out, err
	}
	if orderID > 0 {
		if _, err = tx.Exec(ctx, "UPDATE orders SET status='refunded',updated_at=now() WHERE id=$1", orderID); err != nil {
			return out, err
		}
	}
	if orderID == 0 && upgradeID == 0 && kind != "manual" {
		var cycle string
		if err = tx.QueryRow(ctx, "SELECT billing_cycle FROM services WHERE id=$1 FOR UPDATE", renewalServiceID).Scan(&cycle); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, fmt.Sprintf("UPDATE services SET next_due_at=GREATEST(now(),next_due_at-interval '%s'),version=version+1,updated_at=now() WHERE id=$1", renewalInterval(cycle)), renewalServiceID); err != nil {
			return out, err
		}
	}
	if orderID > 0 {
		rows, queryErr := tx.Query(ctx, "SELECT id,status,upstream_id FROM services WHERE order_id=$1 ORDER BY id FOR UPDATE", orderID)
		if queryErr != nil {
			return out, queryErr
		}
		type instance struct {
			id               int64
			status, resource string
		}
		instances := []instance{}
		for rows.Next() {
			var v instance
			if queryErr = rows.Scan(&v.id, &v.status, &v.resource); queryErr != nil {
				rows.Close()
				return out, queryErr
			}
			instances = append(instances, v)
		}
		queryErr = rows.Err()
		rows.Close()
		if queryErr != nil {
			return out, queryErr
		}
		for _, v := range instances {
			var unsupported bool
			if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' AND status NOT IN ('terminated','cancelled') AND (upstream_id<>'' OR status='provisioning') FROM services WHERE id=$1", v.id).Scan(&unsupported); err != nil {
				return out, err
			}
			if unsupported {
				return out, conflict("unsupported_supplier_refund", "该上游资源请先在供应商处取消并同步终止状态，再处理本地退款")
			}
			if err = requireNoExternalDelivery(ctx, tx, v.id); err != nil {
				return out, err
			}
		}
		if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='cancelled',updated_at=now(),completed_at=now()
WHERE service_id IN (SELECT id FROM services WHERE order_id=$1) AND status IN ('pending','running','waiting_manual')`, orderID); err != nil {
			return out, err
		}
		for _, v := range instances {
			if err = requireNoConfigurationChange(ctx, tx, v.id); err != nil {
				return out, err
			}
			if v.status == "active" || v.status == "suspended" || v.status == "provisioning" {
				if _, err = tx.Exec(ctx, "UPDATE services SET status='provisioning',version=version+1,updated_at=now() WHERE id=$1", v.id); err != nil {
					return out, err
				}
				if _, err = tx.Exec(ctx, "INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'terminate',$2) ON CONFLICT(idempotency_key) DO NOTHING", v.id, fmt.Sprintf("service-terminate-refund-%d", v.id)); err != nil {
					return out, err
				}
			} else if v.status != "terminated" && v.status != "cancelled" {
				if _, err = tx.Exec(ctx, "UPDATE services SET status='cancelled',version=version+1,updated_at=now() WHERE id=$1", v.id); err != nil {
					return out, err
				}
			}
		}
		if _, err = tx.Exec(ctx, `WITH released AS (
UPDATE services SET inventory_released=true WHERE order_id=$1 AND status IN ('cancelled','terminated') AND NOT inventory_released RETURNING product_id
), counts AS (SELECT product_id,count(*)::int AS n FROM released GROUP BY product_id)
UPDATE products p SET stock=p.stock+c.n,updated_at=now() FROM counts c WHERE p.id=c.product_id AND p.stock IS NOT NULL`, orderID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
VALUES($1,'refund',$2,$3,'invoice',$4,$5,'账单退款')`, userID, out.AmountCents, balance, invoiceID, key); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actorID, "invoice.refunded", "invoice", invoiceID, map[string]any{"amount_cents": out.AmountCents, "balance_cents": balance}); err != nil {
		return out, err
	}
	if err = s.ReverseAffiliateCommissions(ctx, tx, invoiceID, actorID, "confirmed invoice refund"); err != nil {
		return out, err
	}
	out.Status = "refunded"
	out.BalanceCents = balance
	return out, tx.Commit(ctx)
}
