package httpapi

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func queryProtocolResponse(v url.Values, status int64) map[string]any {
	return map[string]any{"code": 1, "pid": "10001", "type": v.Get("type"), "trade_no": "query-trade", "out_trade_no": v.Get("out_trade_no"), "money": v.Get("money"), "status": status}
}

func reconciliationFixture(t *testing.T, handler http.HandlerFunc) (upgradeFixture, int64, int64, url.Values) {
	t.Helper()
	f := configurationUpgradeFixture(t, false)
	merchant := httptest.NewServer(handler)
	t.Cleanup(merchant.Close)
	cfg := epayTestConfig()
	cfg.EpayAPIURL = merchant.URL
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "reconcile-create")
	v := epayIntent(t, f, iid, "epay_alipay", "reconcile-intent-create")
	var paymentID int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", iid).Scan(&paymentID); err != nil {
		t.Fatal(err)
	}
	return f, iid, paymentID, v
}

func queryPath(invoice, paymentID int64, admin bool) string {
	prefix := ""
	if admin {
		prefix = "/admin"
	}
	return fmt.Sprintf("/api/v1%s/invoices/%d/payments/%d/reconcile", prefix, invoice, paymentID)
}

func clearQueryCooldown(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_reconciliation_checks SET started_at=clock_timestamp()-interval '2 minutes'"); err != nil {
		t.Fatal(err)
	}
}

func TestPaymentReconciliationPendingRollbackPaidAndLateCallback(t *testing.T) {
	var calls, status atomic.Int64
	var checkout url.Values
	f, iid, pid, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		if r.URL.Path != "/api.php" || q.Get("key") != "integration-epay-secret" || q.Get("out_trade_no") != checkout.Get("out_trade_no") {
			t.Error("wrong authenticated query")
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, status.Load()))
	})
	checkout = v
	path := queryPath(iid, pid, false)
	expect(t, request(f.h, "POST", path, nil, f.other, ""), 404)
	_, reader := user(t, f.db, f.h, "query-reader", "admin")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='query-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", queryPath(iid, pid, true), nil, reader, ""), 403)
	if calls.Load() != 0 {
		t.Fatal("unauthorized query contacted merchant")
	}
	w := request(f.h, "POST", path, nil, f.customer, "")
	expect(t, w, 200)
	if data(t, w)["status"] != "pending" {
		t.Fatal("unpaid query changed payment state")
	}
	expect(t, request(f.h, "POST", path, nil, f.customer, ""), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{"expected_version": 1}, f.customer, "query-pending-balance"), 409)
	if calls.Load() != 1 {
		t.Fatal("cooldown did not suppress remote request")
	}
	status.Store(1)
	clearQueryCooldown(t, f.db)
	if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION query_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.reconciliation.completed' THEN RAISE EXCEPTION 'query audit rejected'; END IF; RETURN NEW; END $$; CREATE TRIGGER query_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION query_reject_audit()`); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "POST", path, nil, f.customer, ""), 500)
	var invoiceState, checkState string
	var events int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,(SELECT status FROM payment_reconciliation_checks ORDER BY id DESC LIMIT 1),(SELECT count(*) FROM payment_webhook_events) FROM invoices WHERE id=$1`, iid).Scan(&invoiceState, &checkState, &events); err != nil || invoiceState != "unpaid" || checkState != "error" || events != 0 {
		t.Fatal("query settlement/audit did not roll back", invoiceState, checkState, events, err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER query_reject_audit"); err != nil {
		t.Fatal(err)
	}
	clearQueryCooldown(t, f.db)
	w = request(f.h, "POST", queryPath(iid, pid, true), nil, f.root, "")
	expect(t, w, 200)
	if data(t, w)["status"] != "succeeded" {
		t.Fatal("paid query not settled")
	}
	callback := epayCallback(f.h, "POST", "epay_alipay", epayNotification(v, "query-trade"))
	if callback.Code != 200 || callback.Body.String() != "success" {
		t.Fatal("late notification after query rejected", callback.Code, callback.Body.String())
	}
	var completions, settlementAudits, debits int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,
 (SELECT count(*) FROM payment_reconciliation_checks WHERE status='succeeded'),
 (SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=$1::bigint::text),
 (SELECT count(*) FROM balance_ledger WHERE reference_type='invoice' AND reference_id=$1::bigint)
 FROM invoices i WHERE i.id=$1::bigint`, iid).Scan(&invoiceState, &completions, &settlementAudits, &debits); err != nil || invoiceState != "paid" || completions != 1 || settlementAudits != 1 || debits != 0 {
		t.Fatal("query/notification duplicated settlement", invoiceState, completions, settlementAudits, debits, err)
	}
	detail := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", iid), nil, f.customer, "")
	expect(t, detail, 200)
	if strings.Contains(detail.Body.String(), "integration-epay-secret") || !strings.Contains(detail.Body.String(), `"check_status":"succeeded"`) {
		t.Fatal("query history missing or leaked credentials")
	}
}

func TestPaymentReconciliationConcurrentChecksAndCallbackWins(t *testing.T) {
	var calls atomic.Int64
	entered, release := make(chan struct{}), make(chan struct{})
	var checkout url.Values
	f, iid, pid, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		close(entered)
		<-release
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 0))
	})
	checkout = v
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() { result <- request(f.h, "POST", queryPath(iid, pid, false), nil, f.customer, "") }()
	<-entered
	var wg sync.WaitGroup
	codes := make(chan int, 6)
	for i := 0; i < cap(codes); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes <- request(f.h, "POST", queryPath(iid, pid, false), nil, f.customer, "").Code
		}()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 409 {
			t.Fatal("concurrent query escaped reservation", code)
		}
	}
	callback := epayCallback(f.h, "GET", "epay_alipay", epayNotification(v, "query-trade"))
	if callback.Code != 200 {
		t.Fatal("callback while query in flight failed", callback.Code)
	}
	close(release)
	w := <-result
	expect(t, w, 200)
	if calls.Load() != 1 || data(t, w)["status"] != "succeeded" {
		t.Fatal("pending query overwrote paid callback", calls.Load(), w.Body.String())
	}
	var state string
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM payment_reconciliation_checks").Scan(&state); err != nil || state != "succeeded" {
		t.Fatal("race check result incorrect", state, err)
	}
}

