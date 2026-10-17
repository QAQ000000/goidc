package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
)

type LocalRenewalEffect struct {
	ID           int64      `json:"id"`
	ServiceID    int64      `json:"service_id"`
	BillingCycle string     `json:"billing_cycle"`
	BeforeDue    *time.Time `json:"before_due"`
	AfterDue     time.Time  `json:"after_due"`
	PeriodStart  time.Time  `json:"period_start"`
	BeforeUsed   int64      `json:"before_used"`
	AfterUsed    int64      `json:"after_used"`
}
type ExternalRefundRenewalReversal struct {
	Status         string     `json:"status"`
	EffectID       int64      `json:"effect_id"`
	ServiceID      int64      `json:"service_id"`
	InvoiceVersion int64      `json:"invoice_version"`
	ServiceVersion int64      `json:"service_version"`
	BeforeDue      *time.Time `json:"before_due"`
	AfterDue       time.Time  `json:"after_due"`
	BeforeUsed     int64      `json:"before_used"`
	AfterUsed      int64      `json:"after_used"`
	ReviewBefore   *time.Time `json:"review_before"`
	CreatedAt      time.Time  `json:"created_at"`
	AppliedAt      *time.Time `json:"applied_at"`
	ReleasedAt     *time.Time `json:"released_at"`
}
type ExternalRefundRenewalContext struct {
	InvoiceID       int64                          `json:"invoice_id"`
	InvoiceVersion  int64                          `json:"invoice_version"`
	Eligible        bool                           `json:"eligible"`
	ReasonCode      string                         `json:"reason_code"`
	Reason          string                         `json:"reason"`
	Effect          *LocalRenewalEffect            `json:"effect"`
	ServiceVersion  int64                          `json:"service_version"`
	ReviewBefore    *time.Time                     `json:"review_before"`
	RenewalReversal *ExternalRefundRenewalReversal `json:"renewal_reversal"`
}

