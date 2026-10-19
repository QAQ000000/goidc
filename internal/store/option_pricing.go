package store

import (
	"context"
	"sort"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

// OptionPriceTier is the normalized representation of one legacy quantity
// pricing interval. Intervals are inclusive.
type OptionPriceTier struct {
	PricingMode   string `json:"pricing_mode,omitempty"`
	Minimum       int64  `json:"qty_minimum"`
	Maximum       int64  `json:"qty_maximum"`
	PriceCents    int64  `json:"price_cents"`
	SetupFeeCents int64  `json:"setup_fee_cents"`
}

type OptionPriceTiersInput struct {
	BillingCycle     string            `json:"billing_cycle"`
	Tiers            []OptionPriceTier `json:"tiers"`
	ExpectedRevision *int64            `json:"expected_revision"`
}

func validOptionPricingCycle(cycle string) bool {
	return cycle == "hourly" || cycle == "daily" || supplier.IsBillingCycle(cycle)
}

func (s *Store) ReplaceOptionPriceTiers(ctx context.Context, actor, product, group, option int64, in OptionPriceTiersInput) error {
	changes, err := normalizeOptionPriceTierChanges(map[string][]OptionPriceTier{in.BillingCycle: in.Tiers})
	if err != nil {
		return err
	}
	tx, err := s.configurationTx(ctx, actor, product, in.ExpectedRevision, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var optionID, optionType int64
	if err = tx.QueryRow(ctx, `SELECT po.id,pog.option_type FROM product_options po JOIN product_option_groups pog ON pog.id=po.group_id WHERE po.id=$1 AND po.group_id=$2 AND pog.product_id=$3 FOR UPDATE`, option, group, product).Scan(&optionID, &optionType); err != nil {
		return err
	}
	if !isQuantityOptionType(optionType) {
		return conflict("invalid_option_pricing", "只有数量型配置支持阶梯价格")
	}
	if err = replaceOptionPriceTierChanges(ctx, tx, optionID, changes); err != nil {
		return err
	}
	return configurationCommit(ctx, tx, actor, product, optionID, "product.option_price_tiers.updated")
}

func normalizeOptionPriceTierChanges(changes map[string][]OptionPriceTier) (map[string][]OptionPriceTier, error) {
	if len(changes) > 16 {
		return nil, conflict("invalid_option_pricing", "阶梯价格账期数量无效")
	}
	normalized := make(map[string][]OptionPriceTier, len(changes))
	for cycle, tiers := range changes {
		if !validOptionPricingCycle(cycle) || len(tiers) > 100 {
			return nil, conflict("invalid_option_pricing", "阶梯价格账期或数量无效")
		}
		ordered := append([]OptionPriceTier(nil), tiers...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].Minimum < ordered[j].Minimum })
		for i, tier := range ordered {
			if tier.PricingMode != "" && tier.PricingMode != "graduated" {
				return nil, conflict("invalid_option_pricing", "原生规格只支持累进阶梯定价")
			}
			if tier.Minimum < 1 || tier.Maximum < tier.Minimum || tier.Maximum > 1000000 || tier.PriceCents < 0 || tier.PriceCents > 100000000 || tier.SetupFeeCents < 0 || tier.SetupFeeCents > 100000000 || (i > 0 && tier.Minimum <= ordered[i-1].Maximum) {
				return nil, conflict("invalid_option_pricing", "阶梯价格区间重叠或金额无效")
			}
		}
		normalized[cycle] = ordered
	}
	return normalized, nil
}

func replaceOptionPriceTierChanges(ctx context.Context, q pgx.Tx, option int64, changes map[string][]OptionPriceTier) error {
	cycles := make([]string, 0, len(changes))
	for cycle := range changes {
		cycles = append(cycles, cycle)
	}
	sort.Strings(cycles)
	for _, cycle := range cycles {
		if _, err := q.Exec(ctx, "DELETE FROM product_option_price_tiers WHERE option_id=$1 AND billing_cycle=$2", option, cycle); err != nil {
			return err
		}
		for _, tier := range changes[cycle] {
			if _, err := q.Exec(ctx, `INSERT INTO product_option_price_tiers(option_id,billing_cycle,qty_minimum,qty_maximum,price_cents,setup_fee_cents) VALUES($1,$2,$3,$4,$5,$6)`, option, cycle, tier.Minimum, tier.Maximum, tier.PriceCents, tier.SetupFeeCents); err != nil {
				return err
			}
		}
	}
	return nil
}

// PriceForQuantity preserves progressive pricing by default. Volume tiers use
// the selected rate for all units. Setup applies once at the selected interval;
// malformed, mixed-mode, overlapping or unmatched tiers are rejected.
func PriceForQuantity(tiers []OptionPriceTier, quantity, fallbackPrice, fallbackSetup int64) (int64, int64, bool) {
	if quantity < 1 {
		return 0, 0, false
	}
	if len(tiers) == 0 {
		price, setup, ok := multiplyOptionAmount(fallbackPrice, fallbackSetup, quantity)
		return price, setup, !ok
	}
	ordered := append([]OptionPriceTier(nil), tiers...)
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].Minimum == ordered[j].Minimum {
			return ordered[i].Maximum < ordered[j].Maximum
		}
		return ordered[i].Minimum < ordered[j].Minimum
	})
	mode := ordered[0].PricingMode
	if mode == "" {
		mode = "graduated"
	}
	if mode != "graduated" && mode != "volume" {
		return 0, 0, false
	}
	var total int64
	matched := false
	var selected OptionPriceTier
	for i, tier := range ordered {
		tierMode := tier.PricingMode
		if tierMode == "" {
			tierMode = "graduated"
		}
		if tierMode != mode {
			return 0, 0, false
		}
		if tier.Minimum < 1 || tier.Maximum < tier.Minimum || tier.PriceCents < 0 || tier.SetupFeeCents < 0 {
			return 0, 0, false
		}
		if i > 0 && tier.Minimum <= ordered[i-1].Maximum {
			return 0, 0, false
		}
		if quantity >= tier.Minimum && quantity <= tier.Maximum {
			matched = true
			selected = tier
		}
	}
	if !matched {
		return 0, 0, false
	}
	if mode == "volume" {
		price, _, overflow := multiplyOptionAmount(selected.PriceCents, 0, quantity)
		return price, selected.SetupFeeCents, !overflow
	}
	for _, tier := range ordered {
		if tier.Minimum >= selected.Minimum {
			break
		}
		units := tier.Maximum - tier.Minimum + 1
		price, _, overflow := multiplyOptionAmount(tier.PriceCents, 0, units)
		if overflow || price > maxInt64-total {
			return 0, 0, false
		}
		total += price
	}
	units := quantity - selected.Minimum + 1
	price, _, overflow := multiplyOptionAmount(selected.PriceCents, 0, units)
	if overflow || price > maxInt64-total {
		return 0, 0, false
	}
	return total + price, selected.SetupFeeCents, true
}
