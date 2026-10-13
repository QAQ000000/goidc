package supplier

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
	"time"
)

// RenewalAdapter is optional: capability alone never enables local collection.
type RenewalAdapter interface {
	QuoteRenewal(context.Context, RenewalRequest) (RenewalRequest, error)
	Renew(context.Context, RenewalRequest, func(CreateResult) error) (CreateResult, Service, error)
	ConfirmRenewal(context.Context, RenewalRequest, int64) (CreateResult, Service, error)
}

type RenewalRequest struct {
	UpstreamID       int64  `json:"upstream_id"`
	ProductID        int64  `json:"product_id"`
	Cycle            string `json:"cycle"`
	Currency         string `json:"currency"`
	MaximumCostCents int64  `json:"maximum_cost_cents"`
	CostCents        int64  `json:"cost_cents"`
	PreviousDue      string `json:"previous_due"`
}

func renewalTime(value string) (time.Time, bool) {
	if n, err := strconv.ParseInt(value, 10, 64); err == nil && n > 0 {
		return time.Unix(n, 0), true
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, time.FixedZone("Asia/Shanghai", 8*3600)); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func (z *zjmf) QuoteRenewal(ctx context.Context, r RenewalRequest) (RenewalRequest, error) {
	if r.UpstreamID < 1 || r.ProductID < 1 || r.Currency != "CNY" || r.MaximumCostCents < 0 {
		return r, &Error{Code: "invalid_renewal"}
	}
	cycle := map[string]string{"monthly": "monthly", "quarterly": "quarterly", "annually": "annually"}[r.Cycle]
	if cycle == "" {
		return r, &Error{Code: "unsupported_cycle"}
	}
	svc, err := z.Sync(ctx, r.UpstreamID)
	if err != nil {
		return r, err
	}
	if svc.ProductID != r.ProductID || (svc.Status != "Active" && svc.Status != "active" && svc.Status != "Suspended" && svc.Status != "suspended") {
		return r, &Error{Code: "renewal_resource_mismatch"}
	}
	w, err := z.get(ctx, "host/renewpage", url.Values{"hostid": {strconv.FormatInt(r.UpstreamID, 10)}, "billingcycles": {cycle}})
	if err != nil {
		return r, err
	}
	var data struct {
		Host struct {
			ProductID int64           `json:"productid"`
			Cycle     string          `json:"billingcycle"`
			Amount    json.RawMessage `json:"amount"`
			Due       json.RawMessage `json:"nextduedate"`
		} `json:"host"`
		Currency struct {
			Code string `json:"code"`
		} `json:"currency"`
	}
	if json.Unmarshal(w.Data, &data) != nil || data.Host.ProductID != r.ProductID || data.Host.Cycle != cycle || data.Currency.Code != r.Currency {
		return r, &Error{Code: "renewal_quote_invalid"}
	}
	cost, ok := moneyRaw(data.Host.Amount)
	due, valid := renewalTime(rawDate(data.Host.Due))
	remoteDue, remoteValid := renewalTime(svc.NextDueDate)
	if !ok || cost > r.MaximumCostCents || !valid || !remoteValid || !due.Equal(remoteDue) {
		return r, &Error{Code: "renewal_quote_invalid"}
	}
	r.CostCents, r.PreviousDue = cost, due.UTC().Format(time.RFC3339)
	return r, nil
}

func (z *zjmf) renewalInvoice(ctx context.Context, r RenewalRequest, id int64) (CreateResult, string, error) {
	out := CreateResult{UpstreamID: r.UpstreamID, InvoiceID: id, ProductID: r.ProductID, Currency: r.Currency, CostCents: r.CostCents}
	w, err := z.get(ctx, "get_invoices_detail", url.Values{"id": {strconv.FormatInt(id, 10)}})
	var inv purchaseInvoice
	if err != nil || json.Unmarshal(w.Data, &inv) != nil || number(inv.Detail.ID) != id {
		return out, "", failure("renewal_invoice_invalid", true, out)
	}
	cost, ok := moneyRaw(inv.Detail.Subtotal)
	total, totalOK := moneyRaw(inv.Detail.Total)
	if !ok || !totalOK || cost != r.CostCents || cost > r.MaximumCostCents || total > cost || inv.Currency.Code != r.Currency {
		return out, "", failure("renewal_invoice_price_mismatch", inv.Detail.Status == "Paid", out)
	}
	renewItems := 0
	for _, item := range inv.Items {
		if number(item.RelID) != r.UpstreamID {
			return out, "", failure("renewal_invoice_resource_mismatch", inv.Detail.Status == "Paid", out)
		}
		switch item.Type {
		case "renew":
			renewItems++
		case "discount", "promo":
		default:
			return out, "", failure("renewal_invoice_resource_mismatch", inv.Detail.Status == "Paid", out)
		}
	}
	if renewItems != 1 || (inv.Detail.Status != "Paid" && inv.Detail.Status != "Unpaid") {
		return out, "", failure("renewal_invoice_invalid", inv.Detail.Status != "Unpaid", out)
	}
	return out, inv.Detail.Status, nil
}

func (z *zjmf) ConfirmRenewal(ctx context.Context, r RenewalRequest, id int64) (CreateResult, Service, error) {
	out, status, err := z.renewalInvoice(ctx, r, id)
	if err != nil {
		return out, Service{}, failure("renewal_payment_unconfirmed", true, out)
	}
	if status != "Paid" {
		return out, Service{}, failure("renewal_payment_unconfirmed", true, out)
	}
	svc, err := z.Sync(ctx, r.UpstreamID)
	due, ok := renewalTime(svc.NextDueDate)
	before, valid := renewalTime(r.PreviousDue)
	months := map[string]int{"monthly": 1, "quarterly": 3, "annually": 12}[r.Cycle]
	// Require a complete purchased period, using the supplier's local calendar.
	expected := before.In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, months, 0)
	if err != nil || svc.ProductID != r.ProductID || !ok || !valid || months == 0 || due.Before(expected) || (svc.Status != "Active" && svc.Status != "active" && svc.Status != "Suspended" && svc.Status != "suspended") {
		return out, svc, failure("renewal_due_unconfirmed", true, out)
	}
	out.Status, out.Domain, out.NextDueDate = svc.Status, svc.Domain, svc.NextDueDate
	return out, svc, nil
}

