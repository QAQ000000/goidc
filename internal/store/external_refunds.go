package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/codex/zjmfgo/internal/payment"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var externalRefundEvidenceHash = regexp.MustCompile(`^[0-9a-f]{64}$`)

// merchantScope uses only immutable, non-secret original merchant identity.
func (s *Store) externalRefundMerchantScope(provider, reference string, sealed []byte) (string, error) {
	if provider == "mock" {
		return "mock:test-only", nil
	}
	unavailable := func() (string, error) {
		return "", conflict("external_refund_merchant_unavailable", "原支付商户身份不可核验，不能登记或确认退款")
	}
	if !strings.HasPrefix(provider, "epay_") || len(sealed) == 0 {
		return unavailable()
	}
	raw, err := s.paymentCredentials(reference, sealed, false)
	if err != nil {
		return unavailable()
	}
	var cfg struct {
		payment.EpayConfig
		Unavailable bool `json:"unavailable"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Unavailable || strings.TrimSpace(cfg.PID) == "" {
		return unavailable()
	}
	u, err := url.Parse(strings.TrimSpace(cfg.APIURL))
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Hostname() == "" {
		return unavailable()
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return unavailable()
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	canonical := scheme + "://" + host + strings.TrimRight(u.EscapedPath(), "/")
	sum := sha256.Sum256([]byte("epay\x00" + canonical + "\x00" + strings.TrimSpace(cfg.PID)))
	return "epay:" + hex.EncodeToString(sum[:]), nil
}

type externalRefundSessionKey struct{}

func WithExternalRefundSession(ctx context.Context, hash string) context.Context {
	return context.WithValue(ctx, externalRefundSessionKey{}, hash)
}

type ExternalRefundInput struct {
	PaymentID        int64     `json:"payment_id"`
	AmountCents      int64     `json:"amount_cents"`
	Method           string    `json:"method"`
	ProviderRefundID string    `json:"provider_refund_id"`
	ReceivedAt       time.Time `json:"received_at"`
	EvidenceRef      string    `json:"evidence_ref"`
	EvidenceSHA256   string    `json:"evidence_sha256"`
	Note             string    `json:"note"`
	ExpectedVersion  int64     `json:"expected_version"`
}
type ExternalRefundReviewInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	Note            string `json:"note"`
}
type ExternalRefund struct {
	ID                    int64                   `json:"id"`
	InvoiceID             int64                   `json:"invoice_id"`
	PaymentID             int64                   `json:"payment_id"`
	UserID                int64                   `json:"user_id"`
	AmountCents           int64                   `json:"amount_cents"`
	Status                string                  `json:"status"`
	Version               int64                   `json:"version"`
	Method                string                  `json:"method"`
	Provider              string                  `json:"provider"`
	ChannelInstanceID     string                  `json:"channel_instance_id"`
	MerchantScope         string                  `json:"merchant_scope"`
	OriginalTransactionID string                  `json:"original_transaction_id"`
	ProviderPaymentID     string                  `json:"provider_payment_id"`
	ProviderRefundID      string                  `json:"provider_refund_id"`
	ReceivedAt            time.Time               `json:"received_at"`
	EvidenceRef           string                  `json:"evidence_ref"`
	EvidenceSHA256        string                  `json:"evidence_sha256"`
	Note                  string                  `json:"note"`
	RequestedBy           int64                   `json:"requested_by"`
	ReviewedBy            *int64                  `json:"reviewed_by"`
	ReviewNote            string                  `json:"review_note"`
	ConfirmedAt           *time.Time              `json:"confirmed_at"`
	CreatedAt             time.Time               `json:"created_at"`
	UpdatedAt             time.Time               `json:"updated_at"`
	Reversal              *ExternalRefundReversal `json:"reversal,omitempty"`
}
type ExternalRefundReversal struct {
	Status      string     `json:"status"`
	AmountCents int64      `json:"amount_cents"`
	LedgerID    *int64     `json:"ledger_id"`
	AppliedAt   *time.Time `json:"applied_at"`
	ReleasedAt  *time.Time `json:"released_at"`
}
type ExternalRefundResult struct {
	Refund   ExternalRefund `json:"refund"`
	Replayed bool           `json:"replayed"`
}

// reserveExternalRefundBalance freezes spendable balance for a top-up refund
// while the independent reviewer checks the merchant evidence.  The customer
// row is already held by LockFinancialCustomer, so this read cannot race a
// balance payment, top-up liability resolution, or another reservation.
func reserveExternalRefundBalance(ctx context.Context, tx pgx.Tx, refundID, invoiceID, userID, amount int64) error {
	var balance, reserved int64
	if err := tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&balance); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, "SELECT coalesce(sum(amount_cents),0)::bigint FROM external_refund_reversal_reservations WHERE user_id=$1 AND status='pending'", userID).Scan(&reserved); err != nil {
		return err
	}
	if reserved > balance || amount > balance-reserved {
		return conflict("external_refund_balance_reserved", "客户余额不足，无法冻结外部退款冲正金额")
	}
	_, err := tx.Exec(ctx, `INSERT INTO external_refund_reversal_reservations(refund_id,invoice_id,user_id,amount_cents) VALUES($1,$2,$3,$4)`, refundID, invoiceID, userID, amount)
	return externalRefundDBError(err)
}

func releaseExternalRefundReservation(ctx context.Context, tx pgx.Tx, refundID int64) error {
	_, err := tx.Exec(ctx, `UPDATE external_refund_reversal_reservations SET status='released',released_at=clock_timestamp() WHERE refund_id=$1 AND status='pending'`, refundID)
	return err
}

// applyExternalRefundReversal performs the only supported balance reversal.
// It intentionally does not change the immutable gateway receipt or create a
// second payment/refund fact.  The customer lock is held by the caller.
func applyExternalRefundReversal(ctx context.Context, tx pgx.Tx, actorID, refundID, invoiceID, userID, amount int64) error {
	var reservedAmount int64
	var reservationStatus string
	if err := tx.QueryRow(ctx, `SELECT amount_cents,status FROM external_refund_reversal_reservations WHERE refund_id=$1 AND invoice_id=$2 AND user_id=$3 FOR UPDATE`, refundID, invoiceID, userID).Scan(&reservedAmount, &reservationStatus); err != nil {
		return conflict("external_refund_reversal_missing", "缺少充值退款余额冻结事实")
	}
	if reservationStatus != "pending" || reservedAmount != amount {
		return conflict("external_refund_reversal_state", "充值退款余额冻结状态已变化")
	}
	var balance int64
	if err := tx.QueryRow(ctx, "SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE", userID).Scan(&balance); err != nil {
		return err
	}
	if balance < amount {
		return conflict("external_refund_balance_insufficient", "客户余额低于已冻结的外部退款金额，需人工核对")
	}
	newBalance := balance - amount
	if _, err := tx.Exec(ctx, "UPDATE users SET balance_cents=$2,updated_at=clock_timestamp() WHERE id=$1", userID, newBalance); err != nil {
		return err
	}
	var ledgerID int64
	if err := tx.QueryRow(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
VALUES($1,'refund',$2,$3,'invoice',$4,$5,'外部退款余额冲正') RETURNING id`, userID, -amount, newBalance, invoiceID, fmt.Sprintf("external-refund-balance-%d", refundID)).Scan(&ledgerID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE external_refund_reversal_reservations SET status='applied',ledger_id=$2,applied_at=clock_timestamp() WHERE refund_id=$1 AND status='pending'`, refundID, ledgerID); err != nil {
		return err
	}
	return Audit(ctx, tx, actorID, "external_refund.balance_reversed", "external_refund", refundID, map[string]any{"invoice_id": invoiceID, "amount_cents": amount, "ledger_id": ledgerID})
}

func externalRefundAuthorize(ctx context.Context, tx pgx.Tx, actor int64, permission string, session bool) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, "SELECT role,admin_role_id FROM users WHERE id=$1 AND status='active' FOR SHARE", actor).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "员工无效")
	}
	if role != "super_admin" {
		if role != "admin" || roleID == nil {
			return conflict("permission_denied", "没有外部退款权限")
		}
		var allowed bool
		if err := tx.QueryRow(ctx, "SELECT active AND $2=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID, permission).Scan(&allowed); err != nil {
			return err
		}
		if !allowed {
			return conflict("permission_denied", "没有外部退款权限")
		}
	}
	if session {
		var id int64
		if err := tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", ctx.Value(externalRefundSessionKey{}), actor).Scan(&id); err != nil {
			return conflict("permission_denied", "外部退款需要有效浏览器会话")
		}
	}
	return nil
}
func (s *Store) ListExternalRefunds(ctx context.Context, actorID, invoiceID int64, limit, offset int) ([]ExternalRefund, int64, error) {
	out := []ExternalRefund{}
	if limit < 1 || limit > 100 || offset < 0 {
		return out, 0, conflict("invalid_pagination", "分页参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, 0, err
	}
	defer tx.Rollback(ctx)
	if err = externalRefundAuthorize(ctx, tx, actorID, "external_refunds.view", false); err != nil {
		return out, 0, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "invoice", invoiceID); err != nil {
		return out, 0, err
	}
	var total int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM external_refunds WHERE invoice_id=$1", invoiceID).Scan(&total); err != nil {
		return out, 0, err
	}
	rows, err := tx.Query(ctx, "SELECT "+externalRefundJSON+" FROM external_refunds r WHERE invoice_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3", invoiceID, limit, offset)
	if err != nil {
		return out, 0, err
	}
	for rows.Next() {
		var b []byte
		var r ExternalRefund
		if err = rows.Scan(&b); err != nil {
			break
		}
		if err = json.Unmarshal(b, &r); err != nil {
			break
		}
		out = append(out, r)
	}
	rows.Close()
	if err != nil {
		return out, 0, err
	}
	if err = rows.Err(); err != nil {
		return out, 0, err
	}
	return out, total, tx.Commit(ctx)
}

const externalRefundJSON = `to_jsonb(r)||jsonb_build_object('reversal',(SELECT jsonb_build_object('status',v.status,'amount_cents',v.amount_cents,'ledger_id',v.ledger_id,'applied_at',v.applied_at,'released_at',v.released_at) FROM external_refund_reversal_reservations v WHERE v.refund_id=r.id))`

func externalRefundRead(ctx context.Context, tx pgx.Tx, id int64) (ExternalRefund, error) {
	var r ExternalRefund
	var b []byte
	err := tx.QueryRow(ctx, "SELECT "+externalRefundJSON+" FROM external_refunds r WHERE id=$1", id).Scan(&b)
	if err == nil {
		err = json.Unmarshal(b, &r)
	}
	return r, err
}
func externalRefundDBError(err error) error {
	var e *pgconn.PgError
	if errors.As(err, &e) && e.Code == "23505" {
		return conflict("external_refund_duplicate", "该账单、支付或退款流水已登记")
	}
	if errors.As(err, &e) && e.Code == "23514" && e.Message == "original transaction is already claimed" {
		return conflict("external_refund_duplicate", "该原交易已被其他收款或退款占用")
	}
	return err
}

func (s *Store) CreateExternalRefund(ctx context.Context, actorID, invoiceID int64, in ExternalRefundInput, key string) (ExternalRefundResult, error) {
	in.ProviderRefundID = strings.TrimSpace(in.ProviderRefundID)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.Note = strings.TrimSpace(in.Note)
	if in.PaymentID < 1 || in.AmountCents < 1 || in.ExpectedVersion < 1 || (in.Method != "original_route" && in.Method != "bank_transfer" && in.Method != "other") || in.ReceivedAt.IsZero() || in.ReceivedAt.After(time.Now()) || strings.TrimSpace(in.ProviderRefundID) == "" || !contentTextValid(in.ProviderRefundID, 200, false) || strings.TrimSpace(in.EvidenceRef) == "" || !contentTextValid(in.EvidenceRef, 500, false) || !externalRefundEvidenceHash.MatchString(in.EvidenceSHA256) || in.Note == "" || !contentTextValid(in.Note, 1000, true) {
		return ExternalRefundResult{}, conflict("invalid_external_refund", "退款金额、版本、到账时间或证据无效")
	}
	return s.externalRefundWrite(ctx, actorID, invoiceID, 0, false, in, ExternalRefundReviewInput{}, key)
}
func (s *Store) ReviewExternalRefund(ctx context.Context, actorID, invoiceID, refundID int64, confirm bool, in ExternalRefundReviewInput, key string) (ExternalRefundResult, error) {
	if refundID < 1 || in.ExpectedVersion < 1 || strings.TrimSpace(in.Note) == "" || !contentTextValid(in.Note, 1000, true) {
		return ExternalRefundResult{}, conflict("invalid_external_refund_review", "退款版本或审核说明无效")
	}
	return s.externalRefundWrite(ctx, actorID, invoiceID, refundID, confirm, ExternalRefundInput{}, in, key)
}
func (s *Store) externalRefundWrite(ctx context.Context, actor, invoice, id int64, confirm bool, in ExternalRefundInput, review ExternalRefundReviewInput, key string) (out ExternalRefundResult, err error) {
	defer func() { err = externalRefundDBError(err) }()
	if actor < 1 || invoice < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_external_refund", "账单或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	permission := "external_refunds.create"
	if id > 0 {
		permission = "external_refunds.review"
	}
	if err = externalRefundAuthorize(ctx, tx, actor, permission, true); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	var customer int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM invoices WHERE id=$1", invoice).Scan(&customer); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('invoice-customer:'||$1::bigint::text,0))", customer); err != nil {
		return out, err
	}
	if err = LockFinancialCustomer(ctx, tx, customer); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "external-refund", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return out, err
	}
	b, _ := json.Marshal([]any{invoice, id, confirm, in, review})
	h := sha256.Sum256(b)
	hash := hex.EncodeToString(h[:])
	var oldHash string
	var result []byte
	err = tx.QueryRow(ctx, "SELECT request_hash,result FROM external_refund_operations WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&oldHash, &result)
	if err == nil {
		if oldHash != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同请求")
		}
		if err = json.Unmarshal(result, &out); err != nil {
			return out, err
		}
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	payment := in.PaymentID
	if id > 0 {
		if err = tx.QueryRow(ctx, "SELECT payment_id FROM external_refunds WHERE id=$1 AND invoice_id=$2", id, invoice).Scan(&payment); err != nil {
			return out, err
		}
	}
	// Callback order is payment transaction, then invoice. Customer advisory lock
	// serializes invoice-application creation and other financial mutations.
	var provider, channel, original, originalTransaction string
	var paidInvoice, paidUser, paidAmount int64
	var paidStatus string
	var completedAt *time.Time
	var sealed []byte
	var merchantScope string
	if err = tx.QueryRow(ctx, "SELECT provider,coalesce(channel_instance_id,''),provider_payment_id,external_transaction_id,invoice_id,user_id,amount_cents,status,completed_at,credential_snapshot FROM payment_transactions WHERE id=$1 FOR UPDATE", payment).Scan(&provider, &channel, &original, &originalTransaction, &paidInvoice, &paidUser, &paidAmount, &paidStatus, &completedAt, &sealed); err != nil {
		return out, err
	}
	var amount, version int64
	var status, kind string
	if err = tx.QueryRow(ctx, "SELECT amount_cents,version,status,invoice_kind FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&amount, &version, &status, &kind); err != nil {
		return out, err
	}
	// Rejection closes a pending record without money movement; changed business
	// eligibility must never prevent an independent reviewer from rejecting it.
	if id == 0 || confirm {
		if merchantScope, err = s.externalRefundMerchantScope(provider, original, sealed); err != nil {
			return out, err
		}
		if id > 0 {
			var storedScope string
			if err = tx.QueryRow(ctx, "SELECT merchant_scope FROM external_refunds WHERE id=$1", id).Scan(&storedScope); err != nil {
				return out, err
			}
			if storedScope != merchantScope {
				return out, conflict("external_refund_merchant_unavailable", "原支付商户身份已变化")
			}
		}
		if paidInvoice != invoice || paidUser != customer || paidAmount != amount || paidStatus != "succeeded" || status != "paid" || (kind != "manual" && kind != "topup") {
			return out, conflict("external_refund_not_supported", "仅支持实际外部全额收款的已付人工账单或已入账充值")
		}
		if kind == "manual" {
			if err = requireManualInvoiceValid(ctx, tx, invoice); err != nil {
				return out, err
			}
		} else {
			var topupStatus, settlementStatus string
			var settlementLedger *int64
			if err = tx.QueryRow(ctx, `SELECT t.status,s.status,s.ledger_id FROM balance_topups t LEFT JOIN balance_topup_settlements s ON s.topup_id=t.id WHERE t.invoice_id=$1 FOR UPDATE OF t`, invoice).Scan(&topupStatus, &settlementStatus, &settlementLedger); err != nil {
				return out, conflict("external_refund_topup_not_credited", "仅支持原结算直接入账的充值；待补账或后补账充值需专项核对")
			}
			if topupStatus != "credited" || settlementStatus != "credited" || settlementLedger == nil {
				return out, conflict("external_refund_topup_not_credited", "仅支持原结算直接入账的充值；待补账或后补账充值需专项核对")
			}
		}
		if err = requireInvoiceReviewed(ctx, tx, invoice); err != nil {
			return out, err
		}
		var blocked bool
		if err = tx.QueryRow(ctx, `SELECT
 (SELECT count(*)<>1 FROM payment_transactions WHERE invoice_id=$1 AND status='succeeded')
 OR EXISTS(SELECT 1 FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1 AND entry_type IN ('payment','refund'))
 OR EXISTS(SELECT 1 FROM credit_invoice_payments WHERE invoice_id=$1)
 OR EXISTS(SELECT 1 FROM traffic_orders WHERE invoice_id=$1)
 OR EXISTS(SELECT 1 FROM service_configuration_changes WHERE invoice_id=$1)
 OR EXISTS(SELECT 1 FROM affiliate_commission_entries WHERE invoice_id=$1)
 OR EXISTS(SELECT 1 FROM invoice_application_items x JOIN invoice_applications a ON a.id=x.application_id WHERE x.source_invoice_id=$1 AND a.status IN ('pending','issued'))`, invoice).Scan(&blocked); err != nil {
			return out, err
		}
		if blocked {
			return out, conflict("external_refund_not_supported", "账单存在混合收款、业务关联或有效发票申请")
		}
	}

	action := "external_refund.created"
	if id == 0 {
		if completedAt == nil || in.ReceivedAt.Before(*completedAt) {
			return out, conflict("invalid_external_refund", "退款到账时间不能早于原支付完成时间")
		}
		if version != in.ExpectedVersion {
			return out, conflict("version_conflict", "账单版本已变化")
		}
		if amount != in.AmountCents {
			return out, conflict("external_refund_amount_mismatch", "仅支持原单全额退款")
		}
		err = tx.QueryRow(ctx, `INSERT INTO external_refunds(invoice_id,payment_id,user_id,amount_cents,method,provider,channel_instance_id,provider_payment_id,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by,original_transaction_id,merchant_scope) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16) RETURNING id`, invoice, payment, customer, amount, in.Method, provider, channel, original, in.ProviderRefundID, in.ReceivedAt, in.EvidenceRef, in.EvidenceSHA256, in.Note, actor, originalTransaction, merchantScope).Scan(&id)
		if err != nil {
			return out, err
		}
		if kind == "topup" {
			if err = reserveExternalRefundBalance(ctx, tx, id, invoice, customer, amount); err != nil {
				return out, err
			}
		}
	} else {
		var requested, rv int64
		var rs string
		if err = tx.QueryRow(ctx, "SELECT requested_by,version,status FROM external_refunds WHERE id=$1 FOR UPDATE", id).Scan(&requested, &rv, &rs); err != nil {
			return out, err
		}
		if requested == actor {
			return out, conflict("external_refund_self_review", "禁止审核本人登记的退款")
		}
		if rv != review.ExpectedVersion {
			return out, conflict("version_conflict", "退款版本已变化")
		}
		if rs != "pending_review" {
			return out, conflict("external_refund_final", "退款已经复核")
		}
		next := "rejected"
		action = "external_refund.rejected"
		if confirm {
			next = "confirmed"
			action = "external_refund.confirmed"
			if kind == "topup" {
				if err = applyExternalRefundReversal(ctx, tx, actor, id, invoice, customer, amount); err != nil {
					return out, err
				}
			}
			if _, err = tx.Exec(ctx, "UPDATE invoices SET status='refunded' WHERE id=$1", invoice); err != nil {
				return out, err
			}
		} else if kind == "topup" {
			if err = releaseExternalRefundReservation(ctx, tx, id); err != nil {
				return out, err
			}
		}
		if _, err = tx.Exec(ctx, "UPDATE external_refunds SET status=$2,version=version+1,reviewed_by=$3,review_note=$4,confirmed_at=CASE WHEN $2='confirmed' THEN clock_timestamp() END,updated_at=clock_timestamp() WHERE id=$1", id, next, actor, review.Note); err != nil {
			return out, err
		}
	}
	if out.Refund, err = externalRefundRead(ctx, tx, id); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, action, "external_refund", id, map[string]any{"invoice_id": invoice, "payment_id": payment, "amount_cents": out.Refund.AmountCents, "status": out.Refund.Status}); err != nil {
		return out, err
	}
	result, err = json.Marshal(out)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO external_refund_operations(actor_id,idempotency_key,invoice_id,refund_id,request_hash,result) VALUES($1,$2,$3,$4,$5,$6)", actor, key, invoice, id, hash, result); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
