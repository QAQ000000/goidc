package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

type supplierRenewalSnapshot struct {
	supplier.RenewalRequest
	SupplierID     int64 `json:"supplier_id"`
	AccountVersion int64 `json:"account_version"`
	InvoiceID      int64 `json:"local_invoice_id"`
}

// supplierRenewalQuote carries a checked local snapshot across an upstream
// read. It is never persisted until the snapshot is revalidated under locks.
type supplierRenewalQuote struct {
	adapter        supplier.RenewalAdapter
	snapshot       supplierRenewalSnapshot
	request        supplier.RenewalRequest
	serviceVersion int64
	due            *time.Time
}

func (s *Store) requireSupplierRenewal(ctx context.Context, tx pgx.Tx, serviceID int64) (supplier.RenewalAdapter, supplierRenewalSnapshot, error) {
	var snap supplierSnapshot
	var raw []byte
	var upstream, cycle string
	var out supplierRenewalSnapshot
	if err := tx.QueryRow(ctx, "SELECT delivery_snapshot,upstream_id,billing_cycle FROM services WHERE id=$1", serviceID).Scan(&raw, &upstream, &cycle); err != nil {
		return nil, out, err
	}
	if json.Unmarshal(raw, &snap) != nil || snap.BindingID < 1 {
		return nil, out, conflict("supplier_snapshot_invalid", "供应商交付快照无效")
	}
	// All reads must reuse the caller's transaction connection. Acquiring a
	// second pool connection while holding the service lock can deadlock a
	// full pool of concurrent renewals waiting on that same service.
	b, err := s.supplierBindingWith(ctx, tx, snap.BindingID, false)
	if err != nil {
		return nil, out, err
	}
	if b.SupplierID != snap.SupplierID || b.Provider != snap.Provider || b.BillingCycle != cycle {
		return nil, out, conflict("supplier_snapshot_invalid", "供应商交付绑定不匹配")
	}
	// Keep the account configuration stable through this short transaction.
	// No upstream request may run while this lock is held.
	if _, err = tx.Exec(ctx, "SELECT id FROM supplier_accounts WHERE id=$1 FOR SHARE", snap.SupplierID); err != nil {
		return nil, out, err
	}
	a, version, err := s.supplierAdapterWith(ctx, tx, snap.SupplierID)
	if err != nil {
		return nil, out, err
	}
	renew, ok := a.(supplier.RenewalAdapter)
	if !ok || !a.Capabilities().Renew {
		return nil, out, conflict("unsupported_supplier_renewal", "该供应商暂不支持自动续费")
	}
	id, _ := strconv.ParseInt(upstream, 10, 64)
	if id < 1 {
		return nil, out, conflict("supplier_resource_missing", "缺少供应商资源标识")
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1 AND action<>'sync' AND status IN ('pending','running','waiting_verification'))
 OR EXISTS(SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id LEFT JOIN provision_jobs j ON j.idempotency_key='supplier-renew-'||i.id::text
 WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='paid' AND coalesce(j.status,'')<>'succeeded')`, serviceID).Scan(&pending); err != nil {
		return nil, out, err
	}
	if pending {
		return nil, out, conflict("supplier_renewal_pending", "该服务有未完成的供应商操作或已收款续费，请先处理")
	}
	out.SupplierID, out.AccountVersion = snap.SupplierID, version
	out.RenewalRequest = supplier.RenewalRequest{UpstreamID: id, ProductID: b.UpstreamProductID, Cycle: cycle, Currency: b.Currency, MaximumCostCents: b.MaximumCostCents}
	return renew, out, nil
}

func queueSupplierRenewal(ctx context.Context, tx pgx.Tx, invoiceID, serviceID int64) (bool, error) {
	var mode string
	if err := tx.QueryRow(ctx, "SELECT provision_mode FROM services WHERE id=$1", serviceID).Scan(&mode); err != nil {
		return false, err
	}
	if mode != "supplier" {
		return false, nil
	}
	var raw []byte
	var snap supplierRenewalSnapshot
	if err := tx.QueryRow(ctx, "SELECT supplier_renewal_snapshot FROM invoice_items WHERE invoice_id=$1 AND service_id=$2 AND item_type='renew'", invoiceID, serviceID).Scan(&raw); err != nil {
		return true, err
	}
	if json.Unmarshal(raw, &snap) != nil || snap.SupplierID < 1 || snap.UpstreamID < 1 || snap.PreviousDue == "" {
		return true, conflict("supplier_renewal_snapshot_missing", "续费账单缺少上游报价，请重新生成账单")
	}
	snap.InvoiceID = invoiceID
	payload, _ := json.Marshal(snap)
	_, err := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,request_payload) VALUES($1,'renew',$2,$3) ON CONFLICT(idempotency_key) DO NOTHING`, serviceID, fmt.Sprintf("supplier-renew-%d", invoiceID), payload)
	return true, err
}

func (s *Store) failUndispatchedRenewal(ctx context.Context, tx pgx.Tx, j ProvisionJob, message string) error {
	if _, err := tx.Exec(ctx, "UPDATE provision_jobs SET status='failed',last_error=$2,locked_at=NULL,completed_at=now(),updated_at=now() WHERE id=$1", j.ID, message); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Reverse local collection only before dispatch or after a known failure.
func refundSupplierRenewal(ctx context.Context, tx pgx.Tx, invoiceID, serviceID int64) (bool, error) {
	var supplierMode bool
	if err := tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
		return false, err
	}
	if !supplierMode {
		return false, nil
	}
	var id int64
	var status string
	var dispatched bool
	if err := tx.QueryRow(ctx, "SELECT id,status,external_started_at IS NOT NULL FROM provision_jobs WHERE service_id=$1 AND action='renew' AND idempotency_key=$2 FOR UPDATE", serviceID, fmt.Sprintf("supplier-renew-%d", invoiceID)).Scan(&id, &status, &dispatched); err != nil {
		return true, conflict("supplier_renewal_refund_unconfirmed", "缺少可信上游续费结果，请先核验")
	}
	if status != "failed" && !(status == "pending" && !dispatched) {
		return true, conflict("supplier_renewal_refund_unconfirmed", "上游续费已开始或已生效，请先处理并核验上游结果，不能只退本站账单")
	}
	_, err := tx.Exec(ctx, "UPDATE provision_jobs SET status='cancelled',completed_at=now(),updated_at=now() WHERE id=$1", id)
	return true, err
}
