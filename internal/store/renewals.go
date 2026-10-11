package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type RenewalResult struct {
	InvoiceID   int64  `json:"invoice_id"`
	InvoiceNo   string `json:"invoice_no"`
	ServiceID   int64  `json:"service_id"`
	AmountCents int64  `json:"amount_cents"`
	Replayed    bool   `json:"replayed"`
}

func (s *Store) CreateRenewalInvoice(ctx context.Context, userID, serviceID int64, key string) (RenewalResult, error) {
	var out RenewalResult
	if userID < 1 || serviceID < 1 || len(key) < 8 || len(key) > 128 {
		return out, conflict("invalid_renewal", "服务或幂等键无效")
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "renewal", fmt.Sprintf("%d:%s", serviceID, key)); err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `SELECT id,invoice_no,amount_cents FROM invoices
WHERE user_id=$1 AND idempotency_key=$2`, userID, key).Scan(&out.InvoiceID, &out.InvoiceNo, &out.AmountCents)
	if err == nil {
		var savedService int64
		if err = tx.QueryRow(ctx, "SELECT service_id FROM invoice_items WHERE invoice_id=$1 AND item_type='renew' LIMIT 1", out.InvoiceID).Scan(&savedService); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, conflict("idempotency_conflict", "相同幂等键已用于其他账单")
			}
			return out, err
		}
		if savedService != serviceID {
			return out, conflict("idempotency_conflict", "相同幂等键对应不同续费服务")
		}
		out.ServiceID = serviceID
		out.Replayed = true
		return out, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, err
	}
	var ownerID int64
	var renewalBase int64
	var benefit RenewalBenefit
	var serviceStatus, cycle, productName string
	if err = tx.QueryRow(ctx, `SELECT s.user_id,s.status,s.billing_cycle,p.name,coalesce(s.recurring_price_cents,oi.price_cents,p.price_cents),s.renewal_discount_percent,s.renewal_promo_code,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used
FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 FOR UPDATE OF s`, serviceID).
		Scan(&ownerID, &serviceStatus, &cycle, &productName, &renewalBase, &benefit.Percent, &benefit.Code, &benefit.FixedCents, &benefit.Limit, &benefit.Used); err != nil {
		return out, err
	}
	if ownerID != userID {
		return out, pgx.ErrNoRows
	}
	var supplierMode bool
	if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
		return out, err
	}
	if supplierMode {
		return out, conflict("unsupported_supplier_renewal", "该上游适配器暂未开放自动续费，请通过管理员核验处理")
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, err
	}
	if serviceStatus != "active" && serviceStatus != "suspended" {
		return out, conflict("invalid_renewal_state", "只有已启用或已暂停的服务可以续费")
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
		return out, err
	}
	if pending {
		return out, conflict("renewal_invoice_pending", "该服务已有待支付的续费账单")
	}
	if cycle == "once" {
		return out, conflict("nonrenewable_service", "一次性服务不可续费")
	}
	if renewalBase < 0 {
		return out, conflict("invalid_renewal_price", "续费价格无效")
	}
	renewalDiscount, snapshot, ordinal, err := benefit.Quote(renewalBase)
	if err != nil {
		return out, err
	}
	out.AmountCents = renewalBase - renewalDiscount
	err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key)
VALUES('R-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'unpaid',$2,$3)
RETURNING id,invoice_no`, userID, out.AmountCents, key).Scan(&out.InvoiceID, &out.InvoiceNo)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id,renewal_base_cents,renewal_discount_cents,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discount_ordinal)
VALUES($1,$2,'renew',$3,$4,$5,$6,$7,$8,$9,$10,$11)`, out.InvoiceID, productName+"续费", out.AmountCents, serviceID, renewalBase, renewalDiscount, snapshot.Percent, snapshot.Code, snapshot.FixedCents, snapshot.Limit, ordinal); err != nil {
		return out, err
	}
	out.ServiceID = serviceID
	if err = Audit(ctx, tx, userID, "service.renewal.created", "service", serviceID, map[string]any{"invoice_id": out.InvoiceID, "amount_cents": out.AmountCents, "billing_cycle": cycle, "base_cents": renewalBase, "discount_cents": renewalDiscount, "discount_percent": snapshot.Percent, "promo_code": snapshot.Code, "fixed_discount_cents": snapshot.FixedCents, "discount_limit": snapshot.Limit, "discount_ordinal": ordinal}); err != nil {
		return out, err
	}
	return out, tx.Commit(ctx)
}

