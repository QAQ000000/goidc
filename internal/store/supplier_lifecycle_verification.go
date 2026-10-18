package store

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

func supplierLifecycleAction(action string) bool {
	return action == "suspend" || action == "resume" || action == "terminate"
}

type supplierManualLifecycleSnapshot struct {
	job                       ProvisionJob
	status, current, upstream string
	serviceVersion            int64
	delivery, payload, result []byte
	snapshot                  supplierSnapshot
	binding                   SupplierBinding
	accountVersion            int64
}

// Hold current authorization rows through each short database phase. No locks
// or durable supplier account lease are held across the read-only network call.
func requireSupplierLifecycleManage(ctx context.Context, tx pgx.Tx, actorID int64) error {
	var role string
	var roleID *int64
	if err := tx.QueryRow(ctx, `SELECT role,admin_role_id FROM users
WHERE id=$1 AND status='active' FOR SHARE`, actorID).Scan(&role, &roleID); err != nil {
		return conflict("permission_denied", "没有核验供应商服务的权限")
	}
	if role == "super_admin" {
		return nil
	}
	if role != "admin" || roleID == nil {
		return conflict("permission_denied", "没有核验供应商服务的权限")
	}
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT active AND 'services.view'=ANY(permissions)
AND 'services.manage'=ANY(permissions) FROM admin_roles WHERE id=$1 FOR SHARE`, *roleID).Scan(&allowed); err != nil || !allowed {
		return conflict("permission_denied", "没有核验供应商服务的权限")
	}
	return nil
}

func requireSupplierLifecycleSession(ctx context.Context, tx pgx.Tx, actorID int64, sessionHash string) error {
	if sessionHash == "" {
		return conflict("unauthorized", "请先登录后核验人工任务")
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2
AND expires_at>clock_timestamp() FOR SHARE`, sessionHash, actorID).Scan(&sessionUser); err != nil {
		return conflict("unauthorized", "登录已失效，请重新登录后核验")
	}
	return nil
}

