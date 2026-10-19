package store

import (
	"context"
	"encoding/json"
	"math/big"

	"github.com/jackc/pgx/v5"
)

// The lock covers absent memberships and product-rule rows as well as updates.
// Acquire it before any catalog/product or promo row lock.
func GroupPricingReadLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock_shared(70108206)")
	return err
}
func GroupPricingWriteLock(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(70108206)")
	return err
}
func purchasePricingAvailable(ctx context.Context, tx pgx.Tx) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, "SELECT to_regclass('order_item_pricing') IS NOT NULL").Scan(&ok)
	return ok, err
}

type PurchaseGroupSnapshot struct {
	Source               string `json:"source"`
	CustomerGroupID      int64  `json:"customer_group_id,omitempty"`
	CustomerGroupName    string `json:"customer_group_name,omitempty"`
	CustomerGroupVersion int64  `json:"customer_group_version,omitempty"`
	ProductGroupID       int64  `json:"product_group_id,omitempty"`
	ProductGroupName     string `json:"product_group_name,omitempty"`
	ProductGroupRuleID   int64  `json:"product_group_rule_id,omitempty"`
	ProductGroupVersion  int64  `json:"product_group_version,omitempty"`
	RuleVersion          int64  `json:"rule_version,omitempty"`
	DiscountType         string `json:"discount_type,omitempty"`
	DiscountPercent      int64  `json:"discount_percent,omitempty"`
	FixedDiscountCents   int64  `json:"fixed_discount_cents,omitempty"`
}
type PurchasePricingLine struct {
	ItemID                int64                 `json:"item_id"`
	ProductID             int64                 `json:"product_id"`
	Quantity              int                   `json:"quantity"`
	GrossCents            int64                 `json:"gross_cents"`
	GroupDiscountCents    int64                 `json:"group_discount_cents"`
	PromoDiscountCents    int64                 `json:"promo_discount_cents"`
	NetCents              int64                 `json:"net_cents"`
	RecurringCents        int64                 `json:"recurring_cents"`
	GroupSnapshot         PurchaseGroupSnapshot `json:"group_snapshot"`
	PromoCode             string                `json:"promo_code,omitempty"`
	PromoVersion          int64                 `json:"promo_version,omitempty"`
	PromoCustomerGroupIDs []int64               `json:"promo_customer_group_ids,omitempty"`
}
type groupPricingRules struct {
	enabled  bool
	fallback PurchaseGroupSnapshot
	rules    map[int64][]PurchaseGroupSnapshot
}

func loadGroupPricing(ctx context.Context, tx pgx.Tx, userID int64) (groupPricingRules, error) {
	r := groupPricingRules{fallback: PurchaseGroupSnapshot{Source: "none"}, rules: map[int64][]PurchaseGroupSnapshot{}}
	var err error
	if r.enabled, err = purchasePricingAvailable(ctx, tx); err != nil || !r.enabled {
		return r, err
	}
	err = tx.QueryRow(ctx, `SELECT g.id,g.name,g.version,g.discount_percent FROM customer_group_members m JOIN customer_groups g ON g.id=m.group_id WHERE m.customer_id=$1`, userID).Scan(&r.fallback.CustomerGroupID, &r.fallback.CustomerGroupName, &r.fallback.CustomerGroupVersion, &r.fallback.DiscountPercent)
	if err == pgx.ErrNoRows {
		return r, nil
	}
	if err != nil {
		return r, err
	}
	r.fallback.Source = "customer_group"
	r.fallback.DiscountType = "percent"
	rows, err := tx.Query(ctx, `SELECT gp.product_id,g.id,g.name,g.version,d.id,d.version,d.discount_type,d.discount_percent,d.fixed_discount_cents FROM customer_product_group_products gp JOIN customer_product_groups g ON g.id=gp.group_id JOIN customer_product_group_discounts d ON d.product_group_id=g.id WHERE d.customer_group_id=$1 ORDER BY d.id,gp.product_id`, r.fallback.CustomerGroupID)
	if err != nil {
		return r, err
	}
	defer rows.Close()
	for rows.Next() {
		v := r.fallback
		v.Source = "product_group"
		var product int64
		if err = rows.Scan(&product, &v.ProductGroupID, &v.ProductGroupName, &v.ProductGroupVersion, &v.ProductGroupRuleID, &v.RuleVersion, &v.DiscountType, &v.DiscountPercent, &v.FixedDiscountCents); err != nil {
			return r, err
		}
		r.rules[product] = append(r.rules[product], v)
	}
	return r, rows.Err()
}

