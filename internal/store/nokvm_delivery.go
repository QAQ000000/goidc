package store

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/jackc/pgx/v5"
)

// All database locks are released before any supplier request. Persisting the
// dispatch marker first makes crash recovery conservative even if commit or
// response delivery is ambiguous. Attempts fence old workers and late replies.
func (s *Store) processNOKVMJob(ctx context.Context, claimed ProvisionJob) error {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	overdueDue, err := lockOverdueSuspensionInvoice(ctx, tx, claimed)
	if err != nil {
		return err
	}
	var job ProvisionJob
	if err = tx.QueryRow(ctx, "SELECT id,service_id,action FROM provision_jobs WHERE id=$1", claimed.ID).Scan(&job.ID, &job.ServiceID, &job.Action); err != nil {
		return err
	}
	var mode, current, upstream string
	var owner int64
	var snapshot []byte
	if err = tx.QueryRow(ctx, "SELECT provision_mode,status,user_id,upstream_id,delivery_snapshot FROM services WHERE id=$1 FOR UPDATE", job.ServiceID).Scan(&mode, &current, &owner, &upstream, &snapshot); err != nil {
		return err
	}
	if err = requireServiceReviewed(ctx, tx, job.ServiceID); err != nil {
		return err
	}
	var state string
	var valid, dispatched bool
	if err = tx.QueryRow(ctx, `SELECT status,attempts,coalesce(locked_at>clock_timestamp()-interval '5 minutes',false),external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1 FOR UPDATE`, job.ID).Scan(&state, &job.Attempts, &valid, &dispatched); err != nil {
		return err
	}
	if state != "running" || job.Attempts != claimed.Attempts || !valid || dispatched || mode != "nokvm" || job.ServiceID != claimed.ServiceID || job.Action != claimed.Action {
		return conflict("job_lease_lost", "任务租约已失效或已发送至供应商")
	}
	if !overdueDue {
		if err = cancelObsoleteOverdueSuspension(ctx, tx, claimed); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	fail := func(code string) error {
		_, e := tx.Exec(ctx, "UPDATE provision_jobs SET status='failed',last_error=$2,completed_at=now(),locked_at=NULL,updated_at=now() WHERE id=$1", job.ID, code)
		if e != nil {
			return e
		}
		return tx.Commit(ctx)
	}
	if !allowedServiceAction(current, job.Action) {
		return fail("服务当前状态不允许该供应商动作")
	}
	var delivery provision.Delivery
	if json.Unmarshal(snapshot, &delivery) != nil || delivery.Resources.Validate() != nil {
		return fail("NOKVM 资源配置无效")
	}
	adapter := s.Provisioners[delivery.ProfileID]
	if adapter == nil || len(s.ProvisionEncryptionKey) != 32 {
		return fail("NOKVM 供应商或交付加密密钥未配置")
	}
	request := provision.Request{Action: job.Action, UpstreamID: upstream, UserID: owner, ServiceID: job.ServiceID, Resources: delivery.Resources}
	if job.Action == "create" {
		if upstream != "" {
			return fail("服务已关联供应商资源，禁止重复开通")
		}
		request.Credentials, err = provision.NewCredentials()
		if err != nil {
			return err
		}
		sealed, e := provision.Seal(s.ProvisionEncryptionKey, job.ServiceID, request.Credentials)
		if e != nil {
			return e
		}
		if _, err = tx.Exec(ctx, "UPDATE services SET credentials_encrypted=$2,status='provisioning',last_error='',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID, sealed); err != nil {
			return err
		}
	} else if !provision.ValidResourceID(upstream) {
		return fail("服务缺少有效供应商资源标识")
	}
	if job.Action == "reset_password" {
		request.Credentials, err = s.pendingJobCredentials(ctx, tx, job)
		if err != nil || !provision.ValidSystemPassword(request.Credentials.Password) {
			return fail("密码重置任务凭据不可用")
		}
	}
	if job.Action == "reinstall" {
		request.Template, err = pendingReinstallTemplate(ctx, tx, job)
		if err != nil {
			return fail("重装任务镜像配置无效")
		}
	}
	var storageRequest provision.StorageRequest
	if provision.IsStorageAction(job.Action) {
		storageRequest, err = pendingStorageRequest(ctx, tx, job)
		limit := delivery.Resources.Snapshots
		if storageRequest.Kind == "backup" {
			limit = delivery.Resources.Backups
		}
		if err != nil || storageRequest.ProfileID != delivery.ProfileID || storageRequest.UpstreamID != upstream || storageRequest.Limit != limit {
			return fail("快照或备份任务资源绑定已变更")
		}
	}
	var natRequest provision.NATRequest
	if provision.IsNATAction(job.Action) {
		natRequest, err = pendingNATRequest(ctx, tx, job)
		if err != nil || natRequest.ProfileID != delivery.ProfileID || natRequest.UpstreamID != upstream || natRequest.Limit != natLimit(delivery, natRequest.Kind) {
			return fail("NAT 任务资源绑定已变更")
		}
	}
	var cdromRequest provision.CDROMRequest
	if provision.IsCDROMAction(job.Action) {
		cdromRequest, err = pendingCDROMRequest(ctx, tx, job)
		if err != nil || cdromRequest.ProfileID != delivery.ProfileID || cdromRequest.UpstreamID != upstream {
			return fail("光驱任务资源绑定已变更")
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET external_started_at=clock_timestamp(),updated_at=now() WHERE id=$1", job.ID); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	timeout := 65 * time.Second
	if provision.IsStorageAction(job.Action) || provision.IsNATAction(job.Action) || provision.IsCDROMAction(job.Action) {
		timeout = 125 * time.Second
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	var result provision.Result
	var callErr error
	if provision.IsStorageAction(job.Action) {
		result, callErr = adapter.ExecuteStorage(callCtx, storageRequest)
	} else if provision.IsNATAction(job.Action) {
		result, callErr = adapter.ExecuteNAT(callCtx, natRequest)
	} else if provision.IsCDROMAction(job.Action) {
		result, callErr = adapter.ExecuteCDROM(callCtx, cdromRequest)
	} else {
		result, callErr = adapter.Execute(callCtx, request)
	}
	cancel()
	// SIGTERM can cancel the network call. Still durably record the unknown
	// outcome, using a short, independent context before the worker exits.
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishCancel()
	return s.finishNOKVMJob(finishCtx, job, result, callErr)
}

func (s *Store) finishNOKVMJob(ctx context.Context, job ProvisionJob, result provision.Result, callErr error) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		var current string
		if err = tx.QueryRow(ctx, "SELECT status FROM services WHERE id=$1 FOR UPDATE", job.ServiceID).Scan(&current); err != nil {
			return err
		}
		var status string
		var attempts int
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT status,attempts,coalesce(locked_at>clock_timestamp()-interval '5 minutes',false) FROM provision_jobs WHERE id=$1 AND external_started_at IS NOT NULL FOR UPDATE`, job.ID).Scan(&status, &attempts, &valid); err != nil {
			return err
		}
		if status != "running" || attempts != job.Attempts || !valid {
			return conflict("job_lease_lost", "供应商响应到达时任务租约已失效，请核验实际资源")
		}
		if callErr != nil {
			state, message := "waiting_verification", "供应商请求结果不明，请核验实际资源，禁止直接重试"
			var failure *provision.Error
			if errors.As(callErr, &failure) && !failure.Unknown {
				state = "failed"
				message = "供应商请求失败：" + failure.Code
			}
			// A read-only action cannot have written upstream state, so a failed read
			// or a local apply failure has no unknown result to reconcile. Report it
			// as retryable instead of blocking the service behind manual verification,
			// which would also block refunds and credential reads.
			if provision.IsReadOnlyAction(job.Action) {
				state, message = "failed", "供应商读取失败，请稍后重试"
			}
			if (job.Action == "reinstall" || provision.IsStorageAction(job.Action) || provision.IsNATAction(job.Action) || provision.IsCDROMAction(job.Action)) && result.CommandAccepted {
				message = "供应商已受理快照或备份操作，请核验实际结果，禁止重复提交"
				if provision.IsNATAction(job.Action) {
					message = "供应商已受理 NAT 操作，请核验实际结果，禁止重复提交"
				}
				if provision.IsCDROMAction(job.Action) {
					message = "供应商已受理光驱操作，请核验实际挂载状态与启动顺序，禁止重复提交"
				}
				if job.Action == "reinstall" {
					message = "供应商已受理重装，请核验安装完成及新登录密码，禁止重复提交"
				}
				raw, _ := json.Marshal(result)
				if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET result=$2 WHERE id=$1", job.ID, raw); err != nil {
					return err
				}
				if job.Action == "reinstall" || provision.IsStorageRestore(job.Action) {
					if _, err = tx.Exec(ctx, `UPDATE services SET resource_info=resource_info||jsonb_build_object('power','processing'),version=version+1,updated_at=now() WHERE id=$1`, job.ServiceID); err != nil {
						return err
					}
				}
			}
			if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status=$2,last_error=$3,locked_at=NULL,completed_at=CASE WHEN $2='failed' THEN now() ELSE NULL END,updated_at=now() WHERE id=$1", job.ID, state, message); err != nil {
				return err
			}
			if job.Action == "create" && state == "failed" {
				if _, err = tx.Exec(ctx, "UPDATE services SET status='failed',last_error=$2,version=version+1,updated_at=now() WHERE id=$1", job.ServiceID, message); err != nil {
					return err
				}
			}
			if err = Audit(ctx, tx, 0, "service.nokvm."+state, "service", job.ServiceID, map[string]any{"job_id": job.ID, "action": job.Action}); err != nil {
				return err
			}
			return nil
		}
		if err = s.applyNOKVMResult(ctx, tx, job, current, result); err != nil {
			return err
		}
		if job.Action == "create" {
			if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, job.ServiceID); err != nil {
				return err
			}
		}
		if job.Action == "suspend" && current == "active" {
			if err = s.QueueServiceSuspendedNotifications(ctx, tx, job.ServiceID); err != nil {
				return err
			}
		}
		if err = s.queueServiceLifecycleResult(ctx, tx, job.ServiceID, current, job.Action, job.ID); err != nil {
			return err
		}
		raw, _ := json.Marshal(result)
		if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='succeeded',result=$2,last_error='',locked_at=NULL,completed_at=now(),updated_at=now() WHERE id=$1", job.ID, raw); err != nil {
			return err
		}
		if provision.IsPowerAction(job.Action) {
			if err = enqueuePowerSync(ctx, tx, job); err != nil {
				return err
			}
		}
		if err = Audit(ctx, tx, 0, "service.nokvm.completed", "service", job.ServiceID, map[string]any{"job_id": job.ID, "action": job.Action}); err != nil {
			return err
		}
		return nil
	})
}

func enqueuePowerSync(ctx context.Context, tx pgx.Tx, job ProvisionJob) error {
	_, err := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
 VALUES($1,'sync','nokvm-power-sync-'||$2::bigint::text,clock_timestamp()+interval '5 seconds')`, job.ServiceID, job.ID)
	return err
}

func (s *Store) applyNOKVMResult(ctx context.Context, tx pgx.Tx, job ProvisionJob, current string, result provision.Result) error {
	if job.Action == "create" {
		if !provision.ValidResourceID(result.UpstreamID) || (current != "provisioning" && current != "queued" && current != "failed") {
			return conflict("invalid_delivery_state", "开通资源或本地状态无效")
		}
		raw, _ := json.Marshal(result)
		_, err := tx.Exec(ctx, `UPDATE services SET status='active',upstream_id=$2,username=delivery_snapshot->'resources'->>'login',resource_info=$3,last_error='',version=version+1,updated_at=now(),
next_due_at=CASE billing_cycle WHEN 'monthly' THEN now()+interval '1 month' WHEN 'quarterly' THEN now()+interval '3 months' WHEN 'annually' THEN now()+interval '1 year' ELSE NULL END WHERE id=$1`, job.ServiceID, result.UpstreamID, raw)
		return err
	}
	if !allowedServiceAction(current, job.Action) {
		return conflict("invalid_delivery_state", "服务当前状态不允许完成该交付任务")
	}
	if job.Action == "reinstall" {
		if !result.CommandAccepted || !provision.ValidSystemPassword(result.Credentials.Password) {
			return conflict("reinstall_unconfirmed", "请核验重装完成并登记新的系统密码")
		}
		template, err := pendingReinstallTemplate(ctx, tx, job)
		if err != nil {
			return err
		}
		var sealed []byte
		if err = tx.QueryRow(ctx, "SELECT credentials_encrypted FROM services WHERE id=$1", job.ServiceID).Scan(&sealed); err != nil {
			return err
		}
		old, err := provision.Unseal(s.ProvisionEncryptionKey, job.ServiceID, sealed)
		if err != nil {
			return conflict("credentials_unavailable", "服务原凭据不可用")
		}
		old.Password = result.Credentials.Password
		sealed, err = provision.Seal(s.ProvisionEncryptionKey, job.ServiceID, old)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(template)
		_, err = tx.Exec(ctx, `UPDATE services SET installed_os=$2,username=$3,credentials_encrypted=$4,
 resource_info=resource_info||jsonb_build_object('power','processing'),last_error='',version=version+1,updated_at=now() WHERE id=$1`, job.ServiceID, raw, template.Login, sealed)
		return err
	}
	if job.Action == "reset_password" {
		if !result.CommandAccepted || !result.PasswordConfirmed {
			return conflict("password_unconfirmed", "新密码尚未确认")
		}
		pending, err := s.pendingJobCredentials(ctx, tx, job)
		if err != nil || !provision.ValidSystemPassword(pending.Password) {
			return conflict("credentials_unavailable", "密码重置任务凭据不可用")
		}
		var sealed []byte
		if err = tx.QueryRow(ctx, "SELECT credentials_encrypted FROM services WHERE id=$1", job.ServiceID).Scan(&sealed); err != nil {
			return err
		}
		currentCredentials, err := provision.Unseal(s.ProvisionEncryptionKey, job.ServiceID, sealed)
		if err != nil {
			return conflict("credentials_unavailable", "服务原凭据不可用")
		}
		currentCredentials.Password = pending.Password
		sealed, err = provision.Seal(s.ProvisionEncryptionKey, job.ServiceID, currentCredentials)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, "UPDATE services SET credentials_encrypted=$2,last_error='',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID, sealed)
		return err
	}
	if provision.IsPowerAction(job.Action) {
		if !result.CommandAccepted || result.Power != "processing" {
			return conflict("invalid_power_result", "电源命令尚未确认受理")
		}
		_, err := tx.Exec(ctx, `UPDATE services SET resource_info=resource_info||jsonb_build_object('power','processing'),last_error='',version=version+1,updated_at=now() WHERE id=$1`, job.ServiceID)
		return err
	}
	if err := applyLocalServiceAction(ctx, tx, job, current); err != nil {
		return err
	}
	if job.Action == "sync" {
		raw, _ := json.Marshal(result)
		// Power-off differs from a billing suspension. Do not change billing
		// state when a VM is off, processing or limited by supplier traffic rules.
		_, err := tx.Exec(ctx, "UPDATE services SET resource_info=$2,last_error='',version=version+1,updated_at=now() WHERE id=$1", job.ServiceID, raw)
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE services SET last_error='',credentials_encrypted=CASE WHEN $2='terminate' THEN NULL ELSE credentials_encrypted END WHERE id=$1", job.ServiceID, job.Action)
	return err
}

func (s *Store) pendingJobCredentials(ctx context.Context, tx pgx.Tx, job ProvisionJob) (provision.Credentials, error) {
	var sealed []byte
	var key string
	if err := tx.QueryRow(ctx, "SELECT idempotency_key,request_credentials_encrypted FROM provision_jobs WHERE id=$1 AND service_id=$2 AND action IN ('reset_password','rescue_system')", job.ID, job.ServiceID).Scan(&key, &sealed); err != nil {
		return provision.Credentials{}, err
	}
	return provision.UnsealJobCredentials(s.ProvisionEncryptionKey, job.ServiceID, key, sealed)
}

type DeliveryVerification struct {
	JobID      int64  `json:"job_id"`
	Resolution string `json:"resolution"`
	UpstreamID string `json:"upstream_id"`
	Note       string `json:"note"`
	Password   string `json:"password,omitempty"`
	Username   string `json:"username,omitempty"`
	OSName     string `json:"os_name,omitempty"`
}

func requireDeliveryManage(ctx context.Context, tx pgx.Tx, actorID int64) error {
	var allowed bool
	if err := tx.QueryRow(ctx, `SELECT coalesce(u.role='super_admin' OR (u.role='admin' AND r.active AND 'services.view'=ANY(r.permissions) AND 'services.manage'=ANY(r.permissions)),false)
FROM users u LEFT JOIN admin_roles r ON r.id=u.admin_role_id WHERE u.id=$1 AND u.status='active'`, actorID).Scan(&allowed); err != nil {
		return err
	}
	if !allowed {
		return conflict("permission_denied", "没有核验服务交付的权限")
	}
	return nil
}

// An administrator must check the supplier console first. "not_applied" ends
// this job as failed; a fresh explicit action is required to send another call.
func (s *Store) VerifyNOKVMJob(ctx context.Context, actorID, serviceID int64, in DeliveryVerification) (ServiceActionResult, error) {
	out := ServiceActionResult{ServiceID: serviceID, JobID: in.JobID}
	in.Note = strings.TrimSpace(in.Note)
	in.UpstreamID = strings.TrimSpace(in.UpstreamID)
	if in.JobID < 1 || (in.Resolution != "applied" && in.Resolution != "not_applied") || !contentTextValid(in.Note, 2000, true) || len(in.Note) < 3 || (in.UpstreamID != "" && !provision.ValidResourceID(in.UpstreamID)) {
		return out, conflict("invalid_verification", "请选择核验结果并填写有效资源标识和核验依据")
	}
	raw, _ := json.Marshal(in)
	sum := sha256.Sum256(raw)
	fingerprint := hex.EncodeToString(sum[:])
	if in.Password != "" {
		mac := hmac.New(sha256.New, s.ProvisionEncryptionKey)
		mac.Write(raw)
		fingerprint = hex.EncodeToString(mac.Sum(nil))
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = requireDeliveryManage(ctx, tx, actorID); err != nil {
		return out, err
	}
	if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
		return out, err
	}
	var mode, current, upstream string
	if err = tx.QueryRow(ctx, "SELECT provision_mode,status,upstream_id FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&mode, &current, &upstream); err != nil {
		return out, err
	}
	var job ProvisionJob
	var status, previous string
	if err = tx.QueryRow(ctx, "SELECT id,service_id,action,status,coalesce(result->>'verification_fingerprint','') FROM provision_jobs WHERE id=$1 AND service_id=$2 FOR UPDATE", in.JobID, serviceID).Scan(&job.ID, &job.ServiceID, &job.Action, &status, &previous); err != nil {
		return out, err
	}
	out.Action = job.Action
	if (job.Action == "reinstall" || provision.IsStorageRestore(job.Action)) && in.Resolution == "applied" {
		if !provision.ValidSystemPassword(in.Password) {
			return out, conflict("invalid_system_password", "请填写12至128位已核验的新系统密码")
		}
		if strings.Contains(in.Note, in.Password) {
			return out, conflict("sensitive_verification_note", "核验依据不能包含系统密码")
		}
	} else if in.Password != "" {
		return out, conflict("unexpected_verification_password", "该核验结果不接受系统密码")
	}
	if provision.IsStorageRestore(job.Action) && in.Resolution == "applied" {
		if (in.Username != "root" && in.Username != "administrator") || strings.TrimSpace(in.OSName) == "" || !contentTextValid(in.OSName, 120, false) {
			return out, conflict("invalid_restored_system", "请登记实际恢复后的系统名称及登录用户名")
		}
	} else if in.Username != "" || in.OSName != "" {
		return out, conflict("unexpected_restored_system", "该核验结果不接受恢复系统资料")
	}
	if previous != "" && previous == fingerprint {
		out.Status = status
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if mode != "nokvm" || status != "waiting_verification" {
		return out, conflict("invalid_verification_state", "任务当前不允许核验或已使用其他结果完成")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if job.Action == "reset_password" {
		pending, err := s.pendingJobCredentials(ctx, tx, job)
		if err == nil && pending.Password != "" && strings.Contains(in.Note, pending.Password) {
			return out, conflict("sensitive_verification_note", "核验依据不能包含系统密码")
		}
	}
	out.Status = "failed"
	if in.Resolution == "applied" {
		if job.Action == "sync" {
			return out, conflict("sync_requires_response", "同步须取得供应商响应，请结束该任务后重新同步")
		}
		if job.Action == "create" {
			if !provision.ValidResourceID(in.UpstreamID) {
				return out, conflict("resource_required", "确认开通必须登记已核验的供应商资源标识")
			}
			upstream = in.UpstreamID
		} else if in.UpstreamID != "" && in.UpstreamID != upstream {
			return out, conflict("resource_mismatch", "核验资源与服务已关联资源不一致")
		}
		verifiedResult := provision.Result{UpstreamID: upstream}
		if job.Action == "reinstall" {
			verifiedResult.CommandAccepted = true
			verifiedResult.Credentials.Password = in.Password
		}
		if job.Action == "reset_password" {
			verifiedResult.CommandAccepted = true
			verifiedResult.PasswordConfirmed = true
		}
		if provision.IsPowerAction(job.Action) {
			verifiedResult.Power = "processing"
			verifiedResult.CommandAccepted = true
		}
		if provision.IsStorageAction(job.Action) {
			err = s.applyStorageVerification(ctx, tx, job, current, in)
		} else if provision.IsNATAction(job.Action) {
			err = s.applyNATVerification(ctx, tx, job, current)
		} else if provision.IsCDROMAction(job.Action) {
			err = s.applyCDROMVerification(ctx, tx, job, current)
		} else {
			err = s.applyNOKVMResult(ctx, tx, job, current, verifiedResult)
		}
		if err != nil {
			return out, err
		}
		if err = s.queueServiceLifecycleResult(ctx, tx, job.ServiceID, current, job.Action, job.ID); err != nil {
			return out, err
		}
		out.Status = "succeeded"
	} else if job.Action == "create" {
		if _, err = tx.Exec(ctx, "UPDATE services SET status='failed',last_error='管理员核验供应商未开通资源',credentials_encrypted=NULL,version=version+1,updated_at=now() WHERE id=$1", serviceID); err != nil {
			return out, err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status=$2,result=jsonb_build_object('verification_fingerprint',$3::text,'resolution',$4::text),last_error=CASE WHEN $2='failed' THEN '管理员核验该动作未完成，可显式重新提交' ELSE '' END,completed_at=now(),locked_at=NULL,updated_at=now() WHERE id=$1", job.ID, out.Status, fingerprint, in.Resolution); err != nil {
		return out, err
	}
	if (out.Status == "succeeded" && provision.IsPowerAction(job.Action)) || job.Action == "reinstall" || provision.IsStorageRestore(job.Action) {
		if err = enqueuePowerSync(ctx, tx, job); err != nil {
			return out, err
		}
	}
	in.Password = ""
	if err = Audit(ctx, tx, actorID, "service.nokvm.verified", "service", serviceID, in); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) ServiceCredentials(ctx context.Context, actorID, serviceID int64, admin bool) (map[string]string, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	if admin {
		if err = requireDeliveryManage(ctx, tx, actorID); err != nil {
			return nil, err
		}
		if err = LockAdminResourceCustomer(ctx, tx, actorID, "service", serviceID); err != nil {
			return nil, err
		}
	}
	var username string
	var sealed []byte
	if err = tx.QueryRow(ctx, "SELECT username,credentials_encrypted FROM services WHERE id=$1 AND ($2::boolean OR user_id=$3) AND status IN ('active','suspended') AND provision_mode IN ('nokvm','supplier') FOR SHARE", serviceID, admin, actorID).Scan(&username, &sealed); err != nil {
		return nil, err
	}
	var resetting bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
 AND action IN ('reset_password','rescue_system','exit_rescue_system','reinstall','snapshot_restore','backup_restore') AND status IN ('pending','running','waiting_verification'))`, serviceID).Scan(&resetting); err != nil {
		return nil, err
	}
	if resetting {
		return nil, conflict("credentials_reset_pending", "系统密码重置、重装或恢复尚未确认，暂不能读取登录凭据")
	}
	value, err := provision.Unseal(s.ProvisionEncryptionKey, serviceID, sealed)
	if err != nil {
		return nil, conflict("credentials_unavailable", "服务凭据暂不可用，请联系管理员")
	}
	if err = Audit(ctx, tx, actorID, "service.credentials.viewed", "service", serviceID, map[string]any{}); err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return map[string]string{"username": username, "password": value.Password}, nil
}

func requireNoExternalDelivery(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var unresolved bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND (status='waiting_verification' OR (status='running' AND external_started_at IS NOT NULL)))`, serviceID).Scan(&unresolved); err != nil {
		return err
	}
	if unresolved {
		return conflict("external_delivery_unresolved", "服务供应商操作正在执行或待核验，请先核验实际资源")
	}
	return nil
}
