package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/provision"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// EnqueuePeriodicSupplierSyncs keeps supplier-backed services in step when the
// upstream cannot reach our callback URL. A stable idempotency key means each
// service has at most one periodic row, which is re-armed after five minutes.
func (s *Store) EnqueuePeriodicSupplierSyncs(ctx context.Context) (int64, error) {
	tag, err := s.Pool.Exec(ctx, `
WITH candidates AS (
 SELECT s.id
 FROM services s
 WHERE s.provision_mode='supplier'
   AND s.status IN ('provisioning','active','suspended')
   AND s.upstream_id <> ''
   AND NOT EXISTS (
     SELECT 1 FROM provision_jobs j
     WHERE j.service_id=s.id
       AND j.status IN ('pending','running','waiting_manual','waiting_verification')
   )
)
INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
SELECT id,'sync','supplier-periodic-sync-'||id::text,now()
FROM candidates
ON CONFLICT (idempotency_key) DO UPDATE
 SET status='pending', attempts=0, available_at=now(), locked_at=NULL,
     completed_at=NULL, external_started_at=NULL, last_error='', updated_at=now()
 WHERE provision_jobs.status IN ('succeeded','failed')
   AND provision_jobs.updated_at <= now()-interval '5 minutes'`)
	return tag.RowsAffected(), err
}

