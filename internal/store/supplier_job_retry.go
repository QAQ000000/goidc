package store

import (
	"context"
	"time"
)

// RetrySupplierJob only re-arms a failed job for which dispatch was never started.
// The worker retains the original upstream idempotency key and rechecks delivery state.
func (s *Store) RetrySupplierJob(ctx context.Context, actorID int64, sessionHash string, jobID int64, attempts int, updatedAt time.Time, key, requestHash string) (bool, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var serviceID int64
	if err = tx.QueryRow(ctx, "SELECT service_id FROM provision_jobs WHERE id=$1", jobID).Scan(&serviceID); err != nil {
		return false, err
	}
	var role string
	var roleID *int64
	if err = tx.QueryRow(ctx, "SELECT role,admin_role_id FROM administrators WHERE id=$1 AND status='active' FOR SHARE", actorID).Scan(&role, &roleID); err != nil {
		return false, conflict("permission_denied", "没有管理服务的权限")
	}
	if role != "super_admin" {
		if role != "admin" || roleID == nil {
			return false, conflict("permission_denied", "没有管理服务的权限")
		}
		var allowed bool
		if err = tx.QueryRow(ctx, "SELECT active AND 'services.view'=ANY(permissions) AND 'services.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE", *roleID).Scan(&allowed); err != nil || !allowed {
			return false, conflict("permission_denied", "没有管理服务的权限")
		}
	}
	var sessionUser int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE", sessionHash, actorID).Scan(&sessionUser); err != nil {
		return false, conflict("unauthorized", "登录已失效")
	}
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
		return false, err
	}
	var state, mode, upstream string
	var supplierID int64
	if err = tx.QueryRow(ctx, `SELECT status,provision_mode,upstream_id,coalesce((delivery_snapshot->>'supplier_id')::bigint,0)
 FROM services WHERE id=$1 FOR UPDATE`, serviceID).Scan(&state, &mode, &upstream, &supplierID); err != nil {
		return false, err
	}
	if mode != "supplier" || supplierID < 1 {
		return false, conflict("supplier_job_unavailable", "该任务不属于供应商交付")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return false, err
	}
	if replayed, err := ReplayOperation(ctx, tx, actorID, "admin:supplier.job.retry", key, requestHash, nil); err != nil {
		return false, err
	} else if replayed {
		return true, nil
	}
	var action, jobStatus string
	var actualAttempts int
	var actualUpdated time.Time
	var started *time.Time
	if err = tx.QueryRow(ctx, `SELECT action,status,attempts,updated_at,external_started_at FROM provision_jobs WHERE id=$1 AND service_id=$2 FOR UPDATE`, jobID, serviceID).Scan(&action, &jobStatus, &actualAttempts, &actualUpdated, &started); err != nil {
		return false, err
	}
	if jobStatus != "failed" || started != nil || attempts != actualAttempts || !updatedAt.Equal(actualUpdated) || action != "sync" || upstream == "" || !allowedServiceAction(state, action) {
		return false, conflict("supplier_retry_unavailable", "任务已变化或未证实可安全重试，请刷新后核验")
	}
	var inflight bool
	if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND id<>$2 AND status IN ('pending','running','waiting_manual','waiting_verification'))", serviceID, jobID).Scan(&inflight); err != nil {
		return false, err
	}
	if inflight {
		return false, conflict("supplier_retry_unavailable", "该服务已有待处理任务")
	}
	var active bool
	var running *int64
	if err = tx.QueryRow(ctx, "SELECT active,running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE", supplierID).Scan(&active, &running); err != nil {
		return false, err
	}
	if !active || running != nil {
		return false, conflict("supplier_retry_unavailable", "供应商账户不可用或正被任务占用")
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='pending',available_at=now(),locked_at=NULL,completed_at=NULL,last_error='',updated_at=clock_timestamp() WHERE id=$1`, jobID); err != nil {
		return false, err
	}
	if err = Audit(ctx, tx, actorID, "supplier.job.retried", "service", serviceID, map[string]any{"job_id": jobID, "attempts": attempts, "supplier_id": supplierID}); err != nil {
		return false, err
	}
	if err = SaveOperation(ctx, tx, actorID, "admin:supplier.job.retry", key, requestHash, map[string]any{"queued": true}); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}