// Fixed reductions consume the recurring component first, then setup; percent
// reductions floor once per service and conserve the remaining setup cents.
func groupUnitAmounts(price, setup int64, rule PurchaseGroupSnapshot) (discount, recurring, setupNet int64, err error) {
	if price < 0 || setup < 0 || price > maxInt64-setup {
		return 0, 0, 0, conflict("invalid_product_options", "配置后金额无效")
	}
	total := price + setup
	switch rule.DiscountType {
	case "percent":
		discount, err = PercentDiscount(total, rule.DiscountPercent)
	case "fixed":
		discount = rule.FixedDiscountCents
		if discount < 0 {
			err = conflict("invalid_discount", "分组减免金额无效")
		}
	}
	if err != nil {
		return
	}
	if discount > total {
		discount = total
	}
	recurringDiscount := discount
	if rule.DiscountType == "percent" {
		recurringDiscount, err = PercentDiscount(price, rule.DiscountPercent)
	}
	if recurringDiscount > price {
		recurringDiscount = price
	}
	recurring = price - recurringDiscount
	setupNet = total - discount - recurring
	return
}
func (r groupPricingRules) price(v checkoutLine) (PurchasePricingLine, int64, error) {
	source := r.fallback
	if v.FirstPriceOverride != nil || v.RenewalPriceOverride != nil {
		source.Source = "override"
		source.DiscountType = "none"
		source.DiscountPercent = 0
		source.FixedDiscountCents = 0
	} else if candidates := r.rules[v.ProductID]; len(candidates) > 0 {
		source = candidates[0]
		best := int64(-1)
		for _, candidate := range candidates {
			d, _, _, err := groupUnitAmounts(v.UnitPrice, v.UnitSetup, candidate)
			if err != nil {
				return PurchasePricingLine{}, 0, err
			}
			if d > best {
				best = d
				source = candidate
			}
		}
	}
	discount, recurring, setupNet, err := groupUnitAmounts(v.UnitPrice, v.UnitSetup, source)
	if err != nil {
		return PurchasePricingLine{}, 0, err
	}
	if v.FirstPriceOverride != nil {
		recurring = v.DefaultRecurring
	}
	if v.RenewalPriceOverride != nil {
		recurring = *v.RenewalPriceOverride
	}
	if v.Cycle == "once" {
		recurring = 0
	}
	gross := (v.UnitPrice + v.UnitSetup) * int64(v.Qty)
	return PurchasePricingLine{ItemID: v.ID, ProductID: v.ProductID, Quantity: v.Qty, GrossCents: gross, GroupDiscountCents: discount * int64(v.Qty), NetCents: gross - discount*int64(v.Qty), RecurringCents: recurring, GroupSnapshot: source}, setupNet * int64(v.Qty), nil
}

// Allocate using cumulative weights, not independent rounding. big.Int keeps
// multiplication safe even for values near PostgreSQL bigint's limit.
func allocatePurchaseCents(amount int64, weights []int64) []int64 {
	out := make([]int64, len(weights))
	total := new(big.Int)
	for _, v := range weights {
		total.Add(total, big.NewInt(v))
	}
	if total.Sign() == 0 || amount == 0 {
		return out
	}
	cumulative, previous := new(big.Int), new(big.Int)
	for i, w := range weights {
		cumulative.Add(cumulative, big.NewInt(w))
		current := new(big.Int).Mul(big.NewInt(amount), cumulative)
		current.Quo(current, total)
		out[i] = new(big.Int).Sub(current, previous).Int64()
		previous.Set(current)
	}
	return out
}
func persistPurchasePricing(ctx context.Context, tx pgx.Tx, itemID int64, p PurchasePricingLine) error {
	p.ItemID = itemID
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot) VALUES($1,$2,$3,$4,$5,$6)`, itemID, p.GrossCents, p.GroupDiscountCents, p.PromoDiscountCents, p.NetCents, b)
	return err
}
