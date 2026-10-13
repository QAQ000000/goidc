package supplier

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPurchaseInvoiceRecoveryAndPaymentConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, settle, payment, expected          string
		paid, confirmed, wrongProduct, expensive bool
		wantPayments                             int
	}{
		{name: "invoice-only", settle: `{"invoiceid":90}`, confirmed: true, wantPayments: 1},
		{name: "scalar-host", settle: `{"hostid":"42","invoiceid":"90"}`, confirmed: true, wantPayments: 1},
		{name: "ambiguous-hosts", settle: `{"hostid":[42,43],"invoiceid":90}`, expected: "purchase_identity_invalid"},
		{name: "wrong-host", settle: `{"hostid":[43],"invoiceid":90}`, expected: "invoice_host_mismatch"},
		{name: "wrong-product", settle: `{"invoiceid":90}`, wrongProduct: true, expected: "resource_product_unconfirmed"},
		{name: "over-budget", settle: `{"invoiceid":90}`, expensive: true, expected: "invoice_price_mismatch"},
		{name: "paid-without-host-response", settle: `{"invoiceid":90}`, payment: `{"status":1001}`, confirmed: true, wantPayments: 1},
		{name: "success-but-still-unpaid", settle: `{"invoiceid":90}`, payment: `{"status":1001}`, expected: "payment_not_confirmed", wantPayments: 1},
		{name: "invalid-response-but-paid", settle: `{"invoiceid":90}`, payment: `{"status":`, confirmed: true, wantPayments: 1},
		{name: "already-paid", settle: `{"invoiceid":90}`, paid: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates, pays := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/zjmf_api_login":
					fmt.Fprint(w, `{"status":200,"jwt":"private-jwt"}`)
				case "/cart/get_product_config":
					fmt.Fprint(w, `{"status":200,"data":{"products":{"id":7,"name":"CDN","location_version":1,"pay_type":{"pay_type":"recurring"},"host":"{\"show\":\"1\",\"modify\":0,\"prefix\":\"ser\",\"rule\":{\"len_num\":\"8\",\"lower\":\"1\",\"num\":\"1\"}}","password":{"show":"1","modify":0,"rule":{"len_num":"12","lower":"1","num":"1"}}},"product_pricings":[{"code":"CNY","monthly":"16.00"}],"config_groups":[],"customfields":[],"advanced":[]}}`)
				case "/user_info":
					fmt.Fprint(w, `{"status":200,"user":{"currency":1}}`)
				case "/cart/clear":
					fmt.Fprint(w, `{"status":200}`)
				case "/cart/add_to_shop":
					if len(r.PostFormValue("host")) != 11 || len(r.PostFormValue("password")) != 12 {
						t.Error("automatic credential rules ignored")
					}
					fmt.Fprint(w, `{"status":200}`)
				case "/cart/settle":
					creates++
					fmt.Fprintf(w, `{"status":200,"data":%s}`, tc.settle)
				case "/get_invoices_detail":
					status, cost := "Unpaid", "16.00"
					if tc.paid || (pays > 0 && tc.confirmed) {
						status = "Paid"
					}
					if tc.expensive {
						cost = "16.01"
					}
					fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":"90","subtotal":%q,"status":%q},"currency":{"code":"CNY"},"invoice_items":[{"type":"host","rel_id":"42"}]}}`, cost, status)
				case "/apply_credit":
					pays++
					if r.PostFormValue("enough") != "1" || r.PostFormValue("invoiceid") != "90" {
						t.Error("unsafe invoice payment")
					}
					response := tc.payment
					if response == "" {
						response = `{"status":1001,"data":{"hostid":[42]}}`
					}
					fmt.Fprint(w, response)
				case "/host/header":
					product, status := 7, "Pending"
					if tc.wrongProduct {
						product = 8
					}
					if tc.paid || (pays > 0 && tc.confirmed) {
						status = "Active"
					}
					fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":42,"productid":%d,"domainstatus":%q,"nextduedate":1790000000}}}`, product, status)
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			a, err := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "account", Password: "private-password"})
			if err != nil {
				t.Fatal(err)
			}
			p, err := a.Product(t.Context(), 7)
			if err != nil || !p.Supported {
				t.Fatal(p, err)
			}
			out, err := a.Create(t.Context(), createRequest(p))
			if tc.expected == "" {
				if err != nil || out.Status != "active" || out.UpstreamID != 42 || out.InvoiceID != 90 || out.CostCents != 1600 {
					t.Fatal(out, err)
				}
			} else {
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != tc.expected || !failure.Unknown {
					t.Fatal(out, err)
				}
			}
			if creates != 1 || pays != tc.wantPayments {
				t.Fatal("duplicate/unsafe purchase", creates, pays)
			}
		})
	}
}

func TestAutomaticCredentialRules(t *testing.T) {
	for _, raw := range []string{`{"show":1,"modify":1}`, `{"show":1,"modify":0,"rule":{"len_num":2,"lower":1}}`, `{"show":1,"modify":0,"rule":{"len_num":12,"lower":"invalid"}}`} {
		if _, ok := parseCredentialRule(json.RawMessage(raw), false); ok {
			t.Fatal("unsafe credential rule accepted", raw)
		}
	}
	rule, ok := parseCredentialRule(json.RawMessage(`{"show":1,"modify":0,"rule":{"len_num":12,"lower":1,"num":1}}`), false)
	if !ok {
		t.Fatal("valid automatic rule rejected")
	}
	first, err := rule.generate()
	second, err2 := rule.generate()
	if err != nil || err2 != nil || len(first) != 12 || first == second || strings.Trim(first, "abcdefghijklmnopqrstuvwxyz0123456789") != "" || !strings.ContainsAny(first, "0123456789") || !strings.ContainsAny(first, "abcdefghijklmnopqrstuvwxyz") {
		t.Fatal("invalid generated credentials")
	}
}
