// Package supplier defines provider-neutral upstream product and service operations.
package supplier

import "context"

const ProviderZJMF = "zjmf_finance_v1"

type Account struct {
	ID                                    int64
	Provider, BaseURL, Username, Password string
}

type Price struct{ AmountCents, SetupCents int64 }

type Choice struct {
	ID     int64
	Name   string
	Prices map[string]Price
}
type Option struct {
	ID         int64
	Name, Type string
	Required   bool
	Choices    []Choice
}
type Field struct {
	ID                      int64
	Name, Type, Description string
	Required                bool
	Choices                 []string
}

type Product struct {
	ID                                    int64
	Name, Description, Currency, Revision string
	Stock                                 *int64
	Prices                                map[string]Price
	Options                               []Option
	Fields                                []Field
	Supported                             bool
	UnsupportedReason                     string
}

type Capabilities struct{ Catalog, Product, Create, Sync, Suspend, Resume, Terminate, Renew bool }

type CreateRequest struct {
	ProductID                      int64
	Cycle, Revision                string
	ConfigOptions, CustomFields    map[int64]string
	LocalServiceID                 int64
	DownstreamURL, DownstreamToken string
	MaximumCostCents               int64
	Currency                       string
	Host, Password                 string
}

type CreateResult struct {
	UpstreamID, InvoiceID int64
	Status                string
	CostCents             int64
	Currency              string
}

type Service struct {
	UpstreamID                  int64
	Status, Domain, NextDueDate string
}

type Error struct {
	Code                  string
	Unknown               bool
	UpstreamID, InvoiceID int64
}

func (e *Error) Error() string { return "supplier: " + e.Code }

type Adapter interface {
	Capabilities() Capabilities
	Catalog(context.Context) ([]Product, error)
	Product(context.Context, int64) (Product, error)
	Create(context.Context, CreateRequest) (CreateResult, error)
	Sync(context.Context, int64) (Service, error)
	Execute(context.Context, string, int64) (Service, error)
}
