package store

import (
	"math"
	"testing"
)

func TestRenewalBenefitQuoteLimitFixedAndOverflow(t *testing.T) {
	for _, tc := range []struct {
		base              int64
		b                 RenewalBenefit
		discount, ordinal int64
	}{
		{1234, RenewalBenefit{FixedCents: 500, Limit: 2, Code: "FIX"}, 500, 1},
		{1234, RenewalBenefit{FixedCents: 500, Limit: 2, Used: 1, Code: "FIX"}, 500, 2},
		{1234, RenewalBenefit{FixedCents: 500, Limit: 2, Used: 2, Code: "FIX"}, 0, 0},
		{100, RenewalBenefit{FixedCents: 500, Code: "FIX"}, 100, 1},
		{0, RenewalBenefit{FixedCents: 500, Code: "FIX"}, 0, 1},
		{math.MaxInt64, RenewalBenefit{FixedCents: math.MaxInt64, Code: "FIX"}, math.MaxInt64, 1},
		{1234, RenewalBenefit{Percent: 10, Limit: 1, Code: "PCT"}, 123, 1},
		{1234, RenewalBenefit{Percent: 10, Limit: 1, Used: 1, Code: "PCT"}, 0, 0},
		{1, RenewalBenefit{Percent: 10, Code: "PCT"}, 0, 1},
		{1234, RenewalBenefit{}, 0, 0},
	} {
		discount, snapshot, ordinal, err := tc.b.Quote(tc.base)
		if err != nil || discount != tc.discount || ordinal != tc.ordinal || ordinal == 0 && snapshot.Code != "" {
			t.Fatalf("%+v => %d/%d %+v %v", tc, discount, ordinal, snapshot, err)
		}
	}
	for _, b := range []RenewalBenefit{
		{Percent: 101, Code: "BAD"}, {FixedCents: -1}, {Percent: 10, FixedCents: 1, Code: "BAD"},
		{Percent: 10}, {FixedCents: 1, Code: "BAD", Limit: 10001}, {Percent: 10, Code: "BAD", Used: -1},
		{Percent: 10, Code: "BAD", Limit: 1, Used: 2}, {Percent: 10, Code: "BAD", Used: math.MaxInt64},
	} {
		if _, _, _, err := b.Quote(1234); err == nil {
			t.Fatalf("invalid benefit accepted: %+v", b)
		}
	}
}
