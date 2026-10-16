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
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
)

type PaymentCloseVerification struct {
	ID                    int64      `json:"id"`
	CloseOperationID      int64      `json:"close_operation_id"`
	PaymentID             int64      `json:"payment_id"`
	InvoiceID             int64      `json:"invoice_id"`
	UserID                int64      `json:"-"`
	RequestedBy           int64      `json:"requested_by"`
	RequestKeySHA256      string     `json:"request_key_sha256,omitempty"`
	OriginalActorID       int64      `json:"original_actor_id"`
	Provider              string     `json:"provider"`
	ProviderPaymentID     string     `json:"provider_payment_id"`
	OperationID           string     `json:"operation_id"`
	AmountCents           int64      `json:"amount_cents"`
	ExpectedVersion       int64      `json:"expected_version"`
	RequestedAt           time.Time  `json:"requested_at"`
	LeaseUntil            time.Time  `json:"lease_until"`
	ResultStatus          string     `json:"result_status"`
	ResultCode            string     `json:"result_code"`
	ExternalTransactionID string     `json:"external_transaction_id"`
	ProviderEvidenceID    string     `json:"provider_evidence_id"`
	ReceivedAt            *time.Time `json:"received_at"`
	ReviewStatus          string     `json:"review_status"`
	ReviewedBy            *int64     `json:"reviewed_by"`
	ReviewedAt            *time.Time `json:"reviewed_at"`
	ReviewNote            string     `json:"review_note"`
	Version               int64      `json:"version"`
	PaymentStatus         string     `json:"payment_status"`
	InvoiceStatus         string     `json:"invoice_status"`
	Status                string     `json:"status"`
	Replayed              bool       `json:"replayed"`
	CanConfirm            bool       `json:"can_confirm"`
	CanReject             bool       `json:"can_reject"`
}

const closeVerificationSelect = `SELECT r.id,r.close_operation_id,r.payment_id,r.invoice_id,r.user_id,r.actor_id,c.actor_id,r.provider,r.provider_payment_id,r.operation_id,r.amount_cents,r.expected_version,r.created_at,r.lease_until,
COALESCE(v.status,''),COALESCE(v.result_code,''),COALESCE(v.external_transaction_id,''),COALESCE(v.provider_evidence_id,''),v.received_at,
COALESCE(d.status,''),d.actor_id,d.created_at,COALESCE(d.note,''),i.version,p.status,i.status,encode(sha256(convert_to(r.idempotency_key,'UTF8')),'hex')
FROM payment_close_verification_requests r JOIN payment_close_operations c ON c.id=r.close_operation_id
JOIN payment_transactions p ON p.id=r.payment_id JOIN invoices i ON i.id=r.invoice_id
LEFT JOIN payment_close_verification_results v ON v.request_id=r.id LEFT JOIN payment_close_verification_decisions d ON d.request_id=r.id `

