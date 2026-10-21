package supplier

import "time"

// BillingCycles is ordered by duration, followed by the non-renewing cycle.
// Hour/day lengths and trial eligibility require a separate contract.
func BillingCycles() []string {
	result := make([]string, 0, len(supplierCycles))
	for _, c := range supplierCycles {
		result = append(result, c.cycle)
	}
	return result
}

func BillingMonths(cycle string) int {
	return map[string]int{"monthly": 1, "quarterly": 3, "semiannually": 6, "annually": 12, "biennially": 24, "triennially": 36, "fourly": 48, "fively": 60, "sixly": 72, "sevenly": 84, "eightly": 96, "ninely": 108, "tenly": 120}[cycle]
}

func IsBillingCycle(cycle string) bool { return cycle == "once" || BillingMonths(cycle) > 0 }

// BillingPeriodEnd uses the supplier's Shanghai calendar and clips to the last
// day of a shorter month, matching PostgreSQL timestamp + calendar interval.
// Go's AddDate normalizes January 31 + one month into March instead.
func BillingPeriodEnd(before time.Time, cycle string) (time.Time, bool) {
	months := BillingMonths(cycle)
	if months == 0 || before.IsZero() {
		return time.Time{}, false
	}
	local := before.In(time.FixedZone("Asia/Shanghai", 8*3600))
	target := time.Date(local.Year(), local.Month()+time.Month(months), 1, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), local.Location())
	if target.Year() < 1 || target.Year() > 9999 {
		return time.Time{}, false
	}
	last := time.Date(target.Year(), target.Month()+1, 0, 0, 0, 0, 0, local.Location()).Day()
	day := min(local.Day(), last)
	return time.Date(target.Year(), target.Month(), day, local.Hour(), local.Minute(), local.Second(), local.Nanosecond(), local.Location()).UTC(), true
}
