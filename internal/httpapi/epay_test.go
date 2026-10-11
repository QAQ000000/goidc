package httpapi

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func epayTestConfig() config.Config {
	return config.Config{PublicOrigin: "http://localhost:3088", PaymentMockEnabled: true, ProvisionEncryptionKey: []byte("01234567890123456789012345678901"), PaymentGatewaySecret: "integration-payment-secret", EpayAPIURL: "https://merchant.example", EpayPID: "10001", EpayKey: "integration-epay-secret", EpayTypes: []string{"alipay", "wxpay"}}
}

func TestEpayBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	page := template.Must(template.New("checkout").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>本地易支付协议测试</title><main style="max-width:32rem;margin:3rem auto;padding:1rem;font-family:sans-serif"><h1>本地易支付协议测试</h1><p>测试金额：{{.Money}} 元</p><p>本页面不会收取真实资金。</p><form action="{{.Action}}" method="post"><button type="submit">确认模拟付款</button></form></main></html>`))
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		original := q.Get("sign")
		signEpayTest(q)
		if original != q.Get("sign") {
			http.Error(w, "bad signature", 400)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/submit.php" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_ = page.Execute(w, map[string]any{"Money": q.Get("money"), "Action": template.URL("/complete?" + q.Encode())})
			return
		}
		if r.Method != "POST" || r.URL.Path != "/complete" {
			http.NotFound(w, r)
			return
		}
		target := q.Get("notify_url")
		if target != "http://localhost:3089/api/v1/payments/epay_alipay/webhook" && target != "http://localhost:3089/api/v1/payments/epay_wxpay/webhook" {
			http.Error(w, "invalid test target", 400)
			return
		}
		callback := epayNotification(q, "browser-"+q.Get("out_trade_no"))
		client := http.Client{Timeout: 5 * time.Second}
		response, err := client.PostForm(target, callback)
		if err != nil {
			http.Error(w, "test callback failed", 502)
			return
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != 200 || string(body) != "success" {
			http.Error(w, "test callback rejected", 502)
			return
		}
		if !strings.HasPrefix(q.Get("return_url"), "http://localhost:3089/console/invoices/") {
			http.Error(w, "invalid test return", 400)
			return
		}
		http.Redirect(w, r, q.Get("return_url"), http.StatusSeeOther)
	}))
	defer merchant.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", merchant.URL)
	adminBrowserFixture(t, "epay-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "epay-browser-create")
	})
}

// Independent implementation of the PHP plugin's published sorted MD5 recipe.
func signEpayTest(v url.Values) {
	signEpayTestKey(v, "integration-epay-secret")
}

func signEpayTestKey(v url.Values, key string) {
	var keys []string
	for k := range v {
		if k != "sign" && k != "sign_type" && v.Get(k) != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		parts = append(parts, k+"="+v.Get(k))
	}
	sum := md5.Sum([]byte(strings.Join(parts, "&") + key))
	v.Set("sign", hex.EncodeToString(sum[:]))
	v.Set("sign_type", "MD5")
}

func epayCallback(h http.Handler, method, provider string, v url.Values) *httptest.ResponseRecorder {
	path := "/api/v1/payments/" + provider + "/webhook"
	body := ""
	if method == "GET" {
		path += "?" + v.Encode()
	} else {
		body = v.Encode()
	}
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func epayIntent(t *testing.T, f upgradeFixture, invoice int64, provider, key string) url.Values {
	t.Helper()
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": provider, "expected_version": 1}, f.customer, key)
	expect(t, w, 201)
	u, err := url.Parse(data(t, w)["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	v := u.Query()
	var amount int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT amount_cents FROM invoices WHERE id=$1", invoice).Scan(&amount); err != nil {
		t.Fatal(err)
	}
	if v.Get("money") != fmt.Sprintf("%d.%02d", amount/100, amount%100) || v.Get("pid") != "10001" {
		t.Fatal("unexpected checkout", v.Get("money"), v.Get("pid"))
	}
	return v
}

func epayNotification(checkout url.Values, trade string) url.Values {
	v := url.Values{"pid": {checkout.Get("pid")}, "type": {checkout.Get("type")}, "out_trade_no": {checkout.Get("out_trade_no")}, "trade_no": {trade}, "money": {checkout.Get("money")}, "trade_status": {"TRADE_SUCCESS"}}
	signEpayTest(v)
	return v
}

func TestEpayPaymentLifecycleAndReplay(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "epay-manual-create")
	v := epayIntent(t, f, iid, "epay_alipay", "epay-intent-create")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{"expected_version": 1}, f.customer, "epay-pending-balance"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "epay_wxpay", "expected_version": 1}, f.customer, "epay-other-channel"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "epay_alipay", "expected_version": 1}, f.customer, "epay-intent-create"), 200)
	callback := epayNotification(v, "trade-1")
	bad := epayNotification(v, "trade-1")
	bad.Set("money", "1.00")
	if w := epayCallback(f.h, "GET", "epay_alipay", bad); w.Code != 401 {
		t.Fatal("tampered callback", w.Code)
	}
	bad.Set("money", "29.99")
	signEpayTest(bad)
	if w := epayCallback(f.h, "POST", "epay_alipay", bad); w.Code != 409 {
		t.Fatal("wrong amount callback", w.Code)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE site_settings SET maintenance_enabled=true WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{"GET", "POST", "GET"} {
		w := epayCallback(f.h, method, "epay_alipay", callback)
		if w.Code != 200 || w.Body.String() != "success" {
			t.Fatal("epay callback", w.Code, w.Body.String())
		}
	}
	var status, external string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT i.status,p.external_transaction_id FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1", iid).Scan(&status, &external); err != nil || status != "paid" || external != payment.EpayMerchantNamespace(epayTestConfig().EpayAPIURL, "10001")+":trade-1" {
		t.Fatal("epay did not settle", status, external, err)
	}
	var ledger, jobs int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1),(SELECT count(*) FROM provision_jobs)", iid).Scan(&ledger, &jobs); err != nil || ledger != 0 || jobs != 1 {
		t.Fatal("manual payment created balance debit or jobs", ledger, jobs, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE site_settings SET maintenance_enabled=false WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	other := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "epay-manual-other")
	v2 := epayIntent(t, f, other, "epay_wxpay", "epay-intent-other")
	if w := epayCallback(f.h, "GET", "epay_wxpay", epayNotification(v2, "trade-1")); w.Code != 409 {
		t.Fatal("cross-method duplicate trade accepted", w.Code)
	}
	if w := epayCallback(f.h, "GET", "epay_alipay", epayNotification(v, "different-trade")); w.Code != 409 {
		t.Fatal("different trade for paid intent accepted", w.Code)
	}
	page := request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, page, 200)
	if strings.Contains(page.Body.String(), "integration-epay-secret") || !strings.Contains(page.Body.String(), "epay_alipay") {
		t.Fatal("provider directory invalid")
	}
}

func TestEpayCallbackRollbackAndInvalidFields(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "epay-rollback-create")
	v := epayIntent(t, f, iid, "epay_alipay", "epay-rollback-intent")
	for _, pair := range [][2]string{{"money", "30.001"}, {"money", "NaN"}, {"money", "3e1"}, {"pid", "10002"}, {"type", "wxpay"}, {"trade_status", "WAIT_BUYER_PAY"}, {"out_trade_no", "123"}} {
		bad := epayNotification(v, "rollback-trade")
		bad.Set(pair[0], pair[1])
		signEpayTest(bad)
		w := epayCallback(f.h, "GET", "epay_alipay", bad)
		if w.Code < 400 {
			t.Fatal("invalid callback accepted", pair)
		}
	}
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_epay_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.webhook.succeeded' THEN RAISE EXCEPTION 'epay audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_epay_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_epay_audit()`); err != nil {
		t.Fatal(err)
	}
	callback := epayNotification(v, "rollback-trade")
	if w := epayCallback(f.h, "POST", "epay_alipay", callback); w.Code != 500 {
		t.Fatal("audit failure acknowledged", w.Code)
	}
	var status string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", iid).Scan(&status); err != nil || status != "unpaid" {
		t.Fatal("callback failed to roll back", status, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_epay_audit"); err != nil {
		t.Fatal(err)
	}
	if w := epayCallback(f.h, "GET", "epay_alipay", callback); w.Code != 200 {
		t.Fatal("retry after rollback", w.Code)
	}
}

