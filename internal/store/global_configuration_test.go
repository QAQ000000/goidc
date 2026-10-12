package store

import "testing"

func TestGlobalConfigurationRejectsUnsafeDefinitions(t *testing.T) {
	active := true
	valid := func() GlobalConfigurationInput {
		return GlobalConfigurationInput{Name: "Shared", Definitions: []GlobalDefinition{{ProductOptionGroupInput: ProductOptionGroupInput{Name: "Memory", Slug: "memory", Active: &active, OptionType: 4, QtyMinimum: 1, QtyMaximum: 10}, Items: []GlobalOption{{ProductOptionInput: ProductOptionInput{Label: "GB", Value: "gb", Active: &active, PriceTierChanges: map[string][]OptionPriceTier{"monthly": {{Minimum: 1, Maximum: 10, PriceCents: 100}}}}}}}}}
	}
	if err := ValidateGlobalConfiguration(func() *GlobalConfigurationInput { v := valid(); return &v }()); err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*GlobalConfigurationInput){
		"hierarchy":      func(v *GlobalConfigurationInput) { v.Definitions[0].OptionType = 20 },
		"duplicate slug": func(v *GlobalConfigurationInput) { v.Definitions = append(v.Definitions, v.Definitions[0]) },
		"overlapping tiers": func(v *GlobalConfigurationInput) {
			v.Definitions[0].Items[0].PriceTierChanges["monthly"] = []OptionPriceTier{{Minimum: 1, Maximum: 5}, {Minimum: 5, Maximum: 8}}
		},
		"nonquantity tiers": func(v *GlobalConfigurationInput) { v.Definitions[0].OptionType = 1 },
		"required empty":    func(v *GlobalConfigurationInput) { v.Definitions[0].Required = true; v.Definitions[0].Items = nil },
		"duplicate cycle": func(v *GlobalConfigurationInput) {
			v.Definitions[0].Items[0].Prices = []GlobalOptionPrice{{BillingCycle: "monthly"}, {BillingCycle: "monthly"}}
		},
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			v := valid()
			change(&v)
			if ValidateGlobalConfiguration(&v) == nil {
				t.Fatal("accepted unsafe definition")
			}
		})
	}
}
