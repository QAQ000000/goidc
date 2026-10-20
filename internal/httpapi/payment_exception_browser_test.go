package httpapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

// Opt-in only. Both exception workflows share one isolated schema and API
// origin; the gateway is loopback and never contacts a real merchant.
func TestPaymentExceptionBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" || os.Getenv("ZJMFGO_BROWSER_FINISH_FILE") == "" {
		t.Skip("opt-in payment exception browser fixture")
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "isolated gateway only", http.StatusServiceUnavailable)
	}))
	defer gateway.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", gateway.URL)
	adminBrowserFixture(t, "payment-exception-super", func(db *store.Store, h http.Handler) {
		f, refunds := setupExternalRefundReversalBrowser(t, db, h, gateway.URL)
		closeRows, accounts := setupPaymentCloseBrowser(t, f)
		for _, name := range []string{"payment-exception-super", "reversal-requester", "reversal-reviewer", "reversal-reader", "reversal-limited", "reversal-denied"} {
			accounts = append(accounts, map[string]any{"username": name, "password": "integration-password", "role": "admin"})
		}
		writePaymentBrowserManifest(t, map[string]any{
			"fixture": "payment_exception", "real_external_actions": false,
			"accounts": accounts, "customer_id": f.uid,
			"payment_close":   map[string]any{"invoices": closeRows},
			"external_refund": refunds,
		})
	})
}

