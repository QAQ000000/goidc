package httpapi

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
)

func TestPaymentSchemaMismatchRejectsNewIntentAndPreservesSnapshot(t *testing.T) {
	db, handler := testServer(t)
	f := setupConfigurationUpgradeFixture(t, db, handler, false)
	f.h = New(f.db, epayTestConfig(), slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	expect(t, request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", selectedPaymentInstanceInput("http://127.0.0.1:1"), f.root, ""), http.StatusCreated)
	oldInvoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "schema-old-payment")
	checkout := selectedPaymentIntent(t, f, oldInvoice, "schema-old-payment-intent")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE channel_instances SET schema_version=2 WHERE id=$1", selectedPaymentInstanceID); err != nil {
		t.Fatal(err)
	}
	newInvoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "schema-new-payment")
	rejected := request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", newInvoice), map[string]any{
		"provider": "epay_alipay", "channel_instance_id": selectedPaymentInstanceID, "expected_version": 1,
	}, f.customer, "schema-new-payment-intent")
	expect(t, rejected, http.StatusConflict)
	if !strings.Contains(rejected.Body.String(), "channel_schema_incompatible") {
		t.Fatal("new payment did not report the incompatible schema")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM payment_transactions WHERE invoice_id=$1", newInvoice).Scan(&count); err != nil || count != 0 {
		t.Fatal("incompatible configuration created a payment", err)
	}
	notice := epayNotification(checkout, "schema-old-trade")
	signEpayTestKey(notice, "selected-instance-secret")
	callback := epayCallback(f.h, http.MethodGet, "epay_alipay", notice)
	if callback.Code != http.StatusOK || callback.Body.String() != "success" {
		t.Fatal("schema upgrade blocked historical snapshot callback", callback.Code)
	}
	var state string
	if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM invoices WHERE id=$1", oldInvoice).Scan(&state); err != nil || state != "paid" {
		t.Fatal("historical payment was not settled", state, err)
	}
}

func TestDefaultPaymentContractRejectsSchemaWithoutRequiringFallbackFields(t *testing.T) {
	db, handler := testServer(t)
	_, admin := user(t, db, handler, "default-contract-admin", "super_admin")
	customerID, customer := user(t, db, handler, "default-contract-customer", "customer")
	fallback := store.PaymentConfiguration{
		PaymentSettings: store.PaymentSettings{Enabled: true, MockEnabled: true, APIURL: "https://gateway.example.test", PID: "10001", Types: []string{"alipay"}},
		Key:             "fallback-private", MockSecret: "integration-payment-secret", PublicOrigin: "http://localhost:3088",
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE channel_instances SET enabled=true WHERE id='payment-default'"); err != nil {
		t.Fatal(err)
	}
	// The migrated instance fields are empty until the legacy singleton is
	// configured. Deployment values must still resolve under the same schema.
	configuration, err := db.PaymentConfigurationForInstance(t.Context(), "payment-default", fallback)
	if err != nil || configuration.APIURL != fallback.APIURL {
		t.Fatal("legacy deployment fallback was treated as an incomplete instance", err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE channel_instances SET schema_version=2 WHERE category='payment'"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PaymentConfigurationForInstance(t.Context(), "payment-default", fallback); err == nil {
		t.Fatal("default payment ignored an incompatible schema")
	}
	providers := request(handler, http.MethodGet, "/api/v1/payment-providers", nil, customer, "")
	expect(t, providers, http.StatusOK)
	if len(listData(t, providers)) != 0 {
		t.Fatal("incompatible default payment remained selectable")
	}
	invoiceID := manualInvoiceCreate(t, handler, admin, manualInvoiceInput(customerID), "default-contract-invoice")
	rejected := request(handler, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", invoiceID), map[string]any{
		"provider": "mock", "expected_version": 1,
	}, customer, "default-contract-intent")
	expect(t, rejected, http.StatusConflict)
	if !strings.Contains(rejected.Body.String(), "channel_schema_incompatible") {
		t.Fatal("implicit default payment bypassed its schema contract")
	}
}
