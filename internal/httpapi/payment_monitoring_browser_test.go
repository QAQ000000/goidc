package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func TestPaymentMonitoringBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	var orders, paid sync.Map
	var db *store.Store
	page := template.Must(template.New("monitor-checkout").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>隔离支付验收</title><main style="max-width:32rem;margin:3rem auto;padding:1rem;font-family:sans-serif"><h1>隔离支付验收</h1><p>测试金额：{{.Money}} 元</p><form action="{{.Action}}" method="post"><button type="submit">确认模拟付款</button></form></main></html>`))
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if r.Method == "GET" && r.URL.Path == "/api.php" {
			if q.Get("act") != "order" || q.Get("pid") != "10001" || q.Get("key") != "integration-epay-secret" {
				w.WriteHeader(401)
				return
			}
			stored, found := orders.Load(q.Get("out_trade_no"))
			if !found {
				_ = json.NewEncoder(w).Encode(map[string]any{"code": -1})
				return
			}
			status := int64(0)
			if _, found := paid.Load(q.Get("out_trade_no")); found {
				status = 1
			}
			_ = json.NewEncoder(w).Encode(queryProtocolResponse(stored.(url.Values), status))
			return
		}
		original := q.Get("sign")
		signEpayTest(q)
		if original != q.Get("sign") {
			http.Error(w, "invalid fixture signature", 400)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/submit.php" {
			orders.Store(q.Get("out_trade_no"), q)
			_ = page.Execute(w, map[string]any{"Money": q.Get("money"), "Action": template.URL("/complete?" + q.Encode())})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/complete" && strings.HasPrefix(q.Get("return_url"), "http://localhost:3099/console/invoices/") {
			paid.Store(q.Get("out_trade_no"), true)
			if _, err := db.Pool.Exec(r.Context(), "UPDATE payment_transactions SET next_reconcile_at=clock_timestamp()-interval '1 second' WHERE provider_payment_id=$1", q.Get("out_trade_no")); err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			http.Redirect(w, r, q.Get("return_url"), http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
	}))
	defer merchant.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", merchant.URL)
	ctx, stop := context.WithCancel(t.Context())
	var workers sync.WaitGroup
	defer func() { stop(); workers.Wait() }()
	var invoices []int64
	adminBrowserFixture(t, "monitor-super", func(database *store.Store, h http.Handler) {
		db = database
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		cfg := epayTestConfig()
		cfg.PublicOrigin = "http://localhost:3099"
		cfg.EpayAPIURL = merchant.URL
		for i := 0; i < 3; i++ {
			invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), fmt.Sprintf("monitor-browser-%d", i))
			invoices = append(invoices, invoice)
			details, err := db.InvoicePaymentDetails(t.Context(), f.uid, invoice)
			if err != nil {
				t.Fatal(err)
			}
			gateway := cfg.PaymentQueryGateways()["epay_alipay"]
			intent, err := gateway.CreateIntent(payment.IntentRequest{InvoiceID: invoice, InvoiceNo: details.InvoiceNo, AmountCents: details.AmountCents})
			if err != nil {
				t.Fatal(err)
			}
			paymentConfig := store.PaymentConfiguration{PaymentSettings: store.PaymentSettings{Enabled: true, MockEnabled: true, APIURL: cfg.EpayAPIURL, PID: cfg.EpayPID, Types: cfg.EpayTypes, Version: 1}, Key: cfg.EpayKey, PublicOrigin: cfg.PublicOrigin}
			db.ProvisionEncryptionKey = cfg.ProvisionEncryptionKey
			if _, err := db.CreatePaymentIntent(store.WithPaymentConfiguration(t.Context(), paymentConfig), f.uid, invoice, gateway.Name(), intent.ProviderPaymentID, intent.CheckoutURL, fmt.Sprintf("monitor-browser-intent-%d", i), details.Version); err != nil {
				t.Fatal(err)
			}
			checkout, err := url.Parse(intent.CheckoutURL)
			if err != nil {
				t.Fatal(err)
			}
			v := checkout.Query()
			orders.Store(v.Get("out_trade_no"), v)
			if i == 1 {
				if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET auto_reconcile_attempts=32,next_reconcile_at=NULL,expires_at=now()-interval '1 day' WHERE invoice_id=$1", invoice); err != nil {
					t.Fatal(err)
				}
			}
			if i == 2 {
				if _, err := db.Pool.Exec(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source,status,result_code,completed_at)
 SELECT id,NULL,'worker','error','provider_invalid',clock_timestamp() FROM payment_transactions WHERE invoice_id=$1`, invoice); err != nil {
					t.Fatal(err)
				}
			}
		}
		user(t, db, f.h, "monitor-reader", "admin")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='monitor-reader-role'"); err != nil {
			t.Fatal(err)
		}
		workers.Add(1)
		go func(gateways map[string]payment.QueryGateway) {
			defer workers.Done()
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if _, err := db.ProcessAutomaticPaymentCheck(ctx, gateways); err != nil && ctx.Err() == nil {
						t.Error(err)
					}
				}
			}
		}(cfg.PaymentQueryGateways())
	})
	stop()
	workers.Wait()
	var confirmed, balanceEntries int
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER(WHERE i.status='paid'),
 (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=ANY($1::bigint[]))
 FROM invoices i WHERE i.id=ANY($1::bigint[])`, invoices).Scan(&confirmed, &balanceEntries); err != nil || confirmed != 1 || balanceEntries != 0 {
		t.Fatal("browser did not prove automatic payment settlement", confirmed, balanceEntries, err)
	}
}