func setupPaymentCloseBrowser(t *testing.T, f upgradeFixture) ([]map[string]any, []map[string]any) {
	t.Helper()
	accounts := []map[string]any{{"username": "upgrade-customer", "identifier": "upgrade-customer@example.test", "password": "integration-password", "role": "customer", "id": f.uid}}
	var closer string
	for _, actor := range []struct {
		name        string
		permissions []string
		limited     bool
	}{
		{"payment-closer", []string{"invoices.view", "payments.close"}, false},
		{"payment-reader", []string{"invoices.view"}, false},
		{"payment-denied", []string{"settings.view"}, false},
		{"payment-limited", []string{"invoices.view", "payments.close"}, true},
	} {
		id, cookie := externalRefundActor(t, f.db, f.h, actor.name, actor.permissions)
		if actor.name == "payment-closer" {
			closer = cookie
		}
		if actor.limited {
			if _, err := f.db.Pool.Exec(t.Context(), `UPDATE administrators SET customer_scope='owned',customer_ownerless=false WHERE id=$1`, id); err != nil {
				t.Fatal(err)
			}
		}
		accounts = append(accounts, map[string]any{"id": id, "username": actor.name, "password": "integration-password", "role": "admin", "permissions": actor.permissions, "limited": actor.limited})
	}
	rows := []map[string]any{}
	// Several untouched invoices allow a browser to exercise cancel, save,
	// idempotent replay, and stale-version handling without resetting fixtures.
	for index, scenario := range []string{"closable", "closable", "closable", "closable", "closable", "closable", "closed_retry", "unknown_frozen", "late_anomaly_frozen", "epay_unsupported"} {
		key := fmt.Sprintf("payment-close-browser-%d", index)
		invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), key+"-invoice")
		provider := "mock"
		if scenario == "epay_unsupported" {
			provider = "epay_alipay"
		}
		intent := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": provider, "expected_version": 1}, f.customer, key+"-intent")
		expect(t, intent, 201)
		payload := data(t, intent)
		pid := int64(payload["payment_id"].(float64))
		if scenario == "closed_retry" || scenario == "late_anomaly_frozen" {
			expect(t, request(f.h, "POST", closePath(invoice, pid), map[string]any{"expected_version": 1}, closer, key+"-close"), 200)
		}
		if scenario == "unknown_frozen" {
			actor, hash := closeActor(t, f, closer)
			op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, key+"-close", hash)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.FailPaymentClose(t.Context(), op, "unknown"); err != nil {
				t.Fatal(err)
			}
		}
		if scenario == "late_anomaly_frozen" {
			if _, err := f.db.ProcessPaymentWebhook(t.Context(), "mock", key+"-late", payload["provider_payment_id"].(string), invoice, 3000, "succeeded", []byte(`{"fixture":"late-success"}`), "fixture:"+key); err != nil {
				t.Fatal(err)
			}
		}
		read := request(f.h, "GET", fmt.Sprintf("/api/v1/admin/invoices/%d", invoice), nil, closer, "")
		expect(t, read, 200)
		details := data(t, read)
		payments := details["payments"].([]any)
		payment := payments[0].(map[string]any)
		wantEnabled := scenario == "closable"
		if payment["close_enabled"] != wantEnabled || (scenario == "late_anomaly_frozen" && details["payment_close_anomaly"] != true) || (scenario == "epay_unsupported" && payment["close_supported"] != false) {
			t.Fatalf("payment fixture %s has unexpected capability/state", scenario)
		}
		construction := "POST manual invoice + POST mock payment intent"
		switch scenario {
		case "closed_retry":
			construction += " + POST trusted close using compiled mock adapter"
		case "unknown_frozen":
			construction += " + Store.BeginPaymentClose/FailPaymentClose(unknown)"
		case "late_anomaly_frozen":
			construction += " + POST trusted mock close + Store.ProcessPaymentWebhook(late success)"
		case "epay_unsupported":
			construction = "POST manual invoice + POST epay_alipay intent against loopback gateway; no close capability added"
		}
		rows = append(rows, map[string]any{
			"constructed_by": construction,
			"scenario":       scenario, "invoice_id": invoice, "payment_id": pid,
			"provider": provider, "amount_cents": 3000, "expected_version": details["version"],
			"admin_path":    fmt.Sprintf("/admin/invoices/%d", invoice),
			"customer_path": fmt.Sprintf("/console/invoices/%d", invoice),
			"close_path":    closePath(invoice, pid), "close_enabled": wantEnabled,
		})
	}
	for _, purpose := range []string{"cancel", "retry"} {
		key := "payment-close-browser-topup-" + purpose
		invoice := onlineTopupCreate(t, f, key)
		checkout := epayIntent(t, f, invoice, "epay_alipay", key+"-intent")
		var pid int64
		if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1 AND provider_payment_id=$2", invoice, checkout.Get("out_trade_no")).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		actor, hash := closeActor(t, f, closer)
		op, err := f.db.BeginPaymentClose(t.Context(), actor, invoice, pid, 1, key+"-verified-close", hash)
		if err != nil {
			t.Fatal(err)
		}
		// Same explicit trusted-result construction as TestPaymentCloseTopupReservationReuseCancelAndLateAnomaly.
		// This seeds a historical fact, never adds CloseGateway to the Epay adapter.
		if _, err = f.db.CompletePaymentClose(t.Context(), op, closeVerified(op), hash); err != nil {
			t.Fatal(err)
		}
		read := request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", invoice), nil, f.customer, "")
		expect(t, read, 200)
		details := data(t, read)
		payments := details["payments"].([]any)
		if details["topup_can_retry"] != true || payments[0].(map[string]any)["close_supported"] != false {
			t.Fatal("closed accepted topup must be retryable without inventing Epay close capability")
		}
		rows = append(rows, map[string]any{
			"scenario": "closed_topup_" + purpose, "invoice_id": invoice, "payment_id": pid,
			"provider": "epay_alipay", "amount_cents": 1234, "expected_version": details["version"],
			"topup_can_retry": true, "close_enabled": false,
			"admin_path": fmt.Sprintf("/admin/invoices/%d", invoice), "customer_path": fmt.Sprintf("/console/invoices/%d", invoice),
			"close_path":     closePath(invoice, pid),
			"constructed_by": "POST accepted topup + POST Epay intent + Store.BeginPaymentClose/CompletePaymentClose with explicit trusted-result fixture (same as backend test); runtime Epay close remains unsupported",
		})
	}
	return rows, accounts
}
