package store

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminServiceTransferScope = "admin:service.transfer"

type AdminServiceTransferInput struct {
	ExpectedVersion             int64  `json:"expected_version"`
	ExpectedOwnerGeneration     *int64 `json:"expected_owner_generation"`
	SourceCustomerID            int64  `json:"source_customer_id"`
	TargetCustomerID            int64  `json:"target_customer_id"`
	CredentialHandoverConfirmed bool   `json:"credential_handover_confirmed"`
	Reason                      string `json:"reason"`
	HandoverEvidence            string `json:"handover_evidence"`
	QuoteHash                   string `json:"quote_hash,omitempty"`
}

type AdminServiceTransferResult struct {
	ServiceID        int64 `json:"service_id"`
	SourceCustomerID int64 `json:"source_customer_id"`
	TargetCustomerID int64 `json:"target_customer_id"`
	OwnerGeneration  int64 `json:"owner_generation"`
	Version          int64 `json:"version"`
	AutoRenew        bool  `json:"auto_renew"`
	Replayed         bool  `json:"replayed"`
}

type AdminServiceTransferQuote struct {
	ServiceID          int64      `json:"service_id"`
	SourceCustomerID   int64      `json:"source_customer_id"`
	TargetCustomerID   int64      `json:"target_customer_id"`
	OwnerGeneration    int64      `json:"owner_generation"`
	Version            int64      `json:"version"`
	AutoRenew          bool       `json:"auto_renew"`
	CurrentDueAt       *time.Time `json:"current_due_at"`
	RenewalPricingMode string     `json:"renewal_pricing_mode"`
	RenewalAmountCents int64      `json:"renewal_amount_cents"`
	QuoteHash          string     `json:"quote_hash"`
	ExpiresAt          time.Time  `json:"expires_at"`
}

type adminServiceTransferState struct {
	Owner, Version, Generation, Original int64
	Status, Mode                         string
	Due                                  *time.Time
	Active                               bool
	Snapshot, Pricing, Term              json.RawMessage
}

func validateAdminServiceTransfer(in *AdminServiceTransferInput, apply bool) error {
	in.Reason, in.HandoverEvidence = strings.TrimSpace(in.Reason), strings.TrimSpace(in.HandoverEvidence)
	if in.ExpectedVersion < 1 || in.ExpectedOwnerGeneration == nil || *in.ExpectedOwnerGeneration < 0 || in.SourceCustomerID < 1 || in.TargetCustomerID < 1 ||
		in.SourceCustomerID == in.TargetCustomerID || !in.CredentialHandoverConfirmed || in.Reason == "" || !contentTextValid(in.Reason, 500, true) ||
		in.HandoverEvidence == "" || !contentTextValid(in.HandoverEvidence, 500, true) || (apply && !validRenewalBatchID(in.QuoteHash)) || (!apply && in.QuoteHash != "") {
		return &AccountBusinessError{400, "invalid_service_transfer", "请明确服务版本、归属代次、不同的源/目标客户、转移原因及已完成凭据轮换交接的核验记录"}
	}
	return nil
}

func lockAdminServiceTransfer(ctx context.Context, tx pgx.Tx, actor, service int64, in AdminServiceTransferInput) (adminServiceTransferState, error) {
	var out adminServiceTransferState
	if err := lockAdminRenewalAuthority(ctx, tx, actor, in.SourceCustomerID, "services.view", "customers.view", "services.transfer"); err != nil {
		return out, err
	}
	if err := LockAdminCustomer(ctx, tx, actor, in.TargetCustomerID); err != nil {
		return out, err
	}
	customers := []int64{in.SourceCustomerID, in.TargetCustomerID}
	sort.Slice(customers, func(i, j int) bool { return customers[i] < customers[j] })
	out.Active = true
	for _, customer := range customers {
		var active bool
		// Serialize finance and account shutdown for both owners in global ID
		// order. The service lock is NOWAIT, so old service->customer writers
		// cannot form a wait cycle with this customer->service command.
		if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR NO KEY UPDATE", customer).Scan(&active); err != nil {
			return out, err
		}
		out.Active = out.Active && active
		if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
			return out, err
		}
	}
	if err := tx.QueryRow(ctx, `SELECT user_id,version,status,provision_mode,next_due_at,service_ownership_generation(id),service_original_customer_id(id),external_refund_order_snapshot(s)
 FROM services s WHERE id=$1 FOR UPDATE NOWAIT`, service).Scan(&out.Owner, &out.Version, &out.Status, &out.Mode, &out.Due, &out.Generation, &out.Original, &out.Snapshot); err != nil {
		return out, cancellationServiceLockError(err)
	}
	if err := LockAdminCustomer(ctx, tx, actor, out.Owner); err != nil {
		return out, err
	}
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	return out, nil
}

