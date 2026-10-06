package store

import "testing"

func TestLegacyQuantityRangeParsing(t *testing.T) {
	for _, raw := range []string{
		`{"101":{"qty_minimum":1.5,"qty_maximum":3}}`,
		`{"101":{"qty_minimum":"wrong","qty_maximum":3}}`,
		`{"101":{"qty_minimum":-1,"qty_maximum":3}}`,
		`{"101":{"qty_minimum":4,"qty_maximum":3}}`,
		`{"101":{"qty_minimum":null,"qty_maximum":3}}`,
		`{"101":{"qty_minimum":1,"qty_maximum":9223372036854775808}}`,
		`{"01":{"qty_minimum":1,"qty_maximum":3}}`,
		`{"101":[]}`,
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseLegacySubIDs(raw); err == nil {
				t.Fatal("malformed legacy range accepted")
			}
		})
	}
	ranges, err := parseLegacySubIDs(`{"101":{"qty_minimum":"9007199254740993","qty_maximum":9223372036854775807}}`)
	if err != nil || ranges[101].min != 9007199254740993 || ranges[101].max != maxInt64 {
		t.Fatal("legacy integer precision lost", ranges, err)
	}
}

func TestLegacyQuantityPredicatesUseClosedRangeUnion(t *testing.T) {
	link := legacyOptionLink{relation: "seq", ranges: map[int64]legacySubRange{
		101: {min: 3, max: 5, bounded: true},
		102: {min: 8, max: 9, bounded: true},
	}}
	for _, quantity := range []int64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10} {
		group := &legacyGroupSelection{selected: true, quantityType: true, quantity: quantity}
		inside := quantity >= 3 && quantity <= 5 || quantity >= 8 && quantity <= 9
		link.relation = "seq"
		if legacyLinkMatches(link, group) != inside {
			t.Fatalf("inclusive union mismatch at %d", quantity)
		}
		link.relation = "sneq"
		if legacyLinkMatches(link, group) == inside {
			t.Fatalf("range complement mismatch at %d", quantity)
		}
	}
	group := &legacyGroupSelection{quantityType: true}
	link.relation = "seq"
	if legacyLinkMatches(link, group) {
		t.Fatal("absent quantity matched range")
	}
	link.relation = "sneq"
	if !legacyLinkMatches(link, group) {
		t.Fatal("absent quantity should be outside range")
	}
}

func TestLegacyGroupHierarchyRequiresSelectedParentAndRejectsCycles(t *testing.T) {
	hierarchies := map[int64]legacyGroupHierarchy{
		10: {active: true, source: "legacy", legacyID: 10},
		20: {active: true, source: "legacy", legacyID: 20, parentID: 10, parentGroupID: 10},
		30: {active: true, source: "legacy", legacyID: 30, parentID: 20, parentGroupID: 20},
	}
	eligible, err := legacyGroupEligible(30, map[int64]bool{30: true}, hierarchies, map[int64]bool{})
	if err != nil || eligible {
		t.Fatalf("child should require selected parent: %v/%v", eligible, err)
	}
	eligible, err = legacyGroupEligible(30, map[int64]bool{10: true, 20: true, 30: true}, hierarchies, map[int64]bool{})
	if err != nil || !eligible {
		t.Fatalf("selected hierarchy should be eligible: %v/%v", eligible, err)
	}
	hierarchies[10] = legacyGroupHierarchy{active: true, source: "legacy", legacyID: 10, parentID: 30, parentGroupID: 30}
	if _, err = legacyGroupEligible(30, map[int64]bool{10: true, 20: true, 30: true}, hierarchies, map[int64]bool{}); err == nil {
		t.Fatal("hierarchy cycle accepted")
	}
	if err = validateLegacyGroupGraph(hierarchies); err == nil {
		t.Fatal("unselected hierarchy cycle accepted")
	}
}

func TestParseLegacyGroupHierarchy(t *testing.T) {
	legacyID := int64(20)
	hierarchy, err := parseLegacyGroupHierarchy("legacy", &legacyID, []byte(`{"linkage_pid":"10"}`))
	if err != nil || hierarchy.parentID != 10 {
		t.Fatalf("legacy parent not parsed: %#v/%v", hierarchy, err)
	}
	if _, err = parseLegacyGroupHierarchy("legacy", &legacyID, []byte(`{"linkage_pid":1.5}`)); err == nil {
		t.Fatal("fractional legacy parent accepted")
	}
}

func TestPriceForQuantityUsesProgressiveInclusiveTiers(t *testing.T) {
	tiers := []OptionPriceTier{
		{Minimum: 1, Maximum: 3, PriceCents: 100, SetupFeeCents: 7},
		{Minimum: 4, Maximum: 6, PriceCents: 80, SetupFeeCents: 11},
		{Minimum: 7, Maximum: 10, PriceCents: 50, SetupFeeCents: 13},
	}
	for _, tc := range []struct {
		quantity, price, setup int64
	}{
		{1, 100, 7}, {3, 300, 7}, {4, 380, 11}, {6, 540, 11}, {7, 590, 13}, {10, 740, 13},
	} {
		price, setup, ok := PriceForQuantity(tiers, tc.quantity, 0, 0)
		if !ok || price != tc.price || setup != tc.setup {
			t.Fatalf("quantity %d: got %d/%d/%v, want %d/%d", tc.quantity, price, setup, ok, tc.price, tc.setup)
		}
	}
	if _, _, ok := PriceForQuantity(tiers, 11, 0, 0); ok {
		t.Fatal("quantity outside tiers matched")
	}
	if _, _, ok := PriceForQuantity([]OptionPriceTier{{Minimum: 1, Maximum: 3}, {Minimum: 3, Maximum: 4}}, 3, 0, 0); ok {
		t.Fatal("overlapping tiers matched")
	}
	price, setup, ok := PriceForQuantity(nil, 4, 25, 3)
	if !ok || price != 100 || setup != 12 {
		t.Fatalf("fallback linear price mismatch: %d/%d/%v", price, setup, ok)
	}
}