func (s *Store) processSupplierJob(ctx context.Context, claimed ProvisionJob) error {
	var raw []byte
	if err := s.Pool.QueryRow(ctx, "SELECT delivery_snapshot FROM services WHERE id=$1 AND provision_mode='supplier'", claimed.ServiceID).Scan(&raw); err != nil {
		return err
	}
	var snap supplierSnapshot
	if json.Unmarshal(raw, &snap) != nil || snap.BindingID < 1 {
		return conflict("supplier_snapshot_invalid", "供应商交付快照无效")
	}
	b, err := s.supplierBinding(ctx, snap.BindingID, false)
	if err != nil {
		return err
	}
	if b.SupplierID != snap.SupplierID || b.Provider != snap.Provider {
		return conflict("supplier_snapshot_invalid", "供应商交付绑定不匹配")
	}
	adapter, version, err := s.supplierAdapter(ctx, b.SupplierID)
	// Even a disabled/misconfigured account must durably fail its claimed job.
	if err != nil {
		return s.failUndispatchedSupplierJob(ctx, claimed, err)
	}
	expiryRemote, err := s.readSupplierExpiryBeforeDispatch(ctx, claimed, b, adapter)
	if err != nil {
		return s.failUndispatchedSupplierJob(ctx, claimed, err)
	}
	var reinstallItems []supplier.Template
	if claimed.Action == "reinstall" {
		var upstream string
		if err = s.Pool.QueryRow(ctx, "SELECT upstream_id FROM services WHERE id=$1", claimed.ServiceID).Scan(&upstream); err != nil {
			return err
		}
		id, _ := strconv.ParseInt(upstream, 10, 64)
		console, ok := adapter.(supplier.ConsoleAdapter)
		if !ok {
			return s.failUndispatchedSupplierJob(ctx, claimed, nil)
		}
		reinstallItems, err = console.Templates(ctx, id)
		if err != nil {
			return s.failUndispatchedSupplierJob(ctx, claimed, err)
		}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	overdueDue, err := lockOverdueSuspensionInvoice(ctx, tx, claimed)
	if err != nil {
		return err
	}
	var current, upstream, cycle string
	if err = tx.QueryRow(ctx, "SELECT status,upstream_id,billing_cycle FROM services WHERE id=$1 FOR UPDATE", claimed.ServiceID).Scan(&current, &upstream, &cycle); err != nil {
		return err
	}
	if err = requireServiceReviewed(ctx, tx, claimed.ServiceID); err != nil {
		return err
	}
	var state string
	var attempts int
	var valid, dispatched bool
	if err = tx.QueryRow(ctx, `SELECT status,attempts,coalesce(locked_at>clock_timestamp()-interval '5 minutes',false),external_started_at IS NOT NULL FROM provision_jobs WHERE id=$1 AND service_id=$2 AND action=$3 FOR UPDATE`, claimed.ID, claimed.ServiceID, claimed.Action).Scan(&state, &attempts, &valid, &dispatched); err != nil {
		return err
	}
	if state != "running" || attempts != claimed.Attempts || !valid || dispatched {
		return conflict("job_lease_lost", "任务租约失效，禁止重复发送")
	}
	// The first check established invoice -> service lock order. Recheck the
	// provenance after waiting for service/job locks: a concurrent sync or
	// administrator action may have superseded this automatic recovery.
	overdueDue, err = lockOverdueSuspensionInvoice(ctx, tx, claimed)
	if err != nil {
		return err
	}
	if !overdueDue {
		if err = cancelObsoleteOverdueSuspension(ctx, tx, claimed); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	proceed, err := reviewCancellationJob(ctx, tx, claimed)
	if err != nil {
		return err
	}
	if !proceed {
		return tx.Commit(ctx)
	}
	var accountVersion int64
	var active bool
	var running *int64
	if err = tx.QueryRow(ctx, "SELECT version,active,running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE", b.SupplierID).Scan(&accountVersion, &active, &running); err != nil {
		return err
	}
	if running != nil {
		// Shared upstream carts must never overlap, including an unresolved earlier purchase.
		_, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='pending',attempts=greatest(attempts-1,0),available_at=now()+interval '30 seconds',locked_at=NULL,last_error='供应商账户正在处理其他任务或等待核验',updated_at=now() WHERE id=$1`, claimed.ID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if !active || accountVersion != version || cycle != b.BillingCycle || (!allowedServiceAction(current, claimed.Action) && !(claimed.Action == "sync" && current == "provisioning")) || (claimed.Action == "create" && upstream != "") {
		_, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='failed',last_error='供应商配置或服务状态已变更',locked_at=NULL,completed_at=now(),updated_at=now() WHERE id=$1", claimed.ID)
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if expiryRemote != nil {
		deferred, deferErr := s.deferSupplierExpiryFromRemote(ctx, tx, claimed, *expiryRemote)
		if deferErr != nil {
			return deferErr
		}
		if deferred {
			return tx.Commit(ctx)
		}
	}
	if !adapter.Capabilities().SupportsAction(claimed.Action) {
		if supplierLifecycleAction(claimed.Action) {
			// Old pending jobs may outlive the provider implementation that
			// originally supported them. Preserve a visible manual responsibility
			// without a dispatch marker, account lease or invented local state.
			if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='waiting_manual',
request_payload=request_payload||'{"manual_lifecycle":true}'::jsonb,
last_error='供应商不支持自动执行此动作，请在上游人工处理后核验实际状态',
locked_at=NULL,completed_at=NULL,updated_at=clock_timestamp() WHERE id=$1`, claimed.ID); err != nil {
				return err
			}
			if err = Audit(ctx, tx, 0, "supplier.lifecycle.manual_requested", "service", claimed.ServiceID,
				map[string]any{"job_id": claimed.ID, "supplier_id": b.SupplierID, "action": claimed.Action, "reason": "provider_capability_unsupported"}); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		return conflict("unsupported_supplier_action", "该适配器不支持该动作")
	}
	upstreamID, _ := strconv.ParseInt(upstream, 10, 64)
	if claimed.Action != "create" && upstreamID < 1 {
		return conflict("supplier_resource_missing", "缺少供应商资源标识")
	}
	var renewal supplierRenewalSnapshot
	if claimed.Action == "renew" {
		if err = tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1", claimed.ID).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &renewal) != nil || renewal.SupplierID != b.SupplierID || renewal.AccountVersion != accountVersion || renewal.ProductID != b.UpstreamProductID || renewal.UpstreamID != upstreamID || renewal.Cycle != cycle || renewal.InvoiceID < 1 {
			return s.failUndispatchedRenewal(ctx, tx, claimed, "供应商续费快照或账户已变更")
		}
		var paid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoices i JOIN invoice_items ii ON ii.invoice_id=i.id JOIN services s ON s.id=ii.service_id WHERE i.id=$1 AND i.status='paid' AND ii.item_type='renew' AND s.id=$2 AND s.user_id=i.user_id)`, renewal.InvoiceID, claimed.ServiceID).Scan(&paid); err != nil {
			return err
		}
		if !paid {
			return s.failUndispatchedRenewal(ctx, tx, claimed, "本地续费账单未支付或已退款")
		}
		if err = snapshotSupplierRenewalExpiryRecovery(ctx, tx, claimed, b); err != nil {
			return err
		}
	}
	actionRequest := supplier.ActionRequest{Action: claimed.Action, UpstreamID: upstreamID}
	if claimed.Action == "reinstall" {
		template, templateErr := pendingReinstallTemplate(ctx, tx, claimed)
		if templateErr != nil {
			return templateErr
		}
		found := false
		for _, item := range reinstallItems {
			if item.ID == template.ID && item.Name == template.Name && item.Group == template.Group {
				found = true
				break
			}
		}
		if !found {
			if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET status='failed',last_error='供应商镜像目录已变更',locked_at=NULL,completed_at=now(),updated_at=now() WHERE id=$1", claimed.ID); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		actionRequest.TemplateID = template.ID
	}
	if claimed.Action == "reset_password" || claimed.Action == "rescue_system" {
		credentials, credentialsErr := s.pendingJobCredentials(ctx, tx, claimed)
		if credentialsErr != nil || !provision.ValidSystemPassword(credentials.Password) {
			return conflict("credentials_unavailable", "供应商动作凭据不可用")
		}
		actionRequest.Password = credentials.Password
		if claimed.Action == "rescue_system" {
			var payload struct {
				System int `json:"system"`
			}
			if err = tx.QueryRow(ctx, "SELECT request_payload FROM provision_jobs WHERE id=$1", claimed.ID).Scan(&raw); err != nil || json.Unmarshal(raw, &payload) != nil || (payload.System != 1 && payload.System != 2) {
				return conflict("invalid_rescue_system", "救援系统参数无效")
			}
			actionRequest.System = payload.System
		}
	}
	var fields map[int64]string
	if err = supplierUnseal(s.ProvisionEncryptionKey, "binding-fields", b.ProductID, b.sealed, &fields); err != nil {
		return err
	}
	req := supplier.CreateRequest{ProductID: b.UpstreamProductID, Cycle: b.BillingCycle, Revision: b.UpstreamRevision, ConfigOptions: b.ConfigOptions, CustomFields: fields, LocalServiceID: claimed.ServiceID, DownstreamURL: s.SupplierDownstreamURL, DownstreamToken: supplierDownstreamToken(s.ProvisionEncryptionKey, claimed.ServiceID), MaximumCostCents: b.MaximumCostCents, Currency: b.Currency, Host: "svc-" + strconv.FormatInt(claimed.ServiceID, 10)}
	if _, err = tx.Exec(ctx, "UPDATE supplier_accounts SET running_job_id=$2,updated_at=now() WHERE id=$1", b.SupplierID, claimed.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE provision_jobs SET external_started_at=clock_timestamp(),updated_at=now() WHERE id=$1", claimed.ID); err != nil {
		return err
	}
	if claimed.Action == "create" {
		if _, err = tx.Exec(ctx, "UPDATE services SET status='provisioning',last_error='',version=version+1,updated_at=now() WHERE id=$1", claimed.ServiceID); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	var result supplier.CreateResult
	var remote supplier.Service
	var callErr error
	if claimed.Action == "create" {
		result, callErr = adapter.Create(callCtx, req)
	} else if claimed.Action == "renew" {
		renew, ok := adapter.(supplier.RenewalAdapter)
		if !ok {
			callErr = &supplier.Error{Code: "unsupported_renewal"}
		} else {
			result, remote, callErr = renew.Renew(callCtx, renewal.RenewalRequest, func(checkpoint supplier.CreateResult) error {
				payload, _ := json.Marshal(map[string]any{"upstream_id": checkpoint.UpstreamID, "invoice_id": checkpoint.InvoiceID, "cost_cents": checkpoint.CostCents, "currency": checkpoint.Currency, "product_id": checkpoint.ProductID})
				tag, e := s.Pool.Exec(callCtx, `UPDATE provision_jobs SET result=result||$2::jsonb,updated_at=now() WHERE id=$1 AND status='running' AND attempts=$3 AND locked_at>clock_timestamp()-interval '5 minutes'`, claimed.ID, payload, claimed.Attempts)
				if e == nil && tag.RowsAffected() != 1 {
					return conflict("job_lease_lost", "续费任务租约失效")
				}
				return e
			})
		}
	} else if claimed.Action == "sync" {
		remote, callErr = adapter.Sync(callCtx, upstreamID)
	} else {
		remote, callErr = adapter.Execute(callCtx, actionRequest)
	}
	cancel()
	finishCtx, finishCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer finishCancel()
	return s.finishSupplierJob(finishCtx, claimed, b, result, remote, callErr)
}
func (s *Store) failUndispatchedSupplierJob(ctx context.Context, j ProvisionJob, _ error) error {
	_, err := s.Pool.Exec(ctx, `UPDATE provision_jobs SET status='failed',last_error='供应商已停用或配置不可用',locked_at=NULL,completed_at=now(),updated_at=now() WHERE id=$1 AND status='running' AND attempts=$2 AND external_started_at IS NULL`, j.ID, j.Attempts)
	return err
}
func (s *Store) finishSupplierJob(ctx context.Context, j ProvisionJob, b SupplierBinding, result supplier.CreateResult, remote supplier.Service, callErr error) error {
	return s.WithTx(ctx, func(tx pgx.Tx) error {
		var err error
		var current, existing string
		if err = tx.QueryRow(ctx, "SELECT status,upstream_id FROM services WHERE id=$1 FOR UPDATE", j.ServiceID).Scan(&current, &existing); err != nil {
			return err
		}
		var state string
		var attempts int
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT status,attempts,coalesce(locked_at>clock_timestamp()-interval '5 minutes',false) FROM provision_jobs WHERE id=$1 FOR UPDATE`, j.ID).Scan(&state, &attempts, &valid); err != nil {
			return err
		}
		if state != "running" || attempts != j.Attempts || !valid {
			return conflict("job_lease_lost", "供应商响应到达时任务租约失效，请核验")
		}
		var running *int64
		if err = tx.QueryRow(ctx, "SELECT running_job_id FROM supplier_accounts WHERE id=$1 FOR UPDATE", b.SupplierID).Scan(&running); err != nil {
			return err
		}
		if running == nil || *running != j.ID {
			return conflict("supplier_lease_lost", "供应商账户租约失效")
		}
		outcome, message := "succeeded", ""
		if callErr != nil {
			outcome, message = "waiting_verification", "供应商请求结果不明，请核验上游订单及资源，禁止直接重试"
			var failure *supplier.Error
			if errors.As(callErr, &failure) {
				if failure.UpstreamID > 0 {
					result.UpstreamID = failure.UpstreamID
				}
				if failure.InvoiceID > 0 {
					result.InvoiceID = failure.InvoiceID
				}
				if !failure.Unknown && (j.Action == "renew" || (failure.UpstreamID == 0 && failure.InvoiceID == 0)) {
					outcome, message = "failed", "供应商请求失败："+failure.Code
				}
			}
		} else if j.Action == "create" {
			if result.UpstreamID < 1 || result.Currency != b.Currency || result.CostCents < 0 || result.CostCents > b.MaximumCostCents {
				outcome, message = "waiting_verification", "供应商交付响应不完整，请核验资源及账单"
			} else {
				remote = supplier.Service{UpstreamID: result.UpstreamID, ProductID: result.ProductID, Status: result.Status, Domain: result.Domain, NextDueDate: result.NextDueDate}
				if remote.ProductID != b.UpstreamProductID {
					outcome, message = "waiting_verification", "上游资源商品归属不匹配，请核验"
				} else {
					err = applySupplierRemoteSafely(ctx, tx, j, remote, s.ProvisionEncryptionKey)
					if err != nil {
						outcome, message = "waiting_verification", "上游资源无法绑定或同步，请核验资源状态和归属"
					}
				}
			}
		} else {
			if remote.ProductID != b.UpstreamProductID || strconv.FormatInt(remote.UpstreamID, 10) != existing {
				outcome, message = "waiting_verification", "上游资源商品归属不匹配，请核验"
			} else if supplierLifecycleAction(j.Action) && (!supplier.LifecycleTargetConfirmed(j.Action, remote.Status) ||
				(!allowedServiceAction(current, j.Action) && !supplier.LifecycleTargetConfirmed(j.Action, current))) {
				outcome, message = "waiting_verification", "上游尚未确认动作目标状态，请核验实际资源，禁止直接重试"
			} else {
				if err = applySupplierRemoteSafely(ctx, tx, j, remote, s.ProvisionEncryptionKey); err != nil {
					outcome, message = "waiting_verification", "上游资源无法绑定或同步，请核验资源状态和归属"
				}
			}
		}
		// Sync cannot create or mutate an upstream resource, so read and apply
		// failures release the account without requiring delivery reconciliation.
		if j.Action == "sync" && outcome != "succeeded" {
			outcome, message = "failed", "供应商同步失败，请稍后重试"
		}
		if outcome == "succeeded" && (j.Action == "create" || (j.Action == "sync" && current == "provisioning")) {
			if err = s.QueueServiceProvisionSuccessNotifications(ctx, tx, j.ServiceID); err != nil {
				return err
			}
		}
		if outcome == "succeeded" && current == "active" && (j.Action == "suspend" || j.Action == "sync") {
			if err = s.QueueServiceSuspendedNotifications(ctx, tx, j.ServiceID); err != nil {
				return err
			}
		}
		if outcome == "succeeded" {
			if err = s.queueServiceLifecycleResult(ctx, tx, j.ServiceID, current, j.Action, j.ID); err != nil {
				return err
			}
		}
		if outcome == "failed" && j.Action == "create" {
			if _, err = tx.Exec(ctx, "UPDATE services SET status='failed',last_error=$2,version=version+1,updated_at=now() WHERE id=$1", j.ServiceID, message); err != nil {
				return err
			}
		}
		// Store only normalized identifiers/costs, never supplier raw payloads or credentials.
		payload, _ := json.Marshal(map[string]any{"upstream_id": result.UpstreamID, "invoice_id": result.InvoiceID, "cost_cents": result.CostCents, "currency": result.Currency, "upstream_status": remote.Status, "command_accepted": outcome == "succeeded" && (provision.IsPowerAction(j.Action) || j.Action == "rescue_system" || j.Action == "exit_rescue_system")})
		if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status=$2,last_error=$3,result=result||$4::jsonb,locked_at=NULL,completed_at=CASE WHEN $2='waiting_verification' THEN NULL ELSE now() END,updated_at=now() WHERE id=$1`, j.ID, outcome, message, payload); err != nil {
			return err
		}
		if outcome != "waiting_verification" {
			if _, err = tx.Exec(ctx, "UPDATE supplier_accounts SET running_job_id=NULL,updated_at=now() WHERE id=$1 AND running_job_id=$2", b.SupplierID, j.ID); err != nil {
				return err
			}
		}
		if outcome == "succeeded" && (remote.Status == "provisioning" || remote.Status == "Pending" || remote.Status == "pending") {
			if _, err = tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
SELECT $1,'sync','supplier-sync-'||$2::bigint::text,now()+interval '30 seconds'
WHERE (SELECT status FROM services WHERE id=$1)='provisioning'
AND (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='sync')<120`, j.ServiceID, j.ID); err != nil {
				return err
			}
		}
		if err = Audit(ctx, tx, 0, "supplier.delivery."+outcome, "service", j.ServiceID, map[string]any{"job_id": j.ID, "supplier_id": b.SupplierID, "action": j.Action}); err != nil {
			return err
		}
		return nil
	})
}

