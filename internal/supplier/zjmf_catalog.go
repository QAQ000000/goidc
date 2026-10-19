package supplier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The legacy API can encode database integers either as JSON numbers or as
// decimal strings. Accept that variation without coercing floats or booleans.
type catalogInt int64

func (n *catalogInt) UnmarshalJSON(raw []byte) error {
	if bytes.Equal(raw, []byte("null")) {
		*n = 0
		return nil
	}
	var value string
	if len(raw) > 0 && raw[0] == '"' {
		if err := json.Unmarshal(raw, &value); err != nil {
			return err
		}
	} else {
		value = string(raw)
	}
	parsed, err := strictQuantity(value)
	if err != nil {
		return fmt.Errorf("invalid catalog integer")
	}
	*n = catalogInt(parsed)
	return nil
}

type catalogProduct struct {
	ID                   catalogInt      `json:"id"`
	Name                 string          `json:"name"`
	Description          string          `json:"description"`
	Type                 string          `json:"type"`
	StockControl         catalogInt      `json:"stock_control"`
	Qty                  catalogInt      `json:"qty"`
	APIType              string          `json:"api_type"`
	UpstreamStockControl catalogInt      `json:"upstream_stock_control"`
	UpstreamQty          catalogInt      `json:"upstream_qty"`
	UpstreamPID          catalogInt      `json:"upstream_pid"`
	UpstreamPriceType    string          `json:"upstream_price_type"`
	UpstreamPriceValue   json.RawMessage `json:"upstream_price_value"`
	PayType              json.RawMessage `json:"pay_type"`
	Hidden               catalogInt      `json:"hidden"`
	Retired              catalogInt      `json:"retired"`
	GroupHidden          catalogInt      `json:"supplier_group_hidden"`
	FirstGroupHidden     catalogInt      `json:"supplier_first_group_hidden"`
	Host                 json.RawMessage `json:"host"`
	Password             json.RawMessage `json:"password"`
	LocationVersion      catalogInt      `json:"location_version"`
}

func catalogStock(raw catalogProduct) *int64 {
	var stock *int64
	if raw.StockControl == 1 {
		qty := int64(raw.Qty)
		stock = &qty
	}
	if raw.APIType == "zjmf_api" && raw.UpstreamStockControl == 1 {
		qty := int64(raw.UpstreamQty)
		if stock == nil || qty < *stock {
			stock = &qty
		}
	}
	return stock
}

func (z *zjmf) Catalog(ctx context.Context) ([]Product, error) {
	w, err := z.get(ctx, "cart/all", nil)
	if err != nil {
		return nil, err
	}
	var data struct {
		Currency string `json:"currency"`
		Products []struct {
			ID       catalogInt       `json:"id"`
			Name     string           `json:"name"`
			Products []catalogProduct `json:"products"`
		} `json:"products"`
	}
	if json.Unmarshal(w.Data, &data) != nil {
		return nil, &Error{Code: "catalog_invalid"}
	}
	result := []Product{}
	seen := map[int64]bool{}
	for _, group := range data.Products {
		for _, raw := range group.Products {
			id := int64(raw.ID)
			if id <= 0 || seen[id] {
				continue
			}
			seen[id] = true
			result = append(result, Product{ID: id, Name: raw.Name, Description: raw.Description, GroupID: int64(group.ID), GroupName: group.Name, Type: raw.Type, Currency: data.Currency, Stock: catalogStock(raw), UnsupportedReason: "detail_required"})
		}
	}
	return result, nil
}

type catalogSub struct {
	ID         catalogInt                   `json:"id"`
	Name       string                       `json:"option_name"`
	QtyMinimum catalogInt                   `json:"qty_minimum"`
	QtyMaximum catalogInt                   `json:"qty_maximum"`
	Hidden     catalogInt                   `json:"hidden"`
	LinkagePID catalogInt                   `json:"linkage_pid"`
	Pricings   []map[string]json.RawMessage `json:"pricings"`
}
type catalogOption struct {
	ID           catalogInt      `json:"id"`
	Name         string          `json:"option_name"`
	Type         catalogInt      `json:"option_type"`
	QtyMinimum   catalogInt      `json:"qty_minimum"`
	QtyMaximum   catalogInt      `json:"qty_maximum"`
	QtyStage     catalogInt      `json:"qty_stage"`
	LinkagePID   catalogInt      `json:"linkage_pid"`
	LinkageTop   catalogInt      `json:"linkage_top_pid"`
	LinkageLevel json.RawMessage `json:"linkage_level"`
	Hidden       catalogInt      `json:"hidden"`
	Sub          []catalogSub    `json:"sub"`
}

