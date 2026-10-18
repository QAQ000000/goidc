package store

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

// reviewCancellationJob runs after the caller locks the service and job, before
// changing a resource or asking an operator to do so. Related financial rows are
// read without taking invoice locks: payment writers lock invoice -> service.
func reviewCancellationJob(ctx context.Context, tx pgx.Tx, job ProvisionJob) (bool, error) {
	if job.Action != "terminate" {
		return true, nil
	}
	var requestID *int64
	var availableAt time.Time
	var dispatched bool
	if err := tx.QueryRow(ctx, `SELECT cancellation_request_id,available_at,external_started_at IS NOT NULL
FROM provision_jobs WHERE id=$1 AND service_id=$2`, job.ID, job.ServiceID).Scan(&requestID, &availableAt, &dispatched); err != nil {
		return false, err
	}
	if requestID == nil {
		return true, nil
	}
	var status, kind string
	var resolvedAt, due *time.Time
	var requestServiceID int64
	var dueInFuture bool
	if err := tx.QueryRow(ctx, `SELECT c.service_id,c.status,c.type,c.resolved_at,s.next_due_at,
COALESCE(s.next_due_at>clock_timestamp(),false)
FROM service_cancellation_requests c JOIN services s ON s.id=c.service_id WHERE c.id=$1`, *requestID).
		Scan(&requestServiceID, &status, &kind, &resolvedAt, &due, &dueInFuture); err != nil {
		return false, err
	}
	stop := func(next, reason, message string) (bool, error) {
		// Once dispatched, even a superseded approval is an unknown external
		// outcome. Keep its evidence and require verification, never retry it.
		if dispatched {
			next = "waiting_verification"
		}
		if _, err := tx.Exec(ctx, `UPDATE provision_jobs SET status=$2,last_error=$3,locked_at=NULL,
completed_at=CASE WHEN $2 IN ('failed','cancelled') THEN clock_timestamp() ELSE NULL END,
updated_at=clock_timestamp() WHERE id=$1`, job.ID, next, message); err != nil {
			return false, err
		}
		action := "service.cancellation.execution_blocked"
		if next == "cancelled" {
			action = "service.cancellation.execution_cancelled"
		}
		if err := Audit(ctx, tx, 0, action, "service", job.ServiceID, map[string]any{
			"request_id": *requestID, "job_id": job.ID, "reason": reason, "status": next,
			"external_dispatched": dispatched, "next_due_at": due,
		}); err != nil {
			return false, err
		}
		return false, nil
	}
	if requestServiceID != job.ServiceID {
		return stop("failed", "cancellation_service_mismatch", "取消任务与服务不一致，请人工核验")
	}
	if status != "approved" {
		return stop("cancelled", "cancellation_not_approved", "取消申请未获批准或已重新审核，保留资源")
	}
	var unresolved bool
	if err := tx.QueryRow(ctx, `SELECT cancellation_renewal_payment_unresolved($1)`, job.ServiceID).Scan(&unresolved); err != nil {
		return false, err
	}
	if unresolved {
		return stop("failed", "renewal_payment_unresolved", "续费存在未终结在线支付或到账异常，核实资金前保留资源")
	}
	if kind == "immediate" {
		var paidAfterApproval bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
LEFT JOIN service_renewal_effects e ON e.invoice_id=i.id
WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='paid'
AND (i.paid_at>$2::timestamptz OR e.effective_at>$2::timestamptz))`, job.ServiceID, resolvedAt).Scan(&paidAfterApproval); err != nil {
			return false, err
		}
		if paidAfterApproval {
			return stop("failed", "renewal_paid_after_approval", "取消获批后存在已付续费，请重新审核权益后再终止")
		}
	}
	if kind == "end_of_billing" && due != nil && dueInFuture && due.After(availableAt) {
		if dispatched {
			return stop("failed", "cancellation_dispatched_before_entitlement_end", "服务权益已延长但终止已发送，请人工核验实际资源")
		}
		if _, err := tx.Exec(ctx, `UPDATE provision_jobs SET status='pending',available_at=$2,
locked_at=NULL,completed_at=NULL,last_error='服务权益已延长，终止任务已重新安排到当前到期日',
updated_at=clock_timestamp() WHERE id=$1`, job.ID, *due); err != nil {
			return false, err
		}
		if err := Audit(ctx, tx, 0, "service.cancellation.execution_rescheduled", "service", job.ServiceID, map[string]any{
			"request_id": *requestID, "job_id": job.ID, "previous_available_at": availableAt, "available_at": due,
		}); err != nil {
			return false, err
		}
		return false, nil
	}
	return true, nil
}
