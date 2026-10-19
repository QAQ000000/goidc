package supplier

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"path"
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
	u, err := zjmfBaseURL(account.BaseURL)
	if err != nil {
		return nil, err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	return &zjmf{account: account, base: u, client: &http.Client{Transport: tr, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func zjmfBaseURL(base string) (*url.URL, error) {
	u, err := url.Parse(base)
	if err != nil || u == nil {
		return nil, &Error{Code: "invalid_url"}
	}
	ip, isIP := netip.ParseAddr(u.Hostname())
	if isIP == nil {
		ip = ip.Unmap()
	}
	localHTTP := u.Hostname() == "localhost" || (isIP == nil && (ip.IsLoopback() || ip.IsPrivate()))
	if u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && !(u.Scheme == "http" && localHTTP)) || strings.Contains(u.Path, "..") || strings.ContainsAny(u.Path, "\\\r\n") {
		return nil, &Error{Code: "invalid_url"}
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if isIP == nil {
		host = ip.String()
	}
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return nil, &Error{Code: "invalid_url"}
		}
		port = strconv.Itoa(number)
	}
	if (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	} else {
		u.Host = host
	}
	// endpoint() appends a fixed path to Path, so raw escaping and trailing
	// slashes cannot create a distinct upstream account for lease purposes.
	if u.Path != "" {
		u.Path = path.Clean(u.Path)
	}
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath, u.ForceQuery = "", false
	return u, nil
}
func (z *zjmf) Capabilities() Capabilities {
	return Capabilities{Catalog: true, Product: true, Create: true, Sync: true, Power: true, ResetPassword: true, Rescue: true, VNC: true, Reinstall: true, Renew: true}
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
		return out, &Error{Code: "upstream_rejected"}
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
	if p.HostRule != nil {
		r.Host, e = p.HostRule.generate()
		if e != nil {
			return out, failure("credentials_unavailable", false, out)
		}
	}
	if p.PasswordRule != nil {
		r.Password, e = p.PasswordRule.generate()
		if e != nil {
			return out, failure("credentials_unavailable", false, out)
		}
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
	out.UpstreamID, out.InvoiceID, e = purchaseIDs(clear)
	if e != nil {
		return out, failure("purchase_identity_invalid", true, out)
	}
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
		out.UpstreamID, out.InvoiceID, e = purchaseIDs(w)
		if e != nil {
			return out, failure("purchase_identity_invalid", true, out)
		}
	}
	if out.InvoiceID <= 0 {
		return out, failure("invoice_missing", true, out)
	}
	invoice, e := z.purchaseInvoice(ctx, &out, r.MaximumCostCents)
	if e != nil {
		return out, e
	}
	// Check resource ownership before spending any supplier balance, including
	// when settlement returned only an invoice and the host came from its items.
	svc, e := z.Sync(ctx, out.UpstreamID)
	if e != nil || svc.ProductID != r.ProductID {
		return out, failure("resource_product_unconfirmed", true, out)
	}
	if invoice.Detail.Status != "Paid" {
		pay := url.Values{"invoiceid": {strconv.FormatInt(out.InvoiceID, 10)}, "use_credit": {"1"}, "enough": {"1"}}
		for k, v := range common {
			pay[k] = v
		}
		w, payErr := z.post(ctx, token, "apply_credit", pay)
		// A transport failure may have applied the payment. Resolve it by reading
		// this exact invoice; never send a second charge or create another order.
		if payErr == nil {
			host, bill, idErr := purchaseIDs(w)
			if idErr != nil || (host > 0 && host != out.UpstreamID) || (bill > 0 && bill != out.InvoiceID) {
				return out, failure("payment_host_mismatch", true, out)
			}
		}
		confirmed := out
		paidInvoice, readErr := z.purchaseInvoice(ctx, &confirmed, r.MaximumCostCents)
		if readErr != nil || confirmed.UpstreamID != out.UpstreamID || confirmed.CostCents != out.CostCents || paidInvoice.Detail.Status != "Paid" {
			return out, failure("payment_not_confirmed", true, out)
		}
	}
	out.Status = "provisioning"
	svc, e = z.Sync(ctx, out.UpstreamID)
	if e != nil {
		return out, failure("resource_unconfirmed", true, out)
	}
	if svc.ProductID != r.ProductID {
		return out, failure("resource_product_mismatch", true, out)
	}
	out.ProductID, out.Domain, out.NextDueDate = svc.ProductID, svc.Domain, svc.NextDueDate
	switch svc.Status {
	case "Active", "active":
		out.Status = "active"
	case "Pending", "pending", "provisioning":
	default:
		return out, failure("resource_status_unconfirmed", true, out)
	}
	return out, nil
}
func (z *zjmf) Sync(ctx context.Context, id int64) (Service, error) {
	if id <= 0 {
		return Service{}, &Error{Code: "invalid_service"}
	}
	// The legacy PHP client uses host/header for the authoritative resource
	// record. Some older installations expose only cart/hostinfo, so retain a
	// compatibility fallback for those deployments.
	if w, e := z.get(ctx, "host/header", url.Values{"host_id": {strconv.FormatInt(id, 10)}, "source": {"API"}}); e == nil {
		if service, ok := parseHostHeader(w.Data, id); ok {
			return service, nil
		}
	}
	w, e := z.get(ctx, "cart/hostinfo", url.Values{"hostid[0]": {strconv.FormatInt(id, 10)}})
	if e != nil {
		return Service{}, e
	}
	var d struct {
		Hosts []struct {
			ID           int64           `json:"id"`
			ProductID    int64           `json:"productid"`
			DomainStatus string          `json:"domainstatus"`
			Domain       string          `json:"domain"`
			NextDueDate  json.RawMessage `json:"nextduedate"`
			Username     string          `json:"username"`
			Password     string          `json:"password"`
			DedicatedIP  string          `json:"dedicatedip"`
			AssignedIPs  json.RawMessage `json:"assignedips"`
			Port         int             `json:"port"`
			OS           string          `json:"os"`
		} `json:"hosts"`
	}
	if json.Unmarshal(w.Data, &d) != nil {
		return Service{}, &Error{Code: "service_invalid"}
	}
	for _, v := range d.Hosts {
		if v.ID == id {
			return Service{UpstreamID: id, ProductID: v.ProductID, Status: v.DomainStatus, Domain: v.Domain, NextDueDate: rawDate(v.NextDueDate), Username: v.Username, Password: v.Password, DedicatedIP: v.DedicatedIP, AssignedIPs: rawIPs(v.AssignedIPs), Port: v.Port, OS: v.OS}, nil
		}
	}
	return Service{}, &Error{Code: "service_missing"}
}

func rawDate(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	return strings.TrimSpace(string(raw))
}

func parseHostHeader(raw json.RawMessage, id int64) (Service, bool) {
	var envelope struct {
		HostData struct {
			ID                int64           `json:"id"`
			ProductID         int64           `json:"productid"`
			DomainStatus      string          `json:"domainstatus"`
			Domain            string          `json:"domain"`
			NextDueDate       json.RawMessage `json:"nextduedate"`
			Username          string          `json:"username"`
			Password          string          `json:"password"`
			DedicatedIP       string          `json:"dedicatedip"`
			AssignedIPs       json.RawMessage `json:"assignedips"`
			Port              int             `json:"port"`
			OS                string          `json:"os"`
			OSURL             string          `json:"os_url"`
			SuspendReason     string          `json:"suspendreason"`
			SuspendReasonType string          `json:"suspendreason_type"`
		} `json:"host_data"`
	}
	if json.Unmarshal(raw, &envelope) != nil || envelope.HostData.ProductID < 1 || envelope.HostData.DomainStatus == "" {
		return Service{}, false
	}
	if envelope.HostData.ID > 0 && envelope.HostData.ID != id {
		return Service{}, false
	}
	v := envelope.HostData
	return Service{UpstreamID: id, ProductID: v.ProductID, Status: v.DomainStatus, Domain: v.Domain, NextDueDate: rawDate(v.NextDueDate), Username: v.Username, Password: v.Password, DedicatedIP: v.DedicatedIP, AssignedIPs: rawIPs(v.AssignedIPs), Port: v.Port, OS: v.OS, OSURL: v.OSURL, SuspendReason: v.SuspendReason, SuspendReasonType: v.SuspendReasonType}, true
}

// Magic Cube installations return assignedips as either a string or an array.
// Normalize both forms into the provider-neutral string field.
func rawIPs(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return value
	}
	var values []string
	if json.Unmarshal(raw, &values) == nil {
		out := values[:0]
		for _, item := range values {
			if strings.TrimSpace(item) != "" {
				out = append(out, strings.TrimSpace(item))
			}
		}
		return strings.Join(out, ",")
	}
	return ""
}
func (z *zjmf) Execute(ctx context.Context, request ActionRequest) (Service, error) {
	if request.UpstreamID <= 0 {
		return Service{}, &Error{Code: "invalid_service"}
	}
	if request.Action == "sync" {
		return z.Sync(ctx, request.UpstreamID)
	}
	funcName := map[string]string{
		"power_on": "on", "power_off": "off", "reboot": "reboot",
		"force_off": "hard_off", "force_reboot": "hard_reboot",
		"reset_password": "crack_pass", "rescue_system": "rescue_system", "reinstall": "reinstall", "exit_rescue_system": "exitRescue",
	}[request.Action]
	if funcName == "" {
		return Service{}, &Error{Code: "unsupported_action"}
	}
	form := url.Values{
		"id":     {strconv.FormatInt(request.UpstreamID, 10)},
		"func":   {funcName},
		"is_api": {"1"},
	}
	if request.Action == "reset_password" {
		if !ValidActionPassword(request.Password) {
			return Service{}, &Error{Code: "invalid_system_password"}
		}
		form.Set("password", request.Password)
		form.Set("other_user", "0")
		form.Set("user", "")
		form.Set("action", "")
	}
	if request.Action == "rescue_system" {
		if request.System != 1 && request.System != 2 {
			return Service{}, &Error{Code: "invalid_rescue_system"}
		}
		if !ValidActionPassword(request.Password) {
			return Service{}, &Error{Code: "invalid_rescue_password"}
		}
		form.Set("system", strconv.Itoa(request.System))
		form.Set("temp_pass", request.Password)
	}
	if request.Action == "reinstall" {
		if request.TemplateID < 1 {
			return Service{}, &Error{Code: "invalid_template"}
		}
		form.Set("os", strconv.FormatInt(request.TemplateID, 10))
		form.Set("format_data_disk", "0")
	}
	token, err := z.login(ctx)
	if err != nil {
		return Service{}, err
	}
	path := "provision/default"
	if request.Action == "exit_rescue_system" {
		path = "provision/custom/" + strconv.FormatInt(request.UpstreamID, 10)
	}
	if _, err = z.post(ctx, token, path, form); err != nil {
		return Service{}, err
	}
	remote, err := z.Sync(ctx, request.UpstreamID)
	if err != nil {
		return Service{}, &Error{Code: "action_sync_failed", Unknown: true, UpstreamID: request.UpstreamID}
	}
	if request.Action == "reset_password" && remote.Password != request.Password {
		return remote, &Error{Code: "password_confirmation_pending", Unknown: true, UpstreamID: request.UpstreamID}
	}
	if request.Action == "reinstall" {
		return remote, &Error{Code: "reinstall_confirmation_pending", Unknown: true, UpstreamID: request.UpstreamID}
	}
	return remote, nil
}

