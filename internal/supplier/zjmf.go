package supplier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type zjmf struct {
	account Account
	base    *url.URL
	client  *http.Client
}

func New(account Account) (Adapter, error) {
	if account.Provider != ProviderZJMF || account.Username == "" || account.Password == "" {
		return nil, &Error{Code: "invalid_account"}
	}
	u, err := url.Parse(account.BaseURL)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && (u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost"))) || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, "\\\r\n") {
		return nil, &Error{Code: "invalid_url"}
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &zjmf{account: account, base: u, client: &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (z *zjmf) Capabilities() Capabilities {
	return Capabilities{Catalog: true, Product: true, Create: true, Sync: true}
}
func (z *zjmf) endpoint(path string) string {
	u := *z.base
	u.Path = strings.TrimRight(u.Path, "/") + "/" + path
	return u.String()
}

type wire struct {
	Status       int             `json:"status"`
	JWT          string          `json:"jwt"`
	Data         json.RawMessage `json:"data"`
	HostID       json.RawMessage `json:"hostid"`
	InvoiceID    json.RawMessage `json:"invoiceid"`
	DomainStatus string          `json:"domainstatus"`
}

func (z *zjmf) call(ctx context.Context, token, method, path string, form url.Values, write bool) (wire, error) {
	var out wire
	target := z.endpoint(path)
	var body io.Reader
	if method == http.MethodGet {
		if len(form) > 0 {
			target += "?" + form.Encode()
		}
	} else {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return out, &Error{Code: "request_invalid"}
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := z.client.Do(req)
	if err != nil {
		return out, &Error{Code: "transport", Unknown: write}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, &Error{Code: "http_status", Unknown: write}
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20+1))
	if err != nil || len(b) > 2<<20 {
		return out, &Error{Code: "response_unavailable", Unknown: write}
	}
	if json.Unmarshal(b, &out) != nil {
		return out, &Error{Code: "response_invalid", Unknown: write}
	}
	if out.Status != 200 && !(write && out.Status == 1001) {
		if write && out.Status == 400 && path == "cart/clear" && (number(out.HostID) > 0 || number(out.InvoiceID) > 0) {
			return out, nil
		}
		return out, &Error{Code: "upstream_rejected", Unknown: write}
	}
	return out, nil
}
func (z *zjmf) login(ctx context.Context) (string, error) {
	w, e := z.call(ctx, "", http.MethodPost, "zjmf_api_login", url.Values{"username": {z.account.Username}, "password": {z.account.Password}}, false)
	if e != nil {
		return "", e
	}
	if w.JWT == "" {
		return "", &Error{Code: "login_invalid"}
	}
	return w.JWT, nil
}
func (z *zjmf) get(ctx context.Context, path string, form url.Values) (wire, error) {
	token, e := z.login(ctx)
	if e != nil {
		return wire{}, e
	}
	return z.call(ctx, token, http.MethodGet, path, form, false)
}
func (z *zjmf) post(ctx context.Context, token, path string, form url.Values) (wire, error) {
	return z.call(ctx, token, http.MethodPost, path, form, true)
}

func (z *zjmf) Catalog(ctx context.Context) ([]Product, error) {
	w, e := z.get(ctx, "cart/all", nil)
	if e != nil {
		return nil, e
	}
	var data struct {
		Products []struct {
			Products []struct {
				ID           int64  `json:"id"`
				Name         string `json:"name"`
				Description  string `json:"description"`
				StockControl int    `json:"stock_control"`
				Qty          int64  `json:"qty"`
			} `json:"products"`
		} `json:"products"`
	}
	if json.Unmarshal(w.Data, &data) != nil {
		return nil, &Error{Code: "catalog_invalid"}
	}
	var result []Product
	for _, g := range data.Products {
		for _, p := range g.Products {
			if p.ID <= 0 {
				continue
			}
			item := Product{ID: p.ID, Name: p.Name, Description: p.Description, Currency: "", Supported: false, UnsupportedReason: "detail_required"}
			if p.StockControl == 1 {
				qty := p.Qty
				item.Stock = &qty
			}
			result = append(result, item)
		}
	}
	return result, nil
}
func (z *zjmf) Product(ctx context.Context, id int64) (Product, error) {
	if id <= 0 {
		return Product{}, &Error{Code: "invalid_product"}
	}
	w, e := z.get(ctx, "cart/get_product_config", url.Values{"pid": {strconv.FormatInt(id, 10)}})
	if e != nil {
		return Product{}, e
	}
	var d struct {
		Products struct {
			ID           int64  `json:"id"`
			Name         string `json:"name"`
			Description  string `json:"description"`
			StockControl int    `json:"stock_control"`
			Qty          int64  `json:"qty"`
			PayType      string `json:"pay_type"`
		} `json:"products"`
		ProductPricings []map[string]json.RawMessage `json:"product_pricings"`
		ConfigGroups    json.RawMessage              `json:"config_groups"`
		CustomFields    json.RawMessage              `json:"customfields"`
		Advanced        json.RawMessage              `json:"advanced"`
	}
	if json.Unmarshal(w.Data, &d) != nil || d.Products.ID != id {
		return Product{}, &Error{Code: "product_invalid"}
	}
	h := sha256.Sum256(w.Data)
	p := Product{ID: id, Name: d.Products.Name, Description: d.Products.Description, Revision: hex.EncodeToString(h[:]), Prices: map[string]Price{}, Supported: true}
	if d.Products.StockControl == 1 {
		qty := d.Products.Qty
		p.Stock = &qty
	}
	if len(d.ConfigGroups) > 2 && string(d.ConfigGroups) != "[]" || len(d.CustomFields) > 2 && string(d.CustomFields) != "[]" || len(d.Advanced) > 2 && string(d.Advanced) != "[]" {
		p.Supported = false
		p.UnsupportedReason = "complex_configuration"
	}
	for _, r := range d.ProductPricings {
		var code string
		json.Unmarshal(r["code"], &code)
		if p.Currency == "" {
			p.Currency = code
		}
		if code != p.Currency {
			p.Supported = false
			p.UnsupportedReason = "multiple_currencies"
			continue
		}
		for _, cycle := range []string{"monthly", "quarterly", "semiannually", "annually", "biennially", "triennially", "onetime"} {
			c, ok := moneyRaw(r[cycle])
			if !ok || c < 0 {
				continue
			}
			setup, _ := moneyRaw(r["msetupfee"])
			p.Prices[cycle] = Price{AmountCents: c, SetupCents: setup}
		}
	}
	if p.Currency != "CNY" || len(p.Prices) == 0 {
		p.Supported = false
		p.UnsupportedReason = "unsupported_currency_or_price"
	}
	return p, nil
}
func money(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, false
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole < 0 || whole > 92233720368547757 {
		return 0, false
	}
	frac := int64(0)
	if len(parts) == 2 {
		if len(parts[1]) < 1 || len(parts[1]) > 2 {
			return 0, false
		}
		f, e := strconv.ParseInt(parts[1], 10, 64)
		if e != nil || f < 0 {
			return 0, false
		}
		frac = f
		if len(parts[1]) == 1 {
			frac *= 10
		}
	}
	return whole*100 + frac, true
}
func moneyRaw(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		s = string(raw)
	}
	return money(s)
}
func number(raw json.RawMessage) int64 {
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		i, _ := n.Int64()
		return i
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		i, _ := strconv.ParseInt(s, 10, 64)
		return i
	}
	return 0
}
func ids(w wire) (int64, int64) {
	host := number(w.HostID)
	invoice := number(w.InvoiceID)
	var d struct {
		HostID    []json.RawMessage `json:"hostid"`
		InvoiceID json.RawMessage   `json:"invoiceid"`
	}
	json.Unmarshal(w.Data, &d)
	if host == 0 && len(d.HostID) > 0 {
		host = number(d.HostID[0])
	}
	if invoice == 0 {
		invoice = number(d.InvoiceID)
	}
	return host, invoice
}
func failure(code string, unknown bool, r CreateResult) error {
	return &Error{Code: code, Unknown: unknown, UpstreamID: r.UpstreamID, InvoiceID: r.InvoiceID}
}
func (z *zjmf) Create(ctx context.Context, r CreateRequest) (CreateResult, error) {
	var out CreateResult
	downstream, parseErr := url.Parse(r.DownstreamURL)
	if r.ProductID <= 0 || r.LocalServiceID <= 0 || len(r.DownstreamToken) != 32 || parseErr != nil || downstream.Host == "" || downstream.User != nil || downstream.RawQuery != "" || downstream.Fragment != "" || (downstream.Scheme != "https" && downstream.Scheme != "http") || r.Currency != "CNY" || r.MaximumCostCents < 0 || r.Cycle == "" {
		return out, failure("invalid_create", false, out)
	}
	p, e := z.Product(ctx, r.ProductID)
	if e != nil {
		return out, e
	}
	if !p.Supported || p.Revision != r.Revision {
		return out, failure("product_changed_or_unsupported", false, out)
	}
	price, ok := p.Prices[r.Cycle]
	if !ok || price.AmountCents+price.SetupCents > r.MaximumCostCents {
		return out, failure("price_exceeded", false, out)
	}
	token, e := z.login(ctx)
	if e != nil {
		return out, e
	}
	user, e := z.call(ctx, token, http.MethodGet, "user_info", nil, false)
	if e != nil {
		return out, e
	}
	var ud struct {
		Currency int64 `json:"currency"`
	}
	_ = json.Unmarshal(user.Data, &ud)
	if ud.Currency <= 0 {
		return out, failure("currency_unknown", false, out)
	}
	common := url.Values{"downstream_url": {r.DownstreamURL}, "downstream_token": {r.DownstreamToken}, "downstream_id": {strconv.FormatInt(r.LocalServiceID, 10)}}
	clear, e := z.post(ctx, token, "cart/clear", common)
	if e != nil {
		return out, e
	}
	out.UpstreamID, out.InvoiceID = ids(clear)
	if clear.Status == 400 && out.UpstreamID > 0 && clear.DomainStatus == "Active" {
		return CreateResult{UpstreamID: out.UpstreamID, Status: "active"}, nil
	}
	if out.UpstreamID == 0 && out.InvoiceID == 0 {
		cart := url.Values{"pid": {strconv.FormatInt(r.ProductID, 10)}, "billingcycle": {r.Cycle}, "currencyid": {strconv.FormatInt(ud.Currency, 10)}, "qty": {"1"}, "supplier_version": {"0"}, "host": {r.Host}, "password": {r.Password}}
		for k, v := range common {
			cart[k] = v
		}
		for k, v := range r.ConfigOptions {
			cart.Set("configoption["+strconv.FormatInt(k, 10)+"]", v)
		}
		for k, v := range r.CustomFields {
			cart.Set("customfield["+strconv.FormatInt(k, 10)+"]", v)
		}
		if _, e = z.post(ctx, token, "cart/add_to_shop", cart); e != nil {
			return out, failure("cart_add_failed", true, out)
		}
		settle := url.Values{}
		for k, v := range common {
			settle[k] = v
		}
		for k, v := range cart {
			if strings.HasPrefix(k, "configoption[") {
				k = "configoptions" + strings.TrimPrefix(k, "configoption")
			}
			if i := strings.IndexByte(k, '['); i >= 0 {
				settle.Set("cart_data["+k[:i]+"]"+k[i:], v[0])
			} else {
				settle.Set("cart_data["+k+"]", v[0])
			}
		}
		w, e := z.post(ctx, token, "cart/settle", settle)
		if e != nil {
			return out, failure("settlement_unknown", true, out)
		}
		out.UpstreamID, out.InvoiceID = ids(w)
	}
	if out.InvoiceID <= 0 {
		return out, failure("invoice_missing", true, out)
	}
	detail, e := z.get(ctx, "get_invoices_detail", url.Values{"id": {strconv.FormatInt(out.InvoiceID, 10)}})
	if e != nil {
		return out, failure("invoice_unavailable", true, out)
	}
	var invoice struct {
		Detail struct {
			ID       int64           `json:"id"`
			Subtotal json.RawMessage `json:"subtotal"`
			Status   string          `json:"status"`
		} `json:"detail"`
		Currency struct {
			Code string `json:"code"`
		} `json:"currency"`
	}
	if json.Unmarshal(detail.Data, &invoice) != nil || invoice.Detail.ID != out.InvoiceID {
		return out, failure("invoice_invalid", true, out)
	}
	var amount string
	if json.Unmarshal(invoice.Detail.Subtotal, &amount) != nil {
		amount = string(invoice.Detail.Subtotal)
	}
	cost, valid := money(amount)
	if !valid || invoice.Currency.Code != "CNY" || cost > r.MaximumCostCents {
		return out, failure("invoice_price_mismatch", true, out)
	}
	out.CostCents = cost
	out.Currency = "CNY"
	if invoice.Detail.Status != "Paid" {
		pay := url.Values{"invoiceid": {strconv.FormatInt(out.InvoiceID, 10)}, "use_credit": {"1"}, "enough": {"0"}}
		for k, v := range common {
			pay[k] = v
		}
		w, e := z.post(ctx, token, "apply_credit", pay)
		if e != nil {
			return out, failure("payment_unknown", true, out)
		}
		host, _ := ids(w)
		if host > 0 {
			out.UpstreamID = host
		}
		if w.Status != 1001 {
			return out, failure("payment_not_confirmed", true, out)
		}
	}
	out.Status = "provisioning"
	if out.UpstreamID > 0 {
		svc, e := z.Sync(ctx, out.UpstreamID)
		if e == nil && svc.Status == "Active" {
			out.Status = "active"
		}
	}
	return out, nil
}
func (z *zjmf) Sync(ctx context.Context, id int64) (Service, error) {
	var s Service
	if id <= 0 {
		return s, &Error{Code: "invalid_service"}
	}
	w, e := z.get(ctx, "cart/hostinfo", url.Values{"hostid[0]": {strconv.FormatInt(id, 10)}})
	if e != nil {
		return s, e
	}
	var d struct {
		Hosts []struct {
			ID           int64  `json:"id"`
			DomainStatus string `json:"domainstatus"`
			Domain       string `json:"domain"`
			NextDueDate  string `json:"nextduedate"`
		} `json:"hosts"`
	}
	if json.Unmarshal(w.Data, &d) != nil {
		return s, &Error{Code: "service_invalid"}
	}
	for _, v := range d.Hosts {
		if v.ID == id {
			return Service{UpstreamID: id, Status: v.DomainStatus, Domain: v.Domain, NextDueDate: v.NextDueDate}, nil
		}
	}
	return s, &Error{Code: "service_missing"}
}
func (z *zjmf) Execute(ctx context.Context, action string, id int64) (Service, error) {
	if action == "sync" {
		return z.Sync(ctx, id)
	}
	return Service{}, &Error{Code: "unsupported_action"}
}

var _ Adapter = (*zjmf)(nil)
