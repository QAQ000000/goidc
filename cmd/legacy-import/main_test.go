package main

import (
	"testing"

	"github.com/codex/zjmfgo/internal/legacy"
)

func TestPriceProductIDUsesRelID(t *testing.T) {
	r := legacy.Record{"id": "14696", "relid": "167", "currency": "1"}
	if got := priceProductID(r); got != 167 {
		t.Fatalf("price product id=%d, want 167", got)
	}
	if got := priceProductID(legacy.Record{"id": "14696"}); got != 0 {
		t.Fatalf("missing relid product id=%d, want 0", got)
	}
}

func TestChoosePricePrefersMonthlyThenFallsBack(t *testing.T) {
	cycle, value := choosePrice(legacy.Record{"monthly": "40.00", "quarterly": "120.00", "annually": "480.00"})
	if cycle != "monthly" || value != 4000 {
		t.Fatalf("monthly price=%s/%d", cycle, value)
	}
	cycle, value = choosePrice(legacy.Record{"monthly": "-1.00", "quarterly": "120.00"})
	if cycle != "quarterly" || value != 12000 {
		t.Fatalf("quarterly fallback=%s/%d", cycle, value)
	}
}
