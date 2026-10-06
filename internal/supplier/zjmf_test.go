package supplier

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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
			fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"VM","description":"Virtual machine","stock_control":1,"qty":5},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
		case "/user_info":
			fmt.Fprint(w, `{"status":200,"data":{"currency":1}}`)
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
			fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":90,"subtotal":%q,"status":"Unpaid"},"currency":{"code":"CNY"}}}`, invoiceAmount)
		case "/apply_credit":
			if r.PostFormValue("invoiceid") != "90" {
				t.Error("wrong invoice")
			}
			fmt.Fprint(w, `{"status":1001,"data":{"hostid":[42]}}`)
		case "/cart/hostinfo":
			fmt.Fprint(w, `{"status":200,"data":{"hosts":[{"id":42,"domainstatus":"Active","domain":"vm.example"}]}}`)
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
	if e != nil || s.Status != "Active" {
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