func (z *zjmf) Renew(ctx context.Context, r RenewalRequest, checkpoint func(CreateResult) error) (CreateResult, Service, error) {
	out := CreateResult{UpstreamID: r.UpstreamID, ProductID: r.ProductID, Currency: r.Currency, CostCents: r.CostCents}
	quote, err := z.QuoteRenewal(ctx, r)
	if err != nil {
		return out, Service{}, err
	}
	if quote.CostCents != r.CostCents || quote.PreviousDue != r.PreviousDue {
		return out, Service{}, &Error{Code: "renewal_quote_changed"}
	}
	token, err := z.login(ctx)
	if err != nil {
		return out, Service{}, err
	}
	w, err := z.post(ctx, token, "host/renew", url.Values{"hostid": {strconv.FormatInt(r.UpstreamID, 10)}, "billingcycles": {r.Cycle}})
	if err != nil {
		return out, Service{}, err
	}
	// This endpoint uses invoice_id, whereas cart/settle uses invoiceid.
	var data struct {
		InvoiceID json.RawMessage `json:"invoice_id"`
	}
	if json.Unmarshal(w.Data, &data) != nil {
		return out, Service{}, failure("renewal_invoice_missing", true, out)
	}
	_, id, idErr := purchaseIDs(w)
	alternate, valid := purchaseID(data.InvoiceID)
	if idErr != nil || !valid || (id > 0 && alternate > 0 && id != alternate) {
		return out, Service{}, failure("renewal_invoice_missing", true, out)
	}
	if id == 0 {
		id = alternate
	}
	out.InvoiceID = id
	if id < 1 {
		return out, Service{}, failure("renewal_invoice_missing", true, out)
	}
	if checkpoint == nil || checkpoint(out) != nil {
		return out, Service{}, failure("renewal_checkpoint_failed", true, out)
	}
	_, status, err := z.renewalInvoice(ctx, r, id)
	if err != nil {
		return out, Service{}, err
	}
	if status != "Paid" {
		// Check ownership again immediately before spending, and never use credit limits.
		svc, readErr := z.Sync(ctx, r.UpstreamID)
		if readErr != nil || svc.ProductID != r.ProductID {
			return out, Service{}, failure("renewal_resource_unconfirmed", true, out)
		}
		_, payErr := z.post(ctx, token, "apply_credit", url.Values{"invoiceid": {strconv.FormatInt(id, 10)}, "use_credit": {"1"}, "enough": {"1"}})
		confirmed, billStatus, readErr := z.renewalInvoice(ctx, r, id)
		if readErr != nil {
			return out, Service{}, failure("renewal_payment_unconfirmed", true, out)
		}
		if billStatus != "Paid" {
			var unknown bool = true
			if e, ok := payErr.(*Error); ok && !e.Unknown {
				unknown = false
			}
			return confirmed, Service{}, failure("renewal_payment_unconfirmed", unknown, confirmed)
		}
	}
	return z.ConfirmRenewal(ctx, r, id)
}
