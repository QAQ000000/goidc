package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type supplierExpirySuspension struct {
	System bool  `json:"system_expiry_suspension"`
	Due    int64 `json:"expiry_due_unix_micro"`
}

func supplierExpirySuspensionKey(serviceID, due int64) string {
	return fmt.Sprintf("expiry-suspend-service-%d-due-%d", serviceID, due)
}

// An expired prepaid service need not have an unpaid renewal invoice. Queue
// its lifecycle work without creating a charge, collecting funds or changing
// the service's observed status. Keep the existing 24-hour suspension grace.
func (s *Store) enqueueExpiredSupplierSuspensions(ctx context.Context, limit int) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.id,s.next_due_at FROM services s
 WHERE s.provision_mode='supplier' AND s.status='active' AND NOT s.import_review_required
 AND s.billing_cycle<>'once' AND s.next_due_at<=clock_timestamp()-interval '24 hours'
 AND NOT EXISTS(SELECT 1 FROM service_cancellation_requests c WHERE c.service_id=s.id AND c.status='approved')
 AND NOT EXISTS(SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id
  AND j.status IN ('pending','running','waiting_manual','waiting_verification'))
 AND NOT EXISTS(SELECT 1 FROM provision_jobs j WHERE j.idempotency_key=
  'expiry-suspend-service-'||s.id::text||'-due-'||(extract(epoch FROM s.next_due_at)*1000000)::bigint::text)
 ORDER BY s.id LIMIT $1 FOR UPDATE OF s SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	var candidates []struct {
		id  int64
		due time.Time
	}
	for rows.Next() {
		var c struct {
			id  int64
			due time.Time
		}
		if err = rows.Scan(&c.id, &c.due); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, c := range candidates {
		// The selection's snapshot can predate a concurrent action's commit.
		// The service lock is held now; take a fresh statement snapshot before
		// assigning another operator/worker any responsibility for this resource.
		var blocked bool
		if err = tx.QueryRow(ctx, `SELECT
 EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
  AND status IN ('pending','running','waiting_manual','waiting_verification'))
 OR EXISTS(SELECT 1 FROM service_cancellation_requests WHERE service_id=$1 AND status='approved')`, c.id).Scan(&blocked); err != nil {
			return 0, err
		}
		if blocked {
			continue
		}
		caps, capErr := s.supplierServiceActionCapabilities(ctx, tx, c.id)
		if capErr != nil {
			var businessErr *Conflict
			if !errors.As(capErr, &businessErr) && !errors.Is(capErr, pgx.ErrNoRows) {
				return 0, capErr
			}
		}
		payload := map[string]any{"system_expiry_suspension": true, "expiry_due_unix_micro": c.due.UnixMicro()}
		state, message := "pending", ""
		manual := capErr != nil || !caps.Suspend || !caps.Sync
		if manual {
			state, message = "waiting_manual", supplierLifecycleManualMessage
			payload["manual_lifecycle"] = true
		}
		raw, _ := json.Marshal(payload)
		command, e := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,last_error,request_payload)
 VALUES($1,'suspend',$2,$3,$4,$5::jsonb) ON CONFLICT(idempotency_key) DO NOTHING`, c.id, supplierExpirySuspensionKey(c.id, c.due.UnixMicro()), state, message, raw)
		if e != nil {
			return 0, e
		}
		if command.RowsAffected() == 0 {
			continue
		}
		action := "service.expiry_suspension.queued"
		if manual {
			action = "supplier.lifecycle.manual_requested"
		}
		if err = Audit(ctx, tx, 0, action, "service", c.id, map[string]any{"action": "suspend", "source": "service_expiry", "expiry_due_unix_micro": c.due.UnixMicro()}); err != nil {
			return 0, err
		}
		queued++
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}

// Called again after the worker owns the service/job rows. A renewed term or
// approved cancellation invalidates only an unsent automatic suspension;
// manual reconciliation still records what an operator actually did upstream.
func supplierExpirySuspensionEligible(ctx context.Context, tx pgx.Tx, j ProvisionJob, key string, raw []byte) (bool, error) {
	var source supplierExpirySuspension
	if json.Unmarshal(raw, &source) != nil || !source.System || source.Due < 1 || key != supplierExpirySuspensionKey(j.ServiceID, source.Due) {
		return false, fmt.Errorf("invalid expiry suspension identity")
	}
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s
 WHERE s.id=$1 AND s.provision_mode='supplier' AND s.status='active' AND NOT s.import_review_required
 AND s.billing_cycle<>'once' AND (extract(epoch FROM s.next_due_at)*1000000)::bigint=$2
 AND s.next_due_at<=clock_timestamp()-interval '24 hours'
 AND NOT EXISTS(SELECT 1 FROM service_cancellation_requests c WHERE c.service_id=s.id AND c.status='approved'))`, j.ServiceID, source.Due).Scan(&eligible)
	return eligible, err
}

