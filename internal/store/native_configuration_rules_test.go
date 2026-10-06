package store

import "testing"

func nativeInt(v int64) *int64 { return &v }

func TestNativePredicateSemantics(t *testing.T) {
	selected := map[int64]OptionSnapshot{1: {ID: 11, GroupID: 1, Quantity: 4}}
	cases := []struct {
		name      string
		predicate NativeRulePredicate
		want      bool
	}{
		{"equal", NativeRulePredicate{GroupID: 1, OptionID: nativeInt(11), Operator: "equals"}, true},
		{"unequal choice", NativeRulePredicate{GroupID: 1, OptionID: nativeInt(12), Operator: "not_equals"}, true},
		{"unequal absent", NativeRulePredicate{GroupID: 2, OptionID: nativeInt(12), Operator: "not_equals"}, false},
		{"absent", NativeRulePredicate{GroupID: 2, Operator: "absent"}, true},
		{"range edge", NativeRulePredicate{GroupID: 1, OptionID: nativeInt(11), Operator: "quantity_in", QtyMinimum: nativeInt(4), QtyMaximum: nativeInt(8)}, true},
		{"outside edge", NativeRulePredicate{GroupID: 1, OptionID: nativeInt(11), Operator: "quantity_out", QtyMinimum: nativeInt(4), QtyMaximum: nativeInt(8)}, false},
		{"outside", NativeRulePredicate{GroupID: 1, OptionID: nativeInt(11), Operator: "quantity_out", QtyMinimum: nativeInt(5), QtyMaximum: nativeInt(8)}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nativePredicateMatches(tc.predicate, selected); got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestNativeRuleInputBounds(t *testing.T) {
	valid := NativeConfigurationRuleInput{Name: "OS needs backup", Enabled: nativeBool(true), Conditions: []NativeRulePredicate{{GroupID: 1, OptionID: nativeInt(2), Operator: "equals"}}, Results: []NativeRulePredicate{{GroupID: 3, Operator: "absent"}}}
	if err := validateNativeRuleInput(&valid); err != nil {
		t.Fatal(err)
	}
	for _, predicate := range []NativeRulePredicate{
		{GroupID: 0, Operator: "absent"},
		{GroupID: 1, OptionID: nativeInt(2), Operator: "absent"},
		{GroupID: 1, OptionID: nativeInt(2), Operator: "quantity_in", QtyMinimum: nativeInt(0), QtyMaximum: nativeInt(3)},
		{GroupID: 1, OptionID: nativeInt(2), Operator: "quantity_out", QtyMinimum: nativeInt(2), QtyMaximum: nativeInt(1000001)},
		{GroupID: 1, OptionID: nativeInt(2), Operator: "unsupported"},
	} {
		in := valid
		in.Results = []NativeRulePredicate{predicate}
		if err := validateNativeRuleInput(&in); err == nil {
			t.Fatalf("accepted %+v", predicate)
		}
	}
}

func nativeBool(v bool) *bool { return &v }