func TestPaymentReconciliationRechecksPermissionAndCustomerTransfer(t *testing.T) {
	for _, mode := range []string{"permission", "transfer", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var checkout url.Values
			f, iid, pid, v := reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(entered)
				<-release
				_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 1))
			})
			checkout = v
			staffID, staff := user(t, f.db, f.h, "query-sales", "admin")
			if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET sales_enabled=true,customer_scope='owned',customer_ownerless=false WHERE id=$1", staffID); err != nil {
				t.Fatal(err)
			}
			salesAssignment(t, f.h, f.root, f.uid, staffID, 1)
			result := make(chan *httptest.ResponseRecorder, 1)
			go func() { result <- request(f.h, "POST", queryPath(iid, pid, true), nil, staff, "") }()
			<-entered
			want := 403
			switch mode {
			case "permission":
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='query-sales-role'"); err != nil {
					t.Fatal(err)
				}
			case "transfer":
				salesAssignment(t, f.h, f.root, f.uid, nil, 2)
				want = 404
			case "disabled":
				want = 404 // Disabled administrators no longer pass the customer scope check.
				if _, err := f.db.Pool.Exec(t.Context(), "UPDATE users SET status='disabled' WHERE id=$1", staffID); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			expect(t, <-result, want)
			var state string
			if err := f.db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", iid).Scan(&state); err != nil || state != "unpaid" {
				t.Fatal("query settled after authority changed", state, err)
			}
		})
	}
}

func TestPaymentReconciliationInvalidResultAndExpiredLease(t *testing.T) {
	var invalid atomic.Bool
	invalid.Store(true)
	var checkout url.Values
	var f upgradeFixture
	var iid, pid int64
	var v url.Values
	f, iid, pid, v = reconciliationFixture(t, func(w http.ResponseWriter, r *http.Request) {
		if invalid.Load() {
			_, _ = io.WriteString(w, `{"code":-1,"msg":"integration-epay-secret"}`)
			return
		}
		if _, err := f.db.Pool.Exec(r.Context(), "UPDATE payment_reconciliation_checks SET lease_until=clock_timestamp()-interval '1 second' WHERE status='checking'"); err != nil {
			t.Error(err)
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 1))
	})
	checkout = v
	path := queryPath(iid, pid, false)
	w := request(f.h, "POST", path, nil, f.customer, "")
	expect(t, w, 502)
	if strings.Contains(w.Body.String(), "integration-epay-secret") {
		t.Fatal("provider message/secret leaked")
	}
	clearQueryCooldown(t, f.db)
	invalid.Store(false)
	w = request(f.h, "POST", path, nil, f.customer, "")
	expect(t, w, 409)
	var state string
	var events, errors int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,(SELECT count(*) FROM payment_webhook_events),
 (SELECT count(*) FROM payment_reconciliation_checks WHERE status='error') FROM invoices i WHERE i.id=$1`, iid).Scan(&state, &events, &errors); err != nil || state != "unpaid" || events != 0 || errors != 2 {
		t.Fatal("expired/invalid query changed money", state, events, errors, err)
	}
	// Simulate a worker/process that died after reserving the remote query.
	if _, err := f.db.Pool.Exec(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,started_at,lease_until)
 VALUES($1,$2,clock_timestamp()-interval '2 minutes',clock_timestamp()-interval '1 minute')`, pid, f.uid); err != nil {
		t.Fatal(err)
	}
	clearQueryCooldown(t, f.db)
	if _, err := f.db.BeginPaymentCheck(t.Context(), f.uid, iid, pid, false); err != nil {
		t.Fatal("stale reservation prevented recovery", err)
	}
	var expired int
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM payment_reconciliation_checks WHERE result_code='lease_expired'").Scan(&expired); err != nil || expired != 1 {
		t.Fatal("stale check not recorded", expired, err)
	}
}

func TestEpayReconciliationBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	var orders, paid sync.Map
	page := template.Must(template.New("query-checkout").Parse(`<!doctype html><html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width"><title>本地付款查询测试</title><main style="max-width:32rem;margin:3rem auto;padding:1rem;font-family:sans-serif"><h1>本地付款查询测试</h1><p>测试金额：{{.Money}} 元</p><p>本页面不收取资金；确认后不发送通知，需回到账单主动查询。</p><form action="{{.Action}}" method="post"><button type="submit">确认模拟付款</button></form></main></html>`))
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
			http.Error(w, "bad test signature", 400)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/submit.php" {
			orders.Store(q.Get("out_trade_no"), q)
			_ = page.Execute(w, map[string]any{"Money": q.Get("money"), "Action": template.URL("/complete?" + q.Encode())})
			return
		}
		if r.Method == "POST" && r.URL.Path == "/complete" && strings.HasPrefix(q.Get("return_url"), "http://localhost:3089/console/invoices/") {
			paid.Store(q.Get("out_trade_no"), true)
			http.Redirect(w, r, q.Get("return_url"), http.StatusSeeOther)
			return
		}
		http.NotFound(w, r)
	}))
	defer merchant.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", merchant.URL)
	adminBrowserFixture(t, "query-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "query-browser-create")
	})
}
