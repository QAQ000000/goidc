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
	paid := false
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
		if fail == "payment_host" && r.URL.Path == "/apply_credit" {
			fmt.Fprint(w, `{"status":1001,"data":{"hostid":[43]}}`)
			return
		}
		switch r.URL.Path {
		case "/zjmf_api_login":
			fmt.Fprint(w, `{"status":200,"jwt":"private-jwt"}`)
		case "/cart/all":
			fmt.Fprint(w, `{"status":200,"data":{"products":[{"products":[{"id":7,"name":"VM","stock_control":1,"qty":5}]}],"count":1,"currency":"CNY"}}`)
		case "/cart/get_product_config":
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","description":"Virtual machine","location_version":1,"stock_control":1,"qty":5,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
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
			status := "Unpaid"
			if paid {
				status = "Paid"
			}
			fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":90,"subtotal":%q,"status":%q},"currency":{"code":"CNY"},"invoice_items":[{"type":"host","rel_id":42,"amount":%q}]}}`, invoiceAmount, status, invoiceAmount)
		case "/apply_credit":
			if r.PostFormValue("invoiceid") != "90" {
				t.Error("wrong invoice")
			}
			paid = true
			fmt.Fprint(w, `{"status":1001,"data":{"hostid":[42]}}`)
		case "/host/header":
			fmt.Fprint(w, `{"status":200,"data":{"host_data":{"id":42,"productid":7,"domainstatus":"Active","domain":"vm.example","assignedips":[],"nextduedate":1790000000}}}`)
		case "/cart/hostinfo":
			fmt.Fprint(w, `{"status":200,"data":{"hosts":[{"id":42,"productid":7,"domainstatus":"Active","domain":"vm.example","nextduedate":1790000000}]}}`)
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
		fmt.Fprintf(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","location_version":2,"stock_control":1,"qty":%d,"pay_type":{"pay_type":"onetime"}},"product_pricings":[{"code":"CNY","onetime":"30.00","osetupfee":"2.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`, qty.Load())
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
	if !a.Capabilities().Renew || a.Capabilities().Terminate {
		t.Fatal("unsupported capability")
	}
}

func TestSupplierActionsSendExpectedProviderPayload(t *testing.T) {
	password := "new-system-password-123!"
	for _, tc := range []struct {
		action string
		fields map[string]string
	}{
		{"force_reboot", map[string]string{"func": "hard_reboot"}},
		{"force_off", map[string]string{"func": "hard_off"}},
		{"reset_password", map[string]string{"func": "crack_pass", "password": password, "other_user": "0", "user": "", "action": ""}},
		{"rescue_system", map[string]string{"func": "rescue_system", "system": "2", "temp_pass": password}},
	} {
		t.Run(tc.action, func(t *testing.T) {
			var seen bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/zjmf_api_login":
					fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
				case "/provision/default":
					if r.Method != http.MethodPost || r.PostFormValue("id") != "42" || r.PostFormValue("is_api") != "1" {
						t.Errorf("unexpected action request: method=%s form=%v", r.Method, r.Form)
					}
					for key, want := range tc.fields {
						if got := r.PostFormValue(key); got != want {
							t.Errorf("%s=%q, want %q", key, got, want)
						}
					}
					if _, expected := tc.fields["password"]; !expected && r.PostFormValue("password") != "" {
						t.Error("unexpected password field")
					}
					if _, expected := tc.fields["temp_pass"]; !expected && r.PostFormValue("temp_pass") != "" {
						t.Error("unexpected temporary password field")
					}
					seen = true
					fmt.Fprint(w, `{"status":200}`)
				case "/host/header":
					fmt.Fprint(w, `{"status":200,"data":{"host_data":{"id":42,"productid":7,"domainstatus":"Active","domain":"vm.example","nextduedate":1790000000,"password":"new-system-password-123!"}}}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			a, err := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "secret"})
			if err != nil {
				t.Fatal(err)
			}
			result, err := a.Execute(t.Context(), ActionRequest{Action: tc.action, UpstreamID: 42, Password: password, System: 2})
			if err != nil || !seen || result.UpstreamID != 42 {
				t.Fatalf("action result=%+v seen=%v err=%v", result, seen, err)
			}
		})
	}
}

