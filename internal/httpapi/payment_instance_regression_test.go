package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
)

const selectedPaymentInstanceID = "payment-selected-regression"

func selectedPaymentInstanceInput(endpoint string) map[string]any {
	return map[string]any{
		"id":                selectedPaymentInstanceID,
		"category":          "payment",
		"implementation_id": "epay",
		"display_name":      "回归测试支付",
		"enabled":           true,
		"config": map[string]any{
			"api_url": endpoint,
			"pid":     "20002",
			"types":   []string{"alipay"},
		},
		"secret": "selected-instance-secret",
	}
}

func disableSelectedPaymentInstance(t *testing.T, f upgradeFixture, endpoint string) {
	t.Helper()
	w := request(f.h, http.MethodPut, "/api/v1/admin/channel-instances/"+selectedPaymentInstanceID, map[string]any{
		"display_name":     "回归测试支付",
		"enabled":          false,
		"config":           map[string]any{"api_url": endpoint, "pid": "20002", "types": []string{"alipay"}},
		"expected_version": 1,
	}, f.root, "")
	expect(t, w, http.StatusOK)
}

func selectedPaymentIntent(t *testing.T, f upgradeFixture, invoiceID int64, key string) url.Values {
	t.Helper()
	w := request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{
		"provider":            "epay_alipay",
		"channel_instance_id": selectedPaymentInstanceID,
		"expected_version":    1,
	}, f.customer, key)
	expect(t, w, http.StatusCreated)
	checkout, err := url.Parse(data(t, w)["checkout_url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if checkout.Query().Get("pid") != "20002" {
		t.Fatalf("selected instance was not used: %s", checkout.String())
	}
	return checkout.Query()
}

func TestSelectedPaymentInstanceCallbackSurvivesDisable(t *testing.T) {
	db, h := testServer(t)
	f := setupConfigurationUpgradeFixture(t, db, h, false)
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	instance := request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", selectedPaymentInstanceInput("http://127.0.0.1:1"), f.root, "")
	expect(t, instance, http.StatusCreated)

	invoiceID := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "selected-instance-callback")
	checkout := selectedPaymentIntent(t, f, invoiceID, "selected-instance-callback-intent")
	disableSelectedPaymentInstance(t, f, "http://127.0.0.1:1")

	notification := epayNotification(checkout, "selected-callback-trade")
	signEpayTestKey(notification, "selected-instance-secret")
	callback := epayCallback(f.h, http.MethodGet, "epay_alipay", notification)
	if callback.Code != http.StatusOK || callback.Body.String() != "success" {
		t.Fatalf("disabled selected instance rejected callback: %d %s", callback.Code, callback.Body.String())
	}
	var state string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoiceID).Scan(&state); err != nil || state != "paid" {
		t.Fatalf("selected instance callback did not settle invoice: %s (%v)", state, err)
	}
}

func TestSelectedPaymentInstanceAutomaticQueryUsesSnapshot(t *testing.T) {
	var requestedKey string
	var requestedPID string
	var requestedReference string
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		requestedKey, requestedPID, requestedReference = query.Get("key"), query.Get("pid"), query.Get("out_trade_no")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code":         1,
			"pid":          query.Get("pid"),
			"type":         "alipay",
			"trade_no":     "selected-query-trade",
			"out_trade_no": query.Get("out_trade_no"),
			"money":        "30.00",
			"status":       1,
		})
	}))
	defer merchant.Close()

	db, h := testServer(t)
	f := setupConfigurationUpgradeFixture(t, db, h, false)
	f.h = New(f.db, config.Config{
		PublicOrigin:           "http://localhost:3088",
		PaymentMockEnabled:     true,
		PaymentGatewaySecret:   "integration-payment-secret",
		EpayAPIURL:             "https://legacy.example",
		EpayPID:                "10001",
		EpayKey:                "legacy-instance-secret",
		EpayTypes:              []string{"alipay"},
		ProvisionEncryptionKey: db.ProvisionEncryptionKey,
	}, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	instance := request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", selectedPaymentInstanceInput(merchant.URL), f.root, "")
	expect(t, instance, http.StatusCreated)

	invoiceID := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "selected-instance-query")
	checkout := selectedPaymentIntent(t, f, invoiceID, "selected-instance-query-intent")
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET next_reconcile_at=clock_timestamp()-interval '1 minute' WHERE provider_payment_id=$1", checkout.Get("out_trade_no")); err != nil {
		t.Fatal(err)
	}
	clearQueryCooldown(t, f.db)
	disableSelectedPaymentInstance(t, f, merchant.URL)

	worked, err := f.db.ProcessConfiguredAutomaticPaymentCheck(t.Context(), store.PaymentConfiguration{PublicOrigin: "http://localhost:3088"})
	if err != nil || !worked {
		t.Fatalf("selected instance automatic query failed: worked=%v err=%v", worked, err)
	}
	if requestedKey != "selected-instance-secret" || requestedPID != "20002" || requestedReference != checkout.Get("out_trade_no") {
		t.Fatalf("automatic query used wrong selected instance: key=%q pid=%q reference=%q", requestedKey, requestedPID, requestedReference)
	}
	var state string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", invoiceID).Scan(&state); err != nil || state != "paid" {
		t.Fatalf("automatic query did not settle invoice: %s (%v)", state, err)
	}
}