func requireAdminServiceTransferFresh(ctx context.Context, tx pgx.Tx, service int64, state adminServiceTransferState, in AdminServiceTransferInput) error {
	if state.Owner != in.SourceCustomerID {
		return conflict("service_owner_changed", "服务客户已变更，请重新核对")
	}
	if state.Version != in.ExpectedVersion {
		return conflict("stale_service", "服务已更新，请重新预览")
	}
	if state.Generation != *in.ExpectedOwnerGeneration {
		return conflict("stale_service_owner", "服务归属代次已变更，请重新预览")
	}
	if !state.Active {
		return conflict("customer_inactive", "源客户与目标客户必须同时启用")
	}
	if state.Status != "active" && state.Status != "suspended" {
		return conflict("service_transfer_unavailable", "仅已交付运行或暂停的服务可以转移")
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
	err := tx.QueryRow(ctx, `SELECT CASE
 WHEN s.inventory_released OR s.upstream_id='' THEN 'service_not_delivered'
 WHEN NOT EXISTS(SELECT 1 FROM orders o WHERE o.id=s.order_id AND o.user_id=$2 AND o.review_status='approved' AND o.status IN ('paid','active'))
   OR NOT EXISTS(SELECT 1 FROM invoices i WHERE i.order_id=s.order_id AND i.user_id=$2 AND i.status='paid' AND NOT i.import_review_required) THEN 'service_order_not_deliverable'
 WHEN external_refund_order_blocked(s.order_id) THEN 'external_refund_order_hold'
 WHEN EXISTS(SELECT 1 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id WHERE r.status='pending_review'
   AND (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id))) THEN 'service_refund_pending'
 WHEN EXISTS(SELECT 1 FROM invoices i WHERE i.status='unpaid' AND (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id)))
   OR EXISTS(SELECT 1 FROM traffic_orders t JOIN invoices i ON i.id=t.invoice_id WHERE t.service_id=s.id AND i.status='unpaid') THEN 'service_invoice_pending'
 WHEN EXISTS(SELECT 1 FROM traffic_orders t WHERE t.service_id=s.id AND t.status='paid' AND t.fulfillment_status<>'fulfilled') THEN 'service_traffic_delivery_pending'
 WHEN EXISTS(SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id AND (j.status IN ('pending','running','waiting_manual','waiting_verification')
   OR (j.external_started_at IS NOT NULL AND j.status<>'succeeded' AND NOT coalesce(
    (j.status='failed' AND j.result->>'verification_outcome'='not_applied' AND EXISTS(
      SELECT 1 FROM audit_events a WHERE a.entity_type='service' AND a.entity_id=s.id::text AND a.action='supplier.delivery.verified'
      AND a.payload->>'job_id'=j.id::text AND a.payload->>'outcome'='not_applied' AND a.created_at>=j.updated_at)),false)))) THEN 'service_external_operation_pending'
 WHEN s.provision_mode='supplier' AND EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='paid'
   AND NOT EXISTS(SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id AND j.action='renew' AND j.idempotency_key='supplier-renew-'||i.id::text AND j.status='succeeded')) THEN 'supplier_renewal_pending'
 WHEN EXISTS(SELECT 1 FROM invoices i WHERE (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id)) AND (
   EXISTS(SELECT 1 FROM payment_transactions p WHERE p.invoice_id=i.id AND p.provider<>'mock' AND p.status IN ('pending','expired'))
   OR EXISTS(SELECT 1 FROM payment_close_operations c WHERE c.invoice_id=i.id AND payment_close_operation_unresolved(c.id))
   OR EXISTS(SELECT 1 FROM payment_close_anomalies a WHERE a.invoice_id=i.id AND payment_close_anomaly_unresolved(a.id)))) THEN 'invoice_payment_pending'
 ELSE '' END FROM services s WHERE s.id=$1`, service, state.Original).Scan(&reason)
	if err != nil {
		return err
	}
	if reason != "" {
		return conflict(reason, "服务存在未完成业务，请先处理原账单、退款、任务及外部收款")
	}
	return nil
}

// Lock every already materialized delivery before checking its state. A worker
// either owns the row and blocks transfer, or sees its terminal state afterwards.
func lockServiceTransferNotifications(ctx context.Context, tx pgx.Tx, service int64, expire bool) error {
	queries := []string{
		`SELECT d.status FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.service_id=$1 AND d.status IN ('pending','sending','unknown','failed') ORDER BY d.id FOR UPDATE OF d NOWAIT`,
		`SELECT o.status FROM email_outbox o WHERE (EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.email_outbox_id=o.id AND n.service_id=$1) OR EXISTS(SELECT 1 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.legacy_email_outbox_id=o.id AND e.service_id=$1)) AND o.status IN ('pending','sending','dead_letter') ORDER BY o.id FOR UPDATE OF o NOWAIT`,
		`SELECT o.status FROM sms_outbox o WHERE (EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.sms_outbox_id=o.id AND n.service_id=$1) OR EXISTS(SELECT 1 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.legacy_sms_outbox_id=o.id AND e.service_id=$1)) AND o.status IN ('pending','sending','dead_letter') ORDER BY o.id FOR UPDATE OF o NOWAIT`,
	}
	for _, query := range queries {
		rows, err := tx.Query(ctx, query, service)
		if err != nil {
			return cancellationServiceLockError(err)
		}
		statuses, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return cancellationServiceLockError(err)
		}
		for _, status := range statuses {
			if status == "sending" || status == "unknown" {
				return conflict("service_notification_pending", "服务通知正在投递或结果未知，请先核验投递结果")
			}
		}
	}
	if !expire {
		return nil
	}
	for _, query := range []string{
		`UPDATE notification_deliveries d SET status='skipped',code='service_owner_changed',subject='',body='',lease_token=NULL,lease_until=NULL,updated_at=clock_timestamp() FROM notification_events e WHERE e.id=d.event_id AND e.service_id=$1 AND d.status IN ('pending','failed')`,
		`UPDATE email_outbox o SET status='expired',body='[expired]',body_encrypted=''::bytea,locked_at=NULL,dead_letter_at=NULL,last_error='service owner changed' WHERE o.status IN ('pending','dead_letter') AND (EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.email_outbox_id=o.id AND n.service_id=$1) OR EXISTS(SELECT 1 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.legacy_email_outbox_id=o.id AND e.service_id=$1))`,
		`UPDATE sms_outbox o SET status='expired',body_encrypted=''::bytea,locked_at=NULL,dead_letter_at=NULL,last_error='service owner changed' WHERE o.status IN ('pending','dead_letter') AND (EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.sms_outbox_id=o.id AND n.service_id=$1) OR EXISTS(SELECT 1 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE d.legacy_sms_outbox_id=o.id AND e.service_id=$1))`,
	} {
		if _, err := tx.Exec(ctx, query, service); err != nil {
			return err
		}
	}
	return nil
}