// Preserve the account lease and normalized response even when a resource
// collides with another service or its status cannot be applied locally.
func applySupplierRemoteSafely(ctx context.Context, tx pgx.Tx, j ProvisionJob, remote supplier.Service, key []byte) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT supplier_remote"); err != nil {
		return err
	}
	if err := applySupplierRemote(ctx, tx, j, remote, key); err != nil {
		if _, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT supplier_remote"); rollbackErr != nil {
			return rollbackErr
		}
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT supplier_remote")
	return err
}
func applySupplierRemote(ctx context.Context, tx pgx.Tx, j ProvisionJob, remote supplier.Service, key []byte) error {
	if remote.UpstreamID < 1 {
		return conflict("supplier_resource_missing", "供应商未返回有效资源")
	}
	state := "provisioning"
	switch strings.ToLower(strings.TrimSpace(remote.Status)) {
	case "active":
		state = "active"
	case "suspended":
		state = "suspended"
	case "terminated", "cancelled", "deleted":
		state = "terminated"
	case "pending", "provisioning":
	default:
		return conflict("supplier_status_unknown", "上游资源状态不可识别，请核验")
	}
	// Match the maintained PHP project's mergeUpstreamLifecycleState, using
	// queued/provisioning for Pending and cancelled/terminated for its terminal
	// states. Lock here so callbacks, in-flight jobs and manual verification all
	// merge against the latest committed state, rather than an earlier snapshot.
	var current, existing, previousReason string
	if err := tx.QueryRow(ctx, `SELECT status,upstream_id,coalesce(resource_info->>'suspend_reason','')
FROM services WHERE id=$1 FOR UPDATE`, j.ServiceID).Scan(&current, &existing, &previousReason); err != nil {
		return err
	}
	if existing != "" && existing != strconv.FormatInt(remote.UpstreamID, 10) {
		return conflict("supplier_resource_mismatch", "供应商资源与本地服务不匹配")
	}
	due, err := supplierDueDate(remote.NextDueDate)
	if err != nil {
		return err
	}
	var renewalRecoveryID int64
	if current == "suspended" && (state == "active" || state == "suspended") {
		renewalRecoveryID, err = supplierRenewalExpiryRecovery(ctx, tx, j, remote, due, previousReason)
		if err != nil {
			return err
		}
	}
	applyLifecycle := true
	switch current {
	case "queued", "provisioning":
	case "active", "suspended":
		applyLifecycle = state != "provisioning" && !(current == "suspended" && state == "active" && j.Action != "resume" && renewalRecoveryID == 0)
	case "failed":
		// An explicitly requested termination may close a failed delivery whose
		// resource was identified later. Other observations never revive it.
		applyLifecycle = j.Action == "terminate" && state == "terminated"
	default:
		// In particular, cancelled and terminated must never be revived.
		applyLifecycle = false
	}
	if !applyLifecycle {
		state = current
	}
	var credentials []byte
	if remote.Password != "" {
		previous, readErr := readSupplierCredentials(ctx, tx, j.ServiceID, key)
		if readErr != nil {
			return readErr
		}
		previous.Password = remote.Password
		credentials, err = provision.Seal(key, j.ServiceID, previous)
		if err != nil {
			return err
		}
	}
	resourceFields := map[string]any{
		"dedicated_ip": remote.DedicatedIP, "assigned_ips": remote.AssignedIPs, "port": remote.Port,
	}
	clearBillingOrigin := j.Action == "suspend" || j.Action == "resume" || j.Action == "terminate"
	clearRenewalRecovery := clearBillingOrigin
	if applyLifecycle {
		reason := remote.SuspendReason
		if state == "active" {
			reason = ""
			clearBillingOrigin = true
		} else if state == "suspended" && remote.SuspendReasonType != "" && !strings.HasPrefix(reason, remote.SuspendReasonType+"-") {
			reason = remote.SuspendReasonType + "-" + reason
		}
		// An omitted reason preserves the local reason; Active clears it.
		if reason != "" || state == "active" {
			resourceFields["suspend_reason"] = reason
			if state == "active" || reason != previousReason {
				clearRenewalRecovery = true
			}
			if j.Action != "suspend" && current == "suspended" && state == "suspended" && reason != previousReason {
				// A new observed upstream reason supersedes the earlier billing
				// suspension, even when the coarse local state stayed suspended.
				clearBillingOrigin = true
			}
		}
	}
	if j.Action == "renew" && renewalRecoveryID > 0 && state == "suspended" && applyLifecycle && !clearRenewalRecovery {
		// Some upstreams extend the term before their separate unsuspend worker
		// runs. A later read may consume only this confirmed renewal's authority.
		resourceFields["supplier_renewal_recovery_job_id"] = renewalRecoveryID
	}
	if j.Action == "suspend" && j.ID > 0 && (current == "active" || current == "suspended") && state == "suspended" && applyLifecycle {
		var payload []byte
		if err = tx.QueryRow(ctx, `SELECT request_payload FROM provision_jobs WHERE id=$1 AND service_id=$2`, j.ID, j.ServiceID).Scan(&payload); err != nil {
			return err
		}
		var billing struct {
			SystemOverdueSuspension bool  `json:"system_overdue_suspension"`
			InvoiceID               int64 `json:"billing_invoice_id"`
		}
		if current == "active" && json.Unmarshal(payload, &billing) == nil && billing.SystemOverdueSuspension && billing.InvoiceID > 0 {
			resourceFields["system_billing_suspension_job_id"] = j.ID
		}
		var expiry supplierExpirySuspension
		reason := remote.SuspendReason
		if remote.SuspendReasonType != "" && !strings.HasPrefix(reason, remote.SuspendReasonType+"-") {
			reason = remote.SuspendReasonType + "-" + reason
		}
		// A callback can precede confirmation of the same expiry action. Only
		// a due suspension can establish that source from an already suspended
		// service; never reclassify an independent policy/manual pause.
		canAttributeExpiry := (current == "active" && (reason == "" || supplierDueSuspension(reason))) ||
			(current == "suspended" && supplierDueSuspension(previousReason) && supplierDueSuspension(reason))
		if canAttributeExpiry && json.Unmarshal(payload, &expiry) == nil && expiry.System && expiry.Due > 0 {
			resourceFields["system_expiry_suspension_job_id"] = j.ID
		}
	}
	resource, _ := json.Marshal(resourceFields)
	installedOS := ""
	if remote.OS != "" || remote.OSURL != "" {
		installed, _ := json.Marshal(map[string]string{"name": remote.OS, "url": remote.OSURL})
		installedOS = string(installed)
	}
	// A renew reaches this shared path only after Renew/ConfirmRenewal has
	// confirmed the purchased period. Polling and callbacks are sync actions
	// and cannot clear an unresolved automatic renewal error.
	_, err = tx.Exec(ctx, `UPDATE services SET status=$2,upstream_id=$3,last_error='',version=version+1,updated_at=now(),
 auto_renew_error=CASE WHEN $11::boolean THEN '' ELSE auto_renew_error END,
 domain=CASE WHEN $4<>'' THEN $4 ELSE domain END,
 username=CASE WHEN $5<>'' THEN $5 ELSE username END,
 credentials_encrypted=CASE WHEN $6::bytea IS NULL THEN credentials_encrypted ELSE $6 END,
 resource_info=(CASE WHEN $12::boolean THEN
  (CASE WHEN $10::boolean THEN resource_info-ARRAY['system_billing_suspension_job_id','system_expiry_suspension_job_id'] ELSE resource_info END)-'supplier_renewal_recovery_job_id'
  ELSE (CASE WHEN $10::boolean THEN resource_info-ARRAY['system_billing_suspension_job_id','system_expiry_suspension_job_id'] ELSE resource_info END) END)||$7::jsonb,
 installed_os=CASE WHEN $8<>'' THEN $8::jsonb ELSE installed_os END,
 next_due_at=CASE WHEN billing_cycle='once' THEN NULL ELSE greatest($9,next_due_at) END WHERE id=$1`, j.ServiceID, state, strconv.FormatInt(remote.UpstreamID, 10), remote.Domain, remote.Username, credentials, resource, string(installedOS), due, clearBillingOrigin, j.Action == "renew", clearRenewalRecovery)
	if err != nil {
		return err
	}
	if state == "terminated" {
		return releaseTerminatedInventory(ctx, tx, j.ServiceID)
	}
	return nil
}
