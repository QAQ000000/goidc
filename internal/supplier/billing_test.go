package supplier

import (
	"testing"
	"time"
)

func TestBillingPeriodEndClipsCalendarBoundaries(t *testing.T) {
	for _, item := range []struct {
		name, before, cycle, after string
	}{
		{"month end", "2027-01-31T09:10:11+08:00", "monthly", "2027-02-28T09:10:11+08:00"},
		{"leap month", "2028-01-31T09:10:11+08:00", "monthly", "2028-02-29T09:10:11+08:00"},
		{"leap year", "2028-02-29T09:10:11+08:00", "annually", "2029-02-28T09:10:11+08:00"},
		{"quarter", "2027-08-31T09:10:11+08:00", "quarterly", "2027-11-30T09:10:11+08:00"},
		{"Shanghai day", "2027-01-30T16:10:11.123456Z", "monthly", "2027-02-27T16:10:11.123456Z"},
		{"ten years", "2028-02-29T09:10:11+08:00", "tenly", "2038-02-28T09:10:11+08:00"},
	} {
		t.Run(item.name, func(t *testing.T) {
			before, err := time.Parse(time.RFC3339Nano, item.before)
			if err != nil {
				t.Fatal(err)
			}
			expected, err := time.Parse(time.RFC3339Nano, item.after)
			if err != nil {
				t.Fatal(err)
			}
			actual, ok := BillingPeriodEnd(before, item.cycle)
			if !ok || !actual.Equal(expected) {
				t.Fatal("billing period did not preserve Shanghai calendar", actual, ok, expected)
			}
		})
	}
	for _, cycle := range []string{"once", "daily", "invalid"} {
		if _, ok := BillingPeriodEnd(time.Now(), cycle); ok {
			t.Fatal("unsupported renewing period accepted", cycle)
		}
	}
	if _, ok := BillingPeriodEnd(time.Time{}, "monthly"); ok {
		t.Fatal("zero deadline accepted")
	}
}
