package store

import (
	"context"
	"encoding/json"
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
	return s.createRenewalInvoiceWithDue(ctx, userID, serviceID, key, nil)
}

func (s *Store) createRenewalInvoiceWithDue(ctx context.Context, userID, serviceID int64, key string, automaticDue *time.Time) (RenewalResult, error) {
	if userID < 1 || serviceID < 1 || len(key) < 8 || len(key) > 128 {
		return RenewalResult{}, conflict("invalid_renewal", "服务或幂等键无效")
	}
	out, quote, err := s.createRenewalInvoiceStep(ctx, userID, serviceID, key, automaticDue, nil)
	if err != nil || quote == nil {
		return out, err
	}
	// The preparation transaction has committed. A slow upstream must hold
	// neither a service lock nor a database connection. The second transaction
	// repeats admission and idempotency checks before accepting the quotation.
	quote.request, err = quote.adapter.QuoteRenewal(ctx, quote.snapshot.RenewalRequest)
	if err != nil {
		return out, supplierReadError(err)
	}
	out, _, err = s.createRenewalInvoiceStep(ctx, userID, serviceID, key, automaticDue, quote)
	return out, err
}

func (s *Store) createRenewalInvoiceStep(ctx context.Context, userID, serviceID int64, key string, automaticDue *time.Time, quote *supplierRenewalQuote) (RenewalResult, *supplierRenewalQuote, error) {
	var out RenewalResult
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, nil, err
	}
	defer tx.Rollback(ctx)
	if err = lockOperationKey(ctx, tx, "renewal", fmt.Sprintf("%d:%s", serviceID, key)); err != nil {
		return out, nil, err
	}
	if automaticDue != nil {
		var generation int64
		var enabled bool
		var actualDue *time.Time
		if err = tx.QueryRow(ctx, `SELECT renewal_generation,auto_renew,next_due_at FROM services WHERE id=$1 AND user_id=$2 FOR UPDATE`, serviceID, userID).Scan(&generation, &enabled, &actualDue); err != nil {
			return out, nil, err
		}
		expected, _ := ctx.Value(autoRenewGenerationKey{}).(int64)
		if generation != expected || !enabled || actualDue == nil || !actualDue.Equal(*automaticDue) {
			return out, nil, conflict("auto_renewal_race", "自动续费代次、开关或期限已变化")
		}
		if err = requireNoLocalRenewalHold(ctx, tx, serviceID); err != nil {
			return out, nil, err
		}
	}
	err = tx.QueryRow(ctx, `SELECT id,invoice_no,amount_cents FROM invoices
WHERE user_id=$1 AND idempotency_key=$2`, userID, key).Scan(&out.InvoiceID, &out.InvoiceNo, &out.AmountCents)
	if err == nil {
		var savedService int64
		if err = tx.QueryRow(ctx, "SELECT service_id FROM invoice_items WHERE invoice_id=$1 AND item_type='renew' LIMIT 1", out.InvoiceID).Scan(&savedService); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return out, nil, conflict("idempotency_conflict", "相同幂等键已用于其他账单")
			}
			return out, nil, err
		}
		if savedService != serviceID {
			return out, nil, conflict("idempotency_conflict", "相同幂等键对应不同续费服务")
		}
		if automaticDue != nil {
			generation, _ := ctx.Value(autoRenewGenerationKey{}).(int64)
			if err = requireAutoRenewalInvoiceSource(ctx, tx, userID, out.InvoiceID, autoRenewalPayment{serviceID: serviceID, due: *automaticDue, generation: generation}); err != nil {
				return out, nil, err
			}
			var supplierMode bool
			if err = tx.QueryRow(ctx, "SELECT provision_mode='supplier' FROM services WHERE id=$1", serviceID).Scan(&supplierMode); err != nil {
				return out, nil, err
			}
			if supplierMode {
				// A prior local payment is not a fresh renewal success. Known
				// failed and unresolved supplier jobs still require settlement;
				// keep their pending/error reason instead of replaying the paid
				// receipt and clearing it on every automatic scan.
				if _, _, err = s.requireSupplierRenewal(ctx, tx, serviceID); err != nil {
					return out, nil, err
				}
			}
		}
		out.ServiceID = serviceID
		out.Replayed = true
		return out, nil, tx.Commit(ctx)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return out, nil, err
	}
	var ownerID int64
	var renewalBase int64
	var benefit RenewalBenefit
	var serviceStatus, cycle, productName string
	var serviceVersion int64
	var due *time.Time
	var autoRenew, supplierMode bool
	if err = tx.QueryRow(ctx, `SELECT s.user_id,s.status,s.billing_cycle,p.name,coalesce(s.recurring_price_cents,oi.price_cents,p.price_cents),s.renewal_discount_percent,s.renewal_promo_code,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,s.version,s.next_due_at,s.auto_renew,s.provision_mode='supplier'
FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 FOR UPDATE OF s`, serviceID).
		Scan(&ownerID, &serviceStatus, &cycle, &productName, &renewalBase, &benefit.Percent, &benefit.Code, &benefit.FixedCents, &benefit.Limit, &benefit.Used, &serviceVersion, &due, &autoRenew, &supplierMode); err != nil {
		return out, nil, err
	}
	if ownerID != userID {
		return out, nil, pgx.ErrNoRows
	}
	if err = requireServiceCancellationOpen(ctx, tx, serviceID); err != nil {
		return out, nil, err
	}
	if automaticDue != nil {
		if !autoRenew || due == nil || (serviceStatus != "active" && serviceStatus != "suspended") {
			return out, nil, pgx.ErrNoRows
		}
		if due.Sub(*automaticDue) > time.Second || automaticDue.Sub(*due) > time.Second || due.After(time.Now().Add(24*time.Hour)) {
			return out, nil, conflict("auto_renewal_race", "服务到期时间已变化，请稍后重试")
		}
	}
	if err = requireNoLocalRenewalHold(ctx, tx, serviceID); err != nil {
		return out, nil, err
	}
	if err = requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return out, nil, err
	}
	if serviceStatus != "active" && serviceStatus != "suspended" {
		return out, nil, conflict("invalid_renewal_state", "只有已启用或已暂停的服务可以续费")
	}
	if err = requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return out, nil, err
	}
	var pending bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(
SELECT 1 FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid')`, serviceID).Scan(&pending); err != nil {
		return out, nil, err
	}
	if pending {
		return out, nil, conflict("renewal_invoice_pending", "该服务已有待支付的续费账单")
	}
	if cycle == "once" {
		return out, nil, conflict("nonrenewable_service", "一次性服务不可续费")
	}
	if renewalBase < 0 {
		return out, nil, conflict("invalid_renewal_price", "续费价格无效")
	}
	supplierQuote := []byte(`{}`)
	if supplierMode {
		adapter, snapshot, quoteErr := s.requireSupplierRenewal(ctx, tx, serviceID)
		if quoteErr != nil {
			return out, nil, quoteErr
		}
		if quote == nil {
			prepared := &supplierRenewalQuote{adapter: adapter, snapshot: snapshot, serviceVersion: serviceVersion, due: due}
			return out, prepared, tx.Commit(ctx)
		}
		sameDue := quote.due == nil && due == nil || quote.due != nil && due != nil && quote.due.Equal(*due)
		if quote.serviceVersion != serviceVersion || !sameDue || quote.snapshot != snapshot {
			return out, nil, conflict("supplier_renewal_quote_changed", "服务或供应商配置已变更，请重新获取续费报价")
		}
		if automaticDue != nil {
			remoteDue, dueErr := supplierDueDate(quote.request.PreviousDue)
			if dueErr != nil || remoteDue == nil {
				return out, nil, conflict("supplier_due_invalid", "上游到期时间不可识别，请核验")
			}
			var outsideWindow bool
			if err = tx.QueryRow(ctx, "SELECT $1::timestamptz>clock_timestamp()+interval '24 hours'", *remoteDue).Scan(&outsideWindow); err != nil {
				return out, nil, err
			}
			if outsideWindow {
				// A stale local deadline must never buy another period that the
				// upstream has already supplied. Admission, binding/account identity
				// and both quotation versions have been checked under their locks.
				if _, err = tx.Exec(ctx, `UPDATE services SET next_due_at=greatest(next_due_at,$2),
auto_renew_error='',version=version+1,updated_at=clock_timestamp() WHERE id=$1`, serviceID, *remoteDue); err != nil {
					return out, nil, err
				}
				if err = Audit(ctx, tx, 0, "supplier.auto_renewal.skipped", "service", serviceID,
					map[string]any{"reason": "upstream_not_due", "previous_due": due, "upstream_due": *remoteDue}); err != nil {
					return out, nil, err
				}
				if err = tx.Commit(ctx); err != nil {
					return out, nil, err
				}
				return out, nil, conflict("supplier_auto_renewal_not_due", "上游尚未进入自动续费期间，已同步到期时间")
			}
		}
		snapshot.RenewalRequest = quote.request
		supplierQuote, _ = json.Marshal(snapshot)
	} else if quote != nil {
		return out, nil, conflict("supplier_renewal_quote_changed", "服务交付方式已变更，请重新获取续费报价")
	}
	renewalDiscount, snapshot, ordinal, err := benefit.Quote(renewalBase)
	if err != nil {
		return out, nil, err
	}
	out.AmountCents = renewalBase - renewalDiscount
	err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key)
