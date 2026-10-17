package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// Real API fixture on a disposable schema; no external merchant or resource writes.
func TestLocalRenewalExternalRefundBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" || os.Getenv("ZJMFGO_BROWSER_FINISH_FILE") == "" {
		t.Skip("opt-in local renewal reversal browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "isolated payment fixture: no external actions", 503)
	}))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	// The existing purchase helper owns an isolated schema and creates/settles
	// the purchase and processes the actual local provision jobs.
	p := renewalPurchaseFixture(t, "fixed", 500, 2)
	first := setupLocalRenewalRefundHTTPFixture(t, p, gateway.URL, "browser-first")
	var secondID int64
	if err := p.db.Pool.QueryRow(t.Context(), `SELECT max(id) FROM services WHERE order_id=$1`, p.orderID).Scan(&secondID); err != nil {
		t.Fatal(err)
	}
	p.sid = secondID
	second := setupLocalRenewalRefundHTTPFixture(t, p, gateway.URL, "browser-second")
	// Serve the same schema through the established ready/finish-file harness.
	// The shared harness receives this same disposable store.
	serveLocalRenewalBrowserFixture(t, first, second)
}

func serveLocalRenewalBrowserFixture(t *testing.T, fixtures ...localRenewalRefundHTTPFixture) {
	t.Helper()
	rows := []map[string]any{}
	accounts := []map[string]any{}
	for i, f := range fixtures {
		in := f.input(t, fmt.Sprintf("browser-renewal-evidence-%d", i))
		rows = append(rows, map[string]any{"scenario": []string{"confirm", "reject"}[i], "invoice_id": f.invoice, "service_id": f.sid, "payment_id": f.payment, "amount_cents": 734, "admin_path": fmt.Sprintf("/admin/invoices/%d", f.invoice), "input": in, "requester_cookie": f.requester, "reviewer_cookie": f.reviewer, "before_due": f.beforeDue})
		accounts = append(accounts, map[string]any{"username": []string{"browser-first", "browser-second"}[i] + "-renewal-refund-requester", "cookie": f.requester}, map[string]any{"username": []string{"browser-first", "browser-second"}[i] + "-renewal-refund-reviewer", "cookie": f.reviewer})
	}
	writePaymentBrowserManifest(t, map[string]any{"fixture": "local_renewal_external_refund", "real_external_actions": false, "customer_id": fixtures[0].uid, "accounts": accounts, "invoices": rows})
	// Reuse the common TCP harness with the already prepared schema.
	adminBrowserFixtureStore(t, "renewal-refund-browser-super", fixtures[0].db, nil)
}
