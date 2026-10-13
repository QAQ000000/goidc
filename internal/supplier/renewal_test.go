package supplier

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRenewalOwnershipCostPaymentAndDueConfirmation(t *testing.T) {
	for _, tc := range []struct {
		name, invoiceType, payment, code                                       string
		wrongProduct, wrongHost, expensive, paid, unchanged, checkpointFailure bool
		paidInvoiceMismatch, shortPeriod                                       bool
		wantCreates, wantPays                                                  int
	}{
		{name: "normal", paid: true, wantCreates: 1, wantPays: 1},
		{name: "lost-payment-response", payment: `{"status":`, paid: true, wantCreates: 1, wantPays: 1},
		{name: "success-but-unpaid", payment: `{"status":1001}`, code: "renewal_payment_unconfirmed", wantCreates: 1, wantPays: 1},
		{name: "paid-but-old-due", paid: true, unchanged: true, code: "renewal_due_unconfirmed", wantCreates: 1, wantPays: 1},
		{name: "paid-but-invoice-changed", paid: true, paidInvoiceMismatch: true, code: "renewal_payment_unconfirmed", wantCreates: 1, wantPays: 1},
		{name: "paid-but-short-period", paid: true, shortPeriod: true, code: "renewal_due_unconfirmed", wantCreates: 1, wantPays: 1},
		{name: "wrong-product", wrongProduct: true, code: "renewal_resource_mismatch"},
		{name: "wrong-host", wrongHost: true, code: "renewal_invoice_resource_mismatch", wantCreates: 1},
		{name: "creation-invoice", invoiceType: "host", code: "renewal_invoice_resource_mismatch", wantCreates: 1},
		{name: "over-budget", expensive: true, code: "renewal_invoice_price_mismatch", wantCreates: 1},
		{name: "checkpoint-failure", checkpointFailure: true, code: "renewal_checkpoint_failed", wantCreates: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			creates, pays, checkpoints := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/zjmf_api_login":
					fmt.Fprint(w, `{"status":200,"jwt":"private"}`)
				case "/host/header":
					product, due := 7, int64(1800000000)
					if tc.wrongProduct {
						product = 8
					}
					if pays > 0 && tc.paid && !tc.unchanged {
						due = 1802678400
					}
					if pays > 0 && tc.shortPeriod {
						due = 1800000060
					}
					fmt.Fprintf(w, `{"status":200,"data":{"host_data":{"id":42,"productid":%d,"domainstatus":"Active","nextduedate":%d}}}`, product, due)
				case "/host/renewpage":
					fmt.Fprint(w, `{"status":200,"data":{"host":{"productid":7,"billingcycle":"monthly","amount":"16.00","nextduedate":1800000000},"currency":{"code":"CNY"}}}`)
				case "/host/renew":
					creates++
					if r.PostFormValue("hostid") != "42" || r.PostFormValue("billingcycles") != "monthly" {
						t.Error("wrong renewal identity")
					}
					fmt.Fprint(w, `{"status":200,"data":{"invoice_id":"91"}}`)
				case "/get_invoices_detail":
					status, total, cost, host, kind := "Unpaid", "16.00", "16.00", 42, "renew"
					if pays > 0 && tc.paid {
						status, total = "Paid", "0.00"
					}
					if tc.expensive {
						cost, total = "16.01", "16.01"
					}
					if pays > 0 && tc.paidInvoiceMismatch {
						cost = "16.01"
					}
					if tc.wrongHost {
						host = 43
					}
					if tc.invoiceType != "" {
						kind = tc.invoiceType
					}
					fmt.Fprintf(w, `{"status":200,"data":{"detail":{"id":91,"subtotal":%q,"total":%q,"status":%q},"currency":{"code":"CNY"},"invoice_items":[{"type":%q,"rel_id":%d}]}}`, cost, total, status, kind, host)
				case "/apply_credit":
					pays++
					if checkpoints != 1 || r.PostFormValue("invoiceid") != "91" || r.PostFormValue("enough") != "1" || r.PostFormValue("use_credit") != "1" {
						t.Error("payment before durable checkpoint or unsafe args")
					}
					if tc.payment != "" {
						fmt.Fprint(w, tc.payment)
					} else {
						fmt.Fprint(w, `{"status":1001}`)
					}
				default:
					t.Errorf("unexpected endpoint %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			a, e := New(Account{Provider: ProviderZJMF, BaseURL: srv.URL, Username: "test", Password: "private"})
			if e != nil {
				t.Fatal(e)
			}
			renew := a.(RenewalAdapter)
			req := RenewalRequest{UpstreamID: 42, ProductID: 7, Cycle: "monthly", Currency: "CNY", CostCents: 1600, MaximumCostCents: 1600, PreviousDue: time.Unix(1800000000, 0).UTC().Format(time.RFC3339)}
			out, remote, e := renew.Renew(t.Context(), req, func(out CreateResult) error {
				checkpoints++
				if out.InvoiceID != 91 {
					t.Error(out)
				}
				if tc.checkpointFailure {
					return errors.New("storage failed")
				}
				return nil
			})
			if tc.code != "" {
				var failure *Error
				if !errors.As(e, &failure) || failure.Code != tc.code {
					t.Fatal(out, remote, e)
				}
				if pays > 0 && tc.paid && !failure.Unknown {
					t.Fatal("post-payment uncertainty treated as known failure", failure)
				}
			} else if e != nil || out.InvoiceID != 91 || remote.NextDueDate != "1802678400" {
				t.Fatal(out, remote, e)
			}
			if creates != tc.wantCreates || pays != tc.wantPays {
				t.Fatal(creates, pays)
			}
		})
	}
}