// Both snapshot and completion must see this exact successful system action.
// A matching reason, key or old job alone does not authorize a renewal recovery.
const supplierExpiryRecoverySourceSQL = `(source.action='suspend'
 AND source.request_payload->'system_expiry_suspension'='true'::jsonb
 AND s.resource_info->>'system_expiry_suspension_job_id'=source.id::text
 AND source.idempotency_key='expiry-suspend-service-'||s.id::text||'-due-'||
  (source.request_payload->>'expiry_due_unix_micro'))`

// The local expiry date can be stale. Read the original remote resource before
// authorizing a destructive lifecycle call, outside all database locks. The
// normal dispatch phase then rechecks the account version and local source.
func (s *Store) readSupplierExpiryBeforeDispatch(ctx context.Context, j ProvisionJob, b SupplierBinding, adapter supplier.Adapter) (*supplier.Service, error) {
	if j.Action != "suspend" {
		return nil, nil
	}
	var key, upstream string
	var raw []byte
	var leased bool
	if err := s.Pool.QueryRow(ctx, `SELECT j.idempotency_key,j.request_payload,s.upstream_id,
 j.status='running' AND j.attempts=$3 AND j.external_started_at IS NULL
 AND coalesce(j.locked_at>clock_timestamp()-interval '5 minutes',false)
 FROM provision_jobs j JOIN services s ON s.id=j.service_id
 WHERE j.id=$1 AND j.service_id=$2`, j.ID, j.ServiceID, j.Attempts).Scan(&key, &raw, &upstream, &leased); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(key, "expiry-suspend-service-") || !adapter.Capabilities().Suspend || !leased {
		// Ordinary dispatch owns the final lease check; an obsolete claim
		// must neither read upstream nor be reported as a completed preflight.
		return nil, nil
	}
	var source supplierExpirySuspension
	if json.Unmarshal(raw, &source) != nil || !source.System || source.Due < 1 || key != supplierExpirySuspensionKey(j.ServiceID, source.Due) {
		return nil, conflict("supplier_expiry_source_invalid", "到期暂停来源无效，请人工核验")
	}
	if !adapter.Capabilities().Sync {
		return nil, conflict("supplier_expiry_read_required", "到期暂停前必须核实上游实际期限，请人工处理")
	}
	id, err := strconv.ParseInt(upstream, 10, 64)
	if err != nil || id < 1 {
		return nil, conflict("supplier_resource_missing", "缺少供应商资源标识")
	}
	readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	remote, err := adapter.Sync(readCtx, id)
	if err != nil {
		return nil, err
	}
	if remote.UpstreamID != id || remote.ProductID != b.UpstreamProductID {
		return nil, conflict("supplier_resource_mismatch", "上游资源归属不匹配，请人工核验")
	}
	due, err := supplierDueDate(remote.NextDueDate)
	if err != nil || due == nil {
		return nil, conflict("supplier_due_invalid", "上游到期时间不可识别，请人工核验")
	}
	return &remote, nil
}

