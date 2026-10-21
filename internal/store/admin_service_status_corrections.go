package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

const adminServiceStatusCorrectionScope = "admin:service.status.correct"

// A correction repairs the local projection of an already completed action.
// It never performs, confirms, retries or cancels a resource operation.
type AdminServiceStatusCorrectionInput struct {
	ExpectedVersion int64  `json:"expected_version"`
	SourceJobID     int64  `json:"source_job_id"`
	TargetStatus    string `json:"target_status"`
	Reason          string `json:"reason"`
	Evidence        string `json:"evidence"`
	QuoteHash       string `json:"quote_hash,omitempty"`
}

type AdminServiceStatusCorrectionPreview struct {
	ServiceID         int64     `json:"service_id"`
	CustomerID        int64     `json:"customer_id"`
	ServiceVersion    int64     `json:"service_version"`
	CurrentStatus     string    `json:"current_status"`
	TargetStatus      string    `json:"target_status"`
	SourceJobID       int64     `json:"source_job_id"`
	SourceAction      string    `json:"source_action"`
	SourceCompletedAt time.Time `json:"source_completed_at"`
	ProvisionMode     string    `json:"provision_mode"`
	QuoteHash         string    `json:"quote_hash"`
	ExpiresAt         time.Time `json:"expires_at"`
}

type AdminServiceStatusCorrectionResult struct {
	ServiceID      int64  `json:"service_id"`
	CustomerID     int64  `json:"customer_id"`
	ServiceVersion int64  `json:"service_version"`
	BeforeStatus   string `json:"before_status"`
	Status         string `json:"status"`
	SourceJobID    int64  `json:"source_job_id"`
	SourceAction   string `json:"source_action"`
	Replayed       bool   `json:"replayed"`
}

type adminServiceStatusState struct {
	Customer, Version             int64
	Status, Cycle, Mode, Upstream string
	Due                           *time.Time
	CustomerActive, Released      bool
	SystemSuspension              bool
	Snapshot                      json.RawMessage
}

type adminServiceStatusSource struct {
	ID          int64
	Action      string
	CompletedAt time.Time
	Job, Audit  json.RawMessage
}

func validateAdminServiceStatusCorrection(in *AdminServiceStatusCorrectionInput, apply bool) error {
	in.Reason = strings.TrimSpace(in.Reason)
	in.Evidence = strings.TrimSpace(in.Evidence)
	if in.ExpectedVersion < 1 || in.SourceJobID < 1 || (in.TargetStatus != "active" && in.TargetStatus != "suspended") ||
		in.Reason == "" || !contentTextValid(in.Reason, 500, true) || in.Evidence == "" || !contentTextValid(in.Evidence, 2000, true) ||
		(apply && !validRenewalBatchID(in.QuoteHash)) || (!apply && in.QuoteHash != "") {
		return &AccountBusinessError{400, "invalid_service_status_correction", "请提供有效服务版本、已完成来源任务、目标状态、纠错原因及核验凭据"}
	}
	return nil
}

