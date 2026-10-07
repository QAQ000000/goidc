// Package supplier defines provider-neutral upstream product and service operations.
package supplier

import (
	"context"
	"math"
	"strconv"
	"strings"
)

const ProviderZJMF = "zjmf_finance_v1"

type Account struct {
	ID       int64  `json:"id"`
	Provider string `json:"provider"`
	BaseURL  string `json:"base_url"`
	Username string `json:"username"`
	Password string `json:"-"`
}

type Price struct {
	AmountCents int64 `json:"amount_cents"`
	SetupCents  int64 `json:"setup_cents"`
}

type Choice struct {
	ID     int64            `json:"id"`
	Name   string           `json:"name"`
	MinQty int64            `json:"min_qty,omitempty"`
	MaxQty int64            `json:"max_qty,omitempty"`
	Prices map[string]Price `json:"prices"`
}
type Option struct {
	ID       int64    `json:"id"`
	Name     string   `json:"name"`
	Type     string   `json:"type"`
	Required bool     `json:"required"`
	Choices  []Choice `json:"choices"`
}
type Field struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description"`
	Required    bool     `json:"required"`
	Choices     []string `json:"choices"`
}

type Product struct {
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
type Provider struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Capabilities Capabilities `json:"capabilities"`
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
	UpstreamID    int64  `json:"upstream_id"`
	ProductID     int64  `json:"product_id"`
	Status        string `json:"status"`
	Domain        string `json:"domain"`
	NextDueDate   string `json:"next_due_date"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"-"`
	DedicatedIP   string `json:"dedicated_ip,omitempty"`
	AssignedIPs   string `json:"assigned_ips,omitempty"`
	Port          int    `json:"port,omitempty"`
	OS            string `json:"os,omitempty"`
	OSURL         string `json:"os_url,omitempty"`
	SuspendReason string `json:"suspend_reason,omitempty"`
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

func ValidateSelection(product Product, cycle string, options, fields map[int64]string) (int64, error) {
	if !product.Supported {
		return 0, &Error{Code: "unsupported_product"}
	}
	if product.Stock != nil && *product.Stock < 1 {
		return 0, &Error{Code: "out_of_stock"}
	}
	if product.Currency != "CNY" {
		return 0, &Error{Code: "unsupported_currency"}
	}
	base, ok := product.Prices[cycle]
	if !ok {
		return 0, &Error{Code: "unsupported_cycle"}
	}
	if options == nil {
		options = map[int64]string{}
	}
	if fields == nil {
		fields = map[int64]string{}
	}
	seen := map[int64]bool{}
	for _, option := range product.Options {
		value, present := options[option.ID]
		if option.Required && (!present || value == "") {
			return 0, &Error{Code: "required_option"}
		}
		if !present || value == "" {
			continue
		}
		if option.Type != "select" && option.Type != "quantity" {
			return 0, &Error{Code: "unsupported_option_type"}
		}
		var choice *Choice
		if option.Type == "quantity" {
			qty, err := strconv.ParseInt(value, 10, 64)
			if err != nil || len(option.Choices) != 1 || qty < option.Choices[0].MinQty || qty > option.Choices[0].MaxQty {
				return 0, &Error{Code: "invalid_option_quantity"}
			}
			choice = &option.Choices[0]
		} else {
			for i := range option.Choices {
				if strconv.FormatInt(option.Choices[i].ID, 10) == value {
					choice = &option.Choices[i]
					break
				}
			}
		}
		if choice == nil {
			return 0, &Error{Code: "invalid_option"}
		}
		seen[option.ID] = true
		price, exists := choice.Prices[cycle]
		if !exists {
			return 0, &Error{Code: "option_price_missing"}
		}
		if option.Type == "quantity" {
			qty, _ := strconv.ParseInt(value, 10, 64)
			if qty > 0 && (price.AmountCents > math.MaxInt64/qty || price.SetupCents > math.MaxInt64/qty) {
				return 0, &Error{Code: "invalid_price"}
			}
			price.AmountCents *= qty
			price.SetupCents *= qty
		}
		if price.AmountCents < 0 || price.SetupCents < 0 || base.AmountCents > math.MaxInt64-price.AmountCents || base.SetupCents > math.MaxInt64-price.SetupCents {
			return 0, &Error{Code: "invalid_price"}
		}
		base.AmountCents += price.AmountCents
		base.SetupCents += price.SetupCents
	}
	for id := range options {
		if !seen[id] {
			found := false
			for _, o := range product.Options {
				if o.ID == id {
					found = true
					break
				}
			}
			if !found {
				return 0, &Error{Code: "unknown_option"}
			}
		}
	}
	fieldIDs := map[int64]bool{}
	for _, field := range product.Fields {
		fieldIDs[field.ID] = true
		value, present := fields[field.ID]
		if field.Required && (!present || strings.TrimSpace(value) == "") {
			return 0, &Error{Code: "required_field"}
		}
		if present && len(field.Choices) > 0 {
			valid := false
			for _, choice := range field.Choices {
				if choice == value {
					valid = true
					break
				}
			}
			if !valid {
				return 0, &Error{Code: "invalid_field"}
			}
		}
	}
	for id := range fields {
		if !fieldIDs[id] {
			return 0, &Error{Code: "unknown_field"}
		}
	}
	if base.AmountCents < 0 || base.SetupCents < 0 || base.AmountCents > math.MaxInt64-base.SetupCents {
		return 0, &Error{Code: "invalid_price"}
	}
	return base.AmountCents + base.SetupCents, nil
}
