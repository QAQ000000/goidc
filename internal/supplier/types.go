// Package supplier defines provider-neutral upstream product and service operations.
package supplier

import (
	"context"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

const ProviderZJMF = "zjmf_finance_v1"

type Account struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"-"`
	// Values and Secrets are runtime configuration, never public account DTOs.
	Values        map[string]any    `json:"-"`
	Secrets       map[string]string `json:"-"`
	SchemaVersion int               `json:"-"`
}

type Price struct {
	AmountCents    int64  `json:"amount_cents"`
	SetupCents     int64  `json:"setup_cents"`
	RawAmountCents *int64 `json:"raw_amount_cents,omitempty"`
	RawSetupCents  *int64 `json:"raw_setup_cents,omitempty"`
}

type Choice struct {
	ID         int64                  `json:"id"`
	Name       string                 `json:"name"`
	MinQty     int64                  `json:"min_qty,omitempty"`
	MaxQty     int64                  `json:"max_qty,omitempty"`
	Prices     map[string]Price       `json:"prices"`
	PriceTiers map[string][]PriceTier `json:"price_tiers,omitempty"`
}

// PriceTier is an inclusive quantity interval. Setup belongs to the selected
// interval only; Option.QuantityMode determines volume or progressive pricing.
type PriceTier struct {
	MinQty         int64  `json:"min_qty"`
	MaxQty         int64  `json:"max_qty"`
	AmountCents    int64  `json:"amount_cents"`
	SetupCents     int64  `json:"setup_cents"`
	RawAmountCents *int64 `json:"raw_amount_cents,omitempty"`
	RawSetupCents  *int64 `json:"raw_setup_cents,omitempty"`
}
type Option struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Type         string   `json:"type"`
	Required     bool     `json:"required"`
	Choices      []Choice `json:"choices"`
	SourceType   int      `json:"source_type,omitempty"`
	MinQty       int64    `json:"min_qty,omitempty"`
	MaxQty       int64    `json:"max_qty,omitempty"`
	QuantityMode string   `json:"quantity_mode,omitempty"`
}
type Field struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Choices     []string `json:"choices"`
	Pattern     string   `json:"pattern,omitempty"`
}

type ExcludedCycle struct {
	Cycle  string `json:"cycle"`
	Reason string `json:"reason"`
}

type Product struct {
	ExcludedCycles    []ExcludedCycle  `json:"excluded_cycles,omitempty"`
	ID                int64            `json:"id"`
	Name              string           `json:"name"`
	Description       string           `json:"description"`
	Currency          string           `json:"currency"`
	Revision          string           `json:"revision"`
	SupplierVersion   int64            `json:"supplier_version"`
	Stock             *int64           `json:"stock"`
	Prices            map[string]Price `json:"prices"`
	Options           []Option         `json:"options"`
	Fields            []Field          `json:"fields"`
	Supported         bool             `json:"supported"`
	UnsupportedReason string           `json:"unsupported_reason,omitempty"`
	HostRule          *credentialRule  `json:"-"`
	PasswordRule      *credentialRule  `json:"-"`
	GroupID           int64            `json:"group_id,omitempty"`
	GroupName         string           `json:"group_name,omitempty"`
	Type              string           `json:"type,omitempty"`
	PriceMultiplier   int64            `json:"price_multiplier,omitempty"`
}

type Capabilities struct {
	Catalog       bool `json:"catalog"`
	Product       bool `json:"product"`
	Create        bool `json:"create"`
	Sync          bool `json:"sync"`
	Power         bool `json:"power"`
	ResetPassword bool `json:"reset_password"`
	Rescue        bool `json:"rescue"`
	VNC           bool `json:"vnc"`
	Reinstall     bool `json:"reinstall"`
	Suspend       bool `json:"suspend"`
	Resume        bool `json:"resume"`
	Terminate     bool `json:"terminate"`
	Renew         bool `json:"renew"`
}

// SupportsAction is shared by admission and dispatch. Metadata never substitutes
// for the worker's account, resource and runtime capability checks.
func (c Capabilities) SupportsAction(action string) bool {
	switch action {
	case "create":
		return c.Create
	case "sync":
		return c.Sync
	case "renew":
		return c.Renew
	case "suspend":
		return c.Suspend
	case "resume":
		return c.Resume
	case "terminate":
		return c.Terminate
	case "power_on", "power_off", "reboot", "force_off", "force_reboot":
		return c.Power
	case "reset_password":
		return c.ResetPassword
	case "rescue_system", "exit_rescue_system":
		return c.Rescue
	case "reinstall":
		return c.Reinstall
	}
	return false
}

// LifecycleTargetConfirmed accepts only a positive observation of the requested
// end state. A successful command response or a missing host is insufficient.
func LifecycleTargetConfirmed(action, status string) bool {
	switch strings.ToLower(status) {
	case "suspended":
		return action == "suspend"
	case "active":
		return action == "resume"
	case "terminated", "cancelled", "deleted":
		return action == "terminate"
	}
	return false
}

type Provider struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Capabilities Capabilities `json:"capabilities"`
	// IdentityFields identify the upstream account independently of rotating
	// credentials and optional transport settings. They must be required,
	// non-secret configuration fields.
	IdentityFields []string `json:"identity_fields,omitempty"`
	// NormalizeConfig canonicalizes equivalent public account identities. It
	// must be pure local code and receives no credentials.
	NormalizeConfig func(map[string]any) (map[string]any, error) `json:"-"`
}

type CreateRequest struct {
	ProductID        int64            `json:"product_id"`
	Cycle            string           `json:"cycle"`
	Revision         string           `json:"revision"`
	ConfigOptions    map[int64]string `json:"config_options"`
	CustomFields     map[int64]string `json:"custom_fields"`
	LocalServiceID   int64            `json:"local_service_id"`
	DownstreamURL    string           `json:"downstream_url"`
	DownstreamToken  string           `json:"-"`
	MaximumCostCents int64            `json:"maximum_cost_cents"`
	Currency         string           `json:"currency"`
	Host             string           `json:"host"`
	Password         string           `json:"-"`
}

type CreateResult struct {
	UpstreamID  int64  `json:"upstream_id"`
	InvoiceID   int64  `json:"invoice_id"`
	Status      string `json:"status"`
	CostCents   int64  `json:"cost_cents"`
	Currency    string `json:"currency"`
	ProductID   int64  `json:"product_id"`
	Domain      string `json:"domain"`
	NextDueDate string `json:"next_due_date"`
}

type Service struct {
	UpstreamID        int64  `json:"upstream_id"`
	ProductID         int64  `json:"product_id"`
	Status            string `json:"status"`
	Domain            string `json:"domain"`
	NextDueDate       string `json:"next_due_date"`
	Username          string `json:"username,omitempty"`
	Password          string `json:"-"`
	DedicatedIP       string `json:"dedicated_ip,omitempty"`
	AssignedIPs       string `json:"assigned_ips,omitempty"`
	Port              int    `json:"port,omitempty"`
	OS                string `json:"os,omitempty"`
	OSURL             string `json:"os_url,omitempty"`
	SuspendReason     string `json:"suspend_reason,omitempty"`
	SuspendReasonType string `json:"suspend_reason_type,omitempty"`
}

// ActionRequest contains the provider action and the one-time secret needed
// by password reset or rescue mode. Password is never serialized or logged.
type ActionRequest struct {
	Action     string
	UpstreamID int64
	Password   string `json:"-"`
	System     int
	TemplateID int64
}

// ConsoleAdapter supplies ephemeral console links and resource-owned OS choices.
type ConsoleAdapter interface {
	VNC(context.Context, int64) (string, error)
	Templates(context.Context, int64) ([]Template, error)
}

type Template struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Group string `json:"group"`
}

type Error struct {
	Code       string `json:"code"`
	Unknown    bool   `json:"unknown"`
	UpstreamID int64  `json:"upstream_id,omitempty"`
	InvoiceID  int64  `json:"invoice_id,omitempty"`
}

func (e *Error) Error() string { return "supplier: " + e.Code }

type Adapter interface {
	Capabilities() Capabilities
	Catalog(context.Context) ([]Product, error)
	Product(context.Context, int64) (Product, error)
	Create(context.Context, CreateRequest) (CreateResult, error)
	Sync(context.Context, int64) (Service, error)
	Execute(context.Context, ActionRequest) (Service, error)
}

// ValidateSelection is the purchase admission check, including current stock.
func ValidateSelection(product Product, cycle string, options, fields map[int64]string) (int64, error) {
	if product.Stock != nil && *product.Stock < 1 {
		return 0, &Error{Code: "out_of_stock"}
	}
	price, err := SelectionPrice(product, cycle, options, fields)
	if err != nil {
		return 0, err
	}
	return price.AmountCents + price.SetupCents, nil
}

// SelectionPrice validates a concrete supplier configuration and separates its
// recurring amount from setup. For percent proxies it returns conservative
// cost ceilings: aggregate raw recurring/setup amounts, then ceil each to cents.
// Supplier versions differ in intermediate invoice rounding, so actual paid
// costs remain authoritative only after reading the exact upstream invoice.
// Zero stock is permitted for preview/import while unavailable for purchase.
func SelectionPrice(product Product, cycle string, options, fields map[int64]string) (Price, error) {
	fail := func(code string) (Price, error) { return Price{}, &Error{Code: code} }
	if !product.Supported {
		return fail("unsupported_product")
	}
	if product.Currency != "CNY" {
		return fail("unsupported_currency")
	}
	base, ok := product.Prices[cycle]
	if !ok {
		return fail("unsupported_cycle")
	}
	percent := product.PriceMultiplier > 0
	base, ok = selectionPriceBasis(base, percent)
	if !ok {
		return fail("invalid_price")
	}
	seen := map[int64]bool{}
	for _, option := range product.Options {
		if option.ID <= 0 || seen[option.ID] {
			return fail("option_schema_invalid")
		}
		seen[option.ID] = true
		value, present := options[option.ID]
		if option.Required && (!present || value == "") {
			return fail("required_option")
		}
		if !present || value == "" {
			continue
		}
		var price Price
		if option.Type == "quantity" {
			qty, err := strictQuantity(value)
			if err != nil || len(option.Choices) != 1 {
				return fail("invalid_option_quantity")
			}
			choice := option.Choices[0]
			minimum, maximum := option.MinQty, option.MaxQty
			if maximum == 0 { // Existing normalized quantity fixtures.
				minimum, maximum = choice.MinQty, choice.MaxQty
			}
			if qty < minimum || qty > maximum {
				return fail("invalid_option_quantity")
			}
			unit, exists := choice.Prices[cycle]
			if !exists {
				return fail("option_price_missing")
			}
			unit, exists = selectionPriceBasis(unit, percent)
			if !exists {
				return fail("invalid_price")
			}
			if qty > 0 {
				if option.QuantityMode == "graduated" || option.QuantityMode == "volume" {
					var valid bool
					price, valid = tieredPrice(choice.PriceTiers[cycle], qty, percent, option.QuantityMode == "volume")
					if !valid {
						return fail("invalid_option_quantity")
					}
				} else {
					if option.QuantityMode != "" && option.QuantityMode != "linear" || unit.AmountCents < 0 || unit.SetupCents < 0 || unit.AmountCents > math.MaxInt64/qty {
						return fail("invalid_price")
					}
					price = Price{AmountCents: unit.AmountCents * qty, SetupCents: unit.SetupCents}
				}
			}
		} else if option.Type == "select" {
			found := false
			for _, choice := range option.Choices {
				if strconv.FormatInt(choice.ID, 10) == value {
					var exists bool
					price, exists = choice.Prices[cycle]
					if !exists {
						return fail("option_price_missing")
					}
					price, exists = selectionPriceBasis(price, percent)
					if !exists {
						return fail("invalid_price")
					}
					found = true
					break
				}
			}
			if !found {
				return fail("invalid_option")
			}
		} else {
			return fail("unsupported_option_type")
		}
		if price.AmountCents < 0 || price.SetupCents < 0 || base.AmountCents > math.MaxInt64-price.AmountCents || base.SetupCents > math.MaxInt64-price.SetupCents {
			return fail("invalid_price")
		}
		base.AmountCents += price.AmountCents
		base.SetupCents += price.SetupCents
	}
	for id := range options {
		if !seen[id] {
			return fail("unknown_option")
		}
	}
	fieldIDs := map[int64]bool{}
	for _, field := range product.Fields {
		if field.ID <= 0 || fieldIDs[field.ID] {
			return fail("field_schema_invalid")
		}
		fieldIDs[field.ID] = true
		value, present := fields[field.ID]
		if field.Required && (!present || strings.TrimSpace(value) == "" || field.Type == "tickbox" && value != "true") {
			return fail("required_field")
		}
		if !present {
			continue
		}
		if !utf8.ValidString(value) || utf8.RuneCountInString(value) > 2048 || strings.ContainsRune(value, 0) {
			return fail("invalid_field")
		}
		if len(field.Choices) > 0 && value != "" {
			valid := false
			for _, choice := range field.Choices {
				if choice == value {
					valid = true
					break
				}
			}
			if !valid {
				return fail("invalid_field")
			}
		}
		if field.Type == "tickbox" && value != "true" && value != "false" && value != "" {
			return fail("invalid_field")
		}
		if field.Type == "link" && value != "" {
			u, err := url.Parse(value)
			if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
				return fail("invalid_field")
			}
		}
		if field.Pattern != "" && value != "" {
			re, err := regexp.Compile(`\A(?:` + field.Pattern + `)\z`)
			if err != nil || !re.MatchString(value) {
				return fail("invalid_field")
			}
		}
	}
	for id := range fields {
		if !fieldIDs[id] {
			return fail("unknown_field")
		}
	}
	if percent {
		amount, valid := scaleCatalogCents(base.AmountCents, product.PriceMultiplier, true)
		setup, setupValid := scaleCatalogCents(base.SetupCents, product.PriceMultiplier, true)
		if !valid || !setupValid {
			return fail("invalid_price")
		}
		base = Price{AmountCents: amount, SetupCents: setup}
	}
	if base.AmountCents < 0 || base.SetupCents < 0 || base.AmountCents > math.MaxInt64-base.SetupCents {
		return fail("invalid_price")
	}
	return base, nil
}

func strictQuantity(value string) (int64, error) {
	if value == "" {
		return 0, &Error{Code: "invalid_option_quantity"}
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, &Error{Code: "invalid_option_quantity"}
		}
	}
	return strconv.ParseInt(value, 10, 64)
}

// Percent proxy APIs multiply the complete configured recurring/setup totals,
// rather than rounding each component before the configuration is summed.
func selectionPriceBasis(price Price, raw bool) (Price, bool) {
	if raw {
		if price.RawAmountCents == nil || price.RawSetupCents == nil {
			return Price{}, false
		}
		price.AmountCents, price.SetupCents = *price.RawAmountCents, *price.RawSetupCents
	}
	return Price{AmountCents: price.AmountCents, SetupCents: price.SetupCents}, price.AmountCents >= 0 && price.SetupCents >= 0
}

func tieredPrice(tiers []PriceTier, quantity int64, raw, volume bool) (Price, bool) {
	var total Price
	var last int64
	matched := false
	for _, tier := range tiers {
		price, valid := selectionPriceBasis(Price{AmountCents: tier.AmountCents, SetupCents: tier.SetupCents, RawAmountCents: tier.RawAmountCents, RawSetupCents: tier.RawSetupCents}, raw)
		if tier.MinQty != last+1 || tier.MaxQty < tier.MinQty || !valid {
			return Price{}, false
		}
		last = tier.MaxQty
		if quantity < tier.MinQty {
			continue
		}
		units := tier.MaxQty - tier.MinQty + 1
		if quantity <= tier.MaxQty {
			units = quantity - tier.MinQty + 1
			total.SetupCents = price.SetupCents
			matched = true
		}
		if volume && matched {
			if price.AmountCents > math.MaxInt64/quantity {
				return Price{}, false
			}
			return Price{AmountCents: price.AmountCents * quantity, SetupCents: price.SetupCents}, true
		}
		if volume {
			continue
		}
		if price.AmountCents > math.MaxInt64/units || total.AmountCents > math.MaxInt64-price.AmountCents*units {
			return Price{}, false
		}
		total.AmountCents += price.AmountCents * units
		if matched {
			return total, true
		}
	}
	return Price{}, false
}
