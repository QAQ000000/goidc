package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type PaymentAnomalyRefundInput struct {
	Method               string    `json:"method"`
	ProviderRefundID     string    `json:"provider_refund_id"`
	ReceivedAt           time.Time `json:"received_at"`
	EvidenceRef          string    `json:"evidence_ref"`
	EvidenceSHA256       string    `json:"evidence_sha256"`
	Note                 string    `json:"note"`
	ExpectedGroupVersion int64     `json:"expected_group_version"`
}
type PaymentAnomalyReviewInput struct {
	ExpectedVersion      int64  `json:"expected_version"`
	ExpectedGroupVersion int64  `json:"expected_group_version"`
	Note                 string `json:"note"`
}
type PaymentAnomalyRefund struct {
	ID                    int64      `json:"id"`
	AnchorAnomalyID       int64      `json:"anchor_anomaly_id"`
	PaymentID             int64      `json:"payment_id"`
	InvoiceID             int64      `json:"invoice_id"`
	UserID                int64      `json:"user_id"`
	Provider              string     `json:"provider"`
	ChannelInstanceID     string     `json:"channel_instance_id"`
	ProviderPaymentID     string     `json:"provider_payment_id"`
	ExternalTransactionID string     `json:"external_transaction_id"`
	AmountCents           int64      `json:"amount_cents"`
	ExpectedGroupVersion  int64      `json:"expected_group_version"`
	Status                string     `json:"status"`
	Version               int64      `json:"version"`
	Method                string     `json:"method"`
	ProviderRefundID      string     `json:"provider_refund_id"`
	ReceivedAt            time.Time  `json:"received_at"`
	EvidenceRef           string     `json:"evidence_ref"`
	EvidenceSHA256        string     `json:"evidence_sha256"`
	Note                  string     `json:"note"`
	RequestedBy           int64      `json:"requested_by"`
	ReviewedBy            *int64     `json:"reviewed_by"`
	ReviewNote            string     `json:"review_note"`
	ReviewedGroupVersion  *int64     `json:"reviewed_group_version"`
	ConfirmedAt           *time.Time `json:"confirmed_at"`
	CreatedAt             time.Time  `json:"created_at"`
	UpdatedAt             time.Time  `json:"updated_at"`
	CanReview             bool       `json:"can_review"`
}
type PaymentAnomalyGroup struct {
	AnchorAnomalyID               int64                  `json:"anchor_anomaly_id"`
	GroupVersion                  int64                  `json:"group_version"`
	PaymentID                     int64                  `json:"payment_id"`
	InvoiceID                     int64                  `json:"invoice_id"`
	UserID                        int64                  `json:"user_id"`
	Provider                      string                 `json:"provider"`
	ProviderPaymentID             string                 `json:"provider_payment_id"`
	ExternalTransactionID         string                 `json:"external_transaction_id"`
	AmountCents                   int64                  `json:"amount_cents"`
	EventCount                    int64                  `json:"event_count"`
	FirstSeen                     time.Time              `json:"first_seen"`
	LastSeen                      time.Time              `json:"last_seen"`
	MerchantVerified              bool                   `json:"merchant_verified"`
	Unresolved                    bool                   `json:"unresolved"`
	Status                        string                 `json:"status"`
	CanCreate                     bool                   `json:"can_create"`
	Refunds                       []PaymentAnomalyRefund `json:"refunds"`
	merchantScope, credentialHash string
	amountConsistent              bool
	paymentStatus                 string
	paymentCreatedAt              time.Time
}
type PaymentAnomalyRefundResult struct {
	Refund               PaymentAnomalyRefund `json:"refund"`
	Group                PaymentAnomalyGroup  `json:"group"`
	InvoiceHasUnresolved bool                 `json:"invoice_has_unresolved"`
	Replayed             bool                 `json:"replayed"`
}
type PaymentAnomalySummary struct {
	Received         int64  `json:"anomaly_received_cents"`
	Refunded         int64  `json:"anomaly_refunded_cents"`
	Pending          int64  `json:"anomaly_pending_cents"`
	UnresolvedGroups int64  `json:"unresolved_groups"`
	UnverifiedGroups int64  `json:"unverified_groups"`
	UnverifiedAmount int64  `json:"unverified_amount_cents"`
	Currency         string `json:"currency"`
}
type PaymentAnomalyEntry struct {
	EventKey        string    `json:"event_key"`
	Source          string    `json:"source"`
	Direction       string    `json:"direction"`
	AnchorAnomalyID int64     `json:"anchor_anomaly_id"`
	PaymentID       int64     `json:"payment_id"`
	RefundID        *int64    `json:"refund_id"`
	InvoiceID       int64     `json:"invoice_id"`
	AmountCents     int64     `json:"amount_cents"`
	TransactionNo   string    `json:"transaction_no"`
	OccurredAt      time.Time `json:"occurred_at"`
	Verified        bool      `json:"verified"`
}

