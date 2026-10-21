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
	terms, err := lockRenewalTerms(ctx, tx, serviceID)
	if err != nil {
		return out, nil, err
	}
	if terms.CustomerID != userID {
		return out, nil, pgx.ErrNoRows
	}
	// Keep customer/automatic rejection precedence unchanged while admission
	// below remains shared with the explicit administrator command.
	if err = requireServiceCancellationOpen(ctx, tx, serviceID); err != nil {
		return out, nil, err
	}
	if automaticDue != nil {
		if !terms.AutoRenew || terms.Due == nil || (terms.Status != "active" && terms.Status != "suspended") {
			return out, nil, pgx.ErrNoRows
		}
		if terms.Due.Sub(*automaticDue) > time.Second || automaticDue.Sub(*terms.Due) > time.Second || terms.Due.After(time.Now().Add(24*time.Hour)) {
			return out, nil, conflict("auto_renewal_race", "服务到期时间已变化，请稍后重试")
		}
	}
	if _, err = requireRenewalAdmission(ctx, tx, serviceID, terms, false); err != nil {
		return out, nil, err
	}
	supplierQuote := []byte(`{}`)
	if terms.SupplierMode {
		adapter, snapshot, quoteErr := s.requireSupplierRenewal(ctx, tx, serviceID)
		if quoteErr != nil {
			return out, nil, quoteErr
		}
		if quote == nil {
			prepared := &supplierRenewalQuote{adapter: adapter, snapshot: snapshot, serviceVersion: terms.Version, due: terms.Due}
			return out, prepared, tx.Commit(ctx)
		}
		sameDue := quote.due == nil && terms.Due == nil || quote.due != nil && terms.Due != nil && quote.due.Equal(*terms.Due)
		if quote.serviceVersion != terms.Version || !sameDue || quote.snapshot != snapshot {
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
			managed, managedErr := supplierTermManaged(ctx, tx, serviceID)
			if managedErr != nil {
				return out, nil, managedErr
			}
			if outsideWindow && !managed {
				// A stale local deadline must never buy another period that the
				// upstream has already supplied. Admission, binding/account identity
				// and both quotation versions have been checked under their locks.
				if _, err = observeSupplierTerm(ctx, tx, serviceID, remoteDue); err != nil {
					return out, nil, err
				}
				if _, err = tx.Exec(ctx, `UPDATE services SET next_due_at=greatest(next_due_at,$2),
auto_renew_error='',version=version+1,updated_at=clock_timestamp() WHERE id=$1`, serviceID, *remoteDue); err != nil {
					return out, nil, err
				}
				if err = Audit(ctx, tx, 0, "supplier.auto_renewal.skipped", "service", serviceID,
					map[string]any{"reason": "upstream_not_due", "previous_due": terms.Due, "upstream_due": *remoteDue}); err != nil {
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
	out, err = insertRenewalInvoice(ctx, tx, userID, serviceID, key, terms, supplierQuote, userID, "")
	if err != nil {
		return out, nil, err
	}
	if automaticDue != nil {
		generation, _ := ctx.Value(autoRenewGenerationKey{}).(int64)
		if _, err = tx.Exec(ctx, `INSERT INTO auto_renewal_invoice_sources(invoice_id,service_id,user_id,expected_due,generation)
VALUES($1,$2,$3,$4,$5)`, out.InvoiceID, serviceID, userID, *automaticDue, generation); err != nil {
			return out, nil, err
		}
	}
	return out, nil, tx.Commit(ctx)
}

// renewalTerms and admission are shared by customer, automatic and explicit
// administrator renewal commands. They never change the contract price or the
// auto-renew intent merely because a new unpaid invoice is being prepared.
type renewalTerms struct {
	CustomerID, Version, BaseCents int64
	Status, Cycle, ProductName     string
	Benefit                        RenewalBenefit
	Due                            *time.Time
	AutoRenew, SupplierMode        bool
}

func lockRenewalTerms(ctx context.Context, tx pgx.Tx, serviceID int64) (renewalTerms, error) {
	var terms renewalTerms
	err := tx.QueryRow(ctx, `SELECT s.user_id,s.status,s.billing_cycle,p.name,coalesce(s.recurring_price_cents,oi.price_cents,p.price_cents),s.renewal_discount_percent,s.renewal_promo_code,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,s.version,s.next_due_at,s.auto_renew,s.provision_mode='supplier'
FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id WHERE s.id=$1 FOR UPDATE OF s`, serviceID).
		Scan(&terms.CustomerID, &terms.Status, &terms.Cycle, &terms.ProductName, &terms.BaseCents, &terms.Benefit.Percent, &terms.Benefit.Code, &terms.Benefit.FixedCents, &terms.Benefit.Limit, &terms.Benefit.Used, &terms.Version, &terms.Due, &terms.AutoRenew, &terms.SupplierMode)
	if err != nil {
		return terms, err
	}
	var fixed *int64
	if err = tx.QueryRow(ctx, "SELECT (SELECT fixed_amount_cents FROM service_renewal_pricing WHERE service_id=$1)", serviceID).Scan(&fixed); err == nil && fixed != nil {
		// The override is already the final amount. Contract benefits remain
		// stored on the service and resume, with the same Used, on restoration.
		terms.BaseCents, terms.Benefit = *fixed, RenewalBenefit{}
	}
	return terms, err
}

func requireRenewalAdmission(ctx context.Context, tx pgx.Tx, serviceID int64, terms renewalTerms, allowPending bool) (int64, error) {
	if err := requireServiceCancellationOpen(ctx, tx, serviceID); err != nil {
		return 0, err
	}
	if err := requireNoLocalRenewalHold(ctx, tx, serviceID); err != nil {
		return 0, err
	}
	if err := requireServiceReviewed(ctx, tx, serviceID); err != nil {
		return 0, err
	}
	if terms.Status != "active" && terms.Status != "suspended" {
		return 0, conflict("invalid_renewal_state", "只有已启用或已暂停的服务可以续费")
	}
	if err := requireNoConfigurationChange(ctx, tx, serviceID); err != nil {
		return 0, err
	}
	var pendingInvoice int64
	if err := tx.QueryRow(ctx, `SELECT coalesce(min(i.id),0) FROM invoice_items ii JOIN invoices i ON i.id=ii.invoice_id
WHERE ii.service_id=$1 AND ii.item_type='renew' AND i.status='unpaid'`, serviceID).Scan(&pendingInvoice); err != nil {
		return 0, err
	}
	if pendingInvoice > 0 && !allowPending {
		return pendingInvoice, conflict("renewal_invoice_pending", "该服务已有待支付的续费账单")
	}
	if terms.Cycle == "once" {
		return 0, conflict("nonrenewable_service", "一次性服务不可续费")
	}
	if terms.BaseCents < 0 {
		return 0, conflict("invalid_renewal_price", "续费价格无效")
	}
	return pendingInvoice, nil
}

func insertRenewalInvoice(ctx context.Context, tx pgx.Tx, customer, service int64, key string, terms renewalTerms, supplierQuote []byte, actor int64, reason string) (RenewalResult, error) {
	var out RenewalResult
	discount, snapshot, ordinal, err := terms.Benefit.Quote(terms.BaseCents)
	if err != nil {
		return out, err
	}
	out.AmountCents, out.ServiceID = terms.BaseCents-discount, service
	err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,idempotency_key)
VALUES('R-'||replace(gen_random_uuid()::text,'-',''),$1,NULL,'unpaid',$2,$3)
RETURNING id,invoice_no`, customer, out.AmountCents, key).Scan(&out.InvoiceID, &out.InvoiceNo)
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,description,item_type,amount_cents,service_id,renewal_base_cents,renewal_discount_cents,renewal_discount_percent,renewal_promo_code,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discount_ordinal,supplier_renewal_snapshot)
VALUES($1,$2,'renew',$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`, out.InvoiceID, terms.ProductName+"续费", out.AmountCents, service, terms.BaseCents, discount, snapshot.Percent, snapshot.Code, snapshot.FixedCents, snapshot.Limit, ordinal, supplierQuote); err != nil {
		return out, err
	}
	payload := map[string]any{"invoice_id": out.InvoiceID, "amount_cents": out.AmountCents, "billing_cycle": terms.Cycle, "base_cents": terms.BaseCents, "discount_cents": discount, "discount_percent": snapshot.Percent, "promo_code": snapshot.Code, "fixed_discount_cents": snapshot.FixedCents, "discount_limit": snapshot.Limit, "discount_ordinal": ordinal}
	if reason != "" {
		payload["customer_id"], payload["reason"] = customer, reason
	}
	if err = Audit(ctx, tx, actor, "service.renewal.created", "service", service, payload); err != nil {
		return out, err
	}
	return out, nil
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
