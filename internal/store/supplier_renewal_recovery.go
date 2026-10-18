package store

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type supplierRenewalExpirySource struct {
	Status         string `json:"status"`
	Reason         string `json:"reason"`
	LifecycleJobID int64  `json:"lifecycle_job_id"`
	BindingID      int64  `json:"binding_id"`
	Provider       string `json:"provider"`
}

func supplierDueSuspension(reason string) bool {
	return reason == "due" || strings.HasPrefix(reason, "due-")
}

// Capture the lifecycle origin before any renewal write. An Active resource
// may become expiry-suspended while the upstream processes payment; recording
// its origin also covers that race without authorizing manual/policy pauses.
// A local lifecycle suspension (including one whose text happens to say due)
// retains its separate, invoice-proven paid-recovery/manual-resume contract.
// Renewal requests are immutable; normalized dispatch evidence lives in result
// and is retained by checkpoint/finish/reconciliation merges.
func snapshotSupplierRenewalExpiryRecovery(ctx context.Context, tx pgx.Tx, j ProvisionJob, b SupplierBinding) error {
	var current, reason, latestAction string
	var latestID int64
	var systemBilling bool
	if err := tx.QueryRow(ctx, `SELECT s.status,coalesce(s.resource_info->>'suspend_reason',''),
 s.resource_info ? 'system_billing_suspension_job_id',coalesce(source.id,0),coalesce(source.action,'')
 FROM services s LEFT JOIN LATERAL (`+latestSupplierLifecycleSQL+`) source ON true WHERE s.id=$1`, j.ServiceID).
		Scan(&current, &reason, &systemBilling, &latestID, &latestAction); err != nil {
		return err
	}
	evidence := map[string]any{}
	if (current == "active" || (current == "suspended" && supplierDueSuspension(reason))) && !systemBilling && latestAction != "suspend" {
		if current == "active" {
			reason = ""
		}
		evidence["renewal_expiry_recovery"] = supplierRenewalExpirySource{Status: current, Reason: reason, LifecycleJobID: latestID, BindingID: b.ID, Provider: b.Provider}
	}
	raw, _ := json.Marshal(evidence)
	_, err := tx.Exec(ctx, `UPDATE provision_jobs SET result=(result-'renewal_expiry_recovery')||$2::jsonb WHERE id=$1`, j.ID, raw)
	return err
}

