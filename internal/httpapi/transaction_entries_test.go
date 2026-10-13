package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

type transactionEntriesFixture struct {
	upgradeFixture
	readerID                                   int64
	reader                                     string
	mockInvoice, creditInvoice, pendingInvoice int64
}

func setupTransactionEntries(t *testing.T, db *store.Store, h http.Handler) transactionEntriesFixture {
	t.Helper()
	f := setupConfigurationUpgradeFixture(t, db, h, false)
	for n := 1; n <= 22; n++ {
		expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", f.uid), map[string]any{"amount_cents": 100 + n, "note": fmt.Sprintf("流水验收充值 %02d", n)}, f.root, fmt.Sprintf("entries-topup-%02d", n)), 201)
	}
	renewal := request(h, "POST", fmt.Sprintf("/api/v1/services/%d/renew", f.sid), map[string]any{}, f.customer, "entries-renew-create")
	expect(t, renewal, 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", int64(data(t, renewal)["invoice_id"].(float64))), map[string]any{}, f.customer, "entries-renew-pay"), 201)
	manual := manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "entries-balance-create")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", manual), map[string]any{"expected_version": 1}, f.customer, "entries-balance-pay"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", manual), map[string]any{}, f.root, "entries-balance-refund"), 201)
	mock := manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "entries-mock-create")
	intent := request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", mock), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "entries-mock-intent")
	expect(t, intent, 201)
	if _, err := db.ProcessPaymentWebhook(t.Context(), "mock", "entries-mock-callback", data(t, intent)["provider_payment_id"].(string), mock, 3000, "succeeded", []byte(`{"status":"succeeded"}`), "entries-external-0001"); err != nil {
		t.Fatal(err)
	}
	pending := manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "entries-pending-create")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", pending), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "entries-pending-intent"), 201)
	expect(t, request(h, "PUT", "/api/v1/admin/credit-settings", map[string]any{"enabled": true, "limit_cents": 50000, "auto_recharge": false, "expected_version": 1}, f.root, "entries-credit-settings"), 200)
	expect(t, request(h, "PUT", fmt.Sprintf("/api/v1/admin/credit/%d/limit", f.uid), map[string]any{"limit_cents": 50000, "expected_version": 1}, f.root, "entries-credit-limit"), 201)
	credit := manualInvoiceCreate(t, h, f.root, manualInvoiceInput(f.uid), "entries-credit-create")
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", credit), map[string]any{"expected_version": 1}, f.customer, "entries-credit-pay"), 201)
	var otherID int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT id FROM users WHERE username='upgrade-other'").Scan(&otherID); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", otherID), map[string]any{"amount_cents": 7777, "note": "其他客户不可泄漏"}, f.root, "entries-other-topup"), 201)
	readerID, reader := user(t, db, h, "transactions-entry-reader", "admin")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view','customers.view'] WHERE name='transactions-entry-reader-role'; UPDATE users SET customer_scope='owned',customer_ownerless=false,sales_enabled=true WHERE username='transactions-entry-reader'"); err != nil {
		t.Fatal(err)
	}
	salesAssignment(t, h, f.root, f.uid, readerID, 1)
	return transactionEntriesFixture{f, readerID, reader, mock, credit, pending}
}

