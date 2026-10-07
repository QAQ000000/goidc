package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
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
       AND j.status IN ('pending','running','waiting_verification')
   )
)
INSERT INTO provision_jobs(service_id,action,idempotency_key,available_at)
SELECT id,'sync','supplier-periodic-sync-'||id::text,now()
FROM candidates
ON CONFLICT (idempotency_key) DO UPDATE
 SET status='pending', attempts=0, available_at=now(), locked_at=NULL,
     completed_at=NULL, last_error='', updated_at=now()
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
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
	caps := adapter.Capabilities()
	supported := (claimed.Action == "create" && caps.Create) || (claimed.Action == "sync" && caps.Sync) || (claimed.Action == "suspend" && caps.Suspend) || (claimed.Action == "resume" && caps.Resume)
	if !supported {
		return conflict("unsupported_supplier_action", "该适配器不支持该动作")
	}
	upstreamID, _ := strconv.ParseInt(upstream, 10, 64)
	if claimed.Action != "create" && upstreamID < 1 {
		return conflict("supplier_resource_missing", "缺少供应商资源标识")
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
	} else if claimed.Action == "sync" {
		remote, callErr = adapter.Sync(callCtx, upstreamID)
	} else {
		remote, callErr = adapter.Execute(callCtx, claimed.Action, upstreamID)
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
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
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
			if !failure.Unknown && failure.UpstreamID == 0 && failure.InvoiceID == 0 {
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
		} else {
			if err = applySupplierRemoteSafely(ctx, tx, j, remote, s.ProvisionEncryptionKey); err != nil {
				outcome, message = "waiting_verification", "上游资源无法绑定或同步，请核验资源状态和归属"
			}
		}
	}
	if outcome == "failed" && j.Action == "create" {
		if _, err = tx.Exec(ctx, "UPDATE services SET status='failed',last_error=$2,version=version+1,updated_at=now() WHERE id=$1", j.ServiceID, message); err != nil {
			return err
		}
	}
	// Store only normalized identifiers/costs, never supplier raw payloads or credentials.
	payload, _ := json.Marshal(map[string]any{"upstream_id": result.UpstreamID, "invoice_id": result.InvoiceID, "cost_cents": result.CostCents, "currency": result.Currency, "upstream_status": remote.Status})
	if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status=$2,last_error=$3,result=$4,locked_at=NULL,completed_at=CASE WHEN $2='waiting_verification' THEN NULL ELSE now() END,updated_at=now() WHERE id=$1`, j.ID, outcome, message, payload); err != nil {
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
WHERE (SELECT count(*) FROM provision_jobs WHERE service_id=$1 AND action='sync')<120`, j.ServiceID, j.ID); err != nil {
			return err
		}
	}
	if err = Audit(ctx, tx, 0, "supplier.delivery."+outcome, "service", j.ServiceID, map[string]any{"job_id": j.ID, "supplier_id": b.SupplierID, "action": j.Action}); err != nil {
		return err
	}
	return tx.Commit(ctx)
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
	switch remote.Status {
	case "Active", "active":
		state = "active"
	case "Suspended", "suspended":
		state = "suspended"
	case "Terminated", "terminated", "Cancelled", "cancelled":
		state = "terminated"
	case "Pending", "pending", "provisioning":
	default:
		return conflict("supplier_status_unknown", "上游资源状态不可识别，请核验")
	}
	due, err := supplierDueDate(remote.NextDueDate)
	if err != nil {
		return err
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
	resource, _ := json.Marshal(map[string]any{
		"dedicated_ip": remote.DedicatedIP, "assigned_ips": remote.AssignedIPs, "port": remote.Port,
		"suspend_reason": remote.SuspendReason,
	})
	installedOS := ""
	if remote.OS != "" || remote.OSURL != "" {
		installed, _ := json.Marshal(map[string]string{"name": remote.OS, "url": remote.OSURL})
		installedOS = string(installed)
	}
	_, err = tx.Exec(ctx, `UPDATE services SET status=$2,upstream_id=$3,last_error='',version=version+1,updated_at=now(),
 domain=CASE WHEN $4<>'' THEN $4 ELSE domain END,
 username=CASE WHEN $5<>'' THEN $5 ELSE username END,
 credentials_encrypted=CASE WHEN $6::bytea IS NULL THEN credentials_encrypted ELSE $6 END,
 resource_info=resource_info||$7::jsonb,
 installed_os=CASE WHEN $8<>'' THEN $8::jsonb ELSE installed_os END,
 next_due_at=CASE WHEN billing_cycle='once' THEN NULL ELSE coalesce($9,next_due_at) END WHERE id=$1`, j.ServiceID, state, strconv.FormatInt(remote.UpstreamID, 10), remote.Domain, remote.Username, credentials, resource, string(installedOS), due)
	if err != nil {
		return err
	}
	if state == "terminated" {
		return releaseTerminatedInventory(ctx, tx, j.ServiceID)
	}
	return nil
}