// A successful, fully confirmed renewal can clear only the expiry suspension
// captured for that exact write. A later Sync needs a service marker referencing
// the already succeeded job, whose upstream Paid/full-term proof came from
// Renew/ConfirmRenewal. Neither an ordinary read nor a historical paid invoice
// creates this authority. All reads reuse the caller's service lock; in
// particular, do not acquire invoice locks after service/job/account locks.
func supplierRenewalExpiryRecovery(ctx context.Context, tx pgx.Tx, j ProvisionJob, remote supplier.Service, due *time.Time, reason string) (int64, error) {
	if (j.Action != "renew" && j.Action != "sync") || j.ID < 1 || !supplierDueSuspension(reason) || due == nil {
		return 0, nil
	}
	id := j.ID
	if j.Action == "sync" {
		var marker string
		// Only a freshly dispatched Sync may consume delayed recovery authority.
		// Callbacks have no read identity and may be stale, so never do so.
		if err := tx.QueryRow(ctx, `SELECT coalesce(s.resource_info->>'supplier_renewal_recovery_job_id','')
 FROM services s JOIN provision_jobs sync ON sync.service_id=s.id
 WHERE s.id=$1 AND sync.id=$2 AND sync.action='sync' AND sync.status='running' AND sync.attempts=$3
 AND sync.external_started_at IS NOT NULL AND sync.locked_at>clock_timestamp()-interval '5 minutes'`, j.ServiceID, j.ID, j.Attempts).Scan(&marker); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return 0, nil
			}
			return 0, err
		}
		id, _ = strconv.ParseInt(marker, 10, 64)
	}
	if id < 1 {
		return 0, nil
	}
	var payload, result []byte
	var state, key string
	var dispatched bool
	if err := tx.QueryRow(ctx, `SELECT request_payload,result,status,idempotency_key,external_started_at IS NOT NULL
 FROM provision_jobs WHERE id=$1 AND service_id=$2 AND action='renew'`, id, j.ServiceID).
		Scan(&payload, &result, &state, &key, &dispatched); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	var renewal supplierRenewalSnapshot
	var evidence struct {
		Source *supplierRenewalExpirySource `json:"renewal_expiry_recovery"`
	}
	if !dispatched || json.Unmarshal(payload, &renewal) != nil || json.Unmarshal(result, &evidence) != nil || evidence.Source == nil ||
		!((evidence.Source.Status == "suspended" && evidence.Source.Reason == reason) ||
			(evidence.Source.Status == "active" && evidence.Source.Reason == "")) ||
		evidence.Source.BindingID < 1 || evidence.Source.Provider == "" ||
		renewal.InvoiceID < 1 || key != "supplier-renew-"+strconv.FormatInt(renewal.InvoiceID, 10) ||
		(j.Action == "sync" && state != "succeeded") || (j.Action == "renew" && state != "running" && state != "waiting_verification") {
		return 0, nil
	}
	before, err := supplierDueDate(renewal.PreviousDue)
	months := map[string]int{"monthly": 1, "quarterly": 3, "annually": 12}[renewal.Cycle]
	if err != nil || before == nil || months == 0 || remote.UpstreamID != renewal.UpstreamID || remote.ProductID != renewal.ProductID ||
		due.Before(before.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, months, 0)) {
		return 0, nil
	}
	var eligible bool
	err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s
 JOIN supplier_product_bindings b ON b.id=$3 AND b.product_id=s.product_id
 JOIN supplier_accounts a ON a.id=b.supplier_id
 JOIN invoices i ON i.id=$2 AND i.user_id=s.user_id AND i.status='paid' AND NOT i.import_review_required
 LEFT JOIN LATERAL (`+latestSupplierLifecycleSQL+`) source ON true
 WHERE s.id=$1 AND s.provision_mode='supplier' AND s.status='suspended' AND NOT s.import_review_required
 AND s.resource_info->>'suspend_reason'=$16
 AND NOT s.resource_info ? 'system_billing_suspension_job_id'
 AND coalesce(source.id,0)=$12 AND coalesce(source.action,'')<>'suspend'
 AND s.delivery_snapshot->>'binding_id'=b.id::text
 AND s.delivery_snapshot->>'supplier_id'=a.id::text AND s.delivery_snapshot->>'provider'=$4
 AND a.active AND a.version=$6 AND a.provider=$4
 AND b.provider=$4 AND b.supplier_id=$5 AND b.upstream_product_id=$8 AND b.billing_cycle=$9
 AND b.currency=$10 AND b.maximum_cost_cents=$11
 AND s.upstream_id=$7 AND s.billing_cycle=$9
 AND $14::timestamptz<=clock_timestamp()+interval '24 hours' AND $15::timestamptz>clock_timestamp()
 AND EXISTS(SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id AND ii.item_type='renew')
 AND `+noOverdueServiceInvoiceSQL+`
 AND NOT EXISTS(SELECT 1 FROM provision_jobs other WHERE other.service_id=s.id AND other.id<>$13
 AND other.action<>'sync' AND other.status IN ('pending','running','waiting_manual','waiting_verification')))`,
		j.ServiceID, renewal.InvoiceID, evidence.Source.BindingID, evidence.Source.Provider, renewal.SupplierID, renewal.AccountVersion,
		strconv.FormatInt(renewal.UpstreamID, 10), renewal.ProductID, renewal.Cycle, renewal.Currency, renewal.MaximumCostCents,
		evidence.Source.LifecycleJobID, id, before, due, reason).Scan(&eligible)
	if err != nil || !eligible {
		return 0, err
	}
	return id, nil
}
