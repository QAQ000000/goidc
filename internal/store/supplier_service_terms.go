package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// Caller owns the service row. Record only actual upstream reads; a local
// correction never changes this observation or claims a successful remote write.
func observeSupplierTerm(ctx context.Context, tx pgx.Tx, service int64, due *time.Time) (string, error) {
	var mode string
	if due == nil {
		// Supplier callbacks frequently omit deadlines. An omitted field is
		// not a new observation that invalidates the last confirmed deadline.
		err := tx.QueryRow(ctx, "SELECT mode FROM service_term_management WHERE service_id=$1", service).Scan(&mode)
		if errors.Is(err, pgx.ErrNoRows) {
			return "follow_supplier", nil
		}
		return mode, err
	}
	err := tx.QueryRow(ctx, `INSERT INTO service_term_management(service_id,supplier_next_due_at,supplier_observed_at)
 VALUES($1,$2,clock_timestamp()) ON CONFLICT(service_id) DO UPDATE
 SET supplier_next_due_at=excluded.supplier_next_due_at,supplier_observed_at=excluded.supplier_observed_at,updated_at=clock_timestamp()
 RETURNING mode`, service, due).Scan(&mode)
	return mode, err
}

func supplierTermManaged(ctx context.Context, tx pgx.Tx, service int64) (bool, error) {
	var managed bool
	err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_term_management WHERE service_id=$1 AND mode='managed')", service).Scan(&managed)
	return managed, err
}

// Only the confirmed renewal action may buy managed customer time. Ordinary
// sync/callbacks update observations only. A unique immutable job effect makes
// lost responses and reconciliation replays harmless to the customer deadline.
func mergeSupplierCustomerTerm(ctx context.Context, tx pgx.Tx, j ProvisionJob, remoteDue *time.Time) (*time.Time, error) {
	mode, err := observeSupplierTerm(ctx, tx, j.ServiceID, remoteDue)
	if err != nil {
		return nil, err
	}
	var current *time.Time
	if err = tx.QueryRow(ctx, "SELECT next_due_at FROM services WHERE id=$1", j.ServiceID).Scan(&current); err != nil {
		return nil, err
	}
	if mode != "managed" {
		if current == nil || remoteDue != nil && remoteDue.After(*current) {
			return remoteDue, nil
		}
		return current, nil
	}
	if j.Action != "renew" {
		return current, nil
	}
	var existing int64
	err = tx.QueryRow(ctx, "SELECT service_id FROM service_managed_renewal_effects WHERE job_id=$1", j.ID).Scan(&existing)
	if err == nil {
		if existing != j.ServiceID {
			return nil, conflict("managed_renewal_source_invalid", "续费权益事实与服务不匹配")
		}
		return current, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if j.ID < 1 || remoteDue == nil || current == nil {
		return nil, conflict("managed_renewal_source_invalid", "缺少真实确认期限或客户合同期限，请人工核验")
	}
	var payload []byte
	if err = tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2 AND action='renew'", j.ID, j.ServiceID).Scan(&payload); err != nil {
		return nil, err
	}
	var source supplierRenewalSnapshot
	if json.Unmarshal(payload, &source) != nil {
		return nil, conflict("managed_renewal_source_invalid", "续费缺少可信来源，请人工核验")
	}
	before, dueErr := supplierDueDate(source.PreviousDue)
	var expected time.Time
	var calendarValid bool
	if before != nil {
		expected, calendarValid = supplier.BillingPeriodEnd(*before, source.Cycle)
	}
	if dueErr != nil || !calendarValid || remoteDue.Before(expected) {
		return nil, conflict("managed_renewal_source_invalid", "上游尚未确认完整续费周期，请人工核验")
	}
	var after time.Time
	err = tx.QueryRow(ctx, `WITH term AS (
 SELECT s.id,s.billing_cycle,s.next_due_at,
 greatest(s.next_due_at,clock_timestamp()) AS period_start,
 (j.request_payload->>'local_invoice_id')::bigint AS invoice_id
 FROM services s JOIN provision_jobs j ON j.service_id=s.id WHERE s.id=$1 AND j.id=$2
 ), effect AS (
 INSERT INTO service_managed_renewal_effects(job_id,service_id,invoice_id,billing_cycle,before_due,period_start,after_due,supplier_after_due)
 SELECT $2,id,invoice_id,billing_cycle,next_due_at,period_start,
 ((period_start AT TIME ZONE 'Asia/Shanghai')+make_interval(months=>service_term_billing_months(billing_cycle))) AT TIME ZONE 'Asia/Shanghai',$3
 FROM term RETURNING after_due
 ) SELECT after_due FROM effect`, j.ServiceID, j.ID, remoteDue).Scan(&after)
	if err != nil {
		return nil, err
	}
	return &after, nil
}
