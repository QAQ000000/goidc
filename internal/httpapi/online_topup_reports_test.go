package httpapi

import (
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"testing"
	"time"
)

// Cash receipt and consumption are distinct HTTP projections. All merchant
// traffic uses onlineTopupFixture's loopback gateway and signed test callbacks.
func TestOnlineTopupHTTPCashConsumptionAndTraceability(t *testing.T) {
	f := onlineTopupFixture(t)
	get := func(path, cookie string) map[string]any {
		t.Helper()
		w := request(f.h, "GET", path, nil, cookie, "")
		expect(t, w, 200)
		return data(t, w)
	}
	year := time.Now().In(time.FixedZone("Shanghai", 8*3600)).Year()
	summaryPath := fmt.Sprintf("/api/v1/admin/transactions/summary?customer_id=%d", f.uid)
	snapshot := func() map[string]float64 {
		income := get(fmt.Sprintf("/api/v1/admin/reports/income?year=%d", year), f.root)
		sales := get(fmt.Sprintf("/api/v1/admin/reports/sales?year=%d", year), f.root)
		dashboard := get("/api/v1/admin/dashboard", f.root)
		cash := get(summaryPath, f.root)
		sum := float64(0)
		for _, r := range sales["items"].([]any) {
			sum += r.(map[string]any)["income_cents"].(float64)
		}
		return map[string]float64{"income": income["total"].(map[string]any)["income_cents"].(float64), "sales": sum, "annual_dashboard": dashboard["annual_income_cents"].(float64), "month_dashboard": dashboard["month_income_cents"].(float64), "cash": cash["gateway_income_cents"].(float64), "net_cash": cash["net_gateway_cents"].(float64)}
	}
	before := snapshot()
	accountBefore := get("/api/v1/account", f.customer)["balance_cents"].(float64)
	iid := onlineTopupCreate(t, f, "cash-consumption-topup")
	assertProjection := func(status string) {
		t.Helper()
		for _, path := range []string{"/api/v1/invoices/page?limit=100", "/api/v1/admin/invoices/page?limit=100"} {
			cookie := f.customer
			if path == "/api/v1/admin/invoices/page?limit=100" {
				cookie = f.root
			}
			page := get(path, cookie)
			found := false
			for _, raw := range page["items"].([]any) {
				r := raw.(map[string]any)
				if r["id"] == float64(iid) {
					found = true
					if r["topup_status"] != status || r["topup_pending_liability_cents"] != nil {
						t.Fatal("list topup projection", r)
					}
				}
			}
			if !found {
				t.Fatal("missing real topup list row", path)
			}
		}
	}
	assertProjection("created")
	checkout := epayIntent(t, f, iid, "epay_alipay", "cash-consumption-intent")
	assertProjection("accepted")
	expect(t, epayCallback(f.h, "POST", "epay_alipay", epayNotification(checkout, "cash-consumption-trade")), 200)
	afterTopup := snapshot()
	for k, v := range before {
		delta := float64(0)
		if k == "cash" || k == "net_cash" {
			delta = 1234
		}
		if afterTopup[k] != v+delta {
			t.Fatalf("topup changed %s: %v -> %v expected delta %v", k, v, afterTopup[k], delta)
		}
	}
	assertProjection("credited")
	detail := get(fmt.Sprintf("/api/v1/invoices/%d", iid), f.customer)
	topup := detail["topup"].(map[string]any)
	if topup["status"] != "credited" || topup["amount_cents"] != float64(1234) || topup["payment_id"] == nil || topup["ledger_id"] == nil {
		t.Fatal("topup detail missing settlement trace", topup)
	}
	account := get("/api/v1/account", f.customer)
	if account["balance_cents"] != accountBefore+1234 {
		t.Fatal("account missing credit", account)
	}
	found := false
	for _, raw := range account["ledger"].([]any) {
		r := raw.(map[string]any)
		if r["reference_type"] == "gateway_topup" && r["reference_id"] == float64(iid) {
			found = true
			if r["delta_cents"] != float64(1234) || r["id"] != topup["ledger_id"] {
				t.Fatal("account ledger trace", r)
			}
		}
	}
	if !found {
		t.Fatal("missing account reference")
	}
	entries := get(fmt.Sprintf("/api/v1/admin/transactions/page?customer_id=%d&channel=gateway_topup", f.uid), f.root)
	if entries["total"] != float64(1) {
		t.Fatal("internal credit duplicated", entries)
	}
	row := entries["items"].([]any)[0].(map[string]any)
	if row["source"] != "ledger" || row["direction"] != "neutral" || row["invoice_id"] != float64(iid) || row["amount_cents"] != float64(1234) {
		t.Fatal("internal transfer counted as cash", row)
	}
	in := manualInvoiceInput(f.uid)
	in.Items = []store.ManualInvoiceItem{{Description: "余额消费财务回归", AmountCents: 200}}
	bill := manualInvoiceCreate(t, f.h, f.root, in, "cash-consumption-manual")
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", bill), map[string]any{"expected_version": 1}, f.customer, "cash-consumption-pay"), 201)
	afterSpend := snapshot()
	for k, v := range afterTopup {
		delta := float64(200)
		if k == "cash" || k == "net_cash" {
			delta = 0
		}
		if afterSpend[k] != v+delta {
			t.Fatalf("balance purchase changed %s: %v -> %v expected delta %v", k, v, afterSpend[k], delta)
		}
	}
	if balance := get("/api/v1/account", f.customer)["balance_cents"]; balance != accountBefore+1234-200 {
		t.Fatal("account consumption mismatch", balance)
	}
}
