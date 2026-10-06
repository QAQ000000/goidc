package store

import "math"

type RenewalBenefit struct {
	Percent    int64
	FixedCents int64
	Limit      int64
	Used       int64
	Code       string
}

// Quote returns only the terms actually applied to this renewal. Ordinal zero
// means no benefit; payment consumes an ordinal atomically with due extension.
func (b RenewalBenefit) Quote(base int64) (discount int64, snapshot RenewalBenefit, ordinal int64, err error) {
	if base < 0 || b.Percent < 0 || b.Percent > 100 || b.FixedCents < 0 || b.Limit < 0 || b.Limit > 10000 || b.Used < 0 || b.Percent > 0 && b.FixedCents > 0 || (b.Percent > 0 || b.FixedCents > 0) != (b.Code != "") || b.Limit > 0 && b.Used > b.Limit {
		return 0, snapshot, 0, conflict("invalid_renewal_benefit", "续费权益无效")
	}
	if b.Code == "" || b.Limit > 0 && b.Used >= b.Limit {
		return 0, snapshot, 0, nil
	}
	if b.Used == math.MaxInt64 {
		return 0, snapshot, 0, conflict("invalid_renewal_benefit", "续费权益计数超出范围")
	}
	if b.FixedCents > 0 {
		discount = min(base, b.FixedCents)
	} else {
		discount, err = PercentDiscount(base, b.Percent)
	}
	return discount, b, b.Used + 1, err
}
