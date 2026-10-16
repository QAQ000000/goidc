package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

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
	ID                int64      `json:"id"`
	InvoiceID         int64      `json:"invoice_id"`
	PaymentID         int64      `json:"payment_id"`
	UserID            int64      `json:"user_id"`
	AmountCents       int64      `json:"amount_cents"`
	Status            string     `json:"status"`
	Version           int64      `json:"version"`
	Method            string     `json:"method"`
	Provider          string     `json:"provider"`
	ChannelInstanceID string     `json:"channel_instance_id"`
	ProviderPaymentID string     `json:"provider_payment_id"`
	ProviderRefundID  string     `json:"provider_refund_id"`
	ReceivedAt        time.Time  `json:"received_at"`
	EvidenceRef       string     `json:"evidence_ref"`
	EvidenceSHA256    string     `json:"evidence_sha256"`
	Note              string     `json:"note"`
	RequestedBy       int64      `json:"requested_by"`
	ReviewedBy        *int64     `json:"reviewed_by"`
	ReviewNote        string     `json:"review_note"`
	ConfirmedAt       *time.Time `json:"confirmed_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}
type ExternalRefundResult struct {
	Refund   ExternalRefund `json:"refund"`
	Replayed bool           `json:"replayed"`
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
	rows, err := tx.Query(ctx, "SELECT to_jsonb(r) FROM external_refunds r WHERE invoice_id=$1 ORDER BY id DESC LIMIT $2 OFFSET $3", invoiceID, limit, offset)
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
func externalRefundRead(ctx context.Context, tx pgx.Tx, id int64) (ExternalRefund, error) {
	var r ExternalRefund
	var b []byte
	err := tx.QueryRow(ctx, "SELECT to_jsonb(r) FROM external_refunds r WHERE id=$1", id).Scan(&b)
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
	return err
}

func (s *Store) CreateExternalRefund(ctx context.Context, actorID, invoiceID int64, in ExternalRefundInput, key string) (ExternalRefundResult, error) {
	if in.PaymentID < 1 || in.AmountCents < 1 || in.ExpectedVersion < 1 || (in.Method != "original_route" && in.Method != "bank_transfer" && in.Method != "other") || in.ReceivedAt.IsZero() || in.ReceivedAt.After(time.Now()) || strings.TrimSpace(in.ProviderRefundID) == "" || len(in.ProviderRefundID) > 200 || strings.TrimSpace(in.EvidenceRef) == "" || len(in.EvidenceRef) > 500 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(in.EvidenceSHA256) || !contentTextValid(in.Note, 500, true) {
		return ExternalRefundResult{}, conflict("invalid_external_refund", "退款金额、版本、到账时间或证据无效")
	}
	return s.externalRefundWrite(ctx, actorID, invoiceID, 0, false, in, ExternalRefundReviewInput{}, key)
}
func (s *Store) ReviewExternalRefund(ctx context.Context, actorID, invoiceID, refundID int64, confirm bool, in ExternalRefundReviewInput, key string) (ExternalRefundResult, error) {
	if refundID < 1 || in.ExpectedVersion < 1 || strings.TrimSpace(in.Note) == "" || !contentTextValid(in.Note, 500, true) {
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
	var provider, channel, original string
	var paidInvoice, paidUser, paidAmount int64
	var paidStatus string
	var completedAt *time.Time
	if err = tx.QueryRow(ctx, "SELECT provider,coalesce(channel_instance_id,''),provider_payment_id,invoice_id,user_id,amount_cents,status,completed_at FROM payment_transactions WHERE id=$1 FOR UPDATE", payment).Scan(&provider, &channel, &original, &paidInvoice, &paidUser, &paidAmount, &paidStatus, &completedAt); err != nil {
		return out, err
	}
	var amount, version int64
	var status, kind string
	if err = tx.QueryRow(ctx, "SELECT amount_cents,version,status,invoice_kind FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&amount, &version, &status, &kind); err != nil {
		return out, err
	}
	if paidInvoice != invoice || paidUser != customer || paidAmount != amount || paidStatus != "succeeded" || status != "paid" || kind != "manual" {
		return out, conflict("external_refund_not_supported", "仅支持实际外部全额收款的已付普通人工账单")
	}
	if err = requireManualInvoiceValid(ctx, tx, invoice); err != nil {
		return out, err
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
		err = tx.QueryRow(ctx, `INSERT INTO external_refunds(invoice_id,payment_id,user_id,amount_cents,method,provider,channel_instance_id,provider_payment_id,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`, invoice, payment, customer, amount, in.Method, provider, channel, original, in.ProviderRefundID, in.ReceivedAt, in.EvidenceRef, in.EvidenceSHA256, in.Note, actor).Scan(&id)
		if err != nil {
			return out, err
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
			if _, err = tx.Exec(ctx, "UPDATE invoices SET status='refunded' WHERE id=$1", invoice); err != nil {
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
	if err = Audit(ctx, tx, actor, action, "external_refund", id, map[string]any{"invoice_id": invoice, "payment_id": payment, "amount_cents": amount, "status": out.Refund.Status}); err != nil {
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