var supplierCycles = []struct{ cycle, amount, setup string }{
	{"monthly", "monthly", "msetupfee"}, {"quarterly", "quarterly", "qsetupfee"},
	{"semiannually", "semiannually", "ssetupfee"}, {"annually", "annually", "asetupfee"},
	{"biennially", "biennially", "bsetupfee"}, {"triennially", "triennially", "tsetupfee"},
	{"fourly", "fourly", "foursetupfee"}, {"fively", "fively", "fivesetupfee"},
	{"sixly", "sixly", "sixsetupfee"}, {"sevenly", "sevenly", "sevensetupfee"},
	{"eightly", "eightly", "eightsetupfee"}, {"ninely", "ninely", "ninesetupfee"},
	{"tenly", "tenly", "tensetupfee"}, {"once", "onetime", "osetupfee"},
}

func rejectProduct(p *Product, reason string) {
	if p.Supported {
		p.Supported, p.UnsupportedReason = false, reason
	}
}

func (z *zjmf) Product(ctx context.Context, id int64) (Product, error) {
	if id <= 0 {
		return Product{}, &Error{Code: "invalid_product"}
	}
	w, err := z.get(ctx, "cart/get_product_config", url.Values{"pid": {strconv.FormatInt(id, 10)}})
	if err != nil {
		return Product{}, err
	}
	var data struct {
		Products        catalogProduct               `json:"products"`
		ProductPricings []map[string]json.RawMessage `json:"product_pricings"`
		ConfigGroups    []struct {
			Options []catalogOption `json:"options"`
		} `json:"config_groups"`
		CustomFields []struct {
			ID          catalogInt `json:"id"`
			Name        string     `json:"fieldname"`
			Description string     `json:"description"`
			Type        string     `json:"fieldtype"`
			Options     string     `json:"fieldoptions"`
			Regex       string     `json:"regexpr"`
			Required    catalogInt `json:"required"`
		} `json:"customfields"`
		Advanced json.RawMessage `json:"advanced"`
	}
	if json.Unmarshal(w.Data, &data) != nil || int64(data.Products.ID) != id {
		return Product{}, &Error{Code: "product_invalid"}
	}
	raw := data.Products
	p := Product{ID: id, Name: raw.Name, Description: raw.Description, Type: raw.Type, SupplierVersion: int64(raw.LocationVersion), Currency: "CNY", Prices: map[string]Price{}, Options: []Option{}, Fields: []Field{}, Stock: catalogStock(raw), Supported: true}
	payType, valid := productRule(raw.PayType, "pay_type")
	if !valid || payType != "recurring" && payType != "onetime" {
		rejectProduct(&p, "unsupported_payment_type")
	}
	if raw.Hidden != 0 || raw.Retired != 0 || raw.GroupHidden != 0 || raw.FirstGroupHidden != 0 {
		rejectProduct(&p, "product_unavailable")
	}
	var hostOK, passwordOK bool
	p.HostRule, hostOK = parseCredentialRule(raw.Host, true)
	p.PasswordRule, passwordOK = parseCredentialRule(raw.Password, false)
	if !hostOK || !passwordOK {
		rejectProduct(&p, "unsupported_host_credentials")
	}
	if p.SupplierVersion < 1 {
		rejectProduct(&p, "supplier_version_unknown")
	}
	if !emptyCatalogStructure(data.Advanced) {
		rejectProduct(&p, "advanced_configuration")
	}
	multiplier := int64(0)
	if raw.APIType == "zjmf_api" && raw.UpstreamPID > 0 && raw.UpstreamPriceType == "percent" {
		var ok bool
		multiplier, ok = moneyRaw(raw.UpstreamPriceValue)
		if !ok || multiplier <= 0 || multiplier > 100000000 {
			rejectProduct(&p, "unsupported_price_rule")
		} else {
			p.PriceMultiplier = multiplier
		}
	}
	pricing, priceOK := cnyPricing(data.ProductPricings)
	if !priceOK {
		rejectProduct(&p, "unsupported_currency_or_price")
	}
	if payType == "recurring" {
		// Custom hour/day lengths and trial eligibility are separate contracts.
		// Preserve normal paid cycles and disclose every omitted offering.
		for _, key := range []string{"hour", "day"} {
			if _, available := moneyRaw(pricing[key]); available {
				p.ExcludedCycles = append(p.ExcludedCycles, ExcludedCycle{Cycle: key, Reason: "custom_duration_unsupported"})
			}
		}
		trial, _ := productRule(raw.PayType, "pay_ontrial_status")
		if trial == "1" {
			p.ExcludedCycles = append(p.ExcludedCycles, ExcludedCycle{Cycle: "ontrial", Reason: "trial_eligibility_unsupported"})
		}
	}
	for _, cycle := range supplierCycles {
		if payType == "onetime" && cycle.cycle != "once" || payType == "recurring" && cycle.cycle == "once" {
			continue
		}
		amount, ok := moneyRaw(pricing[cycle.amount])
		if !ok {
			continue
		}
		setup, valid := setupMoney(pricing[cycle.setup])
		if !valid {
			rejectProduct(&p, "invalid_setup_price")
			continue
		}
		price, valid := multiplyCatalogPrice(Price{AmountCents: amount, SetupCents: setup}, multiplier)
		if !valid {
			rejectProduct(&p, "invalid_price")
			continue
		}
		p.Prices[cycle.cycle] = price
	}
	if len(p.Prices) == 0 {
		if len(p.ExcludedCycles) > 0 {
			rejectProduct(&p, "unsupported_cycle")
		} else {
			rejectProduct(&p, "unsupported_currency_or_price")
		}
	}
	optionIDs := map[int64]bool{}
	for _, group := range data.ConfigGroups {
		for _, source := range group.Options {
			option, reason := normalizeCatalogOption(source, p.Prices, multiplier)
			if optionIDs[option.ID] {
				reason = "option_schema_invalid"
			}
			optionIDs[option.ID] = true
			if reason != "" {
				rejectProduct(&p, reason)
				continue
			}
			p.Options = append(p.Options, option)
		}
	}
	// A supplier may offer a base annual price while a mandatory specification
	// is monthly-only. Keep only complete cycles rather than reject its usable
	// monthly configuration or accidentally treat a missing option price as zero.
	for cycle := range p.Prices {
		complete := true
		for _, option := range p.Options {
			for _, choice := range option.Choices {
				if _, ok := choice.Prices[cycle]; !ok {
					complete = false
				}
				if (option.QuantityMode == "graduated" || option.QuantityMode == "volume") && len(choice.PriceTiers[cycle]) == 0 {
					complete = false
				}
			}
		}
		if !complete {
			delete(p.Prices, cycle)
			p.ExcludedCycles = append(p.ExcludedCycles, ExcludedCycle{Cycle: cycle, Reason: "option_price_missing"})
		}
	}
	if len(p.Prices) == 0 {
		rejectProduct(&p, "option_price_missing")
	}
	sort.Slice(p.ExcludedCycles, func(i, j int) bool { return p.ExcludedCycles[i].Cycle < p.ExcludedCycles[j].Cycle })
	fieldIDs := map[int64]bool{}
	for _, source := range data.CustomFields {
		field := Field{ID: int64(source.ID), Name: source.Name, Description: source.Description, Type: source.Type, Required: source.Required == 1}
		if field.ID <= 0 || fieldIDs[field.ID] {
			rejectProduct(&p, "field_schema_invalid")
			continue
		}
		fieldIDs[field.ID] = true
		switch field.Type {
		case "text", "textarea", "link", "password", "tickbox", "dropdown":
		default:
			rejectProduct(&p, "unsupported_field_type")
			continue
		}
		if source.Regex != "" {
			var ok bool
			field.Pattern, ok = catalogFieldPattern(source.Regex)
			if !ok || field.Type == "dropdown" || field.Type == "tickbox" {
				rejectProduct(&p, "unsupported_field_regex")
				continue
			}
		}
		if field.Type == "dropdown" {
			seen := map[string]bool{}
			for _, choice := range strings.Split(source.Options, ",") {
				choice = strings.TrimSpace(choice)
				if choice != "" && !seen[choice] {
					field.Choices = append(field.Choices, choice)
					seen[choice] = true
				}
			}
			if len(field.Choices) == 0 {
				rejectProduct(&p, "field_schema_invalid")
			}
		}
		p.Fields = append(p.Fields, field)
	}
	stable, _ := json.Marshal(struct {
		ID, SupplierVersion int64
		Currency            string
		Prices              map[string]Price
		Options             []Option
		Fields              []Field
		HostRule            *credentialRule `json:",omitempty"`
		PasswordRule        *credentialRule `json:",omitempty"`
		PriceMultiplier     int64           `json:",omitempty"`
		Type                string          `json:",omitempty"`
	}{p.ID, p.SupplierVersion, p.Currency, p.Prices, p.Options, p.Fields, p.HostRule, p.PasswordRule, p.PriceMultiplier, p.Type})
	h := sha256.Sum256(stable)
	p.Revision = hex.EncodeToString(h[:])
	return p, nil
}