func TestSupplierActionRejectionIsDefinitive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
			return
		}
		if r.URL.Path == "/provision/default" {
			fmt.Fprint(w, `{"status":403,"message":"unsupported"}`)
			return
		}
		t.Errorf("action rejection should stop before sync: %s", r.URL.Path)
	}))
	defer srv.Close()
	a, err := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Execute(t.Context(), ActionRequest{Action: "force_off", UpstreamID: 42})
	var failure *Error
	if !errors.As(err, &failure) || failure.Code != "upstream_rejected" || failure.Unknown {
		t.Fatalf("expected definitive rejection, got %v", err)
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

func TestMismatchedPaymentHostIsUnknown(t *testing.T) {
	a, _ := fixture(t, "16.00", "payment_host")
	p, _ := a.Product(t.Context(), 7)
	_, err := a.Create(t.Context(), createRequest(p))
	var failure *Error
	if !errors.As(err, &failure) || !failure.Unknown || failure.UpstreamID != 42 {
		t.Fatal(err)
	}
}

func TestMoneyBoundaries(t *testing.T) {
	for _, value := range []string{"-1", "+1", "1.234", "92233720368547759", "92233720368547758.08", "92233720368547758.99"} {
		if _, valid := money(value); valid {
			t.Fatal("accepted", value)
		}
	}
	if amount, valid := money("92233720368547758.07"); !valid || amount != 9223372036854775807 {
		t.Fatal(amount, valid)
	}
}

func TestProductAvailabilityAndPaymentRules(t *testing.T) {
	for _, tc := range []struct {
		rules, setup string
		supported    bool
		cycles       int
	}{
		{`"pay_type":{"pay_type":"recurring"}`, `"0.00"`, true, 1},
		{`"pay_type":"{\"pay_type\":\"onetime\"}"`, `"0.00"`, true, 1},
		{`"pay_type":{"pay_type":"free"}`, `"0.00"`, false, 2},
		{`"pay_type":{"pay_type":"recurring"},"hidden":1`, `"0.00"`, false, 1},
		{`"pay_type":{"pay_type":"recurring"},"retired":1`, `"0.00"`, false, 1},
		{`"pay_type":{"pay_type":"recurring"},"password":"{\"show\":1}"`, `"0.00"`, false, 1},
		{`"pay_type":{"pay_type":"recurring"},"host":{"show":1}`, `"0.00"`, false, 1},
		{`"pay_type":{"pay_type":"recurring"}`, `"-1.00"`, false, 1},
		{`"pay_type":{"pay_type":"recurring"}`, `"oops"`, false, 1},
	} {
		t.Run(tc.rules+tc.setup, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/zjmf_api_login" {
					fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
					return
				}
				fmt.Fprintf(w, `{"status":200,"data":{"products":{"id":7,"location_version":1,%s},"product_pricings":[{"code":"CNY","monthly":"16.00","msetupfee":%s,"onetime":"30.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`, tc.rules, tc.setup)
			}))
			defer srv.Close()
			a, _ := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "pass"})
			p, err := a.Product(t.Context(), 7)
			if err != nil || p.Supported != tc.supported {
				t.Fatal(p, err)
			}
			if tc.supported && len(p.Prices) != tc.cycles {
				t.Fatal("unavailable cycles", p.Prices)
			}
		})
	}
}

func TestDCIMFixedSpecificationOptionsAreSupported(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/zjmf_api_login" {
			fmt.Fprint(w, `{"status":200,"jwt":"jwt"}`)
			return
		}
		if r.URL.Path != "/cart/get_product_config" {
			t.Errorf("unexpected path %s", r.URL.Path)
			return
		}
		fmt.Fprint(w, `{"status":200,"data":{"products":{"id":369,"name":"香港1区CN2 2H2G","location_version":79,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"20.00","quarterly":"60.00","annually":"200.00"}],"config_groups":[{"options":[{"id":33165,"option_name":"area|区域","option_type":12,"qty_minimum":0,"qty_maximum":0,"qty_stage":0,"sub":[{"id":136795,"option_name":"2|香港将军澳","pricings":[{"code":"CNY","monthly":"0.00","quarterly":"0.00","annually":"0.00"}]}]},{"id":33166,"option_name":"os|操作系统","option_type":5,"qty_minimum":0,"qty_maximum":0,"qty_stage":0,"sub":[{"id":136798,"option_name":"67|CentOS","pricings":[{"code":"CNY","monthly":"0.00","quarterly":"0.00","annually":"0.00"}]},{"id":136801,"option_name":"68|Ubuntu","pricings":[{"code":"CNY","monthly":"0.00","quarterly":"0.00","annually":"0.00"}]}]},{"id":33170,"option_name":"bw|带宽","option_type":11,"qty_minimum":20,"qty_maximum":20,"qty_stage":1,"sub":[{"id":136819,"option_name":"带宽","pricings":[{"code":"CNY","monthly":"0.00","quarterly":"0.00","annually":"0.00"}]}]}]}],"customfields":[],"advanced":[]}}`)
	}))
	defer srv.Close()
	a, _ := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "user", Password: "pass"})
	p, err := a.Product(t.Context(), 369)
	if err != nil || !p.Supported || len(p.Options) != 3 || p.Options[1].Choices[1].ID != 136801 {
		t.Fatal(p, err)
	}
	cost, err := ValidateSelection(p, "monthly", map[int64]string{33165: "136795", 33166: "136801", 33170: "136819"}, nil)
	if err != nil || cost != 2000 {
		t.Fatal(cost, err)
	}
}

func TestSingleRangeDiskQuantitySelection(t *testing.T) {
	p := Product{Currency: "CNY", Supported: true, Prices: map[string]Price{"monthly": {AmountCents: 2000}}, Options: []Option{{ID: 14, Type: "quantity", Required: true, Choices: []Choice{{ID: 15, MinQty: 0, MaxQty: 100, Prices: map[string]Price{"monthly": {AmountCents: 10}}}}}}}
	if cost, err := ValidateSelection(p, "monthly", map[int64]string{14: "30"}, nil); err != nil || cost != 2300 {
		t.Fatal(cost, err)
	}
	if _, err := ValidateSelection(p, "monthly", map[int64]string{14: "101"}, nil); err == nil {
		t.Fatal("accepted quantity outside range")
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
	if len(providers) != 1 || providers[0].ID != ProviderZJMF || !providers[0].Capabilities.Renew {
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
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","location_version":1,"pay_type":{"pay_type":"recurring"}},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
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