func buildAdminServiceTransferQuote(ctx context.Context, tx pgx.Tx, service int64, state *adminServiceTransferState, in AdminServiceTransferInput) (AdminServiceTransferQuote, error) {
	out := AdminServiceTransferQuote{ServiceID: service, SourceCustomerID: in.SourceCustomerID, TargetCustomerID: in.TargetCustomerID, OwnerGeneration: state.Generation, Version: state.Version, CurrentDueAt: state.Due, RenewalPricingMode: "contract"}
	terms, err := lockRenewalTerms(ctx, tx, service)
	if err != nil {
		return out, err
	}
	discount, _, _, err := terms.Benefit.Quote(terms.BaseCents)
	if err != nil {
		return out, err
	}
	out.RenewalAmountCents = terms.BaseCents - discount
	var now time.Time
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp(),(SELECT external_refund_order_snapshot(p) FROM service_renewal_pricing p WHERE p.service_id=$1),(SELECT external_refund_order_snapshot(t) FROM service_term_management t WHERE t.service_id=$1)`, service).Scan(&now, &state.Pricing, &state.Term); err != nil {
		return out, err
	}
	if state.Pricing != nil {
		out.RenewalPricingMode = "fixed"
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	in.QuoteHash = ""
	out.QuoteHash = accountBusinessHash(struct {
		Scope string
		State adminServiceTransferState
		Input AdminServiceTransferInput
		Quote AdminServiceTransferQuote
	}{adminServiceTransferScope, *state, in, out})
	return out, nil
}

func (s *Store) QuoteAdminServiceTransfer(ctx context.Context, actor, service int64, in AdminServiceTransferInput) (out AdminServiceTransferQuote, err error) {
	if err = validateAdminServiceTransfer(&in, false); err != nil {
		return out, err
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceTransfer(ctx, tx, actor, service, in)
	if err != nil {
		return out, err
	}
	if err = requireAdminServiceTransferFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	if err = lockServiceTransferNotifications(ctx, tx, service, false); err != nil {
		return out, err
	}
	out, err = buildAdminServiceTransferQuote(ctx, tx, service, &state, in)
	if err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) TransferAdminService(ctx context.Context, actor, service int64, key string, in AdminServiceTransferInput) (out AdminServiceTransferResult, err error) {
	key = strings.TrimSpace(key)
	if err = validateAdminServiceTransfer(&in, true); err != nil {
		return out, err
	}
	if len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_service_transfer", "幂等键须为8至128字节"}
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceTransfer(ctx, tx, actor, service, in)
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(struct {
		Service int64
		Input   AdminServiceTransferInput
	}{service, in})
	replayed, err := ReplayOperation(ctx, tx, actor, adminServiceTransferScope, key, hash, &out)
	if err != nil {
		if auth := checkAdminRenewalCredential(ctx, tx, actor); auth != nil {
			return out, auth
		}
		return out, err
	}
	if replayed {
		out.Replayed = true
		return out, commitAdminRenewal(ctx, tx, actor)
	}
	if err = requireAdminServiceTransferFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	if err = lockServiceTransferNotifications(ctx, tx, service, true); err != nil {
		return out, err
	}
	quote, err := buildAdminServiceTransferQuote(ctx, tx, service, &state, in)
	if err != nil {
		return out, err
	}
	if quote.QuoteHash != in.QuoteHash {
		return out, conflict("stale_service_transfer_quote", "服务、归属或报价窗口已变化，请重新预览")
	}
	var auditFloor, jobFloor, changeFloor, cancelFloor int64
	if err = tx.QueryRow(ctx, `SELECT coalesce((SELECT max(id) FROM audit_events WHERE entity_type='service' AND entity_id=$1::bigint::text),0),coalesce((SELECT max(id) FROM provision_jobs WHERE service_id=$1),0),coalesce((SELECT max(id) FROM service_configuration_changes WHERE service_id=$1),0),coalesce((SELECT max(id) FROM service_cancellation_requests WHERE service_id=$1),0)`, service).Scan(&auditFloor, &jobFloor, &changeFloor, &cancelFloor); err != nil {
		return out, err
	}
	var after json.RawMessage
	if err = tx.QueryRow(ctx, `UPDATE services s SET user_id=$2,auto_renew=false,auto_renew_error='',renewal_generation=renewal_generation+1,version=version+1,updated_at=clock_timestamp() WHERE id=$1 RETURNING external_refund_order_snapshot(s)`, service, in.TargetCustomerID).Scan(&after); err != nil {
		return out, err
	}
	out = AdminServiceTransferResult{ServiceID: service, SourceCustomerID: in.SourceCustomerID, TargetCustomerID: in.TargetCustomerID, OwnerGeneration: state.Generation + 1, Version: state.Version + 1}
	var transfer int64
	err = tx.QueryRow(ctx, `INSERT INTO service_ownership_transfers(service_id,generation,source_customer_id,target_customer_id,original_customer_id,actor_id,service_version_before,service_version_after,audit_floor_id,job_floor_id,change_floor_id,cancellation_floor_id,reason,handover_evidence,idempotency_key,request_hash,service_before_snapshot,service_after_snapshot)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18) RETURNING id`, service, out.OwnerGeneration, in.SourceCustomerID, in.TargetCustomerID, state.Original, actor, state.Version, out.Version, auditFloor, jobFloor, changeFloor, cancelFloor, in.Reason, in.HandoverEvidence, key, hash, state.Snapshot, after).Scan(&transfer)
	if err != nil {
		return out, err
	}
	if err = Audit(ctx, tx, actor, "service.ownership.transferred", "service", service, map[string]any{"transfer_id": transfer, "source_customer_id": in.SourceCustomerID, "target_customer_id": in.TargetCustomerID, "owner_generation": out.OwnerGeneration, "request_hash": hash, "reason": in.Reason, "handover_evidence": in.HandoverEvidence}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, adminServiceTransferScope, key, hash, out); err != nil {
		return out, err
	}
	// Audit/receipt writes can wait; a revoked original credential must remain
	// observable until all business waits finish. Lock it only for final commit.
	if err = commitAdminRenewal(ctx, tx, actor); err != nil {
		return out, err
	}
	return out, nil
}
