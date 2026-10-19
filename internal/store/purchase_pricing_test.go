package store

import (
	"reflect"
	"testing"
)

func TestPurchaseGroupPricingRulesAndCents(t *testing.T) {
	for _, tc := range []struct {
		name                          string
		price, setup                  int64
		rule                          PurchaseGroupSnapshot
		discount, recurring, setupNet int64
	}{
		{"percent includes setup", 101, 51, PurchaseGroupSnapshot{DiscountType: "percent", DiscountPercent: 20}, 30, 81, 41},
		{"fixed recurring first", 100, 50, PurchaseGroupSnapshot{DiscountType: "fixed", FixedDiscountCents: 120}, 120, 0, 30},
		{"fixed clamp", 100, 50, PurchaseGroupSnapshot{DiscountType: "fixed", FixedDiscountCents: 999}, 150, 0, 0},
		{"bigint", maxInt64, 0, PurchaseGroupSnapshot{DiscountType: "percent", DiscountPercent: 100}, maxInt64, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, r, s, err := groupUnitAmounts(tc.price, tc.setup, tc.rule)
			if err != nil || d != tc.discount || r != tc.recurring || s != tc.setupNet {
				t.Fatal(d, r, s, err)
			}
		})
	}
	if _, _, _, err := groupUnitAmounts(maxInt64, 1, PurchaseGroupSnapshot{}); err == nil {
		t.Fatal("overflow accepted")
	}
	rules := groupPricingRules{fallback: PurchaseGroupSnapshot{Source: "customer_group", DiscountType: "percent", DiscountPercent: 50}, rules: map[int64][]PurchaseGroupSnapshot{1: {{Source: "product_group", ProductGroupRuleID: 1, DiscountType: "percent", DiscountPercent: 10}, {Source: "product_group", ProductGroupRuleID: 2, DiscountType: "fixed", FixedDiscountCents: 15}, {Source: "product_group", ProductGroupRuleID: 3, DiscountType: "fixed", FixedDiscountCents: 15}}}}
	v := checkoutLine{ID: 7, ProductID: 1, Qty: 3, Cycle: "monthly", UnitPrice: 100, UnitSetup: 50, DefaultRecurring: 100}
	p, _, err := rules.price(v)
	if err != nil || p.GroupSnapshot.ProductGroupRuleID != 1 || p.GroupDiscountCents != 45 || p.NetCents != 405 || p.RecurringCents != 90 {
		t.Fatal(p, err)
	}
	v.ProductID = 2
	p, _, err = rules.price(v)
	if err != nil || p.GroupDiscountCents != 225 {
		t.Fatal(p, err)
	}
	override := int64(33)
	v.FirstPriceOverride = &override
	v.UnitPrice = 33
	v.UnitSetup = 0
	p, _, err = rules.price(v)
	if err != nil || p.GroupDiscountCents != 0 || p.GroupSnapshot.Source != "override" || p.RecurringCents != 100 {
		t.Fatal(p, err)
	}
}
func TestPurchaseDiscountAllocationConservesCents(t *testing.T) {
	for _, tc := range []struct {
		amount        int64
		weights, want []int64
	}{
		{1, []int64{1, 1, 1}, []int64{0, 0, 1}},
		{2, []int64{0, 2, 1}, []int64{0, 1, 1}},
		{maxInt64 - 1, []int64{maxInt64 - 1, 1}, []int64{maxInt64 - 2, 1}},
	} {
		got := allocatePurchaseCents(tc.amount, tc.weights)
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatal(got, tc.want)
		}
	}
}