func TestEpayPendingGuardsAndServiceSettlement(t *testing.T) {
	for _, kind := range []string{"order", "upgrade", "renewal"} {
		t.Run(kind, func(t *testing.T) {
			f := configurationUpgradeFixture(t, false)
			f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
			var invoice, order, change int64
			var action string
			var dueBefore time.Time
			var balanceBefore int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT s.next_due_at,u.balance_cents FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=$1", f.sid).Scan(&dueBefore, &balanceBefore); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "order":
				version, item := configuredCart(t, f.h, f.customer, f.pid, []int64{f.small}, 1)
				w := request(f.h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{item}}, f.customer, "epay-new-order")
				expect(t, w, 201)
				invoice = int64(data(t, w)["invoice_id"].(float64))
				order = int64(data(t, w)["order_id"].(float64))
				action = "create"
			case "upgrade":
				out := f.submit(t, f.quote(t, f.large), "epay-upgrade-submit")
				invoice, change = int64(out["invoice_id"].(float64)), int64(out["id"].(float64))
				action = "upgrade"
			case "renewal":
				w := request(f.h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "epay-renewal-create")
				expect(t, w, 201)
				invoice = int64(data(t, w)["invoice_id"].(float64))
				action = "renew"
			}
			// Two independently keyed channels must not create two payable intents.
			codes := make(chan int, 2)
			var wg sync.WaitGroup
			for _, provider := range []string{"epay_alipay", "epay_wxpay"} {
				wg.Add(1)
				go func() {
					defer wg.Done()
					codes <- request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": provider, "expected_version": 1}, f.customer, "epay-race-"+provider).Code
				}()
			}
			wg.Wait()
			close(codes)
			created, conflicts := 0, 0
			for code := range codes {
				switch code {
				case 201:
					created++
				case 409:
					conflicts++
				default:
					t.Fatal("unexpected create race response", code)
				}
			}
			if created != 1 || conflicts != 1 {
				t.Fatal("multiple payable intents", created, conflicts)
			}
			var provider, checkout string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT provider,checkout_url FROM payment_transactions WHERE invoice_id=$1", invoice).Scan(&provider, &checkout); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()-interval '1 hour' WHERE invoice_id=$1", invoice); err != nil {
				t.Fatal(err)
			}
			path := fmt.Sprintf("/api/v1/invoices/%d", invoice)
			detail := request(f.h, "GET", path, nil, f.customer, "")
			expect(t, detail, 200)
			if data(t, detail)["real_payment_pending"] != true || !strings.Contains(detail.Body.String(), "submit.php") {
				t.Fatal("expired pending intent lost customer resume")
			}
			admin := request(f.h, "GET", "/api/v1/admin/invoices/"+fmt.Sprint(invoice), nil, f.root, "")
			expect(t, admin, 200)
			if strings.Contains(admin.Body.String(), "submit.php") || strings.Contains(admin.Body.String(), "integration-epay-secret") {
				t.Fatal("administrator received payable URL or credentials")
			}
			expect(t, request(f.h, "GET", path, nil, f.other, ""), 404)
			expect(t, request(f.h, "POST", path+"/pay", map[string]any{}, f.customer, "epay-expired-balance"), 409)
			if kind == "order" {
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/orders/%d/cancel", order), map[string]any{}, f.customer, "epay-order-cancel"), 409)
				expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/orders/%d/cancel", order), map[string]any{"expected_version": orderVersion(t, f.db, order)}, f.root, "epay-admin-cancel"), 409)
			}
			if kind == "upgrade" {
				expect(t, request(f.h, "DELETE", fmt.Sprintf("/api/v1/services/%d/configuration-changes/%d", f.sid, change), nil, f.customer, ""), 409)
			}
			u, err := url.Parse(checkout)
			if err != nil {
				t.Fatal(err)
			}
			callback := epayNotification(u.Query(), "race-trade")
			responses := make(chan *httptest.ResponseRecorder, 8)
			cancelCode := make(chan int, 1)
			if kind == "order" || kind == "upgrade" {
				wg.Add(1)
				go func() {
					defer wg.Done()
					if kind == "order" {
						cancelCode <- request(f.h, "POST", fmt.Sprintf("/api/v1/orders/%d/cancel", order), map[string]any{}, f.customer, "epay-concurrent-cancel").Code
					} else {
						cancelCode <- request(f.h, "DELETE", fmt.Sprintf("/api/v1/services/%d/configuration-changes/%d", f.sid, change), nil, f.customer, "").Code
					}
				}()
			}
			for i := 0; i < cap(responses); i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					responses <- epayCallback(f.h, "GET", provider, callback)
				}()
			}
			wg.Wait()
			if (kind == "order" || kind == "upgrade") && <-cancelCode != 409 {
				t.Fatal("concurrent cancellation escaped pending guard or deadlocked")
			}
			close(responses)
			for w := range responses {
				if w.Code != 200 || w.Body.String() != "success" {
					t.Fatal("concurrent notification rejected", w.Code, w.Body.String())
				}
			}
			var status string
			var events, audits, jobs int
			expectedJobs := 1
			if kind == "renewal" {
				expectedJobs = 0 // Active service renewal extends the due date directly.
			}
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,
 (SELECT count(*) FROM payment_webhook_events),
 (SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=$1::bigint::text),
 (SELECT count(*) FROM provision_jobs WHERE action=$2 AND status='pending')
 FROM invoices i WHERE i.id=$1::bigint`, invoice, action).Scan(&status, &events, &audits, &jobs); err != nil || status != "paid" || events != 1 || audits != 1 || jobs != expectedJobs {
				t.Fatal("settlement duplicated or not queued", status, events, audits, jobs, err)
			}
			var balanceAfter int64
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.uid).Scan(&balanceAfter); err != nil || balanceAfter != balanceBefore {
				t.Fatal("external settlement debited balance", balanceAfter, err)
			}
			if kind == "renewal" {
				var extendedOnce bool
				if err := f.db.Pool.QueryRow(t.Context(), "SELECT next_due_at=$2::timestamptz+interval '1 month' FROM services WHERE id=$1", f.sid, dueBefore).Scan(&extendedOnce); err != nil || !extendedOnce {
					t.Fatal("renewal did not extend exactly once", err)
				}
			}
			expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", invoice), map[string]any{}, f.root, "epay-blocked-refund"), 409)
		})
	}
}