func scanCloseVerification(row pgx.Row) (PaymentCloseVerification, error) {
	var v PaymentCloseVerification
	err := row.Scan(&v.ID, &v.CloseOperationID, &v.PaymentID, &v.InvoiceID, &v.UserID, &v.RequestedBy, &v.OriginalActorID, &v.Provider, &v.ProviderPaymentID, &v.OperationID, &v.AmountCents, &v.ExpectedVersion, &v.RequestedAt, &v.LeaseUntil, &v.ResultStatus, &v.ResultCode, &v.ExternalTransactionID, &v.ProviderEvidenceID, &v.ReceivedAt, &v.ReviewStatus, &v.ReviewedBy, &v.ReviewedAt, &v.ReviewNote, &v.Version, &v.PaymentStatus, &v.InvoiceStatus, &v.RequestKeySHA256)
	v.Status = v.ResultStatus
	if v.Status == "" {
		v.Status = "checking"
		if !v.LeaseUntil.After(time.Now()) {
			v.Status = "expired"
		}
	}
	if v.ResultStatus == "closed" || v.ResultStatus == "succeeded" {
		v.Status = "awaiting_review"
	}
	if v.ReviewStatus != "" {
		v.Status = v.ReviewStatus
	}
	return v, err
}
func verificationHash(action string, invoice, paymentID, record, version int64, note string) string {
	raw, _ := json.Marshal([]any{action, invoice, paymentID, record, version, note})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
func validVerificationKey(key string) bool { return len(key) >= 8 && len(key) <= 128 }

func (s *Store) BeginPaymentCloseVerification(ctx context.Context, actor, invoice, paymentID, operation, version int64, key, session string) (PaymentCloseVerification, error) {
	var out PaymentCloseVerification
	if actor < 1 || invoice < 1 || paymentID < 1 || operation < 1 || version < 1 || !validVerificationKey(key) {
		return out, conflict("invalid_close_verification", "核验参数或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, actor, session, invoice, "payments.close_verify"); err != nil {
		return out, err
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, invoice); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "close-verification", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return out, err
	}
	hash := verificationHash("lookup", invoice, paymentID, operation, version, "")
	var existing int64
	var saved string
	err = tx.QueryRow(ctx, "SELECT id,request_hash FROM payment_close_verification_requests WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&existing, &saved)
	if err == nil {
		if saved != hash {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同核验请求")
		}
		out, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1", existing))
		out.Replayed = true
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var state string
	if err = tx.QueryRow(ctx, "SELECT status FROM payment_transactions WHERE id=$1 AND invoice_id=$2 FOR UPDATE", paymentID, invoice).Scan(&state); err != nil {
		return out, err
	}
	var invoiceState string
	var current int64
	if err = tx.QueryRow(ctx, "SELECT status,version FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&invoiceState, &current); err != nil {
		return out, err
	}
	if current != version {
		return out, conflict("stale_invoice", "账单版本已变化，请重新读取")
	}
	if (state != "pending" && state != "expired") || invoiceState != "unpaid" {
		return out, conflict("close_verification_ineligible", "当前支付无需未知关单核验")
	}
	if err = requireNoPaymentCloseAnomaly(ctx, tx, invoice); err != nil {
		return out, err
	}
	var eligible, busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_close_operations WHERE id=$1 AND payment_id=$2 AND payment_close_operation_unresolved(id) AND (status<>'checking' OR lease_until<=clock_timestamp())),
 EXISTS(SELECT 1 FROM payment_reconciliation_checks WHERE payment_id=$2 AND status='checking' AND lease_until>clock_timestamp()) OR
 EXISTS(SELECT 1 FROM payment_close_verification_requests r WHERE r.payment_id=$2 AND r.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM payment_close_verification_results v WHERE v.request_id=r.id))`, operation, paymentID).Scan(&eligible, &busy)
	if err != nil {
		return out, err
	}
	if !eligible {
		return out, conflict("close_verification_ineligible", "原操作仍在执行或已得到最终结果")
	}
	if busy {
		return out, conflict("close_verification_in_progress", "已有核验或查询正在处理")
	}
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO payment_close_verification_requests(close_operation_id,payment_id,invoice_id,user_id,actor_id,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents,credential_sha256,expected_version,idempotency_key,request_hash)
 SELECT id,payment_id,invoice_id,user_id,$2,provider,channel_instance_id,provider_payment_id,operation_id,amount_cents,credential_sha256,$3,$4,$5 FROM payment_close_operations WHERE id=$1 RETURNING id`, operation, actor, version, key, hash).Scan(&id)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "payment.close_verification.requested", "invoice", invoice, map[string]any{"payment_id": paymentID, "verification_id": id, "close_operation_id": operation}); err != nil {
		return out, err
	}
	out, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1", id))
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

// This resolver never falls back to the current merchant for missing snapshots.
func (s *Store) PaymentCloseVerificationGateway(ctx context.Context, v PaymentCloseVerification, fallback PaymentConfiguration) (payment.Gateway, error) {
	var valid bool
	err := s.Pool.QueryRow(ctx, `SELECT octet_length(p.credential_snapshot)>0 AND r.credential_sha256=encode(sha256(p.credential_snapshot),'hex') AND r.channel_instance_id=COALESCE(p.channel_instance_id,'')
 FROM payment_close_verification_requests r JOIN payment_transactions p ON p.id=r.payment_id WHERE r.id=$1`, v.ID).Scan(&valid)
	if err != nil {
		return nil, err
	}
	if !valid {
		return nil, conflict("payment_credentials_unavailable", "原关单凭据快照缺失或变化，继续冻结")
	}
	gateway, enabled, err := s.PaymentTransactionGateway(ctx, v.Provider, v.ProviderPaymentID, fallback)
	if err != nil {
		return nil, err
	}
	if !enabled || gateway == nil {
		return nil, conflict("payment_credentials_unavailable", "原关单凭据不可用，继续冻结")
	}
	return gateway, nil
}