func commitSupplierLifecycleTx(ctx context.Context, tx pgx.Tx, actorID int64, sessionHash string) error {
	if err := requireSupplierLifecycleSession(ctx, tx, actorID, sessionHash); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Store) readSupplierManualLifecycle(ctx context.Context, tx pgx.Tx, actorID, serviceID int64, in SupplierVerificationInput) (supplierManualLifecycleSnapshot, bool, error) {
	var out supplierManualLifecycleSnapshot
	if err := requireSupplierLifecycleManage(ctx, tx, actorID); err != nil {
		return out, false, err
	}
	if err := requireSupplierLifecycleSession(ctx, tx, actorID, in.SessionHash); err != nil {
		return out, false, err
	}
	if err := LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
		return out, false, err
	}
	// Automatic billing actions lock their invoice before the service, matching
	// payment settlement. The full task identity is checked again under its lock.
	var candidate ProvisionJob
	if err := tx.QueryRow(ctx, `SELECT id,service_id,action,attempts FROM provision_jobs
WHERE id=$1 AND service_id=$2`, in.JobID, serviceID).
		Scan(&candidate.ID, &candidate.ServiceID, &candidate.Action, &candidate.Attempts); err != nil {
		return out, false, err
	}
	if _, err := lockOverdueSuspensionInvoice(ctx, tx, candidate); err != nil {
		return out, false, err
	}
	var mode string
	if err := tx.QueryRow(ctx, `SELECT provision_mode,status,upstream_id,version,delivery_snapshot
FROM services WHERE id=$1 FOR UPDATE`, serviceID).Scan(&mode, &out.current, &out.upstream, &out.serviceVersion, &out.delivery); err != nil {
		return out, false, err
	}
	if mode != "supplier" || json.Unmarshal(out.delivery, &out.snapshot) != nil || out.snapshot.SupplierID < 1 || out.snapshot.BindingID < 1 {
		return out, false, conflict("supplier_snapshot_invalid", "供应商交付快照无效")
	}
	var dispatched bool
	if err := tx.QueryRow(ctx, `SELECT id,service_id,action,attempts,status,request_payload,result,
external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1 AND service_id=$2 FOR UPDATE`, in.JobID, serviceID).
		Scan(&out.job.ID, &out.job.ServiceID, &out.job.Action, &out.job.Attempts, &out.status, &out.payload, &out.result, &dispatched); err != nil {
		return out, false, err
	}
	var marker struct {
		ManualLifecycle bool `json:"manual_lifecycle"`
	}
	if !supplierLifecycleAction(out.job.Action) || dispatched || json.Unmarshal(out.payload, &marker) != nil || !marker.ManualLifecycle || out.job.Attempts != in.ExpectedAttempts {
		return out, false, conflict("supplier_manual_unavailable", "该任务不是未派发的供应商人工生命周期任务，请刷新后核验")
	}
	if out.status == "succeeded" || out.status == "failed" {
		var result struct {
			Verification *SupplierVerificationInput `json:"manual_lifecycle_verification"`
		}
		if json.Unmarshal(out.result, &result) == nil && result.Verification != nil {
			// The receipt never stores a session token hash. A fresh, authorized
			// session may replay the same completed business acknowledgement.
			result.Verification.SessionHash = in.SessionHash
			if *result.Verification == in {
				if err := requireSupplierLifecycleSession(ctx, tx, actorID, in.SessionHash); err != nil {
					return out, false, err
				}
				return out, true, nil
			}
		}
		return out, false, conflict("version_conflict", "任务已经使用不同的核验结果完成，请重新加载")
	}
	if out.status != "waiting_manual" {
		return out, false, conflict("version_conflict", "人工任务已变更，请重新加载")
	}
	// Billing intent may have become obsolete while an operator was acting
	// upstream. The invoice lock orders reconciliation with payment writers;
	// eligibility never substitutes for confirming the actual external result.
	if err := requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, false, err
	}
	if in.Outcome == "delivered" && !allowedServiceAction(out.current, out.job.Action) && !supplier.LifecycleTargetConfirmed(out.job.Action, out.current) {
		return out, false, conflict("invalid_service_state", "服务当前状态不允许登记此动作，请先核验资源状态")
	}
	upstreamID, err := strconv.ParseInt(out.upstream, 10, 64)
	if err != nil || upstreamID < 1 || (in.Outcome == "delivered" && in.UpstreamID != upstreamID) {
		return out, false, conflict("supplier_resource_mismatch", "人工核验必须使用服务原有的上游资源")
	}
	out.binding, err = s.supplierBindingWith(ctx, tx, out.snapshot.BindingID, false)
	if err != nil {
		return out, false, err
	}
	if out.binding.SupplierID != out.snapshot.SupplierID || out.binding.Provider != out.snapshot.Provider {
		return out, false, conflict("supplier_snapshot_invalid", "供应商交付绑定不匹配")
	}
	var active bool
	var running *int64
	if err = tx.QueryRow(ctx, `SELECT active,version,running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE`, out.snapshot.SupplierID).
		Scan(&active, &out.accountVersion, &running); err != nil {
		return out, false, err
	}
	if !active || running != nil {
		return out, false, conflict("supplier_manual_unavailable", "供应商账户已停用或仍有进行中及待核验操作，请先处理")
	}
	return out, false, nil
}