func emptyCatalogStructure(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return true
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	switch v := value.(type) {
	case nil:
		return true
	case []any:
		return len(v) == 0
	case map[string]any:
		return len(v) == 0
	}
	return false
}

func cnyPricing(rows []map[string]json.RawMessage) (map[string]json.RawMessage, bool) {
	var selected map[string]json.RawMessage
	for _, row := range rows {
		var code string
		if json.Unmarshal(row["code"], &code) != nil || code != "CNY" {
			continue
		}
		if selected != nil {
			return nil, false
		}
		selected = row
	}
	return selected, selected != nil
}

// Legacy percent proxy prices truncate the result to currency precision. Use
// exact integer arithmetic, including values near int64 limits.
func multiplyCatalogPrice(price Price, multiplier int64) (Price, bool) {
	if multiplier == 0 {
		return price, price.AmountCents >= 0 && price.SetupCents >= 0
	}
	amount, ok := scaleCatalogCents(price.AmountCents, multiplier, false)
	setup, setupOK := scaleCatalogCents(price.SetupCents, multiplier, false)
	return Price{AmountCents: amount, SetupCents: setup, RawAmountCents: &price.AmountCents, RawSetupCents: &price.SetupCents}, ok && setupOK
}

func scaleCatalogCents(value, multiplier int64, roundUp bool) (int64, bool) {
	if value < 0 || multiplier <= 0 {
		return 0, false
	}
	result := new(big.Int).Mul(big.NewInt(value), big.NewInt(multiplier))
	if roundUp {
		result.Add(result, big.NewInt(9999))
	}
	result.Quo(result, big.NewInt(10000))
	return result.Int64(), result.IsInt64()
}

