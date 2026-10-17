package g09fixture

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// SupplierResource preallocates identities; requests cannot invent resources.
type SupplierResource struct {
	LocalID   int64 `json:"localId"`
	HostID    int64 `json:"hostId"`
	InvoiceID int64 `json:"invoiceId"`
}

type SupplierConfig struct {
	Username         string           `json:"-"`
	Password         string           `json:"-"`
	Token            string           `json:"-"`
	DownstreamURL    string           `json:"-"`
	DownstreamTokens map[int64]string `json:"-"`
	Resources        []SupplierResource
	RequestBudget    int
}

type supplierState struct {
	SupplierResource
	added, settled, paid bool
}

type SupplierFact struct {
	SupplierResource
	Paid bool `json:"paid"`
}

// SupplierSink implements only the compiled ZJMF adapter's fixed one-product
// purchase/read protocol. It is not a general upstream, nor capacity evidence.
// All successful protocol responses are durably journaled before state changes.
type SupplierSink struct {
	mu        sync.Mutex
	journal   *Journal
	config    SupplierConfig
	resources map[int64]*supplierState
	requests  int
	failed    error
}

func NewSupplierSink(journal *Journal, config SupplierConfig) (*SupplierSink, error) {
	if journal == nil || config.Username == "" || config.Password == "" || config.Token == "" || len(config.DownstreamTokens) != len(config.Resources) || config.RequestBudget < 1 || config.RequestBudget > maxAcceptances || len(config.Resources) < 1 || len(config.Resources) > 512 {
		return nil, errors.New("invalid supplier fixture configuration")
	}
	u, err := url.Parse(config.DownstreamURL)
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
		return nil, errors.New("supplier fixture requires an explicit IPv4 loopback downstream origin")
	}
	s := &SupplierSink{journal: journal, config: config, resources: make(map[int64]*supplierState)}
	s.config.DownstreamTokens = make(map[int64]string, len(config.DownstreamTokens))
	hosts, invoices := map[int64]bool{}, map[int64]bool{}
	for _, resource := range config.Resources {
		if resource.LocalID <= 0 || resource.HostID <= 0 || resource.InvoiceID <= 0 || s.resources[resource.LocalID] != nil || hosts[resource.HostID] || invoices[resource.InvoiceID] || len(config.DownstreamTokens[resource.LocalID]) != 32 {
			return nil, errors.New("supplier fixture resource identity is not unique")
		}
		s.resources[resource.LocalID] = &supplierState{SupplierResource: resource}
		s.config.DownstreamTokens[resource.LocalID] = config.DownstreamTokens[resource.LocalID]
		hosts[resource.HostID], invoices[resource.InvoiceID] = true, true
	}
	// Do not retain mutable caller-owned slices.
	s.config.Resources = nil
	return s, nil
}

func (s *SupplierSink) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	return s.journal.Err()
}

// Snapshot returns only admitted, settled identities and paid facts. It cannot
// be used to infer successful worker completion or an actual upstream resource.
func (s *SupplierSink) Snapshot() []SupplierFact {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []SupplierFact{}
	for _, state := range s.resources {
		if state.settled {
			out = append(out, SupplierFact{state.SupplierResource, state.paid})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LocalID < out[j].LocalID })
	return out
}