func (s *Store) paymentAnomalyTx(ctx context.Context, actor, invoice int64, permission string, write bool) (pgx.Tx, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	fail := func(e error) (pgx.Tx, error) { tx.Rollback(ctx); return nil, e }
	// Match final-close scope -> employee -> financial customer lock order.
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return fail(err)
	}
	for _, p := range []string{"invoices.view", "payment_anomalies.view", permission} {
		if err = externalRefundAuthorize(ctx, tx, actor, p, write); err != nil {
			return fail(err)
		}
	}
	// Scope may have narrowed while authorization waited for the employee row.
	// Keep the initial scope lock order, then recheck under the employee lock.
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return fail(err)
	}
	if _, err = lockOnlineTopupCustomer(ctx, tx, invoice); err != nil {
		return fail(err)
	}
	return tx, nil
}

func anomalyTransactionTrusted(v string) bool {
	return v != "" && v == strings.TrimSpace(v) && contentTextValid(v, 200, false)
}

func (s *Store) readPaymentAnomalyGroup(ctx context.Context, tx pgx.Tx, anchor, actor int64, create, review bool) (PaymentAnomalyGroup, error) {
	var g PaymentAnomalyGroup
	var sealed []byte
	var instanceID string
	err := tx.QueryRow(ctx, `SELECT g.anchor_anomaly_id,g.group_version,g.payment_id,g.invoice_id,g.user_id,g.provider,p.provider_payment_id,
 g.external_transaction_id,g.amount_cents,g.event_count,g.first_seen,g.last_seen,g.amount_consistent,g.credential_sha256,p.credential_snapshot,p.status,p.created_at,coalesce(p.channel_instance_id,''),
 EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.payment_id=g.payment_id AND a.external_transaction_id=g.external_transaction_id AND payment_close_anomaly_unresolved(a.id))
 FROM payment_close_anomaly_groups g JOIN payment_transactions p ON p.id=g.payment_id WHERE g.anchor_anomaly_id=$1`, anchor).Scan(&g.AnchorAnomalyID, &g.GroupVersion, &g.PaymentID, &g.InvoiceID, &g.UserID, &g.Provider, &g.ProviderPaymentID, &g.ExternalTransactionID, &g.AmountCents, &g.EventCount, &g.FirstSeen, &g.LastSeen, &g.amountConsistent, &g.credentialHash, &sealed, &g.paymentStatus, &g.paymentCreatedAt, &instanceID, &g.Unresolved)
	if err != nil {
		return g, err
	}
	g.merchantScope, err = s.externalRefundMerchantScope(g.Provider, g.ProviderPaymentID, sealed, instanceID)
	g.MerchantVerified = err == nil && anomalyTransactionTrusted(g.ExternalTransactionID) && g.amountConsistent && g.paymentStatus == "closed"
	g.Refunds = []PaymentAnomalyRefund{}
	rows, err := tx.Query(ctx, `SELECT to_jsonb(r)-'merchant_scope'-'credential_sha256' FROM payment_close_anomaly_refunds r WHERE anchor_anomaly_id=$1 ORDER BY id DESC`, anchor)
	if err != nil {
		return g, err
	}
	live := false
	for rows.Next() {
		var raw []byte
		var r PaymentAnomalyRefund
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &r); err != nil {
			break
		}
		r.CanReview = review && r.Status == "pending_review" && r.RequestedBy != actor
		if r.Status != "rejected" {
			live = true
		}
		g.Refunds = append(g.Refunds, r)
	}
	rows.Close()
	if err != nil {
		return g, err
	}
	if err = rows.Err(); err != nil {
		return g, err
	}
	g.Status = "pending_verification"
	if !g.Unresolved {
		g.Status = "resolved"
	}
	if !g.MerchantVerified {
		g.Status = "unverified"
	}
	g.CanCreate = create && g.MerchantVerified && g.Unresolved && !live
	return g, nil
}
func anomalyPermission(ctx context.Context, tx pgx.Tx, actor int64, p string) bool {
	return externalRefundAuthorize(ctx, tx, actor, p, false) == nil
}
func (s *Store) ListPaymentAnomalyGroups(ctx context.Context, actor, invoice int64, limit, offset int) ([]PaymentAnomalyGroup, int64, error) {
	out := []PaymentAnomalyGroup{}
	if limit < 1 || limit > 100 || offset < 0 {
		return out, 0, conflict("invalid_pagination", "分页参数无效")
	}
	tx, err := s.paymentAnomalyTx(ctx, actor, invoice, "payment_anomalies.view", false)
	if err != nil {
		return out, 0, err
	}
	defer tx.Rollback(ctx)
	var total int64
	if err = tx.QueryRow(ctx, "SELECT count(*) FROM payment_close_anomaly_groups WHERE invoice_id=$1", invoice).Scan(&total); err != nil {
		return out, 0, err
	}
	rows, err := tx.Query(ctx, "SELECT anchor_anomaly_id FROM payment_close_anomaly_groups WHERE invoice_id=$1 ORDER BY anchor_anomaly_id DESC LIMIT $2 OFFSET $3", invoice, limit, offset)
	if err != nil {
		return out, 0, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return out, 0, err
	}
	if err = rows.Err(); err != nil {
		return out, 0, err
	}
	create, review := anomalyPermission(ctx, tx, actor, "payment_anomalies.create"), anomalyPermission(ctx, tx, actor, "payment_anomalies.review")
	for _, id := range ids {
		g, e := s.readPaymentAnomalyGroup(ctx, tx, id, actor, create, review)
		if e != nil {
			return out, 0, e
		}
		out = append(out, g)
	}
	return out, total, tx.Commit(ctx)
}
func (s *Store) paymentAnomalyAll(ctx context.Context, actor, invoice int64) ([]PaymentAnomalyGroup, error) {
	tx, err := s.paymentAnomalyTx(ctx, actor, invoice, "payment_anomalies.view", false)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, "SELECT anchor_anomaly_id FROM payment_close_anomaly_groups WHERE invoice_id=$1 ORDER BY anchor_anomaly_id", invoice)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err != nil {
		return nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	groups := []PaymentAnomalyGroup{}
	for _, id := range ids {
		g, e := s.readPaymentAnomalyGroup(ctx, tx, id, actor, false, false)
		if e != nil {
			return nil, e
		}
		groups = append(groups, g)
	}
	return groups, tx.Commit(ctx)
}
func (s *Store) PaymentAnomalySummary(ctx context.Context, actor, invoice int64) (PaymentAnomalySummary, error) {
	out := PaymentAnomalySummary{Currency: "CNY"}
	groups, err := s.paymentAnomalyAll(ctx, actor, invoice)
	if err != nil {
		return out, err
	}
	for _, g := range groups {
		if g.Unresolved {
			out.UnresolvedGroups++
		}
		if !g.MerchantVerified {
			out.UnverifiedGroups++
			out.UnverifiedAmount += g.AmountCents
			continue
		}
		out.Received += g.AmountCents
		if g.Unresolved {
			out.Pending += g.AmountCents
		} else {
			out.Refunded += g.AmountCents
		}
	}
	return out, nil
}
func (s *Store) PaymentAnomalyEntries(ctx context.Context, actor, invoice int64, limit, offset int) ([]PaymentAnomalyEntry, int64, error) {
	out := []PaymentAnomalyEntry{}
	if limit < 1 || limit > 100 || offset < 0 {
		return out, 0, conflict("invalid_pagination", "分页参数无效")
	}
	groups, err := s.paymentAnomalyAll(ctx, actor, invoice)
	if err != nil {
		return out, 0, err
	}
	for _, g := range groups {
		source, direction := "anomaly_receipt", "income"
		if !g.MerchantVerified {
			source, direction = "anomaly_unverified", "neutral"
		}
		out = append(out, PaymentAnomalyEntry{EventKey: fmt.Sprintf("anomaly-receipt:%d", g.AnchorAnomalyID), Source: source, Direction: direction, AnchorAnomalyID: g.AnchorAnomalyID, PaymentID: g.PaymentID, InvoiceID: g.InvoiceID, AmountCents: g.AmountCents, TransactionNo: g.ExternalTransactionID, OccurredAt: g.FirstSeen, Verified: g.MerchantVerified})
		for _, r := range g.Refunds {
			if r.Status == "confirmed" {
				id := r.ID
				out = append(out, PaymentAnomalyEntry{EventKey: fmt.Sprintf("anomaly-refund:%d", r.ID), Source: "anomaly_refund", Direction: "expense", AnchorAnomalyID: g.AnchorAnomalyID, PaymentID: g.PaymentID, RefundID: &id, InvoiceID: g.InvoiceID, AmountCents: r.AmountCents, TransactionNo: r.ProviderRefundID, OccurredAt: r.ReceivedAt, Verified: true})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].EventKey < out[j].EventKey
		}
		return out[i].OccurredAt.After(out[j].OccurredAt)
	})
	total := int64(len(out))
	if offset >= len(out) {
		return []PaymentAnomalyEntry{}, total, nil
	}
	end := offset + limit
	if end > len(out) {
		end = len(out)
	}
	return out[offset:end], total, nil
}