// VerifySupplierLifecycleJob records an operator's upstream action. It only
// reads the original resource; it never sends Execute or takes an account lease.
func (s *Store) VerifySupplierLifecycleJob(ctx context.Context, actorID, serviceID int64, in SupplierVerificationInput) error {
	in.Note = strings.TrimSpace(in.Note)
	if in.JobID < 1 || in.ExpectedAttempts < 0 || len(in.Note) < 8 || !contentTextValid(in.Note, 1000, true) ||
		(in.Outcome != "delivered" && in.Outcome != "not_applied") ||
		(in.Outcome == "delivered" && in.UpstreamID < 1) ||
		(in.Outcome == "not_applied" && (!in.AbsenceAcknowledged || in.UpstreamID != 0)) {
		return conflict("invalid_supplier_verification", "请填写任务版本、实际处理结果和核验依据")
	}
	authTx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer authTx.Rollback(ctx)
	before, replayed, err := s.readSupplierManualLifecycle(ctx, authTx, actorID, serviceID, in)
	if err != nil {
		return err
	}
	if replayed {
		return commitSupplierLifecycleTx(ctx, authTx, actorID, in.SessionHash)
	}
	var adapter supplier.Adapter
	if in.Outcome == "delivered" {
		var version int64
		adapter, version, err = s.supplierAdapterWith(ctx, authTx, before.snapshot.SupplierID)
		if err != nil {
			return err
		}
		if version != before.accountVersion || !adapter.Capabilities().Sync {
			return conflict("supplier_manual_unavailable", "该供应商暂不能读取实际资源状态，请继续保留人工待办")
		}
	}
	if err = commitSupplierLifecycleTx(ctx, authTx, actorID, in.SessionHash); err != nil {
		return err
	}
	var remote supplier.Service
	if in.Outcome == "delivered" {
		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		remote, err = adapter.Sync(readCtx, in.UpstreamID)
		cancel()
		if err != nil {
			return supplierReadError(err)
		}
		if remote.UpstreamID != in.UpstreamID || remote.ProductID != before.binding.UpstreamProductID || !supplier.LifecycleTargetConfirmed(before.job.Action, remote.Status) {
			return conflict("supplier_resource_unconfirmed", "上游资源归属或动作目标状态尚未确认，请继续保留人工待办")
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	after, replayed, err := s.readSupplierManualLifecycle(ctx, tx, actorID, serviceID, in)
	if err != nil {
		return err
	}
	if replayed {
		return commitSupplierLifecycleTx(ctx, tx, actorID, in.SessionHash)
	}
	if before.job != after.job || before.current != after.current || before.upstream != after.upstream || before.serviceVersion != after.serviceVersion ||
		before.accountVersion != after.accountVersion || !bytes.Equal(before.delivery, after.delivery) || !bytes.Equal(before.payload, after.payload) {
		return conflict("version_conflict", "读取上游期间服务、任务或供应商配置已变更，请重新核验")
	}
	proceed, err := reviewCancellationJob(ctx, tx, after.job)
	if err != nil {
		return err
	}
	if !proceed {
		if err = commitSupplierLifecycleTx(ctx, tx, actorID, in.SessionHash); err != nil {
			return err
		}
		return conflict("cancellation_job_blocked", "取消任务已停止或重新安排，请刷新后核验服务权益")
	}
	state := "failed"
	if in.Outcome == "delivered" {
		if err = applySupplierRemote(ctx, tx, after.job, remote, s.ProvisionEncryptionKey); err != nil {
			return err
		}
		if after.job.Action == "suspend" && after.current == "active" {
			if err = s.QueueServiceSuspendedNotifications(ctx, tx, serviceID); err != nil {
				return err
			}
		}
		if err = s.queueServiceLifecycleResult(ctx, tx, serviceID, after.current, after.job.Action, after.job.ID); err != nil {
			return err
		}
		state = "succeeded"
	}
	receipt, _ := json.Marshal(in)
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status=$2,
last_error=CASE WHEN $2='failed' THEN '管理员已确认本次人工操作未生效，服务状态保持原样' ELSE '' END,
result=result||jsonb_build_object('manual_lifecycle_verification',$3::jsonb),
locked_at=NULL,completed_at=clock_timestamp(),updated_at=clock_timestamp() WHERE id=$1`, after.job.ID, state, receipt); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actorID, "supplier.lifecycle.manual_verified", "service", serviceID, map[string]any{
		"job_id": after.job.ID, "supplier_id": after.snapshot.SupplierID, "action": after.job.Action,
		"outcome": in.Outcome, "upstream_id": in.UpstreamID, "note": in.Note, "absence_acknowledged": in.AbsenceAcknowledged,
	}); err != nil {
		return err
	}
	return commitSupplierLifecycleTx(ctx, tx, actorID, in.SessionHash)
}
