package supplier

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
