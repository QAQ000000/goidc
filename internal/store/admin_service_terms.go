package store

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

const adminServiceTermScope = "admin:service.term.adjust"

type AdminServiceTermInput struct {
	ExpectedVersion int64      `json:"expected_version"`
	Kind            string     `json:"kind"`
	DueAt           *time.Time `json:"next_due_at"`
	Reason          string     `json:"reason"`
	QuoteHash       string     `json:"quote_hash,omitempty"`
}

type AdminServiceTermPreview struct {
	ServiceID          int64      `json:"service_id"`
	CustomerID         int64      `json:"customer_id"`
	ServiceVersion     int64      `json:"service_version"`
	Kind               string     `json:"kind"`
	Mode               string     `json:"mode"`
	CurrentDueAt       *time.Time `json:"current_due_at"`
	SupplierNextDueAt  *time.Time `json:"supplier_next_due_at"`
	SupplierObservedAt *time.Time `json:"supplier_observed_at"`
	NextDueAt          time.Time  `json:"next_due_at"`
	QuoteHash          string     `json:"quote_hash"`
	ExpiresAt          time.Time  `json:"expires_at"`
}

type AdminServiceTermResult struct {
	ServiceID          int64      `json:"service_id"`
	CustomerID         int64      `json:"customer_id"`
	ServiceVersion     int64      `json:"service_version"`
	Kind               string     `json:"kind"`
	Mode               string     `json:"mode"`
	BeforeDueAt        *time.Time `json:"before_due_at"`
	NextDueAt          time.Time  `json:"next_due_at"`
	SupplierNextDueAt  *time.Time `json:"supplier_next_due_at"`
	SupplierObservedAt *time.Time `json:"supplier_observed_at"`
	Replayed           bool       `json:"replayed"`
}

type serviceTermState struct {
	Customer, Version          int64
	Status, Cycle, Mode        string
	Due, SupplierDue, Observed *time.Time
	Released                   bool
}

func validateAdminServiceTerm(in *AdminServiceTermInput, apply bool) error {
	in.Reason = strings.TrimSpace(in.Reason)
	if in.DueAt != nil {
		due := in.DueAt.UTC()
		in.DueAt = &due
	}
	if in.ExpectedVersion < 1 || (in.Kind != "correction" && in.Kind != "compensation") || in.DueAt == nil || in.DueAt.IsZero() ||
		in.DueAt.Year() < 2000 || in.DueAt.Year() > 9999 || in.DueAt.Nanosecond()%1000 != 0 ||
		in.Reason == "" || !contentTextValid(in.Reason, 500, true) || (apply && !validRenewalBatchID(in.QuoteHash)) || (!apply && in.QuoteHash != "") {
		return &AccountBusinessError{400, "invalid_service_term", "请明确提供服务版本、调整类型、精确到微秒的有效期限及原因"}
	}
	return nil
}

func adminServiceTermPermission(kind string) string {
	if kind == "compensation" {
		return "services.term.compensate"
	}
	return "services.term.correct"
}

// Match account/finance order, and defer credential locks until business waits
// finish. A historical receipt still requires today's authority and credential.
func lockAdminServiceTerm(ctx context.Context, tx pgx.Tx, actor, service int64, kind string) (serviceTermState, error) {
	var out serviceTermState
	var owner int64
	if err := tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", service).Scan(&owner); err != nil {
		return out, err
	}
	if err := lockAdminRenewalAuthority(ctx, tx, actor, owner, "services.view", adminServiceTermPermission(kind)); err != nil {
		return out, err
	}
	var active bool
	if err := tx.QueryRow(ctx, "SELECT status='active' FROM users WHERE id=$1 AND role='customer' FOR SHARE", owner).Scan(&active); err != nil {
		return out, err
	}
	if err := tx.QueryRow(ctx, `SELECT user_id,version,status,billing_cycle,next_due_at,inventory_released FROM services WHERE id=$1 FOR UPDATE`, service).
		Scan(&out.Customer, &out.Version, &out.Status, &out.Cycle, &out.Due, &out.Released); err != nil {
		return out, err
	}
	if err := checkAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	if out.Customer != owner {
		return out, conflict("service_owner_changed", "服务客户已变更，请重新核对")
	}
	if !active {
		// Current account state is checked only for fresh mutations below.
		out.Status = "customer_inactive"
	}
	out.Mode = "follow_supplier"
	if err := tx.QueryRow(ctx, `SELECT mode,supplier_next_due_at,supplier_observed_at FROM service_term_management WHERE service_id=$1`, service).
		Scan(&out.Mode, &out.SupplierDue, &out.Observed); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	return out, nil
}

