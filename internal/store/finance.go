package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// activatedOrderStates lists the order states that satisfy the legacy
// addfunds_require_order switch: the paid state that payment settles to, and
// the active state that a manual review promotes an order to.
var activatedOrderStates = []string{"paid", "active"}

type creditSessionKey struct{}

func WithCreditRefundSession(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, creditSessionKey{}, hash)
}

type PaymentResult struct {
	InvoiceID    int64  `json:"invoice_id"`
	InvoiceNo    string `json:"invoice_no"`
	AmountCents  int64  `json:"amount_cents"`
	BalanceCents int64  `json:"balance_cents"`
	Status       string `json:"status"`
	Replayed     bool   `json:"replayed"`
}

func (s *Store) PayInvoice(ctx context.Context, userID, invoiceID int64, key string, expectedVersion ...int64) (PaymentResult, error) {
	return s.payInvoice(ctx, userID, invoiceID, key, false, expectedVersion...)
}
func (s *Store) PayInvoiceCredit(ctx context.Context, userID, invoiceID int64, key string, expectedVersion int64, sessionHash string) (PaymentResult, error) {
	ctx = context.WithValue(ctx, creditSessionKey{}, sessionHash)
	return s.payInvoice(ctx, userID, invoiceID, key, true, expectedVersion)
}
func (s *Store) payInvoice(ctx context.Context, userID, invoiceID int64, key string, credit bool, expectedVersion ...int64) (PaymentResult, error) {
	var out PaymentResult
	if userID < 1 || invoiceID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_payment", "账单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockFinancialCustomer(ctx, tx, userID); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "payment", fmt.Sprintf("%d:%s", userID, key)); err != nil {
		return out, err
	}
	var balance int64
	if credit {
		var activeID int64
		if err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND role='customer' AND status='active' FOR SHARE", userID).Scan(&activeID); err != nil {
			return out, err
		}
		if err = tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now() FOR SHARE", ctx.Value(creditSessionKey{}), userID).Scan(&activeID); err != nil {
			return out, err
		}

		var replayID, replayVersion int64
		err = tx.QueryRow(ctx, "SELECT invoice_id,amount_cents,expected_version FROM credit_invoice_payments WHERE user_id=$1 AND idempotency_key=$2", userID, key).Scan(&replayID, &out.AmountCents, &replayVersion)
		if err == nil {
			if replayID != invoiceID || len(expectedVersion) != 1 || replayVersion != expectedVersion[0] {
				return out, conflict("idempotency_conflict", "幂等键对应其他账单")
			}
			out.InvoiceID = invoiceID
			var activeID int64
			if err = tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 AND status='active' AND role='customer' FOR SHARE", userID).Scan(&activeID); err != nil {
				return out, err
			}
			out.Status = "paid"
			out.Replayed = true
			return out, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
	} else {
		var delta int64
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
	}
	var ownerID, orderID int64
	var kind string
	var version int64
	err = tx.QueryRow(ctx, `SELECT id,user_id,invoice_no,amount_cents,status,coalesce(order_id,0),invoice_kind,version FROM invoices WHERE id=$1 FOR UPDATE`, invoiceID).
		Scan(&out.InvoiceID, &ownerID, &out.InvoiceNo, &out.AmountCents, &out.Status, &orderID, &kind, &version)
	if err != nil {
		return out, err
	}
	if kind == "topup" {
		return out, conflict("topup_external_payment_required", "充值只能使用在线支付渠道，不能使用余额或信用")
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
	if credit && kind != "manual" && kind != "automatic" {
		return out, conflict("unsupported_credit_invoice", "该账单类型不支持信用付款")
	}
	if kind == "manual" {
		if err = requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	} else if orderID == 0 {
		var renewal bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=$1 AND item_type IN ('renew','upgrade','traffic') AND service_id IS NOT NULL)", invoiceID).Scan(&renewal); err != nil {
			return out, err
		}
		if !renewal {
			return out, conflict("invoice_not_payable", "账单没有可支付的服务关联")
		}
	}
	if credit {
		if kind == "manual" {
			var unsupported bool
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=$1 AND item_type NOT IN ('manual','service'))", invoiceID).Scan(&unsupported); err != nil {
				return out, err
			}
			if unsupported {
				return out, conflict("unsupported_credit_invoice", "充值或特殊项目不支持信用付款")
			}
		}
		if len(expectedVersion) != 1 || expectedVersion[0] < 1 || out.AmountCents < 1 {
			return out, conflict("invalid_credit_payment", "需要账单版本与正数金额")
		}
		var enabled bool
		if err = tx.QueryRow(ctx, "SELECT enabled FROM credit_settings WHERE id=1 FOR SHARE").Scan(&enabled); err != nil {
			return out, err
		}
		if !enabled {
			return out, conflict("credit_disabled", "信用支付未启用")
		}
		var limit, used int64
		var frozen bool
		if err = tx.QueryRow(ctx, "SELECT limit_cents,used_cents,frozen FROM customer_credit_accounts WHERE user_id=$1 FOR UPDATE", userID).Scan(&limit, &used, &frozen); err != nil {
			return out, err
		}
		if frozen || out.AmountCents > limit-used {
			return out, conflict("credit_unavailable", "账户冻结或额度不足")
		}
		if _, err = tx.Exec(ctx, "UPDATE customer_credit_accounts SET used_cents=used_cents+$2,version=version+1,updated_at=now() WHERE user_id=$1", userID, out.AmountCents); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, "INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,idempotency_key,expected_version) VALUES($1,$2,$3,$3,$4,$5)", invoiceID, userID, out.AmountCents, key, expectedVersion[0]); err != nil {
			return out, err
		}
	} else {
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
	if !credit {
		if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
VALUES($1,'payment',$2,$3,'invoice',$4,$5,'账单支付')`, userID, -out.AmountCents, balance, invoiceID, key); err != nil {
			return out, err
		}
	}
	if err = s.QueuePaymentSuccessNotifications(ctx, tx, invoiceID); err != nil {
		return out, err
	}
	if !credit {
		if err = s.QueueAffiliateCommissions(ctx, tx, invoiceID, 0); err != nil {
			return out, err
		}
	}
	if err = Audit(ctx, tx, userID, "invoice.paid", "invoice", invoiceID, map[string]any{"amount_cents": out.AmountCents, "balance_cents": balance}); err != nil {
		return out, err
	}
	out.BalanceCents = balance
	out.Status = "paid"
	return out, tx.Commit(ctx)
}

func (s *Store) TopUp(ctx context.Context, actorID, userID, amount int64, key, note string) (int64, bool, error) {
	// The hardcoded bounds stay in place as the last line of defence: they keep
	// the ledger arithmetic safe even if finance_settings is misconfigured.
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
	// Match online admission: customer funds before finance settings. Waiting
	// settings writers must not create a settings -> customer reverse edge.
	if err = LockFinancialCustomer(ctx, tx, userID); err != nil {
		return 0, false, err
	}
	if err = tx.QueryRow(ctx, `SELECT delta_cents,balance_after FROM balance_ledger WHERE user_id=$1 AND entry_type='topup' AND idempotency_key=$2`, userID, key).Scan(&previousTopUp, &balance); err == nil {
		// A replay of an already-committed top-up returns the recorded result
		// even if the financial settings have changed since: the balance was
		// already credited and re-validating would strand the client without a
		// usable idempotency answer.
		if previousTopUp != amount {
			return 0, false, conflict("idempotency_conflict", "相同幂等键对应不同的充值金额")
		}
		return balance, true, tx.Commit(ctx)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, err
	}
	// The admin-facing finance_settings switches are the source of truth for
	// top-ups; before this fix they were persisted and displayed but never
	// read here. Read inside the same transaction with a shared row lock so a
	// concurrent settings update cannot interleave with the credit.
	// require_active_order mirrors the legacy addfunds_require_order switch,
	// which accepted an order in the activated state and ignored orders the
	// customer had deleted. Legacy had one activated state; Go settles payment
	// to the paid state and only an optional manual review promotes an order
	// to the active state, so both satisfy the switch. See
	// activatedOrderStates for the accepted vocabulary.
	var topupEnabled, requireActiveOrder bool
	var minTopup, maxTopup, maxBalance int64
	if err = tx.QueryRow(ctx, `SELECT topup_enabled,min_topup_cents,max_topup_cents,max_balance_cents,require_active_order FROM finance_settings WHERE id=1 FOR SHARE`).Scan(&topupEnabled, &minTopup, &maxTopup, &maxBalance, &requireActiveOrder); err != nil {
		return 0, false, err
	}
	if !topupEnabled {
		return 0, false, conflict("topup_disabled", "充值功能已关闭")
	}
	if amount < minTopup {
		return 0, false, conflict("topup_below_minimum", fmt.Sprintf("单笔充值金额低于最小值 %d 分", minTopup))
	}
	if amount > maxTopup {
		return 0, false, conflict("topup_above_maximum", fmt.Sprintf("单笔充值金额超过最大值 %d 分", maxTopup))
	}
	if requireActiveOrder {
		var hasActivatedOrder bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM orders WHERE user_id=$1 AND status = ANY($2::text[]) AND deleted_at IS NULL)`, userID, activatedOrderStates).Scan(&hasActivatedOrder); err != nil {
			return 0, false, err
		}
		if !hasActivatedOrder {
			return 0, false, conflict("topup_active_order_required", "充值前必须存在已支付或已激活订单")
		}
	}
	if err = tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 AND role='customer'", userID).Scan(&balance); err != nil {
		return 0, false, err
	}
	if amount > 9223372036854775807-balance {
		return 0, false, conflict("balance_overflow", "余额超出上限")
	}
	// max_balance_cents=0 keeps the migration/legacy default meaning of "no
	// configured ceiling" so default installations continue to allow top-ups.
	var reserved int64
	if err = tx.QueryRow(ctx, "SELECT coalesce(sum(amount_cents),0)::bigint FROM balance_topups WHERE user_id=$1 AND status IN ('accepted','pending_liability')", userID).Scan(&reserved); err != nil {
		return 0, false, err
	}
	if maxBalance > 0 && amount > topupCapacity(maxBalance, balance, reserved) {
		return 0, false, conflict("topup_balance_limit", fmt.Sprintf("充值后余额将超过上限 %d 分", maxBalance))
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
	if ctx.Value(creditSessionKey{}) != nil {
		var role string
		var roleID *int64
		if err = tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
			return out, err
		}
		if role != "super_admin" {
			var allowed bool
			if roleID == nil {
				return out, conflict("permission_denied", "没有退款权限")
			}
			if err = tx.QueryRow(ctx, "SELECT active AND 'invoices.refund'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil {
				return out, err
			}
			if !allowed {
				return out, conflict("permission_denied", "没有退款权限")
			}
		}
		var activeSession int64
		if err = tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>now() FOR SHARE", ctx.Value(creditSessionKey{}), actorID).Scan(&activeSession); err != nil {
			return out, conflict("permission_denied", "信用退款需要有效浏览器会话")
		}

	}

	if err = lockOperationKey(ctx, tx, "refund", key); err != nil {
		return out, err
	}
	var keyInvoice int64
	err = tx.QueryRow(ctx, "SELECT invoice_id FROM credit_invoice_payments WHERE refund_key=$1 UNION ALL SELECT reference_id FROM balance_ledger WHERE entry_type='refund' AND reference_type='invoice' AND idempotency_key=$1 LIMIT 1", key).Scan(&keyInvoice)
	if err == nil && keyInvoice != invoiceID {
		return out, conflict("idempotency_conflict", "退款幂等键对应其他账单")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var delta, replayInvoiceID int64
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
		return out, err
	}
	var lockedCustomer int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoices WHERE id=$1", invoiceID).Scan(&lockedCustomer); err != nil {
		return out, err
	}
	if err = LockFinancialCustomer(ctx, tx, lockedCustomer); err != nil {
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
	if kind == "topup" {
		return out, conflict("topup_refund_not_supported", "充值退款尚未开放，请联系财务核对")
	}
	var creditRemaining int64
	var creditRefunded bool
	err = tx.QueryRow(ctx, "SELECT remaining_cents,status='refunded' FROM credit_invoice_payments WHERE invoice_id=$1 FOR UPDATE", invoiceID).Scan(&creditRemaining, &creditRefunded)
	creditPayment := err == nil
	if creditPayment && ctx.Value(creditSessionKey{}) == nil {
		return out, conflict("permission_denied", "信用退款需要浏览器会话")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if creditPayment && creditRefunded {
		var oldKey string
		var oldActor int64
		if err = tx.QueryRow(ctx, "SELECT refund_key,refund_actor_id FROM credit_invoice_payments WHERE invoice_id=$1", invoiceID).Scan(&oldKey, &oldActor); err != nil {
			return out, err
		}
		if oldKey != key || oldActor != actorID {
			return out, conflict("invoice_not_refundable", "账单已退款")
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if creditPayment && creditRemaining != out.AmountCents {
		return out, conflict("credit_repaid_refund_manual", "信用消费已还款，需先人工核实线下退付，不能自动退款")
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
		return out, conflict("external_refund_requires_gateway", "本站未接入可信原路退款接口，请由财务在商户后台人工退款并核验实际到账；不会退入本站余额")
	}
	var renewalServiceID int64
	var supplierRenewalRefund bool
	var upgradeID int64
	var traffic bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM traffic_orders WHERE invoice_id=$1)", invoiceID).Scan(&traffic); err != nil {
		return out, err
	}
	if traffic {
		if err = ReleaseTrafficInvoice(ctx, tx, invoiceID, "refunded"); err != nil {
			return out, err
		}
	}
	if kind == "manual" {
		if err = requireManualInvoiceValid(ctx, tx, invoiceID); err != nil {
			return out, err
		}
	} else if orderID == 0 && !traffic {
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
	if orderID == 0 && kind != "manual" && !traffic {
		if _, err = tx.Exec(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", renewalServiceID); err != nil {
			return out, err
		}
		if supplierRenewalRefund, err = refundSupplierRenewal(ctx, tx, invoiceID, renewalServiceID); err != nil {
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
	if creditPayment {
		if _, err = tx.Exec(ctx, "UPDATE customer_credit_accounts SET used_cents=used_cents-$2,version=version+1,updated_at=now() WHERE user_id=$1 AND used_cents>=$2", userID, creditRemaining); err != nil {
			return out, err
		}
		if _, err = tx.Exec(ctx, "UPDATE credit_invoice_payments SET remaining_cents=0,status='refunded',version=version+1,refunded_at=now(),refund_key=$2,refund_actor_id=$3 WHERE invoice_id=$1", invoiceID, key, actorID); err != nil {
			return out, err
		}
	} else {
		if balance > 9223372036854775807-out.AmountCents {
			return out, conflict("balance_overflow", "余额超出上限")
		}
		balance += out.AmountCents
		if _, err = tx.Exec(ctx, "UPDATE users SET balance_cents=$1,updated_at=now() WHERE id=$2", balance, userID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET status='refunded',paid_at=NULL WHERE id=$1", invoiceID); err != nil {
		return out, err
	}
	if orderID > 0 {
		if _, err = tx.Exec(ctx, "UPDATE orders SET status='refunded',updated_at=now() WHERE id=$1", orderID); err != nil {
			return out, err
		}
	}
	if orderID == 0 && upgradeID == 0 && kind != "manual" && !traffic && !supplierRenewalRefund {
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
	if !creditPayment {
		if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
VALUES($1,'refund',$2,$3,'invoice',$4,$5,'账单退款')`, userID, out.AmountCents, balance, invoiceID, key); err != nil {
			return out, err
		}
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

// LockFinancialCustomer establishes the shared order for balance/credit payments,
// invoice refunds and confirmed repayments: customer -> invoice -> credit debt/account.
// The owner lookup must not lock the invoice before this call.
func LockFinancialCustomer(ctx context.Context, tx pgx.Tx, userID int64) error {
	var id int64
	return tx.QueryRow(ctx, "SELECT id FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&id)
}
