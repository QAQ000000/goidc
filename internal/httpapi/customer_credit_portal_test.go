package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
)

type creditPortalFixture struct {
	db                *store.Store
	h                 http.Handler
	root, a, b, empty string
	aid, bid, eid     int64
}

func newCreditPortalFixture(t *testing.T) creditPortalFixture {
	t.Helper()
	db, h := testServer(t)
	_, root := user(t, db, h, "credit-portal-seeder", "super_admin")
	aid, a := user(t, db, h, "credit-portal-a", "customer")
	bid, b := user(t, db, h, "credit-portal-b", "customer")
	eid, empty := user(t, db, h, "credit-portal-empty", "customer")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true;UPDATE customer_credit_accounts SET limit_cents=100000;UPDATE users SET balance_cents=1234 WHERE role='customer'"); err != nil {
		t.Fatal(err)
	}
	return creditPortalFixture{db, h, root, a, b, empty, aid, bid, eid}
}
func (f creditPortalFixture) invoice(t *testing.T, uid int64, cookie, key string, paid bool) int64 {
	t.Helper()
	id := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(uid), key)
	if paid {
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay-credit", id), map[string]any{"expected_version": 1}, cookie, key+"-pay"), 201)
	}
	return id
}
func (f creditPortalFixture) repay(t *testing.T, id, amount, version int64, key string) {
	t.Helper()
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/credit/consumptions/%d/repay", id), map[string]any{"amount_cents": amount, "expected_version": version, "receipt": "private-receipt-" + key, "note": "private-internal-note", "received_at": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339Nano)}, f.root, key), 201)
}
func creditPortalWhitelist(t *testing.T, row map[string]any, keys ...string) {
	t.Helper()
	allowed := map[string]bool{}
	for _, k := range keys {
		allowed[k] = true
	}
	for k := range row {
		if !allowed[k] {
			t.Fatal("unexpected public field", k)
		}
	}
}
func creditPortalDetail(t *testing.T, f creditPortalFixture, id int64) map[string]any {
	t.Helper()
	w := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d/credit", id), nil, f.a, "")
	expect(t, w, 200)
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("financial response cacheable")
	}
	return data(t, w)["credit"].(map[string]any)
}

func TestCustomerCreditPortalOwnershipAndPrivacy(t *testing.T) {
	f := newCreditPortalFixture(t)
	aid := f.invoice(t, f.aid, f.a, "portal-a-invoice", true)
	bid := f.invoice(t, f.bid, f.b, "portal-b-invoice", true)
	unpaid := f.invoice(t, f.aid, f.a, "portal-a-unpaid", false)
	f.repay(t, aid, 1000, 1, "portal-private-repay")
	for _, path := range []string{"/api/v1/credit/account", "/api/v1/credit/consumptions", "/api/v1/credit/repayments", fmt.Sprintf("/api/v1/invoices/%d/credit", aid)} {
		expect(t, request(f.h, "GET", path, nil, "", ""), 401)
		expect(t, request(f.h, "GET", path, nil, f.root, ""), 403)
		w := request(f.h, "GET", path, nil, f.a, "")
		expect(t, w, 200)
		if w.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("missing no-store", path)
		}
	}
	for _, path := range []string{"/api/v1/credit/account?user_id=1", "/api/v1/credit/consumptions?customer_id=1", "/api/v1/credit/repayments?tenant_id=1", "/api/v1/credit/consumptions?unexpected=1"} {
		expect(t, request(f.h, "GET", path, nil, f.a, ""), 400)
	}
	expect(t, request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d/credit", bid), nil, f.a, ""), 404)
	w := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d/credit", unpaid), nil, f.a, "")
	expect(t, w, 200)
	if data(t, w)["credit"] != nil {
		t.Fatal("unpaid invoice invented credit")
	}
	account := data(t, request(f.h, "GET", "/api/v1/credit/account", nil, f.a, ""))
	creditPortalWhitelist(t, account, "display_enabled", "payment_enabled", "limit_cents", "used_cents", "available_cents", "frozen", "version", "updated_at")
	consumption := creditPortalDetail(t, f, aid)
	creditPortalWhitelist(t, consumption, "invoice_id", "invoice_no", "amount_cents", "remaining_cents", "status", "version", "created_at", "refunded_at", "repaid_cents")
	for _, suffix := range []string{"consumptions", "repayments"} {
		page := data(t, request(f.h, "GET", "/api/v1/credit/"+suffix, nil, f.a, ""))
		if page["total"] != float64(1) {
			t.Fatal("cross-customer count", page)
		}
		for _, raw := range page["items"].([]any) {
			row := raw.(map[string]any)
			if suffix == "repayments" {
				creditPortalWhitelist(t, row, "id", "invoice_id", "invoice_no", "amount_cents", "received_at", "created_at")
			} else {
				creditPortalWhitelist(t, row, "invoice_id", "invoice_no", "amount_cents", "remaining_cents", "status", "version", "created_at", "refunded_at")
			}
			if row["invoice_id"] != float64(aid) {
				t.Fatal("cross customer row")
			}
		}
	}
	if _, err := f.db.Pool.Exec(t.Context(), "DELETE FROM customer_credit_accounts WHERE user_id=$1", f.eid); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "GET", "/api/v1/credit/account", nil, f.empty, ""), 503)
}

