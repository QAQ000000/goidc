package supplier

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func catalogFixture(t *testing.T, data string) Adapter {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"fixture"}`)
			return
		}
		fmt.Fprintf(w, `{"status":200,"data":%s}`, data)
	}))
	t.Cleanup(server.Close)
	a, err := New(Account{Provider: ProviderZJMF, BaseURL: server.URL, Username: "fixture", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestCatalogPreservesGroupsAndNumericStrings(t *testing.T) {
	a := catalogFixture(t, `{"currency":"CNY","products":[{"id":"3","name":"香港","products":[{"id":"7","name":"VM","type":"server","stock_control":"1","qty":"8","api_type":"zjmf_api","upstream_stock_control":"1","upstream_qty":"2"}]},{"id":4,"name":"重复","products":[{"id":7,"name":"Duplicate"}]}]}`)
	items, err := a.Catalog(t.Context())
	if err != nil || len(items) != 1 || items[0].GroupID != 3 || items[0].GroupName != "香港" || items[0].Type != "server" || items[0].Currency != "CNY" || items[0].Stock == nil || *items[0].Stock != 2 {
		t.Fatal(items, err)
	}
	for _, invalid := range []string{`"7e1"`, `7.0`, `"+7"`, `" 7"`, `"-7"`, `true`, `"9223372036854775808"`} {
		t.Run(invalid, func(t *testing.T) {
			bad := catalogFixture(t, `{"products":[{"id":1,"products":[{"id":`+invalid+`,"name":"bad"}]}]}`)
			if _, err := bad.Catalog(t.Context()); err == nil {
				t.Fatal("accepted invalid integer", invalid)
			}
		})
	}
}

func TestProductCNYPercentAndAdditionalSelect(t *testing.T) {
	a := catalogFixture(t, `{"products":{"id":"7","location_version":"3","api_type":"zjmf_api","upstream_pid":"99","upstream_price_type":"percent","upstream_price_value":"125.00","pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"USD","monthly":"2.00"},{"code":"CNY","monthly":"10.01","msetupfee":"2.00"}],"config_groups":[{"options":[{"id":"8","option_name":"带宽","option_type":"10","sub":[{"id":"9","option_name":"20Mbps","pricings":[{"code":"USD","monthly":"0.50"},{"code":"CNY","monthly":"1.00","msetupfee":"0.20"}]}]}]}],"customfields":[],"advanced": { }}`)
	p, err := a.Product(t.Context(), 7)
	if err != nil || !p.Supported || p.PriceMultiplier != 12500 || p.Currency != "CNY" || p.Prices["monthly"].AmountCents != 1251 || p.Prices["monthly"].SetupCents != 250 || p.Prices["monthly"].RawAmountCents == nil || *p.Prices["monthly"].RawAmountCents != 1001 || p.Prices["monthly"].RawSetupCents == nil || *p.Prices["monthly"].RawSetupCents != 200 || len(p.Options) != 1 || p.Options[0].SourceType != 10 {
		t.Fatal(p, err)
	}
	price, err := SelectionPrice(p, "monthly", map[int64]string{8: "9"}, nil)
	if err != nil || price != (Price{AmountCents: 1377, SetupCents: 275}) {
		t.Fatal(price, err)
	}
	changed := strings.Replace(`{"products":{"id":7,"location_version":3,"api_type":"zjmf_api","upstream_pid":99,"upstream_price_type":"percent","upstream_price_value":"125.00","pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"0.00"}]}`, "125.00", "130.00", 1)
	p2, err := catalogFixture(t, changed).Product(t.Context(), 7)
	changed = strings.Replace(changed, "130.00", "125.00", 1)
	p1, err1 := catalogFixture(t, changed).Product(t.Context(), 7)
	if err != nil || err1 != nil || p1.Revision == p2.Revision {
		t.Fatal("percent rule missing from revision", p1, p2)
	}
}

func TestSupplierPercentConfigurationUsesAggregatedRawCostCeilings(t *testing.T) {
	pricing := func(amount, setup string) []map[string]any {
		return []map[string]any{{"code": "CNY", "monthly": amount, "msetupfee": setup}}
	}
	detail := map[string]any{
		"products":         map[string]any{"id": 7, "location_version": 1, "api_type": "zjmf_api", "upstream_pid": 99, "upstream_price_type": "percent", "upstream_price_value": "125.00", "pay_type": map[string]any{"pay_type": "recurring"}},
		"product_pricings": pricing("0.03", "0.03"),
		"config_groups": []map[string]any{{"options": []map[string]any{
			{"id": 8, "option_type": 1, "sub": []map[string]any{{"id": 9, "pricings": pricing("0.03", "0.03")}}},
			{"id": 10, "option_type": 14, "qty_minimum": 0, "qty_maximum": 4, "qty_stage": 1, "sub": []map[string]any{{"id": 11, "qty_minimum": 0, "qty_maximum": 4, "pricings": pricing("0.03", "0.03")}}},
			{"id": 12, "option_type": 15, "qty_minimum": 0, "qty_maximum": 4, "qty_stage": 1, "sub": []map[string]any{{"id": 13, "qty_minimum": 1, "qty_maximum": 1, "pricings": pricing("0.03", "0.03")}, {"id": 14, "qty_minimum": 2, "qty_maximum": 4, "pricings": pricing("0.01", "0.01")}}},
		}}},
	}
	encoded, err := json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	p, err := catalogFixture(t, string(encoded)).Product(t.Context(), 7)
	if err != nil || !p.Supported {
		t.Fatal("percent configurable product rejected", p.UnsupportedReason, err)
	}
	for _, tc := range []struct {
		linear, graduated string
		want              Price
	}{{"0", "0", Price{AmountCents: 8, SetupCents: 8}}, {"2", "2", Price{AmountCents: 20, SetupCents: 13}}, {"2", "4", Price{AmountCents: 23, SetupCents: 13}}} {
		actual, err := SelectionPrice(p, "monthly", map[int64]string{8: "9", 10: tc.linear, 12: tc.graduated}, nil)
		if err != nil || actual != tc.want {
			t.Fatal("configured percent maximum did not aggregate raw values before rounding", tc.linear, tc.graduated, actual, tc.want, err)
		}
	}
	tier := p.Options[2].Choices[0].PriceTiers["monthly"][1]
	if tier.RawAmountCents == nil || *tier.RawAmountCents != 1 || tier.RawSetupCents == nil || *tier.RawSetupCents != 1 {
		t.Fatal("graduated raw basis was discarded")
	}
	// Three cents recurring plus three cents setup at 125% permits up to eight
	// cents total, covering both separately rounded invoice components.
	p.Options = nil
	actual, err := SelectionPrice(p, "monthly", nil, nil)
	if err != nil || actual != (Price{AmountCents: 4, SetupCents: 4}) {
		t.Fatal("percent ceiling rounded down", actual, err)
	}
	base := p.Prices["monthly"]
	base.RawAmountCents = nil
	p.Prices["monthly"] = base
	if _, err := SelectionPrice(p, "monthly", nil, nil); err == nil {
		t.Fatal("percent price with missing raw basis accepted")
	}
	detail["product_pricings"] = pricing("0.00", "0.00")
	encoded, err = json.Marshal(detail)
	if err != nil {
		t.Fatal(err)
	}
	p, err = catalogFixture(t, string(encoded)).Product(t.Context(), 7)
	zero, err := json.Marshal(p.Prices["monthly"])
	if err != nil || !strings.Contains(string(zero), `"raw_amount_cents":0`) || !strings.Contains(string(zero), `"raw_setup_cents":0`) {
		t.Fatal("zero raw basis omitted from percent projection", err)
	}
}

func TestSupplierLinearQuantityTypesAndSetup(t *testing.T) {
	for _, sourceType := range []int{4, 7, 9, 11, 14} {
		t.Run(fmt.Sprint(sourceType), func(t *testing.T) {
			data := fmt.Sprintf(`{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00"}],"config_groups":[{"options":[{"id":8,"option_name":"数量","option_type":%d,"qty_minimum":0,"qty_maximum":100,"qty_stage":1,"sub":[{"id":9,"qty_minimum":0,"qty_maximum":100,"pricings":[{"code":"CNY","monthly":"0.25","msetupfee":"2.00"}]}]}]}]}`, sourceType)
			p, err := catalogFixture(t, data).Product(t.Context(), 7)
			if err != nil || !p.Supported || p.Options[0].Type != "quantity" {
				t.Fatal(p, err)
			}
			for _, tc := range []struct {
				qty      string
				expected Price
			}{{"0", Price{AmountCents: 1000}}, {"3", Price{AmountCents: 1075, SetupCents: 200}}, {"100", Price{AmountCents: 3500, SetupCents: 200}}} {
				price, err := SelectionPrice(p, "monthly", map[int64]string{8: tc.qty}, nil)
				if err != nil || price != tc.expected {
					t.Fatal(tc.qty, price, err)
				}
			}
			for _, qty := range []string{"101", "-1", "1.0", "+2", " 2"} {
				if _, err := SelectionPrice(p, "monthly", map[int64]string{8: qty}, nil); err == nil {
					t.Fatal("invalid quantity accepted", qty)
				}
			}
		})
	}
}

func TestSupplierGraduatedQuantityAndInvalidTiers(t *testing.T) {
	data := `{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00"}],"config_groups":[{"options":[{"id":8,"option_name":"阶梯","option_type":15,"qty_minimum":0,"qty_maximum":20,"qty_stage":1,"sub":[{"id":9,"qty_minimum":0,"qty_maximum":10,"pricings":[{"code":"CNY","monthly":"1.00","msetupfee":"2.00"}]},{"id":10,"qty_minimum":11,"qty_maximum":20,"pricings":[{"code":"CNY","monthly":"0.50","msetupfee":"3.00"}]}]}]}]}`
	p, err := catalogFixture(t, data).Product(t.Context(), 7)
	if err != nil || !p.Supported || len(p.Options[0].Choices) != 1 || p.Options[0].QuantityMode != "graduated" {
		t.Fatal(p, err)
	}
	for _, tc := range []struct {
		qty      string
		expected Price
	}{{"0", Price{AmountCents: 1000}}, {"3", Price{AmountCents: 1300, SetupCents: 200}}, {"12", Price{AmountCents: 2100, SetupCents: 300}}, {"20", Price{AmountCents: 2500, SetupCents: 300}}} {
		price, err := SelectionPrice(p, "monthly", map[int64]string{8: tc.qty}, nil)
		if err != nil || price != tc.expected {
			t.Fatal(tc.qty, price, err)
		}
	}
	for _, replacement := range []string{`"qty_minimum":10`, `"qty_minimum":12`} {
		bad, err := catalogFixture(t, strings.Replace(data, `"qty_minimum":11`, replacement, 1)).Product(t.Context(), 7)
		if err != nil || bad.Supported || bad.UnsupportedReason != "quantity_tiers_invalid" {
			t.Fatal(bad, err)
		}
	}
}

func TestSupplierFieldsAndSafePattern(t *testing.T) {
	data := `{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00"}],"customfields":[{"id":"1","fieldname":"说明","fieldtype":"textarea","required":"1"},{"id":2,"fieldname":"链接","fieldtype":"link"},{"id":3,"fieldname":"密钥","fieldtype":"password"},{"id":4,"fieldname":"协议","fieldtype":"tickbox","required":1},{"id":5,"fieldname":"编号","fieldtype":"text","regexpr":"/^ID[0-9]+$/"},{"id":6,"fieldname":"区域","fieldtype":"dropdown","fieldoptions":"CN, US"}]}`
	p, err := catalogFixture(t, data).Product(t.Context(), 7)
	if err != nil || !p.Supported || p.Fields[4].Pattern != `^ID[0-9]+$` {
		t.Fatal(p, err)
	}
	fields := map[int64]string{1: "多行\n说明", 2: "https://example.test/", 3: "sensitive-value", 4: "true", 5: "ID123", 6: "CN"}
	if _, err := SelectionPrice(p, "monthly", nil, fields); err != nil {
		t.Fatal(err)
	}
	for id, value := range map[int64]string{2: "javascript:alert(1)", 4: "false", 5: "xID123", 6: "UK"} {
		original := fields[id]
		fields[id] = value
		if _, err := SelectionPrice(p, "monthly", nil, fields); err == nil {
			t.Fatal("invalid field accepted", id)
		}
		fields[id] = original
	}
	for _, pattern := range []string{`/ID[0-9]+/`, `/^ID(?=123)[0-9]+$/`, `/^ID[0-9]+$/i`} {
		encoded, _ := json.Marshal(pattern)
		badData := strings.Replace(data, `"/^ID[0-9]+$/"`, string(encoded), 1)
		bad, err := catalogFixture(t, badData).Product(t.Context(), 7)
		if err != nil || bad.Supported || bad.UnsupportedReason != "unsupported_field_regex" {
			t.Fatal(pattern, bad, err)
		}
	}
}

func TestSelectionPreviewAllowsZeroStockButPurchaseRejects(t *testing.T) {
	stock := int64(0)
	p := Product{Supported: true, Currency: "CNY", Stock: &stock, Prices: map[string]Price{"monthly": {AmountCents: 100}}}
	if price, err := SelectionPrice(p, "monthly", nil, nil); err != nil || price.AmountCents != 100 {
		t.Fatal(price, err)
	}
	if _, err := ValidateSelection(p, "monthly", nil, nil); err == nil {
		t.Fatal("purchased zero stock")
	}
}

func TestSupplierRejectsLinkageAndUnsupportedPeriods(t *testing.T) {
	for _, tc := range []struct{ extra, pricing, reason string }{
		{`"advanced":{"1":[1]}`, `"monthly":"1.00"`, "advanced_configuration"},
		{`"config_groups":[{"options":[{"id":8,"option_type":1,"linkage_pid":9,"sub":[{"id":10}]}]}]`, `"monthly":"1.00"`, "option_linkage_unsupported"},
		{`"advanced":[]`, `"day":"1.00","semiannually":"2.00"`, "unsupported_cycle"},
		{`"advanced":[]`, `"monthly":"1.00","semiannually":"2.00"`, "unsupported_cycle"},
	} {
		data := fmt.Sprintf(`{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY",%s}],%s}`, tc.pricing, tc.extra)
		p, err := catalogFixture(t, data).Product(t.Context(), 7)
		if err != nil || p.Supported || p.UnsupportedReason != tc.reason {
			t.Fatal(p, err)
		}
	}
}

func TestSupplierRetainsOnlyCompleteBillingCycles(t *testing.T) {
	a := catalogFixture(t, `{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00","annually":"100.00"}],"config_groups":[{"options":[{"id":8,"option_type":1,"sub":[{"id":9,"option_name":"A","pricings":[{"code":"CNY","monthly":"1.00","annually":"10.00"}]},{"id":10,"option_name":"B","pricings":[{"code":"CNY","monthly":"2.00","annually":"-1.00"}]}]}]}]}`)
	p, err := a.Product(t.Context(), 7)
	if err != nil || !p.Supported || len(p.Prices) != 1 || p.Prices["monthly"].AmountCents != 1000 {
		t.Fatal(p, err)
	}
	if _, err := SelectionPrice(p, "monthly", map[int64]string{8: "10"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := SelectionPrice(p, "annually", map[int64]string{8: "10"}, nil); err == nil {
		t.Fatal("accepted incomplete annual configuration")
	}
}

func TestSupplierSimpleRevisionRemainsCompatible(t *testing.T) {
	a := catalogFixture(t, `{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00"}],"config_groups":[],"customfields":[],"advanced":[]}`)
	p, err := a.Product(t.Context(), 7)
	if err != nil || !p.Supported {
		t.Fatal(p, err)
	}
	// This is the pre-extension revision representation for a simple product.
	legacy := `{"ID":7,"SupplierVersion":1,"Currency":"CNY","Prices":{"monthly":{"amount_cents":1000,"setup_cents":0}},"Options":[],"Fields":[]}`
	digest := sha256.Sum256([]byte(legacy))
	if p.Revision != hex.EncodeToString(digest[:]) {
		t.Fatal("simple-product revision changed", p.Revision)
	}
}

func TestSupplierQuantityIsSubmittedAsQuantity(t *testing.T) {
	seen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"fixture"}`)
		case "/cart/get_product_config":
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"10.00"}],"config_groups":[{"options":[{"id":8,"option_type":11,"qty_minimum":3,"qty_maximum":3,"qty_stage":1,"sub":[{"id":999,"pricings":[{"code":"CNY","monthly":"1.00","msetupfee":"2.00"}]}]}]}]}}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
		case "/cart/clear":
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/add_to_shop":
			seen = true
			if value := r.PostFormValue("configoption[8]"); value != "3" {
				t.Errorf("quantity=%q, want 3", value)
			}
			fmt.Fprint(w, `{"status":400}`)
		default:
			t.Error("unexpected write after rejected cart", r.URL.Path)
		}
	}))
	t.Cleanup(server.Close)
	a, err := New(Account{Provider: ProviderZJMF, BaseURL: server.URL, Username: "fixture", Password: "fixture"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := a.Product(t.Context(), 7)
	if err != nil || !p.Supported {
		t.Fatal(p, err)
	}
	request := createRequest(p)
	request.ConfigOptions = map[int64]string{8: "3"}
	if _, err := a.Create(t.Context(), request); err == nil || !seen {
		t.Fatal("cart request not checked", err, seen)
	}
}
