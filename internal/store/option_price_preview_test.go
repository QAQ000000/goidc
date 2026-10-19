package store

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestOptionPricePreviewMatchesGo(t *testing.T) {
	node, err := exec.LookPath("node")
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	_, dependencyErr := os.Stat(filepath.Join(root, "web/node_modules/typescript"))
	if err != nil || dependencyErr != nil {
		if os.Getenv("ZJMFGO_REQUIRE_WEB_TESTS") == "1" {
			t.Fatal("option preview parity requires node and web TypeScript dependencies")
		}
		t.Skip("option preview parity requires node and web TypeScript dependencies")
	}
	type previewTier struct {
		OptionPriceTier
		Cycle  string `json:"billing_cycle"`
		Active bool   `json:"active"`
	}
	type previewPrice struct {
		Cycle  string `json:"billing_cycle"`
		Price  int64  `json:"price_cents"`
		Setup  int64  `json:"setup_fee_cents"`
		Active bool   `json:"active"`
	}
	type previewOption struct {
		Price     int64          `json:"price_delta_cents"`
		Setup     int64          `json:"setup_delta_cents"`
		Tiers     []previewTier  `json:"price_tiers"`
		Pricing   []previewPrice `json:"pricing"`
		AllowZero bool           `json:"allow_zero_quantity"`
	}
	type previewCase struct {
		Name     string           `json:"name"`
		Option   previewOption    `json:"option"`
		Cycle    string           `json:"cycle"`
		Quantity int64            `json:"quantity"`
		Expected map[string]int64 `json:"expected"`
	}
	tier := func(minimum, maximum, price, setup int64) previewTier {
		return previewTier{OptionPriceTier{minimum, maximum, price, setup}, "monthly", true}
	}
	base := previewOption{Price: 25, Setup: 3, Pricing: []previewPrice{{"monthly", 50, 5, true}, {"annually", 400, 40, false}}}
	var cases []previewCase
	add := func(name, cycle string, option previewOption, quantity int64) {
		var active []OptionPriceTier
		for _, row := range option.Tiers {
			if row.Active && row.Cycle == cycle {
				active = append(active, row.OptionPriceTier)
			}
		}
		price, setup := option.Price, option.Setup
		for _, row := range option.Pricing {
			if row.Active && row.Cycle == cycle {
				price, setup = row.Price, row.Setup
				break
			}
		}
		price, setup, ok := PriceForQuantity(active, quantity, price, setup)
		if option.AllowZero && quantity == 0 {
			price, setup, ok = 0, 0, true
		}
		var expected map[string]int64
		if ok {
			expected = map[string]int64{"price": price, "setup": setup}
		}
		cases = append(cases, previewCase{name, option, cycle, quantity, expected})
	}
	add("active matrix with quantity", "monthly", base, 4)
	add("inactive matrix falls back", "annually", base, 4)
	add("missing matrix falls back", "once", base, 4)
	for _, tiers := range [][]previewTier{
		{tier(4, 6, 80, 11), tier(1, 3, 125, 7)},
		{tier(1, 2, 125, 7), tier(5, 6, 80, 11)},
		{tier(2, 4, 125, 7)},
		{tier(1, 3, 0, 0), tier(4, 6, 80, 11)},
		{tier(1, 3, 125, 7), tier(3, 6, 80, 11)},
		{tier(1, 3, -1, 7)},
		{tier(3, 1, 125, 7)},
	} {
		option := base
		option.Tiers = tiers
		for quantity := int64(0); quantity <= 7; quantity++ {
			add("tier boundaries and gaps", "monthly", option, quantity)
		}
	}
	option := base
	option.Tiers = []previewTier{tier(1, 6, 125, 7), {OptionPriceTier{1, 6, 950, 12}, "annually", true}, {OptionPriceTier{1, 6, 99, 99}, "monthly", false}}
	add("inactive overlapping tier ignored", "monthly", option, 5)
	add("only selected cycle tiers used", "annually", option, 5)
	add("tier of other cycle does not suppress fallback", "once", option, 5)
	option.Tiers = nil
	add("signed non-tier discount", "once", previewOption{Price: -25, Setup: -3}, 4)
	add("maximum native amount and quantity", "once", previewOption{Price: 100000000, Setup: 100000000}, 1000000)
	option = base
	option.AllowZero = true
	option.Tiers = []previewTier{tier(1, 100, 50, 5)}
	add("imported zero quantity has no periodic or setup cost", "monthly", option, 0)
	add("imported positive quantity charges setup once", "monthly", option, 3)
	option.AllowZero = false
	add("ordinary zero quantity stays invalid", "monthly", option, 0)
	input, err := json.Marshal(cases)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, filepath.Join(root, "scripts/check-option-pricing.cjs"))
	cmd.Stdin = bytes.NewReader(input)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("pricing parity: %v\n%s", err, output)
	}
	t.Log(string(output))
}