func TestCustomerCreditPortalMoneyRepaymentRefundAndSwitches(t *testing.T) {
	f := newCreditPortalFixture(t)
	id := f.invoice(t, f.aid, f.a, "money-a-invoice", true)
	check := func(remaining, repaid, used int64, status string) {
		t.Helper()
		d := creditPortalDetail(t, f, id)
		if d["remaining_cents"] != float64(remaining) || d["repaid_cents"] != float64(repaid) || d["status"] != status {
			t.Fatal("incorrect debt facts", d)
		}
		a := data(t, request(f.h, "GET", "/api/v1/credit/account", nil, f.a, ""))
		if a["used_cents"] != float64(used) || a["available_cents"] != float64(100000-used) {
			t.Fatal("credit conservation", a)
		}
		var balance int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT balance_cents FROM users WHERE id=$1", f.aid).Scan(&balance); err != nil || balance != 1234 {
			t.Fatal("credit changed balance", balance, err)
		}
	}
	check(3000, 0, 3000, "outstanding")
	f.repay(t, id, 1000, 1, "money-partial")
	check(2000, 1000, 2000, "outstanding")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, f.root, "money-partial-refund"), 409)
	check(2000, 1000, 2000, "outstanding")
	f.repay(t, id, 2000, 2, "money-full")
	check(0, 3000, 0, "repaid")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, f.root, "money-full-refund"), 409)
	id = f.invoice(t, f.aid, f.a, "money-refund", true)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, f.root, "money-unpaid-refund"), 201)
	check(0, 0, 0, "refunded")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=false"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE customer_credit_accounts SET frozen=true WHERE user_id=$1", f.aid); err != nil {
		t.Fatal(err)
	}
	a := data(t, request(f.h, "GET", "/api/v1/credit/account", nil, f.a, ""))
	if a["payment_enabled"] != false || a["frozen"] != true {
		t.Fatal("switch state", a)
	}
	check(0, 0, 0, "refunded")
	expect(t, request(f.h, "GET", "/api/v1/credit/repayments", nil, f.a, ""), 200)
}