func normalizeCatalogOption(raw catalogOption, cycles map[string]Price, multiplier int64) (Option, string) {
	o := Option{ID: int64(raw.ID), Name: raw.Name, Type: "select", Required: true}
	if raw.ID <= 0 || raw.Hidden != 0 || len(raw.Sub) == 0 {
		return o, "option_schema_invalid"
	}
	var level string
	if len(raw.LinkageLevel) != 0 && string(raw.LinkageLevel) != "null" {
		if json.Unmarshal(raw.LinkageLevel, &level) != nil {
			level = string(raw.LinkageLevel)
		}
	}
	if raw.LinkagePID != 0 || raw.LinkageTop != 0 || !rootLinkageLevel(level) {
		return o, "option_linkage_unsupported"
	}
	quantity := raw.Type == 4 || raw.Type == 7 || raw.Type == 9 || raw.Type == 11 || raw.Type == 14 || raw.Type >= 15 && raw.Type <= 19
	if !quantity && raw.Type != 1 && raw.Type != 2 && raw.Type != 5 && raw.Type != 6 && raw.Type != 8 && raw.Type != 10 && raw.Type != 12 && raw.Type != 13 {
		return o, "unsupported_option_type"
	}
	if raw.Type != 1 && raw.Type != 2 {
		o.SourceType = int(raw.Type)
	}
	if quantity {
		o.Type, o.QuantityMode, o.MinQty, o.MaxQty = "quantity", "linear", int64(raw.QtyMinimum), int64(raw.QtyMaximum)
		if o.MaxQty < o.MinQty || o.MaxQty > 1000000 || raw.QtyStage > 1 {
			return o, "unsupported_quantity_range"
		}
		if raw.Type >= 15 {
			o.QuantityMode = "graduated"
		}
		if o.QuantityMode == "linear" && len(raw.Sub) != 1 {
			o.QuantityMode = "volume"
		}
	}
	ids := map[int64]bool{}
	for _, sub := range raw.Sub {
		if sub.ID <= 0 || ids[int64(sub.ID)] || sub.Hidden != 0 || sub.LinkagePID != 0 {
			return o, "option_schema_invalid"
		}
		ids[int64(sub.ID)] = true
		choice := Choice{ID: int64(sub.ID), Name: sub.Name, MinQty: int64(sub.QtyMinimum), MaxQty: int64(sub.QtyMaximum), Prices: map[string]Price{}}
		pricing, ok := cnyPricing(sub.Pricings)
		if !ok {
			return o, "option_price_missing"
		}
		for _, cycle := range supplierCycles {
			if _, available := cycles[cycle.cycle]; !available {
				continue
			}
			amount, valid := moneyRaw(pricing[cycle.amount])
			if !valid {
				continue
			}
			setup, valid := setupMoney(pricing[cycle.setup])
			if !valid {
				return o, "invalid_setup_price"
			}
			price, valid := multiplyCatalogPrice(Price{AmountCents: amount, SetupCents: setup}, multiplier)
			if !valid {
				return o, "invalid_price"
			}
			choice.Prices[cycle.cycle] = price
		}
		o.Choices = append(o.Choices, choice)
	}
	if !quantity {
		return o, ""
	}
	if o.QuantityMode == "linear" {
		choice := &o.Choices[0]
		// Some DCIM fixed quantities omit sub bounds. Otherwise the price range
		// must cover the full offered quantity interval.
		if (choice.MinQty != 0 || choice.MaxQty != 0) && (choice.MinQty > o.MinQty || choice.MaxQty < o.MaxQty) {
			return o, "unsupported_quantity_range"
		}
		choice.MinQty, choice.MaxQty = o.MinQty, o.MaxQty
		return o, ""
	}
	ordered := append([]Choice(nil), o.Choices...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].MinQty < ordered[j].MinQty })
	canonical := o.Choices[0]
	canonical.MinQty, canonical.MaxQty = o.MinQty, o.MaxQty
	canonical.PriceTiers = map[string][]PriceTier{}
	last := int64(0)
	for _, choice := range ordered {
		minimum := choice.MinQty
		if minimum == 0 {
			minimum = 1
		}
		if minimum != last+1 || choice.MaxQty < minimum || choice.MaxQty > 1000000 {
			return o, "quantity_tiers_invalid"
		}
		last = choice.MaxQty
		for cycle, price := range choice.Prices {
			canonical.PriceTiers[cycle] = append(canonical.PriceTiers[cycle], PriceTier{MinQty: minimum, MaxQty: choice.MaxQty, AmountCents: price.AmountCents, SetupCents: price.SetupCents, RawAmountCents: price.RawAmountCents, RawSetupCents: price.RawSetupCents})
		}
	}
	for cycle, tiers := range canonical.PriceTiers {
		if len(tiers) != len(ordered) {
			delete(canonical.PriceTiers, cycle)
			delete(canonical.Prices, cycle)
		}
	}
	if last < o.MaxQty {
		return o, "quantity_tiers_invalid"
	}
	o.Choices = []Choice{canonical}
	return o, ""
}

