package supplier

import (
	"context"
	"encoding/json"
	"net/url"
	"strconv"
)

func purchaseID(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, true
	}
	if raw[0] == '[' {
		var ids []json.RawMessage
		if json.Unmarshal(raw, &ids) != nil || len(ids) != 1 {
			return 0, false
		}
		raw = ids[0]
	}
	id := number(raw)
	return id, id > 0
}

// Accept scalar/single-array IDs at either legacy response location, but never
// select the first resource from a multi-resource or contradictory response.
func purchaseIDs(w wire) (int64, int64, error) {
	var data struct {
		HostID    json.RawMessage `json:"hostid"`
		InvoiceID json.RawMessage `json:"invoiceid"`
	}
	if len(w.Data) > 0 && string(w.Data) != "null" && json.Unmarshal(w.Data, &data) != nil {
		return 0, 0, &Error{Code: "purchase_identity_invalid", Unknown: true}
	}
	var result [2]int64
	for i, pair := range [][2]json.RawMessage{{w.HostID, data.HostID}, {w.InvoiceID, data.InvoiceID}} {
		a, okA := purchaseID(pair[0])
		b, okB := purchaseID(pair[1])
		if !okA || !okB || (a > 0 && b > 0 && a != b) {
			return 0, 0, &Error{Code: "purchase_identity_invalid", Unknown: true}
		}
		result[i] = a
		if result[i] == 0 {
			result[i] = b
		}
	}
	return result[0], result[1], nil
}

type purchaseInvoice struct {
	Detail struct {
		ID       json.RawMessage `json:"id"`
		Subtotal json.RawMessage `json:"subtotal"`
		Total    json.RawMessage `json:"total"`
		Status   string          `json:"status"`
	} `json:"detail"`
	Currency struct {
		Code string `json:"code"`
	} `json:"currency"`
	Items []struct {
		Type  string          `json:"type"`
		RelID json.RawMessage `json:"rel_id"`
	} `json:"invoice_items"`
}

func (z *zjmf) purchaseInvoice(ctx context.Context, out *CreateResult, maximum int64) (purchaseInvoice, error) {
	var invoice purchaseInvoice
	detail, err := z.get(ctx, "get_invoices_detail", url.Values{"id": {strconv.FormatInt(out.InvoiceID, 10)}})
	if err != nil {
		return invoice, failure("invoice_unavailable", true, *out)
	}
	if json.Unmarshal(detail.Data, &invoice) != nil || number(invoice.Detail.ID) != out.InvoiceID {
		return invoice, failure("invoice_invalid", true, *out)
	}
	cost, valid := moneyRaw(invoice.Detail.Subtotal)
	if !valid || invoice.Currency.Code != "CNY" || cost > maximum {
		return invoice, failure("invoice_price_mismatch", true, *out)
	}
	if len(invoice.Detail.Total) > 0 {
		total, ok := moneyRaw(invoice.Detail.Total)
		if !ok || total > cost {
			return invoice, failure("invoice_price_mismatch", true, *out)
		}
	}
	host, hostItems := int64(0), 0
	for _, item := range invoice.Items {
		if item.Type == "host" {
			host = number(item.RelID)
			hostItems++
		}
	}
	if hostItems != 1 || host < 1 || (out.UpstreamID > 0 && out.UpstreamID != host) {
		return invoice, failure("invoice_host_mismatch", true, *out)
	}
	for _, item := range invoice.Items {
		if number(item.RelID) != host {
			return invoice, failure("invoice_host_mismatch", true, *out)
		}
		switch item.Type {
		case "host", "setup", "discount", "promo":
		default:
			return invoice, failure("invoice_host_mismatch", true, *out)
		}
	}
	if invoice.Detail.Status != "Unpaid" && invoice.Detail.Status != "Paid" {
		return invoice, failure("invoice_status_invalid", true, *out)
	}
	out.UpstreamID, out.CostCents, out.Currency = host, cost, "CNY"
	return invoice, nil
}
