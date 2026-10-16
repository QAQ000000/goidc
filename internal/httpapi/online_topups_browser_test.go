package httpapi

import (
	"github.com/codex/zjmfgo/internal/store"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestOnlineTopupBrowserFixture(t *testing.T) {
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
	adminBrowserFixture(t, "topup-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		_ = f
		if _, err := db.Pool.Exec(t.Context(), `UPDATE finance_settings SET topup_enabled=true,min_topup_cents=100,max_topup_cents=100000,max_balance_cents=0,require_active_order=false WHERE id=1`); err != nil {
			t.Fatal(err)
		}
	})
}
