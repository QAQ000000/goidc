package store

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/supplier"
)

type SupplierVerificationInput struct {
	JobID               int64  `json:"job_id"`
	ExpectedAttempts    int    `json:"expected_attempts"`
	UpstreamID          int64  `json:"upstream_id"`
	Outcome             string `json:"outcome"`
	Note                string `json:"note"`
	AbsenceAcknowledged bool   `json:"absence_acknowledged"`
	SessionHash         string `json:"-"`
}

// Operator reconciliation is intentionally explicit: absence cannot be inferred
// from a missing host if the supplier still has an unpaid or paid invoice.
func (s *Store) VerifySupplierJob(ctx context.Context, actor, serviceID int64, in SupplierVerificationInput) error {
	in.Note = strings.TrimSpace(in.Note)
	if in.JobID < 1 || in.ExpectedAttempts < 1 || len(in.Note) < 8 || !contentTextValid(in.Note, 1000, true) || (in.Outcome != "delivered" && in.Outcome != "not_applied") || (in.Outcome == "not_applied" && (!in.AbsenceAcknowledged || in.UpstreamID != 0)) || (in.Outcome == "delivered" && in.UpstreamID < 1) {
		return conflict("invalid_supplier_verification", "请填写完整核验结果、任务次数和核验依据")
	}
	// Scope is checked before using supplier credentials or contacting upstream.
	authTx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer authTx.Rollback(ctx)
	if err = requireSupplierLifecycleManage(ctx, authTx, actor); err != nil {
		return err
	}
	if err = requireSupplierLifecycleSession(ctx, authTx, actor, in.SessionHash); err != nil {
		return err
	}
	if err = LockAdminResourceCustomer(ctx, authTx, actor, "service", serviceID); err != nil {
		return err
	}
	if err = commitSupplierLifecycleTx(ctx, authTx, actor, in.SessionHash); err != nil {
		return err
	}
	var raw []byte
	if err = s.Pool.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1 AND provision_mode='supplier'", serviceID).Scan(&raw); err != nil {
		return err
	}
	var snap supplierSnapshot
	if json.Unmarshal(raw, &snap) != nil {
		return conflict("supplier_snapshot_invalid", "交付快照无效")
	}
	var remote supplier.Service
	var verifiedAction string
	binding, err := s.supplierBinding(ctx, snap.BindingID, false)
	if err != nil {
		return err
	}
	if binding.SupplierID != snap.SupplierID || binding.Provider != snap.Provider {
		return conflict("supplier_snapshot_invalid", "供应商交付绑定不匹配")
	}
	if in.Outcome == "delivered" {
		adapter, _, e := s.supplierAdapter(ctx, snap.SupplierID)
		if e != nil {
			return e
		}
		var payload []byte
		var invoiceID int64
		if e = s.Pool.QueryRow(ctx, `SELECT action,request_payload,coalesce((result->>'invoice_id')::bigint,0) FROM provision_jobs WHERE id=$1 AND service_id=$2 AND status='waiting_verification' AND attempts=$3`, in.JobID, serviceID, in.ExpectedAttempts).Scan(&verifiedAction, &payload, &invoiceID); e != nil {
			return e
		}
		if verifiedAction == "renew" {
			var snap supplierRenewalSnapshot
			renew, ok := adapter.(supplier.RenewalAdapter)
			if !ok || json.Unmarshal(payload, &snap) != nil || snap.UpstreamID != in.UpstreamID || invoiceID < 1 {
				return conflict("renewal_confirmation_pending", "缺少可信续费账单标识，请核验并记录账单")
			}
			_, remote, e = renew.ConfirmRenewal(ctx, snap.RenewalRequest, invoiceID)
		} else {
			remote, e = adapter.Sync(ctx, in.UpstreamID)
		}
		if e != nil {
			return supplierReadError(e)
		}
		confirmed := remote.Status == "Active" || remote.Status == "active" || remote.Status == "Suspended" || remote.Status == "suspended"
		if supplierLifecycleAction(verifiedAction) {
			confirmed = supplier.LifecycleTargetConfirmed(verifiedAction, remote.Status)
		}
		if remote.UpstreamID != in.UpstreamID || remote.ProductID != binding.UpstreamProductID || !confirmed {
			return conflict("supplier_resource_unconfirmed", "上游资源归属或动作目标状态尚未确认，请继续核验")
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = requireSupplierLifecycleManage(ctx, tx, actor); err != nil {
		return err
	}
	if err = requireSupplierLifecycleSession(ctx, tx, actor, in.SessionHash); err != nil {
		return err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actor, "service", serviceID); err != nil {
		return err
	}
	var existing, current string
	if err = tx.QueryRow(ctx, "SELECT upstream_id,status FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&existing, &current); err != nil {
		return err
	}
	var j ProvisionJob
	var state string
	if err = tx.QueryRow(ctx, "SELECT id,service_id,action,attempts,status FROM provision_jobs WHERE id=$1 AND service_id=$2 FOR UPDATE", in.JobID, serviceID).Scan(&j.ID, &j.ServiceID, &j.Action, &j.Attempts, &state); err != nil {
		return err
	}
	if state != "waiting_verification" || j.Attempts != in.ExpectedAttempts {
		return conflict("version_conflict", "交付任务已变更，请重新加载")
	}
	if in.Outcome == "delivered" && (j.Action != verifiedAction ||
		(supplierLifecycleAction(j.Action) && !allowedServiceAction(current, j.Action) && !supplier.LifecycleTargetConfirmed(j.Action, current))) {
		return conflict("version_conflict", "动作或服务状态已变更，请重新核验")
	}
	if in.Outcome == "delivered" && existing != "" && existing != strconv.FormatInt(in.UpstreamID, 10) {
		return conflict("supplier_resource_exists", "核验结果不能替换服务已有上游资源")
	}
	var running *int64
	if err = tx.QueryRow(ctx, "SELECT running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE", snap.SupplierID).Scan(&running); err != nil {
		return err
	}
	if running == nil || *running != j.ID {
		return conflict("supplier_lease_lost", "该任务不持有供应商账户租约")
	}
	if in.Outcome == "not_applied" && j.Action == "create" && existing != "" {
		return conflict("supplier_resource_exists", "服务已有上游资源，不能确认未执行")
	}
	state = "failed"
	if in.Outcome == "delivered" {
		if j.Action == "reset_password" {
			wanted, credentialErr := s.pendingJobCredentials(ctx, tx, j)
			if credentialErr != nil || wanted.Password == "" || remote.Password != wanted.Password {
				return conflict("password_confirmation_pending", "上游密码尚未与请求一致，请继续核验")
			}
		}
		if j.Action == "reinstall" {
			template, templateErr := pendingReinstallTemplate(ctx, tx, j)
			if templateErr != nil || remote.OS != template.Name || remote.Password == "" {
				return conflict("reinstall_confirmation_pending", "上游系统与登录凭据尚未确认，请继续核验")
			}
		}
		if err = applySupplierRemote(ctx, tx, j, remote, s.ProvisionEncryptionKey); err != nil {
			return err
		}
		if j.Action == "create" {
			if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, serviceID); err != nil {
				return err
			}
		}
		if j.Action == "suspend" && existing != "" {
			if err = s.QueueServiceSuspendedNotifications(ctx, tx, serviceID); err != nil {
				return err
			}
		}
		if err = s.queueServiceLifecycleResult(ctx, tx, serviceID, current, j.Action, j.ID); err != nil {
			return err
		}
		state = "succeeded"
	} else if j.Action == "create" {
		if _, err = tx.Exec(ctx, "UPDATE services SET status='failed',last_error='管理员已核验上游未产生购买结果',version=version+1,updated_at=now() WHERE id=$1", serviceID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status=$2,last_error=CASE WHEN $2='failed' THEN '管理员已核验操作未生效' ELSE '' END,completed_at=now(),locked_at=NULL,updated_at=now(),result=result||jsonb_build_object('verified_upstream_id',$3::bigint,'verification_outcome',$4::text) WHERE id=$1`, j.ID, state, in.UpstreamID, in.Outcome); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE supplier_accounts SET running_job_id=NULL,updated_at=now() WHERE id=$1", snap.SupplierID); err != nil {
		return err
	}
	if err = Audit(ctx, tx, actor, "supplier.delivery.verified", "service", serviceID, map[string]any{"job_id": j.ID, "outcome": in.Outcome, "upstream_id": in.UpstreamID, "note": in.Note, "absence_acknowledged": in.AbsenceAcknowledged}); err != nil {
		return err
	}
	return commitSupplierLifecycleTx(ctx, tx, actor, in.SessionHash)
}