func (s *Store) CompletePaymentCloseVerification(ctx context.Context, v PaymentCloseVerification, result payment.CloseStatusResult, code, session string) (PaymentCloseVerification, error) {
	if code == "" {
		code = "verified"
	}
	status := result.Status
	if status != "closed" && status != "succeeded" && status != "unknown" && status != "unsupported" && status != "error" {
		return v, conflict("invalid_close_verification_result", "核验结果状态无效")
	}
	if status == "closed" || status == "succeeded" || status == "unknown" {
		if result.OperationID != v.OperationID || result.ProviderPaymentID != v.ProviderPaymentID || result.AmountCents != v.AmountCents || len(result.ExternalTransactionID) > 160 || len(result.ProviderEvidenceID) > 160 || (status == "succeeded" && strings.TrimSpace(result.ExternalTransactionID) == "") {
			return v, conflict("invalid_close_verification_result", "原操作、支付、金额或流水不匹配")
		}
	}
	if code != "verified" && code != "provider_unavailable" && code != "provider_invalid" && code != "credentials_unavailable" && code != "authorization_lost" {
		return v, conflict("invalid_close_verification_result", "核验结果码无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return v, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, v.RequestedBy, session, v.InvoiceID, "payments.close_verify"); err != nil {
		return v, err
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, v.InvoiceID); err != nil {
		return v, err
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM payment_transactions WHERE id=$1 FOR UPDATE", v.PaymentID); err != nil {
		return v, err
	}
	var stored PaymentCloseVerification
	stored, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1 AND r.payment_id=$2 AND r.invoice_id=$3 FOR UPDATE OF r", v.ID, v.PaymentID, v.InvoiceID))
	if err != nil {
		return v, err
	}
	if stored.ResultStatus != "" {
		stored.Replayed = true
		return stored, tx.Commit(ctx)
	}
	if stored.RequestedBy != v.RequestedBy || stored.OperationID != v.OperationID || stored.ProviderPaymentID != v.ProviderPaymentID || stored.AmountCents != v.AmountCents || !stored.LeaseUntil.After(time.Now()) {
		return v, conflict("close_verification_expired", "核验租约或关联失效，继续冻结")
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT r.credential_sha256=encode(sha256(p.credential_snapshot),'hex') AND r.channel_instance_id=COALESCE(p.channel_instance_id,'') AND (octet_length(p.credential_snapshot)>0 OR $2 NOT IN ('closed','succeeded')) FROM payment_close_verification_requests r JOIN payment_transactions p ON p.id=r.payment_id WHERE r.id=$1`, v.ID, status).Scan(&valid)
	if err != nil {
		return v, err
	}
	if !valid {
		return v, conflict("payment_credentials_changed", "原关单凭据快照不匹配")
	}
	raw, _ := json.Marshal(result)
	digest := sha256.Sum256(raw)
	if _, err = tx.Exec(ctx, `INSERT INTO payment_close_verification_results(request_id,status,result_code,external_transaction_id,provider_evidence_id,result,result_sha256) VALUES($1,$2,$3,$4,$5,$6,$7)`, v.ID, status, code, result.ExternalTransactionID, result.ProviderEvidenceID, raw, hex.EncodeToString(digest[:])); err != nil {
		return v, err
	}
	if err = Audit(ctx, tx, v.RequestedBy, "payment.close_verification.recorded", "invoice", v.InvoiceID, map[string]any{"payment_id": v.PaymentID, "verification_id": v.ID, "status": status, "code": code}); err != nil {
		return v, err
	}
	stored, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1", v.ID))
	if err != nil {
		return v, err
	}
	return stored, tx.Commit(ctx)
}

func (s *Store) ReviewPaymentCloseVerification(ctx context.Context, actor, invoice, paymentID, id, version int64, confirm bool, note, key, session string) (PaymentCloseVerification, error) {
	var out PaymentCloseVerification
	if actor < 1 || invoice < 1 || paymentID < 1 || id < 1 || version < 1 || !validVerificationKey(key) || utf8.RuneCountInString(strings.TrimSpace(note)) < 3 || utf8.RuneCountInString(note) > 1000 {
		return out, conflict("invalid_close_verification_review", "版本、说明或幂等键无效")
	}
	action := "reject"
	if confirm {
		action = "confirm"
	}
	hash := verificationHash(action, invoice, paymentID, id, version, note)
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, actor, session, invoice, "payments.close_review"); err != nil {
		return out, err
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, invoice); err != nil {
		return out, err
	}
	if err = lockOperationKey(ctx, tx, "close-verification-review", fmt.Sprintf("%d:%s", actor, key)); err != nil {
		return out, err
	}
	var saved string
	var existing int64
	err = tx.QueryRow(ctx, "SELECT request_id,request_hash FROM payment_close_verification_decisions WHERE actor_id=$1 AND idempotency_key=$2", actor, key).Scan(&existing, &saved)
	if err == nil {
		if saved != hash || existing != id {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同复核请求")
		}
		out, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1", id))
		out.Replayed = true
		if err != nil {
			return out, err
		}
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	// Match the callback order even if a channel reuses our stable event ID:
	// event advisory before payment row, never request it first while holding p.
	var eventProvider, eventExternal string
	if err = tx.QueryRow(ctx, `SELECT r.provider,COALESCE(v.external_transaction_id,'') FROM payment_close_verification_requests r LEFT JOIN payment_close_verification_results v ON v.request_id=r.id WHERE r.id=$1 AND r.payment_id=$2 AND r.invoice_id=$3`, id, paymentID, invoice).Scan(&eventProvider, &eventExternal); err != nil {
		return out, err
	}
	if confirm && eventExternal != "" {
		if err = lockOperationKey(ctx, tx, "payment-webhook", eventProvider+":close-verification:"+fmt.Sprint(id)+":"+eventExternal); err != nil {
			return out, err
		}
	}
	var locked int64
	if err = tx.QueryRow(ctx, "SELECT id FROM payment_transactions WHERE id=$1 AND invoice_id=$2 FOR UPDATE", paymentID, invoice).Scan(&locked); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&locked); err != nil {
		return out, err
	}
	out, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1 AND r.payment_id=$2 AND r.invoice_id=$3 FOR UPDATE OF r", id, paymentID, invoice))
	if err != nil {
		return out, err
	}
	if actor == out.RequestedBy || actor == out.OriginalActorID {
		return out, conflict("close_verification_self_review", "核验申请人和原关单员工不能复核自己的操作")
	}
	if out.ReviewStatus != "" {
		return out, conflict("close_verification_reviewed", "核验已经复核")
	}
	if version != out.Version {
		return out, conflict("stale_invoice", "账单版本已变化，请重新读取")
	}
	if out.ResultStatus == "" {
		return out, conflict("close_verification_unverified", "核验尚未完成")
	}
	var valid, alreadyApplied bool
	err = tx.QueryRow(ctx, `SELECT r.credential_sha256=encode(sha256(p.credential_snapshot),'hex') AND r.channel_instance_id=COALESCE(p.channel_instance_id,'') AND p.provider=r.provider AND p.provider_payment_id=r.provider_payment_id AND p.amount_cents=r.amount_cents,
 EXISTS(SELECT 1 FROM payment_close_verification_decisions WHERE close_operation_id=r.close_operation_id AND status<>'rejected')
 FROM payment_close_verification_requests r JOIN payment_transactions p ON p.id=r.payment_id WHERE r.id=$1`, id).Scan(&valid, &alreadyApplied)
	if err != nil {
		return out, err
	}
	if !valid {
		return out, conflict("payment_credentials_changed", "原关单凭据或关联已变化")
	}
	if alreadyApplied {
		return out, conflict("close_verification_resolved", "原操作已有有效复核结果")
	}
	decision := "rejected"
	if confirm {
		if out.ResultStatus != "closed" && out.ResultStatus != "succeeded" {
			return out, conflict("close_verification_unverified", "渠道没有可信最终结果，不能解除保护")
		}
		if err = requireNoPaymentCloseAnomaly(ctx, tx, invoice); err != nil {
			return out, err
		}
		if out.PaymentStatus == "succeeded" {
			var external string
			if err = tx.QueryRow(ctx, "SELECT external_transaction_id FROM payment_transactions WHERE id=$1", paymentID).Scan(&external); err != nil {
				return out, err
			}
			if strings.TrimSpace(external) == "" {
				return out, conflict("payment_transaction_mismatch", "原成功支付缺少可信渠道流水，不能生成解决回执")
			}
			if out.ResultStatus == "succeeded" && external != out.ExternalTransactionID {
				return out, conflict("payment_transaction_mismatch", "可信流水与已结算渠道流水不一致")
			}
			decision = "already_paid"
			if out.ResultStatus == "closed" {
				decision = "superseded_paid"
			}
		} else {
			if (out.PaymentStatus != "pending" && out.PaymentStatus != "expired") || out.InvoiceStatus != "unpaid" {
				return out, conflict("payment_state_conflict", "账单或支付状态已变化")
			}
			if out.ResultStatus == "closed" {
				if _, err = tx.Exec(ctx, "UPDATE payment_transactions SET status='closed',completed_at=clock_timestamp(),next_reconcile_at=NULL,updated_at=clock_timestamp() WHERE id=$1", paymentID); err != nil {
					return out, err
				}
				if err = tx.QueryRow(ctx, "UPDATE invoices SET updated_at=clock_timestamp() WHERE id=$1 RETURNING version", invoice).Scan(&out.Version); err != nil {
					return out, err
				}
				decision = "applied_closed"
			} else {
				raw, _ := json.Marshal(map[string]any{"source": "close_verification", "verification_id": id, "operation_id": out.OperationID, "external_transaction_id": out.ExternalTransactionID, "amount_cents": out.AmountCents})
				topup, err := lockOnlineTopupCustomer(ctx, tx, invoice)
				if err != nil {
					return out, err
				}
				_, err = s.processPaymentWebhookTx(ctx, tx, out.Provider, "close-verification:"+fmt.Sprint(id)+":"+out.ExternalTransactionID, out.ProviderPaymentID, invoice, out.AmountCents, "succeeded", raw, []string{out.ExternalTransactionID}, nil, topup)
				if err != nil {
					return out, err
				}
				if err = tx.QueryRow(ctx, "SELECT version FROM invoices WHERE id=$1", invoice).Scan(&out.Version); err != nil {
					return out, err
				}
				decision = "applied_paid"
			}
		}
	}
	if _, err = tx.Exec(ctx, `INSERT INTO payment_close_verification_decisions(request_id,close_operation_id,actor_id,status,expected_version,invoice_version,idempotency_key,request_hash,note) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, id, out.CloseOperationID, actor, decision, version, out.Version, key, hash, note); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "payment.close_verification."+decision, "invoice", invoice, map[string]any{"payment_id": paymentID, "verification_id": id, "close_operation_id": out.CloseOperationID}); err != nil {
		return out, err
	}
	out, err = scanCloseVerification(tx.QueryRow(ctx, closeVerificationSelect+"WHERE r.id=$1", id))
	if err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

type PaymentCloseResolution struct {
	VerificationID        int64  `json:"verification_id"`
	DecisionID            int64  `json:"decision_id"`
	CloseOperationID      int64  `json:"close_operation_id"`
	OperationID           string `json:"operation_id"`
	Status                string `json:"status"`
	PaymentStatus         string `json:"payment_status"`
	Version               int64  `json:"version"`
	ExternalTransactionID string `json:"external_transaction_id"`
}

func paymentCloseResolution(ctx context.Context, tx pgx.Tx, paymentID, operation int64) (*PaymentCloseResolution, error) {
	var out PaymentCloseResolution
	err := tx.QueryRow(ctx, `SELECT r.id,d.id,c.id,c.operation_id,d.status,p.status,i.version,p.external_transaction_id
 FROM payment_close_verification_decisions d JOIN payment_close_verification_requests r ON r.id=d.request_id
 JOIN payment_close_operations c ON c.id=d.close_operation_id JOIN payment_transactions p ON p.id=r.payment_id JOIN invoices i ON i.id=r.invoice_id
 WHERE r.payment_id=$1 AND ($2::bigint=0 OR c.id=$2) AND d.status<>'rejected' ORDER BY d.id DESC LIMIT 1`, paymentID, operation).Scan(&out.VerificationID, &out.DecisionID, &out.CloseOperationID, &out.OperationID, &out.Status, &out.PaymentStatus, &out.Version, &out.ExternalTransactionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &out, err
}
func (s *Store) PaymentCloseVerificationResolution(ctx context.Context, paymentID int64) (*PaymentCloseResolution, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	out, err := paymentCloseResolution(ctx, tx, paymentID, 0)
	if err != nil {
		return nil, err
	}
	return out, tx.Commit(ctx)
}
func (s *Store) ListPaymentCloseVerifications(ctx context.Context, actor, invoice, paymentID int64, session string, limit, offset int) (map[string]any, error) {
	if limit < 1 || limit > 100 || offset < 0 {
		return nil, conflict("invalid_pagination", "分页参数无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if err = authorizePaymentClose(ctx, tx, actor, session, invoice, "invoices.view"); err != nil {
		return nil, err
	}
	var version int64
	var state, invoiceState, provider, reference string
	var snapshot []byte
	err = tx.QueryRow(ctx, `SELECT i.version,p.status,i.status,p.provider,p.provider_payment_id,p.credential_snapshot FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1 AND i.id=$2`, paymentID, invoice).Scan(&version, &state, &invoiceState, &provider, &reference, &snapshot)
	if err != nil {
		return nil, err
	}
	canVerify := authorizePaymentClose(ctx, tx, actor, session, invoice, "payments.close_verify") == nil
	canReview := authorizePaymentClose(ctx, tx, actor, session, invoice, "payments.close_review") == nil
	// Use only a non-empty original snapshot for capability discovery.
	supported := false
	if len(snapshot) > 0 {
		g, enabled, e := s.PaymentTransactionGatewayTx(ctx, tx, provider, reference, PaymentConfiguration{})
		_, ok := g.(payment.CloseStatusGateway)
		supported = e == nil && enabled && ok
	}
	var anomaly, busy bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_close_anomalies WHERE invoice_id=$1 AND payment_close_anomaly_unresolved(id)),
 EXISTS(SELECT 1 FROM payment_close_verification_requests r WHERE r.payment_id=$2 AND r.lease_until>clock_timestamp() AND NOT EXISTS(SELECT 1 FROM payment_close_verification_results v WHERE v.request_id=r.id)) OR EXISTS(SELECT 1 FROM payment_reconciliation_checks WHERE payment_id=$2 AND status='checking' AND lease_until>clock_timestamp())`, invoice, paymentID).Scan(&anomaly, &busy)
	if err != nil {
		return nil, err
	}
	operations := []map[string]any{}
	opRows, err := tx.Query(ctx, `SELECT id,operation_id,status,lease_until,payment_close_operation_unresolved(id) AND (status<>'checking' OR lease_until<=clock_timestamp()) FROM payment_close_operations WHERE payment_id=$1 ORDER BY id DESC LIMIT 50`, paymentID)
	if err != nil {
		return nil, err
	}
	for opRows.Next() {
		var id int64
		var external, status string
		var lease time.Time
		var eligible bool
		if err = opRows.Scan(&id, &external, &status, &lease, &eligible); err != nil {
			opRows.Close()
			return nil, err
		}
		operations = append(operations, map[string]any{"id": id, "operation_id": external, "status": status, "lease_until": lease, "can_verify": canVerify && supported && eligible && !anomaly && !busy && invoiceState == "unpaid"})
	}
	err = opRows.Err()
	opRows.Close()
	if err != nil {
		return nil, err
	}
	var total int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM payment_close_verification_requests WHERE payment_id=$1", paymentID).Scan(&total); err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, closeVerificationSelect+"WHERE r.payment_id=$1 ORDER BY r.id DESC LIMIT $2 OFFSET $3", paymentID, limit, offset)
	if err != nil {
		return nil, err
	}
	items := []PaymentCloseVerification{}
	for rows.Next() {
		v, e := scanCloseVerification(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		if actor != v.RequestedBy {
			v.RequestKeySHA256 = ""
		}
		independent := canReview && actor != v.RequestedBy && actor != v.OriginalActorID && v.ReviewStatus == ""
		v.CanReject = independent && v.ResultStatus != ""
		v.CanConfirm = independent && !anomaly && (v.ResultStatus == "closed" || v.ResultStatus == "succeeded") && (state == "succeeded" || ((state == "pending" || state == "expired") && invoiceState == "unpaid"))
		items = append(items, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	resolution, err := paymentCloseResolution(ctx, tx, paymentID, 0)
	if err != nil {
		return nil, err
	}
	if resolution != nil {
		for i := range items {
			items[i].CanConfirm = false
			items[i].CanReject = false
		}
	}
	canRequest := false
	for _, op := range operations {
		if op["can_verify"] == true {
			canRequest = true
		}
	}
	out := map[string]any{"invoice_id": invoice, "payment_id": paymentID, "version": version, "lookup_supported": supported, "can_request": canRequest, "operations": operations, "effective_resolution": resolution, "items": items, "total": total, "limit": limit, "offset": offset}
	return out, tx.Commit(ctx)
}

func (s *Store) PaymentCloseResolutionTx(ctx context.Context, tx pgx.Tx, paymentID int64) (*PaymentCloseResolution, error) {
	return paymentCloseResolution(ctx, tx, paymentID, 0)
}