func sameSecret(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

func exactForm(got, want url.Values) bool {
	if len(got) != len(want) {
		return false
	}
	for key, values := range want {
		if len(got[key]) != 1 || len(values) != 1 || got[key][0] != values[0] {
			return false
		}
	}
	return true
}

func (s *SupplierSink) common(state *supplierState) url.Values {
	return url.Values{"downstream_id": {strconv.FormatInt(state.LocalID, 10)}, "downstream_url": {s.config.DownstreamURL}, "downstream_token": {s.config.DownstreamTokens[state.LocalID]}}
}

func (s *SupplierSink) cart(state *supplierState) url.Values {
	values := s.common(state)
	for key, value := range map[string]string{"pid": "7", "billingcycle": "monthly", "currencyid": "1", "qty": "1", "supplier_version": "1", "host": fmt.Sprintf("svc-%d", state.LocalID), "password": ""} {
		values.Set(key, value)
	}
	return values
}

func (s *SupplierSink) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Refuse accidental non-loopback serving even when mounted by another caller.
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || remote != "127.0.0.1" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil || s.journal.Err() != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if s.requests >= s.config.RequestBudget {
		s.failed = errors.New("supplier request budget exceeded")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	s.requests++ // Includes rejected requests; retries cannot bypass the budget.
	if len(r.URL.RawQuery) > 4096 || (r.Method != http.MethodGet && r.Method != http.MethodPost) {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	if err := r.ParseForm(); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if r.Method == http.MethodPost && len(r.URL.Query()) != 0 {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	path := r.URL.Path
	if path == "/zjmf_api_login" {
		if r.Method != http.MethodPost || !exactForm(r.PostForm, url.Values{"username": {s.config.Username}, "password": {s.config.Password}}) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		s.respond(w, "login", 0, map[string]any{"status": 200, "jwt": s.config.Token}, map[string]any{"authenticated": true}, nil)
		return
	}
	if !sameSecret(r.Header.Get("Authorization"), "Bearer "+s.config.Token) {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method == http.MethodGet {
		s.read(w, r)
		return
	}
	var state *supplierState
	if path == "/apply_credit" {
		for _, candidate := range s.resources {
			if strconv.FormatInt(candidate.InvoiceID, 10) == r.PostForm.Get("invoiceid") {
				state = candidate
				break
			}
		}
	} else {
		state = s.resources[parsePositive(r.PostForm.Get("downstream_id"))]
	}
	if state == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	common := s.common(state)
	ids := map[string]any{"hostid": []int64{state.HostID}, "invoiceid": state.InvoiceID}
	var response any
	var commit func()
	switch path {
	case "/cart/clear":
		if !exactForm(r.PostForm, common) {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response = map[string]any{"status": 200}
		if state.settled {
			response = map[string]any{"status": 200, "data": ids}
		}
	case "/cart/add_to_shop":
		if state.settled || !exactForm(r.PostForm, s.cart(state)) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		response, commit = map[string]any{"status": 200}, func() { state.added = true }
	case "/cart/settle":
		want := s.common(state)
		for key, values := range s.cart(state) {
			want.Set("cart_data["+key+"]", values[0])
		}
		if !state.added || !exactForm(r.PostForm, want) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		response, commit = map[string]any{"status": 200, "data": ids}, func() { state.settled = true }
	case "/apply_credit":
		common.Set("invoiceid", strconv.FormatInt(state.InvoiceID, 10))
		common.Set("use_credit", "1")
		common.Set("enough", "1")
		if !state.settled || !exactForm(r.PostForm, common) {
			w.WriteHeader(http.StatusConflict)
			return
		}
		response, commit = map[string]any{"status": 1001, "data": ids}, func() { state.paid = true }
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	// Replays record another protocol acceptance, but never invent another resource
	// or charge. This models the adapter's existing-ID contract, not all real sites.
	s.respond(w, strings.TrimPrefix(path, "/"), state.LocalID, response, response, commit)
}

func parsePositive(value string) int64 { id, _ := strconv.ParseInt(value, 10, 64); return id }

func (s *SupplierSink) read(w http.ResponseWriter, r *http.Request) {
	var response any
	var localID int64
	switch r.URL.Path {
	case "/cart/all":
		if len(r.Form) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response = map[string]any{"status": 200, "data": map[string]any{"products": []any{map[string]any{"products": []any{map[string]any{"id": 7, "name": "G09 loopback VM"}}}}, "count": 1, "currency": "CNY"}}
	case "/cart/get_product_config":
		if !exactForm(r.Form, url.Values{"pid": {"7"}}) {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		response = map[string]any{"status": 200, "data": map[string]any{
			"products":         map[string]any{"id": 7, "name": "G09 loopback VM", "location_version": 1, "pay_type": map[string]any{"pay_type": "recurring"}},
			"product_pricings": []any{map[string]any{"code": "CNY", "monthly": "1.00"}}, "config_groups": []any{}, "customfields": []any{}, "advanced": []any{},
		}}
	case "/user_info":
		if len(r.Form) != 0 {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		response = map[string]any{"status": 200, "user": map[string]any{"currency": 1}}
	case "/get_invoices_detail", "/host/header", "/cart/hostinfo":
		var state *supplierState
		for _, candidate := range s.resources {
			if !candidate.settled {
				continue
			}
			switch r.URL.Path {
			case "/get_invoices_detail":
				if exactForm(r.Form, url.Values{"id": {strconv.FormatInt(candidate.InvoiceID, 10)}}) {
					state = candidate
				}
			case "/host/header":
				if exactForm(r.Form, url.Values{"host_id": {strconv.FormatInt(candidate.HostID, 10)}, "source": {"API"}}) {
					state = candidate
				}
			case "/cart/hostinfo":
				if exactForm(r.Form, url.Values{"hostid[0]": {strconv.FormatInt(candidate.HostID, 10)}}) {
					state = candidate
				}
			}
		}
		if state == nil {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		localID = state.LocalID
		status, invoiceStatus := "Pending", "Unpaid"
		if state.paid {
			status, invoiceStatus = "Active", "Paid"
		}
		host := map[string]any{"id": state.HostID, "productid": 7, "domainstatus": status, "domain": fmt.Sprintf("g09-%d.test", state.LocalID), "nextduedate": 1795000000, "assignedips": []any{}}
		switch r.URL.Path {
		case "/get_invoices_detail":
			response = map[string]any{"status": 200, "data": map[string]any{"detail": map[string]any{"id": state.InvoiceID, "subtotal": "1.00", "status": invoiceStatus}, "currency": map[string]any{"code": "CNY"}, "invoice_items": []any{map[string]any{"type": "host", "rel_id": state.HostID, "amount": "1.00"}}}}
		case "/host/header":
			response = map[string]any{"status": 200, "data": map[string]any{"host_data": host}}
		default:
			response = map[string]any{"status": 200, "data": map[string]any{"hosts": []any{host}}}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
		return
	}
	s.respond(w, strings.TrimPrefix(r.URL.Path, "/"), localID, response, response, nil)
}

func (s *SupplierSink) respond(w http.ResponseWriter, operation string, localID int64, response, evidence any, commit func()) {
	encoded, err := json.Marshal(response)
	if err != nil {
		s.failed = errors.New("supplier encoding failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	facts, err := json.Marshal(evidence)
	if err != nil {
		s.failed = errors.New("supplier encoding failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if err = s.journal.accept("supplier."+operation, strconv.FormatInt(localID, 10), string(facts)); err != nil {
		s.failed = errors.New("supplier persistence failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	if commit != nil {
		commit()
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}
