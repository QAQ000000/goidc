package store

import (
	"math"
	"testing"
)

func TestPercentDiscountExactCentsAndOverflow(t *testing.T) {
	for _, v := range []struct{ cents, percent, want int64 }{
		{1234, 10, 123}, {1, 99, 0}, {0, 100, 0}, {1333, 100, 1333},
		{math.MaxInt64, 100, math.MaxInt64}, {math.MaxInt64, 99, 9131138316486228048},
	} {
		got, err := PercentDiscount(v.cents, v.percent)
		if err != nil || got != v.want {
			t.Fatalf("discount(%d,%d)=%d want %d: %v", v.cents, v.percent, got, v.want, err)
		}
	}
	for _, v := range [][2]int64{{-1, 10}, {100, -1}, {100, 101}} {
		if _, err := PercentDiscount(v[0], v[1]); err == nil {
			t.Fatal("invalid amount/percent accepted")
		}
	}
}