func requireAdminServiceTermFresh(ctx context.Context, tx pgx.Tx, service int64, state serviceTermState, in AdminServiceTermInput) error {
	if state.Version != in.ExpectedVersion {
		return conflict("stale_service", "服务已更新，请重新预览")
	}
	if state.Status == "customer_inactive" {
		return conflict("customer_inactive", "客户已停用，不能调整服务期限")
	}
	if state.Status != "active" && state.Status != "suspended" || supplier.BillingMonths(state.Cycle) == 0 || state.Due == nil || state.Released {
		return conflict("service_term_unavailable", "只有已交付且未终止的周期服务可以调整期限")
	}
	if in.DueAt.Equal(*state.Due) || in.Kind == "compensation" && !in.DueAt.After(*state.Due) {
		return conflict("invalid_service_term_change", "补偿必须延后期限，纠错期限须与当前期限不同")
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
	var sharedReason string
	if err := tx.QueryRow(ctx, "SELECT service_manual_term_reason($1)", service).Scan(&sharedReason); err != nil {
		return err
	}
	if sharedReason != "" {
		return conflict(sharedReason, "服务存在未完成的账单、退款、变更或交付操作，请先处理")
	}
	var blocked bool
	if err := tx.QueryRow(ctx, `SELECT external_refund_order_blocked(s.order_id)
 OR EXISTS(SELECT 1 FROM invoices i JOIN external_refunds r ON r.invoice_id=i.id
  WHERE r.status='pending_review' AND (i.order_id=s.order_id OR EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id)))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id LEFT JOIN provision_jobs j ON j.idempotency_key='supplier-renew-'||i.id::text
  WHERE ii.service_id=s.id AND ii.item_type='renew' AND i.status='paid' AND s.provision_mode='supplier' AND coalesce(j.status,'')<>'succeeded')
 FROM services s WHERE s.id=$1`, service).Scan(&blocked); err != nil {
		return err
	}
	if blocked {
		return conflict("service_term_operation_pending", "服务存在退款冻结、待核实收款或未完成供应商续费")
	}
	return nil
}

func buildAdminServiceTermPreview(ctx context.Context, tx pgx.Tx, service int64, state serviceTermState, in AdminServiceTermInput) (AdminServiceTermPreview, error) {
	out := AdminServiceTermPreview{ServiceID: service, CustomerID: state.Customer, ServiceVersion: state.Version, Kind: in.Kind, Mode: state.Mode,
		CurrentDueAt: state.Due, SupplierNextDueAt: state.SupplierDue, SupplierObservedAt: state.Observed, NextDueAt: *in.DueAt}
	var now time.Time
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&now); err != nil {
		return out, err
	}
	out.ExpiresAt = time.Unix((now.Unix()/300+1)*300, 0).UTC()
	out.QuoteHash = accountBusinessHash(struct {
		Preview AdminServiceTermPreview
		Reason  string
	}{out, in.Reason})
	return out, nil
}

func (s *Store) AdminServiceTermQuote(ctx context.Context, actor, service int64, in AdminServiceTermInput) (AdminServiceTermPreview, error) {
	var out AdminServiceTermPreview
	if err := validateAdminServiceTerm(&in, false); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 {
		return out, &AccountBusinessError{400, "invalid_service_term", "管理员或服务编号无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceTerm(ctx, tx, actor, service, in.Kind)
	if err != nil {
		return out, err
	}
	if err = requireAdminServiceTermFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	out, err = buildAdminServiceTermPreview(ctx, tx, service, state, in)
	if err != nil {
		return out, err
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}

func (s *Store) AdminAdjustServiceTerm(ctx context.Context, actor, service int64, in AdminServiceTermInput, key string) (out AdminServiceTermResult, err error) {
	key = strings.TrimSpace(key)
	if err = validateAdminServiceTerm(&in, true); err != nil {
		return out, err
	}
	if actor < 1 || service < 1 || len(key) < 8 || len(key) > 128 {
		return out, &AccountBusinessError{400, "invalid_service_term", "管理员、服务编号或幂等键无效"}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	state, err := lockAdminServiceTerm(ctx, tx, actor, service, in.Kind)
	if err != nil {
		return out, err
	}
	hash := accountBusinessHash(struct {
		CustomerID, ServiceID int64
		Input                 AdminServiceTermInput
	}{state.Customer, service, in})
	replayed, err := ReplayOperation(ctx, tx, actor, adminServiceTermScope, key, hash, &out)
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
	if err = requireAdminServiceTermFresh(ctx, tx, service, state, in); err != nil {
		return out, err
	}
	preview, err := buildAdminServiceTermPreview(ctx, tx, service, state, in)
	if err != nil {
		return out, err
	}
	if preview.QuoteHash != in.QuoteHash {
		return out, conflict("stale_service_term_quote", "期限预览已过期或变化，请重新预览")
	}
	if _, err = tx.Exec(ctx, `INSERT INTO service_term_management(service_id,mode) VALUES($1,'managed')
 ON CONFLICT(service_id) DO UPDATE SET mode='managed',updated_at=clock_timestamp()`, service); err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `UPDATE services SET next_due_at=$2,renewal_generation=renewal_generation+1,
	 version=version+1,auto_renew_error='',resource_info=resource_info-'supplier_renewal_recovery_job_id',updated_at=clock_timestamp() WHERE id=$1`, service, in.DueAt); err != nil {
		return out, err
	}
	out = AdminServiceTermResult{ServiceID: service, CustomerID: state.Customer, ServiceVersion: state.Version + 1, Kind: in.Kind, Mode: "managed",
		BeforeDueAt: state.Due, NextDueAt: *in.DueAt, SupplierNextDueAt: state.SupplierDue, SupplierObservedAt: state.Observed}
	if err = Audit(ctx, tx, actor, "service.term."+in.Kind, "service", service, map[string]any{"customer_id": state.Customer, "reason": in.Reason, "before_due_at": state.Due, "next_due_at": in.DueAt, "supplier_next_due_at": state.SupplierDue, "mode": "managed"}); err != nil {
		return out, err
	}
	if err = SaveOperation(ctx, tx, actor, adminServiceTermScope, key, hash, out); err != nil {
		return out, err
	}
	if err = lockAdminRenewalCredential(ctx, tx, actor); err != nil {
		return out, err
	}
	var quoteCurrent bool
	if err = tx.QueryRow(ctx, "SELECT clock_timestamp()<$1", preview.ExpiresAt).Scan(&quoteCurrent); err != nil {
		return out, err
	}
	if !quoteCurrent {
		return out, conflict("stale_service_term_quote", "期限预览已过期，请重新预览")
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}
