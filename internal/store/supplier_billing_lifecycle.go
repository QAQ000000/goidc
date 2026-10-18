package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
)

// Source provenance is the latest completed lifecycle action, not simply a
// suspended status: an operator/upstream suspension must not be auto-resumed.
const latestSupplierLifecycleSQL = `SELECT j.id,j.action,j.idempotency_key,j.request_payload
 FROM provision_jobs j WHERE j.service_id=s.id AND j.status='succeeded'
 AND j.action IN ('create','suspend','resume','terminate')
 ORDER BY j.completed_at DESC NULLS LAST,j.id DESC LIMIT 1`

const relatedServiceInvoiceSQL = `(i.order_id=s.order_id OR EXISTS (
 SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
 AND ii.item_type IN ('renew','service','upgrade')))`

const noOverdueServiceInvoiceSQL = `NOT EXISTS (SELECT 1 FROM invoices debt
 WHERE debt.status='unpaid' AND NOT debt.import_review_required
 AND debt.due_at<clock_timestamp()-interval '24 hours'
 AND (debt.order_id=s.order_id OR EXISTS (
 SELECT 1 FROM invoice_items di WHERE di.invoice_id=debt.id AND di.service_id=s.id
 AND di.item_type IN ('renew','service','upgrade'))))`

type supplierBillingRecovery struct {
	serviceID, invoiceID, sourceJobID int64
}

// This is a separate bounded transaction, so invoices are always locked before
// services even when the same scheduler scan has already queued suspensions.
func (s *Store) enqueuePaidSupplierRecoveries(ctx context.Context, limit int) (int, error) {
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT s.id,i.id,source.id FROM services s
 JOIN LATERAL (`+latestSupplierLifecycleSQL+`) source ON source.action='suspend'
 JOIN invoices i ON source.idempotency_key='overdue-suspend-service-'||s.id::text||'-invoice-'||i.id::text
 WHERE s.provision_mode='supplier' AND s.status='suspended' AND NOT s.import_review_required
 AND s.resource_info->>'system_billing_suspension_job_id'=source.id::text
 AND i.status='paid' AND NOT i.import_review_required
 AND source.request_payload->'system_overdue_suspension'='true'::jsonb
 AND source.request_payload->>'billing_invoice_id'=i.id::text AND `+relatedServiceInvoiceSQL+`
 AND `+noOverdueServiceInvoiceSQL+`
 AND NOT EXISTS (SELECT 1 FROM provision_jobs inflight WHERE inflight.service_id=s.id
 AND inflight.status IN ('pending','running','waiting_manual','waiting_verification'))
 AND NOT EXISTS (SELECT 1 FROM provision_jobs prior WHERE prior.idempotency_key=
 'paid-resume-service-'||s.id::text||'-suspension-'||source.id::text)
 ORDER BY i.id,s.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var candidates []supplierBillingRecovery
	for rows.Next() {
		var c supplierBillingRecovery
		if err = rows.Scan(&c.serviceID, &c.invoiceID, &c.sourceJobID); err != nil {
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
	for _, c := range candidates {
		var id int64
		if err = tx.QueryRow(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", c.invoiceID).Scan(&id); err != nil {
			return 0, err
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].serviceID < candidates[j].serviceID })
	queued := 0
	for _, c := range candidates {
		var id int64
		if err = tx.QueryRow(ctx, "SELECT id FROM services WHERE id=$1 FOR UPDATE", c.serviceID).Scan(&id); err != nil {
			return 0, err
		}
		eligible, e := supplierPaidRecoveryEligible(ctx, tx, c)
		if e != nil {
			return 0, e
		}
		if !eligible {
			continue
		}
		var busy bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
 AND status IN ('pending','running','waiting_manual','waiting_verification'))`, c.serviceID).Scan(&busy); err != nil {
			return 0, err
		}
		if busy {
			continue
		}
		caps, capErr := s.supplierServiceActionCapabilities(ctx, tx, c.serviceID)
		if capErr != nil {
			var businessErr *Conflict
			if !errors.As(capErr, &businessErr) && !errors.Is(capErr, pgx.ErrNoRows) {
				return 0, capErr
			}
		}
		payload := map[string]any{"billing_suspension_job_id": c.sourceJobID, "billing_invoice_id": c.invoiceID}
		status, message := "pending", ""
		manual := capErr != nil || !caps.Resume
		if manual {
			status, message = "waiting_manual", supplierLifecycleManualMessage
			payload["manual_lifecycle"] = true
		}
		raw, _ := json.Marshal(payload)
		key := fmt.Sprintf("paid-resume-service-%d-suspension-%d", c.serviceID, c.sourceJobID)
		command, e := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,last_error,request_payload)
 VALUES($1,'resume',$2,$3,$4,$5::jsonb) ON CONFLICT(idempotency_key) DO NOTHING`, c.serviceID, key, status, message, raw)
		if e != nil {
			return 0, e
		}
		if command.RowsAffected() == 0 {
			continue
		}
		action := "service.paid_recovery.queued"
		if manual {
			action = "supplier.lifecycle.manual_requested"
		}
		if err = Audit(ctx, tx, 0, action, "service", c.serviceID, map[string]any{"invoice_id": c.invoiceID, "suspension_job_id": c.sourceJobID, "action": "resume", "source": "paid_recovery"}); err != nil {
			return 0, err
		}
		queued++
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}

func supplierPaidRecoveryEligible(ctx context.Context, tx pgx.Tx, c supplierBillingRecovery) (bool, error) {
	var eligible bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s
 JOIN invoices i ON i.id=$2
 JOIN LATERAL (`+latestSupplierLifecycleSQL+`) source ON source.id=$3 AND source.action='suspend'
 WHERE s.id=$1 AND s.provision_mode='supplier' AND s.status='suspended'
 AND s.resource_info->>'system_billing_suspension_job_id'=source.id::text
 AND NOT s.import_review_required AND i.status='paid' AND NOT i.import_review_required
 AND source.request_payload->'system_overdue_suspension'='true'::jsonb
 AND source.request_payload->>'billing_invoice_id'=i.id::text
 AND source.idempotency_key='overdue-suspend-service-'||s.id::text||'-invoice-'||i.id::text
 AND `+relatedServiceInvoiceSQL+` AND `+noOverdueServiceInvoiceSQL+`)`, c.serviceID, c.invoiceID, c.sourceJobID).Scan(&eligible)
	return eligible, err
}

func lockPaidSupplierRecoveryInvoice(ctx context.Context, tx pgx.Tx, job ProvisionJob, key string, payload []byte) (bool, error) {
	var source struct {
		SuspensionJobID int64 `json:"billing_suspension_job_id"`
		InvoiceID       int64 `json:"billing_invoice_id"`
	}
	if json.Unmarshal(payload, &source) != nil || source.SuspensionJobID < 1 || source.InvoiceID < 1 ||
		key != fmt.Sprintf("paid-resume-service-%d-suspension-%d", job.ServiceID, source.SuspensionJobID) {
		return false, fmt.Errorf("invalid paid recovery identity")
	}
	var id int64
	err := tx.QueryRow(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", source.InvoiceID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return supplierPaidRecoveryEligible(ctx, tx, supplierBillingRecovery{job.ServiceID, source.InvoiceID, source.SuspensionJobID})
}