func (s *Store) CreateAutoRenewalInvoice(ctx context.Context, userID, serviceID int64, due time.Time, key string) (RenewalResult, error) {
	var out RenewalResult
	if due.IsZero() {
		return out, conflict("invalid_auto_renewal", "自动续费到期时间无效")
	}
	// The worker's due-time snapshot is only a candidate. Lock and re-check it
	// before creating the invoice, so manual renewal wins a race safely.
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var currentDue time.Time
	if err = tx.QueryRow(ctx, "SELECT next_due_at FROM services WHERE id=$1 AND user_id=$2 AND auto_renew AND status IN ('active','suspended') FOR UPDATE", serviceID, userID).Scan(&currentDue); err != nil {
		return out, err
	}
	if currentDue.Sub(due) > time.Second || due.Sub(currentDue) > time.Second || currentDue.After(time.Now().Add(24*time.Hour)) {
		return out, conflict("auto_renewal_race", "服务到期时间已变化，请稍后重试")
	}
	if err = tx.Commit(ctx); err != nil {
		return out, err
	}
	return s.CreateRenewalInvoice(ctx, userID, serviceID, key)
}

func renewalInterval(cycle string) string {
	switch strings.ToLower(cycle) {
	case "quarterly":
		return "3 months"
	case "annually":
		return "1 year"
	default:
		return "1 month"
	}
}

func extendRenewalDue(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var serviceID, ordinal int64
	var snapshot RenewalBenefit
	if err := tx.QueryRow(ctx, "SELECT service_id,renewal_discount_ordinal,renewal_discount_percent,renewal_fixed_discount_cents,renewal_discount_limit,renewal_promo_code FROM invoice_items WHERE invoice_id=$1 AND item_type='renew' LIMIT 1", invoiceID).Scan(&serviceID, &ordinal, &snapshot.Percent, &snapshot.FixedCents, &snapshot.Limit, &snapshot.Code); err != nil {
		return err
	}
	var cycle string
	var current RenewalBenefit
	if err := tx.QueryRow(ctx, "SELECT billing_cycle,renewal_discount_percent,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discounts_used,renewal_promo_code FROM services WHERE id=$1 FOR UPDATE", serviceID).Scan(&cycle, &current.Percent, &current.FixedCents, &current.Limit, &current.Used, &current.Code); err != nil {
		return err
	}
	if ordinal > 0 {
		if current.Used >= maxInt64 || ordinal != current.Used+1 || snapshot.Percent != current.Percent || snapshot.FixedCents != current.FixedCents || snapshot.Limit != current.Limit || snapshot.Code != current.Code || current.Limit > 0 && ordinal > current.Limit {
			return conflict("renewal_benefit_changed", "续费账单的权益次数或规则已变化")
		}
		if _, err := tx.Exec(ctx, "UPDATE services SET renewal_discounts_used=renewal_discounts_used+1 WHERE id=$1", serviceID); err != nil {
			return err
		}
	}
	if err := requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, fmt.Sprintf(`UPDATE services SET next_due_at=GREATEST(COALESCE(next_due_at,now()),now())+interval '%s',version=version+1,updated_at=now() WHERE id=$1`, renewalInterval(cycle)), serviceID)
	return err
}

func settleServiceInvoice(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var kind string
	if err := tx.QueryRow(ctx, "SELECT invoice_kind FROM invoices WHERE id=$1", invoiceID).Scan(&kind); err != nil {
		return err
	}
	if kind == "manual" {
		return requireManualInvoiceValid(ctx, tx, invoiceID)
	}
	if traffic, err := settleTrafficInvoice(ctx, tx, invoiceID); traffic || err != nil {
		return err
	}
	var upgrade bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_configuration_changes WHERE invoice_id=$1)", invoiceID).Scan(&upgrade); err != nil {
		return err
	}
	if upgrade {
		return queueConfigurationChange(ctx, tx, invoiceID)
	}
	return extendRenewalDue(ctx, tx, invoiceID)
}