func lockAdminServiceStatusCorrection(ctx context.Context, tx pgx.Tx, actor, service int64) (adminServiceStatusState, error) {
	var out adminServiceStatusState
	var owner int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", service).Scan(&owner); err != nil {
		return out, err
	}
	if err := lockAdminRenewalAuthority(ctx, tx, actor, owner, "services.view", "services.status.correct"); err != nil {
		return out, err
	}
	// Keep finance's customer -> service order; do not take invoice or order
	// locks after the service. Their admission paths serialize on this service.
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", owner).Scan(&out.CustomerActive); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT s.user_id,s.version,s.status,s.billing_cycle,s.provision_mode,s.upstream_id,s.next_due_at,s.inventory_released,
 (coalesce(s.resource_info->>'suspend_reason','')<>'' OR s.resource_info ?| ARRAY['system_billing_suspension_job_id','system_expiry_suspension_job_id','supplier_renewal_recovery_job_id']),
 external_refund_order_snapshot(s) FROM services s WHERE s.id=$1 FOR UPDATE`, service).
		Scan(&out.Customer, &out.Version, &out.Status, &out.Cycle, &out.Mode, &out.Upstream, &out.Due, &out.Released, &out.SystemSuspension, &out.Snapshot); err != nil {
		return out, err
	}
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	if out.Customer != owner {
		return out, conflict("service_owner_changed", "服务客户已变更，请重新核对")
	}
	return out, nil
}

func requireAdminServiceStatusCorrectionFresh(ctx context.Context, tx pgx.Tx, service int64, state adminServiceStatusState, in AdminServiceStatusCorrectionInput) error {
	if state.Version != in.ExpectedVersion {
		return conflict("stale_service", "服务已更新，请重新预览")
	}
	if !state.CustomerActive {
		return conflict("customer_inactive", "客户已停用，不能纠正服务状态")
	}
	if (state.Mode != "manual" && state.Mode != "simulation") || (state.Status != "active" && state.Status != "suspended") ||
		state.Status == in.TargetStatus || state.Released || state.Upstream == "" || !supplier.IsBillingCycle(state.Cycle) {
		return conflict("service_status_correction_unavailable", "仅已交付人工或模拟服务可根据已完成任务纠正运行或暂停状态")
	}
	if state.SystemSuspension {
		return conflict("service_status_correction_unavailable", "服务存在系统暂停或恢复标记，请通过原业务流程核验")
	}
	if err := requireServiceReviewed(ctx, tx, service); err != nil {
		return err
	}
	if err := requireNoLocalRenewalHold(ctx, tx, service); err != nil {
		return err
	}
	if err := requireServiceChangeIdle(ctx, tx, service); err != nil {
		return err
	}
	if err := requireCancellationPaymentsResolved(ctx, tx, service); err != nil {
		return err
	}
	var reason string
	// This deliberately supports one-off services and does not reuse the
	// recurring-only pricing/term admission functions.
	if err := tx.QueryRow(ctx, `SELECT CASE
 WHEN NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=s.order_id AND o.user_id=s.user_id AND o.review_status='approved' AND o.status IN ('paid','active'))
 OR NOT EXISTS(SELECT 1 FROM invoices i WHERE i.order_id=s.order_id AND i.user_id=s.user_id AND i.status='paid' AND NOT i.import_review_required) THEN 'service_order_not_deliverable'
 WHEN external_refund_order_blocked(s.order_id) THEN 'external_refund_order_hold'
 WHEN EXISTS(SELECT 1 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id WHERE r.status='pending_review' AND
 (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id))) THEN 'service_refund_pending'
 WHEN EXISTS(SELECT 1 FROM invoices i WHERE i.status='unpaid' AND (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id)))
 OR EXISTS(SELECT 1 FROM traffic_orders t JOIN invoices i ON i.id=t.invoice_id WHERE t.service_id=s.id AND i.status='unpaid') THEN 'service_invoice_pending'
 WHEN EXISTS(SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id AND j.external_started_at IS NOT NULL) THEN 'service_external_history_requires_review'
 WHEN EXISTS(SELECT 1 FROM invoices i WHERE (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id)) AND (
 EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.provider<>'mock' AND p.status IN ('pending','expired')) OR
 EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id)) OR
 EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)))) THEN 'invoice_payment_pending'
 ELSE '' END FROM services s WHERE s.id=$1`, service).Scan(&reason); err != nil {
		return err
	}
	if reason != "" {
		return conflict(reason, "服务存在待完成收款、退款、交付或外部操作，请先处理原业务")
	}
	return requireAdminServiceStatusEntitlement(ctx, tx, service, in.TargetStatus)
}

func requireAdminServiceStatusEntitlement(ctx context.Context, tx pgx.Tx, service int64, target string) error {
	if target != "active" {
		return nil
	}
	var eligible bool
	if err := tx.QueryRow(ctx, `SELECT (s.billing_cycle='once' OR coalesce(s.next_due_at>clock_timestamp(),false)) AND `+noOverdueServiceInvoiceSQL+`
 FROM services s WHERE s.id=$1`, service).Scan(&eligible); err != nil {
		return err
	}
	if !eligible {
		return conflict("service_entitlement_expired", "服务期限已到期或存在逾期账单，不能通过状态纠错恢复")
	}
	return nil
}

func lockAdminServiceStatusSource(ctx context.Context, tx pgx.Tx, service int64, mode string, in AdminServiceStatusCorrectionInput) (adminServiceStatusSource, error) {
	var out adminServiceStatusSource
	var status, key string
	var completed *time.Time
	var localSource bool
	// Include every attempt, rather than taking the newest success and silently
	// ignoring a later failure/cancellation. Existing lifecycle writers lock the
	// parent service before inserting or modifying its job.
	err := tx.QueryRow(ctx, `SELECT j.id,j.action,j.status,j.idempotency_key,j.completed_at,
 j.external_started_at IS NULL AND NOT j.request_payload ?| ARRAY['system_overdue_suspension','system_expiry_suspension','manual_lifecycle'],
 external_refund_order_snapshot(j) FROM provision_jobs j WHERE j.service_id=$1 AND j.action IN ('create','suspend','resume','terminate')
 ORDER BY j.id DESC LIMIT 1 FOR UPDATE`, service).Scan(&out.ID, &out.Action, &status, &key, &completed, &localSource, &out.Job)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	if errors.Is(err, pgx.ErrNoRows) || out.ID != in.SourceJobID || status != "succeeded" || completed == nil || !localSource ||
		strings.HasPrefix(key, "overdue-suspend-service-") || strings.HasPrefix(key, "expiry-suspend-service-") ||
		(out.Action != "suspend" || in.TargetStatus != "suspended") && (out.Action != "resume" || in.TargetStatus != "active") {
		return out, conflict("service_status_source_invalid", "来源必须是该服务最新已成功的人工或模拟暂停、恢复任务，并与纠错目标一致")
	}
	out.CompletedAt = *completed
	// A status/result field alone is not completion evidence. Require the
	// original completion audit, including the operator's exact confirmation
	// payload for manual delivery. Hold the audit and job through commit.
	err = tx.QueryRow(ctx, `SELECT external_refund_order_snapshot(a) FROM audit_events a
	 JOIN provision_jobs j ON j.id=$2::bigint AND j.service_id=$1::bigint
	 WHERE a.entity_type='service' AND a.entity_id=($1::bigint)::text AND a.payload->>'job_id'=j.id::text
 AND a.created_at>=j.created_at AND j.completed_at<=clock_timestamp()
 AND (($3::text='manual' AND a.action='service.manual.completed' AND a.actor_id>0
 AND EXISTS(SELECT 1 FROM account_identities ai WHERE ai.id=a.actor_id AND ai.role IN ('admin','super_admin')) AND a.payload=j.result)
 OR ($3::text='simulation' AND a.action='service.simulation.completed' AND a.actor_id IS NULL
 AND a.payload->>'action'=j.action AND j.result=jsonb_build_object('mode','simulation')))
 ORDER BY a.id DESC LIMIT 1 FOR SHARE OF a`, service, out.ID, mode).Scan(&out.Audit)
	if errors.Is(err, pgx.ErrNoRows) {
		return out, conflict("service_status_source_invalid", "来源任务缺少可核对的原始完成记录，请通过原任务流程处理")
	}
	return out, err
}

func buildAdminServiceStatusCorrectionPreview(ctx context.Context, tx pgx.Tx, service int64, state adminServiceStatusState, source adminServiceStatusSource, in AdminServiceStatusCorrectionInput) (AdminServiceStatusCorrectionPreview, error) {
	out := AdminServiceStatusCorrectionPreview{ServiceID: service, CustomerID: state.Customer, ServiceVersion: state.Version,
		CurrentStatus: state.Status, TargetStatus: in.TargetStatus, SourceJobID: source.ID, SourceAction: source.Action,
		SourceCompletedAt: source.CompletedAt, ProvisionMode: state.Mode}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	out.QuoteHash = accountBusinessHash(struct {
		Scope            string
		Preview          AdminServiceStatusCorrectionPreview
		Reason, Evidence string
		Service          json.RawMessage
		Source           adminServiceStatusSource
	}{adminServiceStatusCorrectionScope, out, in.Reason, in.Evidence, state.Snapshot, source})
	return out, nil
}

func (s *Store) QuoteAdminServiceStatusCorrection(ctx context.Context, actor, service int64, in AdminServiceStatusCorrectionInput) (AdminServiceStatusCorrectionPreview, error) {
	var out AdminServiceStatusCorrectionPreview
	if err := validateAdminServiceStatusCorrection(&in, false); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 {
		return out, &AccountBusinessError{400, "invalid_service_status_correction", "管理员或服务编号无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceStatusCorrection(ctx, tx, actor, service)
	if err != nil {
		return out, err
	}
	if err = requireAdminServiceStatusCorrectionFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	source, err := lockAdminServiceStatusSource(ctx, tx, service, state.Mode, in)
	if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
		return out, authErr
	}
	if err != nil {
		return out, err
	}
	out, err = buildAdminServiceStatusCorrectionPreview(ctx, tx, service, state, source, in)
	if err != nil {
		return out, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	if err = requireAdminServiceStatusEntitlement(ctx, tx, service, in.TargetStatus); err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) CorrectAdminServiceStatus(ctx context.Context, actor, service int64, key string, in AdminServiceStatusCorrectionInput) (out AdminServiceStatusCorrectionResult, err error) {
	key = strings.TrimSpace(key)
	if err = validateAdminServiceStatusCorrection(&in, true); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_service_status_correction", "管理员、服务编号或幂等键无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceStatusCorrection(ctx, tx, actor, service)
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(struct {
		Customer, Service int64
		Input             AdminServiceStatusCorrectionInput
	}{state.Customer, service, in})
	replayed, err := ReplayOperation(ctx, tx, actor, adminServiceStatusCorrectionScope, key, hash, &out)
	if err == nil || isRenewalBatchConflict(err) {
		if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
			return out, authErr
		}
	}
	if err != nil {
		return out, err
	}
	if replayed {
		out.Replayed = true
		return out, commitAdminRenewal(ctx, tx, actor)
	}
	if err = requireAdminServiceStatusCorrectionFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	source, err := lockAdminServiceStatusSource(ctx, tx, service, state.Mode, in)
	if authErr := checkAdminRenewalCredential(ctx, tx, actor); authErr != nil {
		return out, authErr
	}
	if err != nil {
		return out, err
	}
	preview, err := buildAdminServiceStatusCorrectionPreview(ctx, tx, service, state, source, in)
	if err != nil {
		return out, err
	}
	if preview.QuoteHash != in.QuoteHash {
		return out, conflict("stale_service_status_quote", "状态纠错预览已过期或来源记录已变化，请重新预览")
	}
	// Do not clear error/provenance fields, change renewal configuration or
	// replay lifecycle notifications. Only the proven local projection changes.
	if _, err = tx.Exec(ctx, "UPDATE services SET status=$2,version=version+1,updated_at=clock_timestamp() WHERE id=$1", service, in.TargetStatus); err != nil {
		return out, err
	}
	out = AdminServiceStatusCorrectionResult{ServiceID: service, CustomerID: state.Customer, ServiceVersion: state.Version + 1,
		BeforeStatus: state.Status, Status: in.TargetStatus, SourceJobID: source.ID, SourceAction: source.Action}
	if err = Audit(ctx, tx, actor, "service.status.corrected", "service", service, map[string]any{
		"customer_id": state.Customer, "before_status": state.Status, "status": in.TargetStatus, "source_job_id": source.ID,
		"source_action": source.Action, "source_completed_at": source.CompletedAt, "reason": in.Reason, "evidence": in.Evidence,
		"source_snapshot_hash": accountBusinessHash(source), "quote_hash": in.QuoteHash,
	}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, adminServiceStatusCorrectionScope, key, hash, out); err != nil {
		return out, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	var current bool
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", preview.ExpiresAt).Scan(&current); err != nil {
		return out, err
	}
	if !current {
		return out, conflict("stale_service_status_quote", "状态纠错预览已过期，请重新预览")
	}
	// Entitlement can expire during a credential lock wait even inside the
	// quote window. Clock checks therefore run again after all commit waits.
	if err = requireAdminServiceStatusEntitlement(ctx, tx, service, in.TargetStatus); err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}
