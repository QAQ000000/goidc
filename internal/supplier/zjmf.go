package supplier

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/netip"
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

func newZJMF(account Account) (Adapter, error) {
	if account.Provider != ProviderZJMF || account.Username == "" || account.Password == "" {
		return nil, &Error{Code: "invalid_account"}
	}
	u, err := url.Parse(account.BaseURL)
	if err != nil || u == nil {
		return nil, &Error{Code: "invalid_url"}
	}
	ip, isIP := netip.ParseAddr(u.Hostname())
	localHTTP := u.Hostname() == "localhost" || (isIP == nil && (ip.IsLoopback() || ip.IsPrivate()))
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localHTTP)) || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, "\\\r\n") {
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
	User         json.RawMessage `json:"user"`
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
			ID              int64           `json:"id"`
			Name            string          `json:"name"`
			Description     string          `json:"description"`
			StockControl    int             `json:"stock_control"`
			Qty             int64           `json:"qty"`
			PayType         json.RawMessage `json:"pay_type"`
			Hidden          json.RawMessage `json:"hidden"`
			Retired         json.RawMessage `json:"retired"`
			Host            json.RawMessage `json:"host"`
			Password        json.RawMessage `json:"password"`
			LocationVersion json.RawMessage `json:"location_version"`
		} `json:"products"`
		ProductPricings []map[string]json.RawMessage `json:"product_pricings"`
		ConfigGroups    json.RawMessage              `json:"config_groups"`
		CustomFields    json.RawMessage              `json:"customfields"`
		Advanced        json.RawMessage              `json:"advanced"`
	}
	if json.Unmarshal(w.Data, &d) != nil || d.Products.ID != id {
		return Product{}, &Error{Code: "product_invalid"}
	}
	p := Product{ID: id, Name: d.Products.Name, Description: d.Products.Description, SupplierVersion: number(d.Products.LocationVersion), Prices: map[string]Price{}, Options: []Option{}, Fields: []Field{}, Supported: true}
	payType, validPayType := productRule(d.Products.PayType, "pay_type")
	if !validPayType || (payType != "recurring" && payType != "onetime") {
		p.Supported, p.UnsupportedReason = false, "unsupported_payment_type"
	}
	if number(d.Products.Hidden) != 0 || number(d.Products.Retired) != 0 {
		p.Supported, p.UnsupportedReason = false, "product_unavailable"
	}
	for _, rule := range []json.RawMessage{d.Products.Host, d.Products.Password} {
		show, valid := productRule(rule, "show")
		if len(rule) > 0 && (!valid || (show != "" && show != "0")) {
			p.Supported, p.UnsupportedReason = false, "unsupported_host_credentials"
		}
	}
	if p.SupplierVersion < 1 {
		p.Supported = false
		p.UnsupportedReason = "supplier_version_unknown"
	}
	if d.Products.StockControl == 1 {
		qty := d.Products.Qty
		p.Stock = &qty
	}
	if len(d.Advanced) > 0 && string(d.Advanced) != "[]" && string(d.Advanced) != "null" && string(d.Advanced) != "{}" {
		p.Supported = false
		p.UnsupportedReason = "advanced_configuration"
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
		for cycle, keys := range map[string][2]string{"monthly": {"monthly", "msetupfee"}, "quarterly": {"quarterly", "qsetupfee"}, "annually": {"annually", "asetupfee"}, "once": {"onetime", "osetupfee"}} {
			if (payType == "onetime" && cycle != "once") || (payType == "recurring" && cycle == "once") {
				continue
			}
			c, ok := moneyRaw(r[keys[0]])
			if !ok || c < 0 {
				continue
			}
			setup, valid := setupMoney(r[keys[1]])
			if !valid {
				p.Supported, p.UnsupportedReason = false, "invalid_setup_price"
				continue
			}
			p.Prices[cycle] = Price{AmountCents: c, SetupCents: setup}
		}
	}
	var groups []struct {
		Options []struct {
			ID           int64  `json:"id"`
			Name         string `json:"option_name"`
			Type         int    `json:"option_type"`
			QtyMinimum   int64  `json:"qty_minimum"`
			QtyMaximum   int64  `json:"qty_maximum"`
			QtyStage     int    `json:"qty_stage"`
			LinkagePID   int64  `json:"linkage_pid"`
			LinkageTop   int64  `json:"linkage_top_pid"`
			LinkageLevel string `json:"linkage_level"`
			Hidden       int    `json:"hidden"`
			Sub          []struct {
				ID         int64                        `json:"id"`
				Name       string                       `json:"option_name"`
				QtyMinimum int64                        `json:"qty_minimum"`
				QtyMaximum int64                        `json:"qty_maximum"`
				Pricings   []map[string]json.RawMessage `json:"pricings"`
			} `json:"sub"`
		} `json:"options"`
	}
	if len(d.ConfigGroups) > 0 && json.Unmarshal(d.ConfigGroups, &groups) != nil {
		p.Supported = false
		p.UnsupportedReason = "option_schema_invalid"
	}
	for _, group := range groups {
		for _, raw := range group.Options {
			o := Option{ID: raw.ID, Name: raw.Name, Type: "select", Required: true}
			// 魔方 DCIM 商品将固定规格暴露为专用 option_type（例如
			// CPU、内存、磁盘和带宽），但其 sub/pricings 仍是普通单选。
			// 只有没有数量区间、联动或隐藏状态的规格才可安全归一化；
			// 可变数量、阶梯价格和高级联动仍保持拒绝。
			supportedType := raw.Type == 1 || raw.Type == 2 || raw.Type == 4 || raw.Type == 5 || raw.Type == 6 || raw.Type == 8 || raw.Type == 11 || raw.Type == 12 || raw.Type == 13 || raw.Type == 14
			fixedQuantity := raw.QtyMinimum == 0 && raw.QtyMaximum == 0 && raw.QtyStage == 0 || raw.QtyMinimum > 0 && raw.QtyMinimum == raw.QtyMaximum && raw.QtyStage == 1 && len(raw.Sub) == 1
			variableDisk := raw.Type == 14 && raw.QtyMinimum == 0 && raw.QtyMaximum > 0 && raw.QtyStage == 1 && len(raw.Sub) == 1 && raw.Sub[0].QtyMinimum == 0 && raw.Sub[0].QtyMaximum == raw.QtyMaximum
			if raw.ID <= 0 || !supportedType || (!fixedQuantity && !variableDisk) || raw.Hidden != 0 || raw.LinkagePID != 0 || raw.LinkageTop != 0 || (raw.LinkageLevel != "" && raw.LinkageLevel != "0") || len(raw.Sub) == 0 {
				p.Supported = false
				p.UnsupportedReason = "unsupported_option_type"
				continue
			}
			if variableDisk {
				o.Type = "quantity"
			}
			for _, sub := range raw.Sub {
				c := Choice{ID: sub.ID, Name: sub.Name, MinQty: sub.QtyMinimum, MaxQty: sub.QtyMaximum, Prices: map[string]Price{}}
				if c.ID <= 0 {
					p.Supported = false
					p.UnsupportedReason = "option_schema_invalid"
				}
				for _, price := range sub.Pricings {
					var code string
					_ = json.Unmarshal(price["code"], &code)
					if code != p.Currency {
						continue
					}
					for cycle, keys := range map[string][2]string{"monthly": {"monthly", "msetupfee"}, "quarterly": {"quarterly", "qsetupfee"}, "annually": {"annually", "asetupfee"}, "once": {"onetime", "osetupfee"}} {
						if _, available := p.Prices[cycle]; !available {
							continue
						}
						amount, ok := moneyRaw(price[keys[0]])
						if !ok || amount < 0 {
							continue
						}
						setup, valid := setupMoney(price[keys[1]])
						if !valid {
							p.Supported, p.UnsupportedReason = false, "invalid_setup_price"
							continue
						}
						c.Prices[cycle] = Price{AmountCents: amount, SetupCents: setup}
					}
				}
				if len(c.Prices) != len(p.Prices) {
					p.Supported = false
					p.UnsupportedReason = "option_price_missing"
				}
				o.Choices = append(o.Choices, c)
			}
			p.Options = append(p.Options, o)
		}
	}
	var fields []struct {
		ID          int64  `json:"id"`
		Name        string `json:"fieldname"`
		Description string `json:"description"`
		Type        string `json:"fieldtype"`
		Options     string `json:"fieldoptions"`
		Regex       string `json:"regexpr"`
		Required    int    `json:"required"`
	}
	if len(d.CustomFields) > 0 && json.Unmarshal(d.CustomFields, &fields) != nil {
		p.Supported = false
		p.UnsupportedReason = "field_schema_invalid"
	}
	for _, raw := range fields {
		f := Field{ID: raw.ID, Name: raw.Name, Description: raw.Description, Type: raw.Type, Required: raw.Required == 1}
		if raw.ID <= 0 || raw.Regex != "" || (raw.Type != "text" && raw.Type != "dropdown") {
			p.Supported = false
			p.UnsupportedReason = "unsupported_field_type"
			continue
		}
		if raw.Type == "dropdown" {
			for _, choice := range strings.Split(raw.Options, ",") {
				choice = strings.TrimSpace(choice)
				if choice != "" {
					f.Choices = append(f.Choices, choice)
				}
			}
			if len(f.Choices) == 0 {
				p.Supported = false
				p.UnsupportedReason = "field_schema_invalid"
			}
		}
		p.Fields = append(p.Fields, f)
	}
	if p.Currency != "CNY" || len(p.Prices) == 0 {
		p.Supported = false
		p.UnsupportedReason = "unsupported_currency_or_price"
	}
	stable, _ := json.Marshal(struct {
		ID, SupplierVersion int64
		Currency            string
		Prices              map[string]Price
		Options             []Option
		Fields              []Field
	}{p.ID, p.SupplierVersion, p.Currency, p.Prices, p.Options, p.Fields})
	h := sha256.Sum256(stable)
	p.Revision = hex.EncodeToString(h[:])
	return p, nil
}
func money(s string) (int64, bool) {
	s = strings.TrimSpace(s)
	parts := strings.Split(s, ".")
	if len(parts) > 2 || len(parts[0]) == 0 {
		return 0, false
	}
	for _, part := range parts {
		for _, char := range part {
			if char < '0' || char > '9' {
				return 0, false
			}
		}
	}
	whole, e := strconv.ParseInt(parts[0], 10, 64)
	if e != nil || whole < 0 || whole > math.MaxInt64/100 {
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
	if whole > (math.MaxInt64-frac)/100 {
		return 0, false
	}
	return whole*100 + frac, true
}

func setupMoney(raw json.RawMessage) (int64, bool) {
	if len(raw) == 0 {
		return 0, true
	}
	return moneyRaw(raw)
}

// Legacy product rules may be JSON objects or JSON strings containing objects.
func productRule(raw json.RawMessage, key string) (string, bool) {
	var encoded string
	if json.Unmarshal(raw, &encoded) == nil {
		raw = json.RawMessage(encoded)
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return "", false
	}
	value := fields[key]
	if len(value) == 0 {
		return "", true
	}
	var result string
	if json.Unmarshal(value, &result) != nil {
		result = string(value)
	}
	return result, true
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
	cost, selectionErr := ValidateSelection(p, r.Cycle, r.ConfigOptions, r.CustomFields)
	if selectionErr != nil || cost > r.MaximumCostCents {
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
		Currency json.RawMessage `json:"currency"`
	}
	_ = json.Unmarshal(user.User, &ud)
	currencyID := number(ud.Currency)
	if currencyID <= 0 {
		return out, failure("currency_unknown", false, out)
	}
	common := url.Values{"downstream_url": {r.DownstreamURL}, "downstream_token": {r.DownstreamToken}, "downstream_id": {strconv.FormatInt(r.LocalServiceID, 10)}}
	clear, e := z.post(ctx, token, "cart/clear", common)
	if e != nil {
		return out, e
	}
	out.UpstreamID, out.InvoiceID = ids(clear)
	if out.UpstreamID == 0 && out.InvoiceID == 0 {
		upstreamCycle := r.Cycle
		if upstreamCycle == "once" {
			upstreamCycle = "onetime"
		}
		cart := url.Values{"pid": {strconv.FormatInt(r.ProductID, 10)}, "billingcycle": {upstreamCycle}, "currencyid": {strconv.FormatInt(currencyID, 10)}, "qty": {"1"}, "supplier_version": {strconv.FormatInt(p.SupplierVersion, 10)}, "host": {r.Host}, "password": {r.Password}}
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
		InvoiceItems []struct {
			Type  string          `json:"type"`
			RelID json.RawMessage `json:"rel_id"`
		} `json:"invoice_items"`
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
	hostItems := 0
	for _, item := range invoice.InvoiceItems {
		if number(item.RelID) != out.UpstreamID || out.UpstreamID < 1 {
			return out, failure("invoice_host_mismatch", true, out)
		}
		switch item.Type {
		case "host":
			hostItems++
		case "setup", "discount", "promo":
		default:
			return out, failure("invoice_host_mismatch", true, out)
		}
	}
	if hostItems != 1 {
		return out, failure("invoice_host_mismatch", true, out)
	}
	out.CostCents = cost
	out.Currency = "CNY"
	if invoice.Detail.Status != "Unpaid" && invoice.Detail.Status != "Paid" {
		return out, failure("invoice_status_invalid", true, out)
	}
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
		if host != out.UpstreamID {
			return out, failure("payment_host_mismatch", true, out)
		}
		if w.Status != 1001 {
			return out, failure("payment_not_confirmed", true, out)
		}
	}
	out.Status = "provisioning"
	if out.UpstreamID > 0 {
		svc, e := z.Sync(ctx, out.UpstreamID)
		if e != nil {
			return out, failure("resource_unconfirmed", true, out)
		}
		if e == nil {
			if svc.ProductID != r.ProductID {
				return out, failure("resource_product_mismatch", true, out)
			}
			out.ProductID, out.Domain, out.NextDueDate = svc.ProductID, svc.Domain, svc.NextDueDate
			if svc.Status == "Active" {
				out.Status = "active"
			}
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
			ID           int64           `json:"id"`
			ProductID    int64           `json:"productid"`
			DomainStatus string          `json:"domainstatus"`
			Domain       string          `json:"domain"`
			NextDueDate  json.RawMessage `json:"nextduedate"`
		} `json:"hosts"`
	}
	if json.Unmarshal(w.Data, &d) != nil {
		return s, &Error{Code: "service_invalid"}
	}
	for _, v := range d.Hosts {
		if v.ID == id {
			var due string
			if json.Unmarshal(v.NextDueDate, &due) != nil {
				due = strings.TrimSpace(string(v.NextDueDate))
			}
			return Service{UpstreamID: id, ProductID: v.ProductID, Status: v.DomainStatus, Domain: v.Domain, NextDueDate: due}, nil
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