func localRenewalConflict(code string) error {
	messages := map[string]string{
		"renewal_effect_missing":        "续费缺少不可变生效事实，需专项核对",
		"renewal_original_due_unproven": "无法证明续费前的原到期时间",
		"renewal_period_consumed":       "续费期限已开始使用，不能进行全额未使用冲正",
		"renewal_external_entitlement":  "供应商续费权益须先核验上游结果",
		"renewal_successor_exists":      "存在尚未冲正的后续续费，请按后进先出顺序处理",
		"renewal_effect_changed":        "服务期限、权益或关键配置与原续费事实不一致",
		"renewal_dependent_invoice":     "服务存在待支付的后续账单，请先处理",
		"renewal_operation_pending":     "服务存在未完成任务、配置变更或取消请求",
		"renewal_payment_unresolved":    "账单存在未核实的在线收款或关单结果",
		"renewal_service_state":         "账单或服务状态不支持续费冲正",
		"renewal_refund_hold":           "服务已有待复核续费退款冻结，或正在变更，请刷新后重试",
		"renewal_already_reversed":      "该续费已完整冲正",
		"renewal_financial_association": "账单存在混合收款、发票、信用、佣金或业绩关联",
		"renewal_effect_incomplete":     "续费生效事实与实际收款不一致",
		"renewal_refund_incomplete":     "续费冲正事实不完整，需专项核对",
		"renewal_effect_immutable":      "续费生效及冲正事实不可修改",
	}
	message := messages[code]
	if message == "" {
		message = "续费冲正状态不一致，需专项核对"
	}
	return conflict(code, message)
}
func lockLocalRenewalService(ctx context.Context, tx pgx.Tx, invoice int64) error {
	rows, err := tx.Query(ctx, `SELECT s.id FROM services s WHERE EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=$1 AND ii.service_id=s.id AND ii.item_type='renew') ORDER BY s.id FOR UPDATE`, invoice)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
	}
	return rows.Err()
}
func requireLocalRenewalRefund(ctx context.Context, tx pgx.Tx, invoice, refund int64, external bool) error {
	var reason string
	var own any
	if refund > 0 {
		own = refund
	}
	if err := tx.QueryRow(ctx, `SELECT local_renewal_refund_reason($1,$2,$3)`, invoice, own, external).Scan(&reason); err != nil {
		return err
	}
	if reason != "" {
		return localRenewalConflict(reason)
	}
	return nil
}
func requireNoLocalRenewalHold(ctx context.Context, tx pgx.Tx, service int64) error {
	var held bool
	if err := tx.QueryRow(ctx, `SELECT local_renewal_service_blocked($1)`, service).Scan(&held); err != nil {
		return err
	}
	if held {
		return localRenewalConflict("renewal_refund_hold")
	}
	return nil
}
func (s *Store) ExternalRefundRenewalContext(ctx context.Context, actor, invoice int64) (out ExternalRefundRenewalContext, err error) {
	out.InvoiceID = invoice
	defer func() { err = externalRefundDBError(err) }()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = externalRefundAuthorize(ctx, tx, actor, "external_refunds.view", true); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "invoice", invoice); err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `SELECT version FROM invoices WHERE id=$1 FOR SHARE`, invoice).Scan(&out.InvoiceVersion); err != nil {
		return out, err
	}
	// No service write lock after SHARE on invoice: read projection and eligibility
	// are advisory. Creation binds both versions under the normal mutation locks.
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT (SELECT to_jsonb(e) FROM service_renewal_effects e WHERE invoice_id=$1)`, invoice).Scan(&raw); err != nil {
		return out, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &out.Effect); err != nil {
			return out, err
		}
		out.ReviewBefore = out.Effect.BeforeDue
		if err = tx.QueryRow(ctx, `SELECT version FROM services WHERE id=$1`, out.Effect.ServiceID).Scan(&out.ServiceVersion); err != nil {
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT (SELECT external_refund_renewal_reversal_json(refund_id) FROM external_refund_renewal_holds WHERE invoice_id=$1 ORDER BY refund_id DESC LIMIT 1)`, invoice).Scan(&raw); err != nil {
		return out, err
	}
	if len(raw) > 0 {
		if err = json.Unmarshal(raw, &out.RenewalReversal); err != nil {
			return out, err
		}
	}
	if err = tx.QueryRow(ctx, `SELECT local_renewal_refund_reason($1,NULL,true)`, invoice).Scan(&out.ReasonCode); err != nil {
		return out, err
	}
	out.Eligible = out.ReasonCode == ""
	if !out.Eligible {
		out.Reason = localRenewalConflict(out.ReasonCode).Error()
	}
	return out, tx.Commit(ctx)
}
func validateExternalRefundRenewalVersions(ctx context.Context, tx pgx.Tx, invoice int64, in ExternalRefundInput) error {
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_renewal_effects e JOIN services s ON s.id=e.service_id WHERE e.invoice_id=$1 AND e.id=$2 AND s.version=$3)`, invoice, in.ExpectedRenewalEffectID, in.ExpectedRenewalServiceVersion).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("version_conflict", "续费生效事实或服务版本已变化")
	}
	return nil
}
func reserveExternalRefundRenewal(ctx context.Context, tx pgx.Tx, id, invoice int64) error {
	_, err := tx.Exec(ctx, `INSERT INTO external_refund_renewal_holds(refund_id,effect_id,invoice_id,payment_id,service_id,user_id,invoice_version,service_version,invoice_snapshot,service_snapshot)
 SELECT r.id,e.id,i.id,r.payment_id,s.id,i.user_id,i.version,s.version,external_refund_order_snapshot(i),external_refund_order_snapshot(s)
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id JOIN service_renewal_effects e ON e.invoice_id=i.id JOIN services s ON s.id=e.service_id WHERE r.id=$1 AND i.id=$2`, id, invoice)
	return err
}
func applyExternalRefundRenewal(ctx context.Context, tx pgx.Tx, actor, id, invoice int64) error {
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT h.status='pending' AND h.invoice_snapshot=external_refund_order_snapshot(i) AND h.service_snapshot=external_refund_order_snapshot(s) FROM external_refund_renewal_holds h JOIN invoices i ON i.id=h.invoice_id JOIN services s ON s.id=h.service_id WHERE h.refund_id=$1 AND i.id=$2 FOR UPDATE OF h`, id, invoice).Scan(&valid); err != nil {
		return err
	}
	if !valid {
		return conflict("version_conflict", "续费退款原始快照已变化")
	}
	if _, err := tx.Exec(ctx, `UPDATE external_refund_renewal_holds SET status='applying' WHERE refund_id=$1`, id); err != nil {
		return err
	}
	// BEFORE invoice trigger executes the exact common inverse and records it.
	if _, err := tx.Exec(ctx, `UPDATE invoices SET status='refunded' WHERE id=$1`, invoice); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE external_refund_renewal_holds SET status='applied',applied_at=clock_timestamp() WHERE refund_id=$1`, id); err != nil {
		return err
	}
	return Audit(ctx, tx, actor, "external_refund.renewal_reversed", "external_refund", id, map[string]any{"invoice_id": invoice, "refund_id": id})
}