// Only an anchored RE2 subset is safe to translate from a supplier's PCRE.
// Unanchored PCRE search rules cannot be changed into local full-match rules.
func catalogFieldPattern(source string) (string, bool) {
	if len(source) > 500 {
		return "", false
	}
	pattern := source
	if len(source) >= 3 && strings.ContainsRune("/#~!%", rune(source[0])) {
		delimiter := source[0]
		end := strings.LastIndexByte(source[1:], delimiter) + 1
		if end <= 0 || end != len(source)-1 {
			return "", false
		}
		pattern = source[1:end]
		pattern = strings.ReplaceAll(pattern, "\\"+string(delimiter), string(delimiter))
	}
	if !(strings.HasPrefix(pattern, "^") || strings.HasPrefix(pattern, `\A`)) || !(strings.HasSuffix(pattern, "$") || strings.HasSuffix(pattern, `\z`)) {
		return "", false
	}
	_, err := regexp.Compile(`\A(?:` + pattern + `)\z`)
	return pattern, err == nil
}

// Imported suppliers may remap IDs but retain the original root path ID.
// A single positive root component is metadata; deeper paths are dependencies.
func rootLinkageLevel(level string) bool {
	if level == "" || level == "0" {
		return true
	}
	if !strings.HasPrefix(level, "0-") {
		return false
	}
	id := strings.TrimPrefix(level, "0-")
	if len(id) == 0 || id[0] == '0' {
		return false
	}
	_, err := strconv.ParseUint(id, 10, 64)
	return err == nil && strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) < 0
}