func (s *Store) CreatePaymentAnomalyRefund(ctx context.Context, actor, invoice, anchor int64, in PaymentAnomalyRefundInput, key string) (PaymentAnomalyRefundResult, error) {
	in.ProviderRefundID = strings.TrimSpace(in.ProviderRefundID)
	in.EvidenceRef = strings.TrimSpace(in.EvidenceRef)
	in.Note = strings.TrimSpace(in.Note)
	if in.ExpectedGroupVersion < 1 || (in.Method != "original_route" && in.Method != "bank_transfer" && in.Method != "other") || in.ReceivedAt.IsZero() || in.ReceivedAt.After(time.Now()) || !contentTextValid(in.ProviderRefundID, 200, false) || in.ProviderRefundID == "" || !contentTextValid(in.EvidenceRef, 500, false) || in.EvidenceRef == "" || !externalRefundEvidenceHash.MatchString(in.EvidenceSHA256) || !contentTextValid(in.Note, 1000, true) || in.Note == "" {
		return PaymentAnomalyRefundResult{}, conflict("invalid_payment_anomaly_refund", "退回证据、时间或组版本无效")
	}
	return s.paymentAnomalyRefundWrite(ctx, actor, invoice, anchor, 0, false, in, PaymentAnomalyReviewInput{}, key)
}
func (s *Store) ReviewPaymentAnomalyRefund(ctx context.Context, actor, invoice, id int64, confirm bool, in PaymentAnomalyReviewInput, key string) (PaymentAnomalyRefundResult, error) {
	in.Note = strings.TrimSpace(in.Note)
	if in.ExpectedVersion < 1 || in.ExpectedGroupVersion < 1 || in.Note == "" || !contentTextValid(in.Note, 1000, true) {
		return PaymentAnomalyRefundResult{}, conflict("invalid_payment_anomaly_review", "复核版本或说明无效")
	}
	return s.paymentAnomalyRefundWrite(ctx, actor, invoice, 0, id, confirm, PaymentAnomalyRefundInput{}, in, key)
}
func (s *Store) paymentAnomalyRefundWrite(ctx context.Context, actor, invoice, anchor, id int64, confirm bool, in PaymentAnomalyRefundInput, review PaymentAnomalyReviewInput, key string) (out PaymentAnomalyRefundResult, err error) {
	defer func() { err = externalRefundDBError(err) }()
	if actor < 1 || invoice < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_payment_anomaly_refund", "账单或幂等键无效")
	}
	permission := "payment_anomalies.create"
	if id > 0 {
		permission = "payment_anomalies.review"
	}
	tx, err := s.paymentAnomalyTx(ctx, actor, invoice, permission, true)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	raw, _ := json.Marshal([]any{invoice, anchor, id, confirm, in, review})
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	if replayed, err := ReplayOperation(ctx, tx, actor, "payment-anomaly-refund", key, hash, &out); err != nil {
		return out, err
	} else if replayed {
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	var pid int64
	if id > 0 {
		err = tx.QueryRow(ctx, "SELECT anchor_anomaly_id,payment_id FROM payment_close_anomaly_refunds WHERE id=$1 AND invoice_id=$2", id, invoice).Scan(&anchor, &pid)
	} else {
		err = tx.QueryRow(ctx, "SELECT payment_id FROM payment_close_anomalies WHERE id=$1 AND invoice_id=$2", anchor, invoice).Scan(&pid)
	}
	if err != nil {
		return out, err
	}
	var locked int64
	if err = tx.QueryRow(ctx, "SELECT id FROM payment_transactions WHERE id=$1 FOR UPDATE", pid).Scan(&locked); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", invoice).Scan(&locked); err != nil {
		return out, err
	}
	g, err := s.readPaymentAnomalyGroup(ctx, tx, anchor, actor, true, true)
	if err != nil {
		return out, err
	}
	expected := in.ExpectedGroupVersion
	if id > 0 {
		expected = review.ExpectedGroupVersion
	}
	if expected != g.GroupVersion {
		return out, conflict("payment_anomaly_group_changed", "异常组新增事实，请重读后复核原申请")
	}
	if id == 0 || confirm {
		if !g.MerchantVerified {
			return out, conflict("payment_anomaly_identity_unverified", "原商户或交易号不可核验，不能登记或解除")
		}
		if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended('refund-original:'||$1,0))", g.ExternalTransactionID); err != nil {
			return out, err
		}
		var occupied bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM payment_transactions WHERE id<>$1 AND external_transaction_id=$2 AND status='succeeded') OR EXISTS(SELECT 1 FROM external_refunds WHERE payment_id<>$1 AND merchant_scope=$3 AND original_transaction_id=$2 AND status='confirmed') OR EXISTS(SELECT 1 FROM payment_close_anomaly_refunds WHERE payment_id<>$1 AND merchant_scope=$3 AND external_transaction_id=$2 AND status<>'rejected')`, pid, g.ExternalTransactionID, g.merchantScope).Scan(&occupied); err != nil {
			return out, err
		}
		if occupied {
			return out, conflict("payment_anomaly_transaction_occupied", "原交易已被其他收款或退款占用，需专项核验")
		}
	}
	action := "payment_anomaly.refund_created"
	if id == 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO payment_close_anomaly_merchant_bindings(payment_id,credential_sha256,merchant_scope,provider,provider_payment_id,channel_instance_id)
 SELECT id,$2,$3,provider,provider_payment_id,coalesce(channel_instance_id,'') FROM payment_transactions WHERE id=$1 ON CONFLICT DO NOTHING`, pid, g.credentialHash, g.merchantScope); err != nil {
			return out, err
		}
		if !g.CanCreate {
			return out, conflict("payment_anomaly_refund_exists", "该异常组已存在有效退回登记")
		}
		if in.ReceivedAt.Before(g.paymentCreatedAt) {
			return out, conflict("invalid_payment_anomaly_refund", "实际退回时间不能早于原支付创建")
		}
		err = tx.QueryRow(ctx, `INSERT INTO payment_close_anomaly_refunds(anchor_anomaly_id,payment_id,invoice_id,user_id,provider,channel_instance_id,provider_payment_id,merchant_scope,credential_sha256,external_transaction_id,amount_cents,expected_group_version,method,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by)
 SELECT $1,p.id,p.invoice_id,p.user_id,p.provider,coalesce(p.channel_instance_id,''),p.provider_payment_id,$2,$3,$4,p.amount_cents,$5,$6,$7,$8,$9,$10,$11,$12 FROM payment_transactions p WHERE p.id=$13 RETURNING id`, anchor, g.merchantScope, g.credentialHash, g.ExternalTransactionID, g.GroupVersion, in.Method, in.ProviderRefundID, in.ReceivedAt, in.EvidenceRef, in.EvidenceSHA256, in.Note, actor, pid).Scan(&id)
		if err != nil {
			return out, err
		}
	} else {
		var requested, version int64
		var status, scope, credential string
		if err = tx.QueryRow(ctx, "SELECT requested_by,version,status,merchant_scope,credential_sha256 FROM payment_close_anomaly_refunds WHERE id=$1 FOR UPDATE", id).Scan(&requested, &version, &status, &scope, &credential); err != nil {
			return out, err
		}
		if requested == actor {
			return out, conflict("permission_denied", "禁止审核本人登记的异常退回")
		}
		if version != review.ExpectedVersion {
			return out, conflict("version_conflict", "退回登记版本已变化")
		}
		if status != "pending_review" {
			return out, conflict("payment_anomaly_refund_final", "退回登记已经复核")
		}
		if confirm && (scope != g.merchantScope || credential != g.credentialHash) {
			return out, conflict("payment_anomaly_identity_unverified", "原商户凭据关联已变化")
		}
		status = "rejected"
		action = "payment_anomaly.refund_rejected"
		if confirm {
			status = "confirmed"
			action = "payment_anomaly.refund_confirmed"
		}
		if _, err = tx.Exec(ctx, "UPDATE payment_close_anomaly_refunds SET status=$2,version=version+1,reviewed_by=$3,review_note=$4,reviewed_group_version=$5,confirmed_at=CASE WHEN $2='confirmed' THEN clock_timestamp() END,updated_at=clock_timestamp() WHERE id=$1", id, status, actor, review.Note, g.GroupVersion); err != nil {
			return out, err
		}
	}
	var payload []byte
	if err = tx.QueryRow(ctx, "SELECT to_jsonb(r)-'merchant_scope'-'credential_sha256' FROM payment_close_anomaly_refunds r WHERE id=$1", id).Scan(&payload); err != nil {
		return out, err
	}
	if err = json.Unmarshal(payload, &out.Refund); err != nil {
		return out, err
	}
	if out.Group, err = s.readPaymentAnomalyGroup(ctx, tx, anchor, actor, true, true); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM payment_close_anomalies WHERE invoice_id=$1 AND payment_close_anomaly_unresolved(id))", invoice).Scan(&out.InvoiceHasUnresolved); err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, action, "payment_anomaly_refund", id, map[string]any{"invoice_id": invoice, "payment_id": pid, "anchor_anomaly_id": anchor, "amount_cents": g.AmountCents, "status": out.Refund.Status}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, "payment-anomaly-refund", key, hash, out); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}