func (z *zjmf) Templates(ctx context.Context, id int64) ([]Template, error) {
	if id < 1 {
		return nil, &Error{Code: "invalid_service"}
	}
	w, err := z.get(ctx, "host/header", url.Values{"host_id": {strconv.FormatInt(id, 10)}, "source": {"API"}})
	if err != nil {
		return nil, err
	}
	var data struct {
		CloudOS []Template `json:"cloud_os"`
	}
	if json.Unmarshal(w.Data, &data) != nil {
		return nil, &Error{Code: "templates_invalid"}
	}
	if data.CloudOS == nil {
		data.CloudOS = []Template{}
	}
	seen := map[int64]bool{}
	for _, t := range data.CloudOS {
		if t.ID < 1 || t.ID > 2147483647 || strings.TrimSpace(t.Name) == "" || len(t.Name) > 120 || len(t.Group) > 80 || strings.ContainsAny(t.Name+t.Group, "\r\n\x00") || seen[t.ID] {
			return nil, &Error{Code: "templates_invalid"}
		}
		seen[t.ID] = true
	}
	return data.CloudOS, nil
}

func (z *zjmf) VNC(ctx context.Context, id int64) (string, error) {
	if id < 1 {
		return "", &Error{Code: "invalid_service"}
	}
	token, err := z.login(ctx)
	if err != nil {
		return "", err
	}
	w, err := z.post(ctx, token, "provision/default", url.Values{"id": {strconv.FormatInt(id, 10)}, "func": {"vnc"}, "is_api": {"1"}})
	if err != nil {
		return "", err
	}
	var data struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(w.Data, &data) != nil || !ValidConsoleURL(data.URL) {
		return "", &Error{Code: "console_url_invalid"}
	}
	return data.URL, nil
}

func ValidConsoleURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || strings.ContainsAny(raw, "\\\r\n\t ") {
		return false
	}
	ip, err := netip.ParseAddr(u.Hostname())
	local := u.Hostname() == "localhost" || (err == nil && (ip.IsLoopback() || ip.IsPrivate()))
	return u.Scheme == "https" || (u.Scheme == "http" && local)
}

func ValidActionPassword(password string) bool {
	if len(password) < 12 || len(password) > 128 {
		return false
	}
	for _, ch := range []byte(password) {
		if ch < 0x21 || ch > 0x7e {
			return false
		}
	}
	return true
}

var _ Adapter = (*zjmf)(nil)