func TestTransactionEntriesPageFiltersAndRecordedSources(t *testing.T) {
	db, h := testServer(t)
	f := setupTransactionEntries(t, db, h)
	get := func(query string) map[string]any {
		t.Helper()
		w := request(h, "GET", "/api/v1/admin/transactions/page"+query, nil, f.root, "")
		expect(t, w, 200)
		return data(t, w)
	}
	page := get("?limit=20")
	if page["total"] != float64(30) || page["limit"] != float64(20) || page["offset"] != float64(0) || len(page["items"].([]any)) != 20 {
		t.Fatal("page contract", page)
	}
	next := get("?limit=20&offset=20")
	if next["total"] != float64(30) || len(next["items"].([]any)) != 10 {
		t.Fatal("second page", next)
	}
	seen := map[string]bool{}
	for _, items := range [][]any{page["items"].([]any), next["items"].([]any)} {
		for _, item := range items {
			row := item.(map[string]any)
			key := row["event_key"].(string)
			if seen[key] {
				t.Fatal("unstable duplicate page", key)
			}
			seen[key] = true
		}
	}
	all := get("?limit=100")
	var gateway, credit int
	for _, item := range all["items"].([]any) {
		row := item.(map[string]any)
		if row["currency"] != "CNY" || row["customer_name"] == nil || row["amount_cents"] == nil {
			t.Fatal("missing provenance", row)
		}
		if row["invoice_id"] == float64(f.pendingInvoice) {
			t.Fatal("pending payment counted as recorded event", row)
		}
		if row["invoice_id"] == float64(f.mockInvoice) {
			gateway++
			if row["source"] != "payment" || row["channel"] != "mock" || row["transaction_no"] != "entries-external-0001" || row["direction"] != "income" {
				t.Fatal("gateway provenance", row)
			}
		}
		if row["invoice_id"] == float64(f.creditInvoice) {
			credit++
			if row["source"] != "invoice" || row["channel"] != "credit" || row["direction"] != "neutral" {
				t.Fatal("credit incorrectly counted as cash", row)
			}
		}
	}
	if gateway != 1 || credit != 1 {
		t.Fatal("paid invoice duplicated or missing", gateway, credit)
	}
	cases := []struct {
		query string
		total float64
	}{
		{"?kind=topup", 24}, {"?kind=product", 1}, {"?kind=renew", 1}, {"?direction=expense", 3}, {"?direction=neutral&channel=credit", 1},
		{"?q=entries-external-0001", 1}, {"?q=upgrade-other", 1}, {"?q=does-not-exist", 0},
		{"?min_cents=102&max_cents=102", 1}, {"?channel=admin_topup&min_cents=100&max_cents=200", 22},
		{fmt.Sprintf("?customer_id=%d", f.uid), 29}, {"?offset=100", 30},
	}
	for _, c := range cases {
		v := get(c.query)
		if v["total"] != c.total {
			t.Fatal("filter total", c.query, v)
		}
		if c.query == "?offset=100" && len(v["items"].([]any)) != 0 {
			t.Fatal("trailing page not empty", v)
		}
	}
	// Test inclusive Shanghai dates with adjacent events across midnight.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE balance_ledger SET created_at=CASE delta_cents WHEN 101 THEN '2026-01-01T15:59:59Z'::timestamptz ELSE '2026-01-01T16:00:00Z'::timestamptz END WHERE user_id=$1 AND entry_type='topup' AND delta_cents IN (101,102)`, f.uid); err != nil {
		t.Fatal(err)
	}
	datePage := get("?from=2026-01-02&to=2026-01-02")
	if datePage["total"] != float64(1) || datePage["items"].([]any)[0].(map[string]any)["amount_cents"] != float64(102) {
		t.Fatal("Shanghai inclusive date boundary", datePage)
	}
	for _, query := range []string{"?kind=unknown", "?direction=unknown", "?from=2026-02-30", "?from=2026-02-02&to=2026-01-01", "?customer_id=-1", "?customer_id=bad", "?min_cents=-1", "?min_cents=1.01", "?min_cents=100&max_cents=99", "?max_cents=9223372036854775808", "?q=" + strings.Repeat("a", 121)} {
		expect(t, request(h, "GET", "/api/v1/admin/transactions/page"+query, nil, f.root, ""), 400)
	}
}

func TestTransactionEntriesCustomerScopePermissionsAndReadOnly(t *testing.T) {
	db, h := testServer(t)
	f := setupTransactionEntries(t, db, h)
	path := "/api/v1/admin/transactions/page"
	expect(t, request(h, "GET", path, nil, "", ""), 401)
	expect(t, request(h, "GET", path, nil, f.customer, ""), 403)
	var before int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events").Scan(&before); err != nil {
		t.Fatal(err)
	}
	page := data(t, request(h, "GET", path+"?limit=100", nil, f.reader, ""))
	if page["total"] != float64(29) {
		t.Fatal("reader scoped total", page)
	}
	for _, item := range page["items"].([]any) {
		row := item.(map[string]any)
		if row["user_id"] != float64(f.uid) {
			t.Fatal("reader crossed customer scope", row)
		}
	}
	for _, query := range []string{"?q=upgrade-other", "?q=其他客户不可泄漏", "?min_cents=7777&max_cents=7777"} {
		w := request(h, "GET", path+query, nil, f.reader, "")
		expect(t, w, 200)
		if data(t, w)["total"] != float64(0) {
			t.Fatal("out of scope search count leaked", w.Body.String())
		}
	}
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", f.mockInvoice), map[string]any{}, f.reader, "entries-reader-refund"), 403)
	var after int64
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM audit_events").Scan(&after); err != nil || after != before {
		t.Fatal("read mutated audit", before, after, err)
	}
	// Manage alone does not confer view access; no new permission expansion.
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.manage'] WHERE name='transactions-entry-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, f.reader, ""), 403)
	if _, err := db.Pool.Exec(t.Context(), "UPDATE admin_roles SET permissions=ARRAY['invoices.view'] WHERE name='transactions-entry-reader-role'"); err != nil {
		t.Fatal(err)
	}
	expect(t, request(h, "GET", path, nil, f.reader, ""), 200)
	salesAssignment(t, h, f.root, f.uid, nil, 2)
	for _, query := range []string{"", fmt.Sprintf("?customer_id=%d", f.uid), "?q=upgrade-customer"} {
		w := request(h, "GET", path+query, nil, f.reader, "")
		expect(t, w, 200)
		v := data(t, w)
		if v["total"] != float64(0) || len(v["items"].([]any)) != 0 {
			t.Fatal("revoked ownership leaked records or total", v)
		}
	}
}

func TestTransactionEntriesBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "transactions-entry-super", func(db *store.Store, h http.Handler) { setupTransactionEntries(t, db, h) })
}