// A pre-existing suspension is an observed external fact, not evidence that
// this unsent system action caused it. Refresh through the ordinary sync merge
// without assigning the expiry job any suspension authority.
func (s *Store) deferSupplierExpiryFromRemote(ctx context.Context, tx pgx.Tx, j ProvisionJob, remote supplier.Service) (bool, error) {
	due, err := supplierDueDate(remote.NextDueDate)
	if err != nil || due == nil {
		return false, conflict("supplier_due_invalid", "上游到期时间不可识别，请人工核验")
	}
	var expired bool
	var raw []byte
	var upstream, previous string
	if err = tx.QueryRow(ctx, `SELECT $1::timestamptz<=clock_timestamp()-interval '24 hours',j.request_payload,s.upstream_id,s.status
 FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$2 AND j.service_id=$3`, due, j.ID, j.ServiceID).Scan(&expired, &raw, &upstream, &previous); err != nil {
		return false, err
	}
	var source supplierExpirySuspension
	if json.Unmarshal(raw, &source) != nil || !source.System || source.Due < 1 || upstream != strconv.FormatInt(remote.UpstreamID, 10) {
		return false, conflict("supplier_expiry_source_invalid", "到期暂停来源或资源已变化，请人工核验")
	}
	// The remote protocol exposes whole seconds; the immutable local source
	// retains PostgreSQL's microseconds for later renewal provenance checks.
	originalDue := time.UnixMicro(source.Due)
	active := strings.EqualFold(strings.TrimSpace(remote.Status), "active")
	managed, err := supplierTermManaged(ctx, tx, j.ServiceID)
	if err != nil {
		return false, err
	}
	if managed && active {
		// The source was rechecked against the customer's managed deadline.
		// A longer prepaid upstream term does not extend that local contract.
		// This observation grants no authority to resume an existing pause.
		if _, err = observeSupplierTerm(ctx, tx, j.ServiceID, due); err != nil {
			return false, err
		}
		return false, nil
	}
	if expired && active && due.Unix() == originalDue.Unix() {
		return false, nil
	}
	read := ProvisionJob{ServiceID: j.ServiceID, Action: "sync"}
	if err = applySupplierRemote(ctx, tx, read, remote, s.ProvisionEncryptionKey); err != nil {
		return false, err
	}
	if previous == "active" {
		if err = s.QueueServiceSuspendedNotifications(ctx, tx, j.ServiceID); err != nil {
			return false, err
		}
	}
	if err = s.queueServiceLifecycleResult(ctx, tx, j.ServiceID, previous, "sync", 0); err != nil {
		return false, err
	}
	if active && due.Unix() < originalDue.Unix() {
		// Ordinary sync never shortens a locally promised term. Keep a visible
		// manual responsibility when the upstream instead reports an earlier
		// deadline; cancelling would strand the same immutable local source.
		if _, err = tx.Exec(ctx, `UPDATE provision_jobs SET status='waiting_manual',
 request_payload=request_payload||'{"manual_lifecycle":true}'::jsonb,
 locked_at=NULL,completed_at=NULL,updated_at=clock_timestamp(),
 last_error='上游到期时间早于本地权益，请人工核实后处理暂停'
 WHERE id=$1 AND status='running' AND attempts=$2 AND external_started_at IS NULL`, j.ID, j.Attempts); err != nil {
			return false, err
		}
	} else {
		if err = cancelObsoleteOverdueSuspension(ctx, tx, j); err != nil {
			return false, err
		}
	}
	if err = Audit(ctx, tx, 0, "service.expiry_suspension.observed", "service", j.ServiceID, map[string]any{
		"job_id": j.ID, "upstream_due": due, "reason": "upstream_term_or_state_changed",
	}); err != nil {
		return false, err
	}
	return true, nil
}
