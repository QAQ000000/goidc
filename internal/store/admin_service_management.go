package store

import (
	"context"
	"time"
)

// AdminServiceManagement is the current management projection. Mutation
// receipts remain historical facts and must not stand in for this read.
type AdminServiceManagement struct {
	ServiceID             int64      `json:"service_id"`
	CustomerID            int64      `json:"customer_id"`
	ServiceVersion        int64      `json:"service_version"`
	Status                string     `json:"status"`
	BillingCycle          string     `json:"billing_cycle"`
	PricingMode           string     `json:"pricing_mode"`
	FixedAmountCents      *int64     `json:"fixed_amount_cents"`
	ContractBaseCents     int64      `json:"contract_base_cents"`
	ContractDiscountCents int64      `json:"contract_discount_cents"`
	ContractAmountCents   int64      `json:"contract_amount_cents"`
	AmountCents           int64      `json:"amount_cents"`
	NextDueAt             *time.Time `json:"next_due_at"`
	TermMode              string     `json:"term_mode"`
	SupplierNextDueAt     *time.Time `json:"supplier_next_due_at"`
	SupplierObservedAt    *time.Time `json:"supplier_observed_at"`
}

func (s *Store) AdminServiceManagement(ctx context.Context, actor, service int64) (AdminServiceManagement, error) {
	var out AdminServiceManagement
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var owner int64
	if err = tx.QueryRow(ctx, "SELECT user_id FROM services WHERE id=$1", service).Scan(&owner); err != nil {
		return out, err
	}
	if err = lockAdminRenewalAuthority(ctx, tx, actor, owner, "services.view"); err != nil {
		return out, err
	}
	var benefit RenewalBenefit
	err = tx.QueryRow(ctx, `SELECT s.id,s.user_id,s.version,s.status,s.billing_cycle,
 coalesce(s.recurring_price_cents,oi.price_cents,p.price_cents),s.renewal_discount_percent,s.renewal_fixed_discount_cents,s.renewal_discount_limit,s.renewal_discounts_used,s.renewal_promo_code,
 rp.fixed_amount_cents,s.next_due_at,coalesce(t.mode,'follow_supplier'),t.supplier_next_due_at,t.supplier_observed_at
 FROM services s JOIN products p ON p.id=s.product_id LEFT JOIN order_items oi ON oi.id=s.order_item_id
 LEFT JOIN service_renewal_pricing rp ON rp.service_id=s.id LEFT JOIN service_term_management t ON t.service_id=s.id
 WHERE s.id=$1 FOR SHARE OF s`, service).Scan(&out.ServiceID, &out.CustomerID, &out.ServiceVersion, &out.Status, &out.BillingCycle,
		&out.ContractBaseCents, &benefit.Percent, &benefit.FixedCents, &benefit.Limit, &benefit.Used, &benefit.Code,
		&out.FixedAmountCents, &out.NextDueAt, &out.TermMode, &out.SupplierNextDueAt, &out.SupplierObservedAt)
	if err != nil {
		return out, err
	}
	if out.CustomerID != owner {
		return out, conflict("service_owner_changed", "服务客户已变更，请刷新后重试")
	}
	out.ContractDiscountCents, _, _, err = benefit.Quote(out.ContractBaseCents)
	if err != nil {
		return out, err
	}
	out.ContractAmountCents = out.ContractBaseCents - out.ContractDiscountCents
	out.PricingMode, out.AmountCents = "contract", out.ContractAmountCents
	if out.FixedAmountCents != nil {
		out.PricingMode, out.AmountCents = "fixed", *out.FixedAmountCents
	}
	return out, commitAdminRenewal(ctx, tx, actor)
}
