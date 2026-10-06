package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func fixture(t *testing.T, invoiceAmount string, fail string) (Adapter, *int) {
	t.Helper()
	writes := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/zjmf_api_login" && r.Header.Get("Authorization") != "Bearer private-jwt" {
			t.Errorf("missing authorization: %s", r.URL.Path)
		}
		if strings.HasPrefix(r.URL.Path, "/cart/") && r.URL.Path != "/cart/all" && r.URL.Path != "/cart/get_product_config" && r.URL.Path != "/cart/hostinfo" {
			*writes++
		}
		if r.URL.Path == fail {
			fmt.Fprint(w, `{"status":200,"data":`)
			return
		}
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"private-jwt"}`)
		case "/cart/all":
			fmt.Fprint(w, `{"status":200,"data":{"products":[{"products":[{"id":7,"name":"VM","stock_control":1,"qty":5}]}],"count":1,"currency":"CNY"}}`)
		case "/cart/get_product_config":
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","description":"Virtual machine","location_version":1,"stock_control":1,"qty":5},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
		case "/cart/clear":
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/add_to_shop":
			if r.PostFormValue("pid") != "7" {
				t.Error("wrong pid")
			}
			fmt.Fprint(w, `{"status":200}`)
		case "/cart/settle":
			if r.PostFormValue("cart_data[pid]") != "7" {
				t.Error("bad cart_data")
			}
			fmt.Fprint(w, `{"status":200,"data":{"hostid":[42],"invoiceid":90}}`)
		case "/get_invoices_detail":
			fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":90,"subtotal":%q,"status":"Unpaid"},"currency":{"code":"CNY"},"invoice_items":[{"type":"host","rel_id":42,"amount":%q}]}}`, invoiceAmount, invoiceAmount)
		case "/apply_credit":
			if r.PostFormValue("invoiceid") != "90" {
				t.Error("wrong invoice")
			}
			fmt.Fprint(w, `{"status":1001,"data":{"hostid":[42]}}`)
		case "/cart/hostinfo":
			fmt.Fprint(w, `{"status":200,"data":{"hosts":[{"id":42,"domainstatus":"Active","domain":"vm.example","nextduedate":1790000000}]}}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)
	a, err := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "account", Password: "private-password"})
	if err != nil {
		t.Fatal(err)
	}
	return a, writes
}

func TestRevisionExcludesStockAndMapsOneTime(t *testing.T) {
	var qty atomic.Int64
	qty.Store(5)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
			return
		}
		if r.URL.Path != "/cart/get_product_config" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		fmt.Fprintf(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","location_version":2,"stock_control":1,"qty":%d},"product_pricings":[{"code":"CNY","onetime":"30.00","osetupfee":"2.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`, qty.Load())
	}))
	defer srv.Close()
	a, _ := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "pass"})
	p1, e := a.Product(context.Background(), 7)
	if e != nil || p1.SupplierVersion != 2 || p1.Prices["once"].AmountCents != 3000 || p1.Prices["once"].SetupCents != 200 {
		t.Fatal(p1, e)
	}
	qty.Store(4)
	p2, e := a.Product(context.Background(), 7)
	if e != nil || p1.Revision != p2.Revision || *p1.Stock == *p2.Stock {
		t.Fatal(p1.Revision, p2.Revision, e)
	}
}
func createRequest(p Product) CreateRequest {
	return CreateRequest{ProductID: 7, Cycle: "monthly", Revision: p.Revision, LocalServiceID: 99, DownstreamURL: "https://downstream.example", DownstreamToken: strings.Repeat("x", 32), MaximumCostCents: 1600, Currency: "CNY"}
}
func TestCatalogProductCreateSync(t *testing.T) {
	ctx := context.Background()
	a, _ := fixture(t, "16.00", "")
	catalog, e := a.Catalog(ctx)
	if e != nil || len(catalog) != 1 || catalog[0].ID != 7 {
		t.Fatal(catalog, e)
	}
	p, e := a.Product(ctx, 7)
	if e != nil || !p.Supported || p.Prices["monthly"].AmountCents != 1600 {
		t.Fatal(p, e)
	}
	out, e := a.Create(ctx, createRequest(p))
	if e != nil || out.UpstreamID != 42 || out.InvoiceID != 90 || out.Status != "active" {
		t.Fatal(out, e)
	}
	s, e := a.Sync(ctx, 42)
	if e != nil || s.Status != "Active" || s.NextDueDate != "1790000000" {
		t.Fatal(s, e)
	}
	if a.Capabilities().Renew || a.Capabilities().Terminate {
		t.Fatal("unsupported capability")
	}
}
func TestInvoiceMismatchBlocksCredit(t *testing.T) {
	ctx := context.Background()
	a, _ := fixture(t, "17.00", "")
	p, _ := a.Product(ctx, 7)
	out, e := a.Create(ctx, createRequest(p))
	var failure *Error
	if !errors.As(e, &failure) || !failure.Unknown || failure.InvoiceID != 90 || out.InvoiceID != 90 {
		t.Fatal(out, e)
	}
}
func TestAmbiguousWriteAndSecretFreeError(t *testing.T) {
	ctx := context.Background()
	a, _ := fixture(t, "16.00", "/cart/settle")
	p, _ := a.Product(ctx, 7)
	_, e := a.Create(ctx, createRequest(p))
	var failure *Error
	if !errors.As(e, &failure) || !failure.Unknown {
		t.Fatal(e)
	}
	b, _ := json.Marshal(failure)
	if strings.Contains(string(b), "private") || strings.Contains(e.Error(), "private") {
		t.Fatal("credential leaked")
	}
}
func TestUnsafeURLs(t *testing.T) {
	for _, base := range []string{"https://user:pass@example.com", "https://example.com/a/../b", "http://example.com", "https://example.com?token=private"} {
		if _, e := New(Account{Provider: ProviderZJMF, BaseURL: base, Username: "u", Password: "p"}); e == nil {
			t.Fatal(base)
		}
	}
}
func TestSelectionAndJSONContract(t *testing.T) {
	p := Product{ID: 7, Currency: "CNY", Supported: true, Prices: map[string]Price{"monthly": {AmountCents: 1600, SetupCents: 100}}, Options: []Option{{ID: 10, Name: "CPU", Type: "select", Required: true, Choices: []Choice{{ID: 11, Name: "2 cores", Prices: map[string]Price{"monthly": {AmountCents: 200}}}}}}, Fields: []Field{{ID: 12, Name: "Region", Type: "dropdown", Required: true, Choices: []string{"CN"}}}}
	cost, e := ValidateSelection(p, "monthly", map[int64]string{10: "11"}, map[int64]string{12: "CN"})
	if e != nil || cost != 1900 {
		t.Fatal(cost, e)
	}
	for _, tc := range []struct{ options, fields map[int64]string }{{nil, map[int64]string{12: "CN"}}, {map[int64]string{10: "99"}, map[int64]string{12: "CN"}}, {map[int64]string{10: "11"}, map[int64]string{12: "US"}}, {map[int64]string{10: "11"}, map[int64]string{12: "CN", 13: "private"}}} {
		if _, e := ValidateSelection(p, "monthly", tc.options, tc.fields); e == nil {
			t.Fatal("accepted invalid selection", tc)
		}
	}
	raw, _ := json.Marshal(p)
	if !strings.Contains(string(raw), `"amount_cents":1600`) || strings.Contains(string(raw), `"unsupported_reason"`) {
		t.Fatal(string(raw))
	}
	providers := Providers()
	if len(providers) != 1 || providers[0].ID != ProviderZJMF || providers[0].Capabilities.Renew {
		t.Fatal(providers)
	}
}
func TestExistingHostWithoutInvoiceIsUnknown(t *testing.T) {
	ctx := context.Background()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
		case "/cart/get_product_config":
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","location_version":1},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
		case "/cart/clear":
			fmt.Fprint(w, `{"status":400,"hostid":42,"domainstatus":"Active"}`)
		default:
			t.Errorf("unexpected %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	a, _ := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "pass"})
	p, _ := a.Product(ctx, 7)
	out, e := a.Create(ctx, createRequest(p))
	var failure *Error
	if !errors.As(e, &failure) || !failure.Unknown || failure.UpstreamID != 42 || out.UpstreamID != 42 || out.Status != "" || out.Currency != "" {
		t.Fatal(out, e)
	}
}