VALUES('R-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'unpaid',$2,$3)
RETURNING id,invoice_no`, userID, out.AmountCents, key).Scan(&out.InvoiceID, &out.InvoiceNo)
	if err != nil {
		return out, nil, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id,renewal_base_cents,renewal_discount_cents,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discount_ordinal,supplier_renewal_snapshot)
VALUES($1,$2,'renew',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, out.InvoiceID, productName+"续费", out.AmountCents, serviceID, renewalBase, renewalDiscount, snapshot.Percent, snapshot.Code, snapshot.FixedCents, snapshot.Limit, ordinal, supplierQuote); err != nil {
		return out, nil, err
	}
	if automaticDue != nil {
		generation, _ := ctx.Value(autoRenewGenerationKey{}).(int64)
		if _, err = tx.Exec(ctx, `INSERT INTO auto_renewal_invoice_sources(invoice_id,service_id,user_id,expected_due,generation)
VALUES($1,$2,$3,$4,$5)`, out.InvoiceID, serviceID, userID, *automaticDue, generation); err != nil {
			return out, nil, err
		}
	}
	out.ServiceID = serviceID
	if err = Audit(ctx, tx, userID, "service.renewal.created", "service", serviceID, map[string]any{"invoice_id": out.InvoiceID, "amount_cents": out.AmountCents, "billing_cycle": cycle, "base_cents": renewalBase, "discount_cents": renewalDiscount, "discount_percent": snapshot.Percent, "promo_code": snapshot.Code, "fixed_discount_cents": snapshot.FixedCents, "discount_limit": snapshot.Limit, "discount_ordinal": ordinal}); err != nil {
		return out, nil, err
	}
	return out, nil, tx.Commit(ctx)
}

type autoRenewGenerationKey struct{}

func (s *Store) CreateAutoRenewalInvoice(ctx context.Context, userID, serviceID int64, due time.Time, key string, expectedGeneration ...int64) (RenewalResult, error) {
	if len(expectedGeneration) > 1 || (len(expectedGeneration) == 1 && expectedGeneration[0] < 0) {
		return RenewalResult{}, conflict("invalid_auto_renewal", "自动续费代次无效")
	}
	var generation int64
	if len(expectedGeneration) == 1 {
		generation = expectedGeneration[0]
	}
	ctx = context.WithValue(ctx, autoRenewGenerationKey{}, generation)
	if due.IsZero() {
		return RenewalResult{}, conflict("invalid_auto_renewal", "自动续费到期时间无效")
	}
	// Recheck the worker's candidate under the same service lock used to
	// create the invoice, including after any upstream quotation request.
	return s.createRenewalInvoiceWithDue(ctx, userID, serviceID, key, &due)
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
	// Local entitlement is settled by the invoice unpaid -> paid trigger. Check
	// before consuming a discount ordinal; supplier renewals retain their adapter.
	var localEffect bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM service_renewal_effects WHERE invoice_id=$1)", invoiceID).Scan(&localEffect); err != nil {
		return err
	}
	if localEffect {
		return nil
	}
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
	if handled, err := queueSupplierRenewal(ctx, tx, invoiceID, serviceID); handled || err != nil {
		return err
	}
	return conflict("renewal_effect_missing", "本地续费缺少与收款事务关联的生效事实")
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