func TestCustomerCreditPortalFiltersDatesAndPaging(t *testing.T) {
	f := newCreditPortalFixture(t)
	ids := []int64{}
	for i := 0; i < 3; i++ {
		ids = append(ids, f.invoice(t, f.aid, f.a, fmt.Sprintf("filter-%d", i), true))
	}
	f.invoice(t, f.bid, f.b, "filter-other", true)
	for i, id := range ids {
		if _, err := f.db.Pool.Exec(t.Context(), "UPDATE credit_invoice_payments SET created_at=$2 WHERE invoice_id=$1", id, time.Date(2026, 10, 15+i, 16, 0, 0, 0, time.UTC)); err != nil {
			t.Fatal(err)
		}
	}
	for _, q := range []string{"%", "_"} {
		w := request(f.h, "GET", "/api/v1/credit/consumptions?q="+url.QueryEscape(q), nil, f.a, "")
		expect(t, w, 200)
		if data(t, w)["total"] != float64(0) {
			t.Fatal("wildcard q not literal", q, w.Body.String())
		}
	}
	page := data(t, request(f.h, "GET", "/api/v1/credit/consumptions?date_from=2026-10-17&date_to=2026-10-17", nil, f.a, ""))
	if page["total"] != float64(1) || page["items"].([]any)[0].(map[string]any)["invoice_id"] != float64(ids[1]) {
		t.Fatal("Shanghai date boundary", page)
	}

	f.repay(t, ids[0], 1000, 1, "filter-repay-one")
	f.repay(t, ids[1], 1000, 1, "filter-repay-two")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE credit_repayments SET received_at=CASE WHEN invoice_id=$1 THEN '2026-10-16 15:59:59+00'::timestamptz ELSE '2026-10-16 16:00:00+00'::timestamptz END", ids[0]); err != nil {
		t.Fatal(err)
	}
	repaymentPage := data(t, request(f.h, "GET", "/api/v1/credit/repayments?date_from=2026-10-17&date_to=2026-10-17", nil, f.a, ""))
	if repaymentPage["total"] != float64(1) || repaymentPage["items"].([]any)[0].(map[string]any)["invoice_id"] != float64(ids[1]) {
		t.Fatal("repayment Shanghai received date boundary", repaymentPage)
	}
	expect(t, request(f.h, "GET", "/api/v1/credit/repayments?status=outstanding", nil, f.a, ""), 400)
	emptyPage := data(t, request(f.h, "GET", "/api/v1/credit/consumptions", nil, f.empty, ""))
	if emptyPage["total"] != float64(0) || len(emptyPage["items"].([]any)) != 0 {
		t.Fatal("empty customer saw data")
	}
	seen := map[float64]bool{}
	for i := 0; i < 3; i++ {
		w := request(f.h, "GET", fmt.Sprintf("/api/v1/credit/consumptions?limit=1&offset=%d", i), nil, f.a, "")
		expect(t, w, 200)
		d := data(t, w)
		if d["total"] != float64(3) {
			t.Fatal("wrong own total")
		}
		row := d["items"].([]any)[0].(map[string]any)
		id := row["invoice_id"].(float64)
		if seen[id] {
			t.Fatal("page duplicate")
		}
		seen[id] = true
	}
	for _, q := range []string{"status=bad", "invoice_id=-1", "date_from=2026-02-30", "date_from=2026-10-17&date_to=2026-10-16"} {
		expect(t, request(f.h, "GET", "/api/v1/credit/consumptions?"+q, nil, f.a, ""), 400)
	}
	w := request(f.h, "GET", "/api/v1/credit/consumptions?offset=999", nil, f.a, "")
	expect(t, w, 200)
	if data(t, w)["total"] != float64(3) || len(data(t, w)["items"].([]any)) != 0 {
		t.Fatal("overflow wrong")
	}
}

func TestCustomerCreditPortalBrowserFixture(t *testing.T) {
	adminBrowserFixture(t, "credit-portal-super", func(db *store.Store, h http.Handler) {
		manager, root := user(t, db, h, "credit-portal-manager", "admin")
		contentRole(t, db, manager, "credit.view", "credit.manage", "invoices.view", "invoices.manage", "invoices.refund", "customers.view", "settings.view", "settings.manage")
		reader, _ := user(t, db, h, "credit-portal-reader", "admin")
		contentRole(t, db, reader, "credit.view", "invoices.view", "customers.view", "settings.view")
		aid, a := user(t, db, h, "credit-portal-a", "customer")
		bid, b := user(t, db, h, "credit-portal-b", "customer")
		eid, _ := user(t, db, h, "credit-portal-empty", "customer")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE credit_settings SET enabled=true;UPDATE customer_credit_accounts SET limit_cents=200000;UPDATE users SET balance_cents=1234 WHERE role='customer'"); err != nil {
			t.Fatal(err)
		}
		f := creditPortalFixture{db, h, root, a, b, "", aid, bid, eid}
		ids := map[string]int64{}
		for i, name := range []string{"outstanding", "partial", "repaid", "refunded", "unpaid", "other"} {
			uid, cookie := aid, a
			if name == "other" {
				uid, cookie = bid, b
			}
			id := f.invoice(t, uid, cookie, fmt.Sprintf("fixture-credit-%d", i), name != "unpaid")
			ids[name] = id
			switch name {
			case "partial":
				f.repay(t, id, 1000, 1, "fixture-partial")
			case "repaid":
				f.repay(t, id, 3000, 1, "fixture-repaid")
			case "refunded":
				expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/refund", id), map[string]any{}, root, "fixture-refund"), 201)
			}
		}
		manifest, err := json.Marshal(map[string]any{"manager_id": manager, "reader_id": reader, "customer_a_id": aid, "customer_b_id": bid, "customer_empty_id": eid, "invoice_ids": ids})
		if err != nil {
			t.Fatal(err)
		}
		p := os.Getenv("ZJMFGO_BROWSER_READY_FILE") + ".fixture.json"
		if err = os.WriteFile(p, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Remove(p) })
	})
}
