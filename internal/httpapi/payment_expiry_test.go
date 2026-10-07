package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

func TestPaymentExpiryMerchantRotationAndLateSettlement(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	cfg := settingsPaymentConfig()
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	var checkout url.Values
	var paid atomic.Int64
	var calls atomic.Int64
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Query().Get("key") != "integration-epay-secret" || r.URL.Query().Get("pid") != "10001" {
			t.Error("rotated query used wrong merchant")
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, paid.Load()))
	}))
	defer merchant.Close()
	body := paymentSettingsBody(merchant.URL, 1)
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "expiry-invoice")
	checkout = epayIntent(t, f, iid, "epay_alipay", "expiry-intent")
	var pid int64
	var sealed []byte
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id,credential_snapshot FROM payment_transactions WHERE invoice_id=$1", iid).Scan(&pid, &sealed); err != nil || len(sealed) == 0 || bytes.Contains(sealed, []byte("integration-epay-secret")) {
		t.Fatal("missing encrypted identity", err)
	}
	body["expected_version"] = 2
	body["api_url"] = "https://replacement.example"
	body["pid"] = "10001"
	body["key"] = "replacement-secret"
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=clock_timestamp()-interval '1 second' WHERE id=$1", pid); err != nil {
		t.Fatal(err)
	}
	// Rotation is allowed even before the worker stamps local expiry.
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	fallback := store.PaymentConfiguration{PublicOrigin: cfg.PublicOrigin}
	worked, err := f.db.ProcessConfiguredAutomaticPaymentCheck(t.Context(), fallback)
	if err != nil || !worked || calls.Load() != 1 {
		t.Fatal("expiry final query failed", worked, err, calls.Load())
	}
	var state, invoiceState string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT p.status,i.status FROM payment_transactions p JOIN invoices i ON i.id=p.invoice_id WHERE p.id=$1", pid).Scan(&state, &invoiceState); err != nil || state != "expired" || invoiceState != "unpaid" {
		t.Fatal("unpaid expiry settled money", state, invoiceState, err)
	}
	if count, err := f.db.ExpirePaymentIntents(t.Context()); err != nil || count != 0 {
		t.Fatal("expiry not idempotent", count, err)
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "epay_wxpay", "expected_version": 1}, f.customer, "expiry-second-intent"), 409)
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", iid), map[string]any{"expected_version": 1}, f.customer, "expiry-balance"), 409)
	cancel := request(f.h, "POST", fmt.Sprintf("/api/v1/admin/invoices/%d/cancel", iid), map[string]any{"expected_version": 1, "reason": "expired payment requires review"}, f.root, "expiry-cancel")
	expect(t, cancel, 409)
	if errorData(t, cancel)["code"] != "invoice_payment_pending" {
		t.Fatal("expired payment did not guard cancellation", cancel.Body.String())
	}
	// New intents use the replacement merchant, old ones keep the original.
	another := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "expiry-new-invoice")
	editInput := manualInvoiceInput(f.uid)
	editInput.ExpectedVersion = 1
	editInput.Items[0].AmountCents = 4000
	edit := request(f.h, "PUT", fmt.Sprintf("/api/v1/admin/invoices/%d", iid), editInput, f.root, "expiry-edit")
	expect(t, edit, 409)
	if errorData(t, edit)["code"] != "invoice_payment_pending" {
		t.Fatal("expired payment did not guard editing", edit.Body.String())
	}
	mergeInput := store.ManualInvoiceMergeInput{Sources: []store.ManualInvoiceMergeSource{{InvoiceID: iid, ExpectedVersion: 1}, {InvoiceID: another, ExpectedVersion: 1}}}
	merge := request(f.h, "POST", "/api/v1/admin/invoices/merge", mergeInput, f.root, "expiry-merge")
	expect(t, merge, 409)
	if errorData(t, merge)["code"] != "invoice_payment_pending" {
		t.Fatal("expired payment did not guard merging", merge.Body.String())
	}
	created := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", another), map[string]any{"provider": "epay_wxpay", "expected_version": 1}, f.customer, "expiry-new-intent")
	expect(t, created, 201)
	replacementURL, parseErr := url.Parse(data(t, created)["checkout_url"].(string))
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	replacement := replacementURL.Query()
	if replacement.Get("pid") != "10001" || replacementURL.Host != "replacement.example" {
		t.Fatal("new payment used old merchant")
	}
	clearQueryCooldown(t, f.db)
	makeMonitoringDue(t, f.db, pid)
	paid.Store(1)
	worked, err = f.db.ProcessConfiguredAutomaticPaymentCheck(t.Context(), fallback)
	if err != nil || !worked || calls.Load() != 2 {
		t.Fatal("old merchant paid query failed", worked, err, calls.Load())
	}
	for range 2 {
		callback := epayCallback(f.h, "POST", "epay_alipay", epayNotification(checkout, "query-trade"))
		if callback.Code != 200 || callback.Body.String() != "success" {
			t.Fatal("old merchant late callback rejected", callback.Code, callback.Body.String())
		}
	}
	forged := epayNotification(checkout, "forged-trade")
	forged.Set("sign", strings.Repeat("0", 32))
	expect(t, epayCallback(f.h, "POST", "epay_alipay", forged), 401)
	var audits int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT i.status,(SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=$1::bigint::text) FROM invoices i WHERE id=$1", iid).Scan(&invoiceState, &audits); err != nil || invoiceState != "paid" || audits != 1 {
		t.Fatal("late payment not settled once", invoiceState, audits, err)
	}
	// Independent merchants may issue the same PID and transaction number.
	notification := epayNotification(replacement, "query-trade")
	signEpayTestKey(notification, "replacement-secret")
	expect(t, epayCallback(f.h, "POST", "epay_wxpay", notification), 200)
	var paidCount, distinctTrades int
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT count(*),count(DISTINCT external_transaction_id) FROM payment_transactions WHERE status='succeeded'").Scan(&paidCount, &distinctTrades); err != nil || paidCount != 2 || distinctTrades != 2 {
		t.Fatal("independent merchant trade collided", paidCount, distinctTrades, err)
	}
}

func TestPaymentExpiryLegacyBackfillAndLeaseGuard(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	cfg := epayTestConfig()
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "legacy-expiry-invoice")
	reference := "epay_" + fmt.Sprint(iid) + "_" + strings.Repeat("a", 32)
	var pid int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,expires_at) VALUES('epay_alipay',$1,$2,$3,3000,now()-interval '1 hour') RETURNING id`, reference, iid, f.uid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	var check int64
	if err := f.db.Pool.QueryRow(t.Context(), "INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,NULL,'worker') RETURNING id", pid).Scan(&check); err != nil {
		t.Fatal(err)
	}
	body := paymentSettingsBody("https://new.example", 1)
	body["key"] = "another-secret"
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 409)
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_reconciliation_checks SET status='error',completed_at=clock_timestamp(),lease_until=now()-interval '1 second' WHERE id=$1", check); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	gateway, ok, err := f.db.PaymentTransactionGateway(t.Context(), "epay_alipay", reference, store.PaymentConfiguration{})
	if err != nil || !ok {
		t.Fatal("legacy credentials not retained", err)
	}
	intent, err := gateway.CreateIntent(payment.IntentRequest{InvoiceID: iid, InvoiceNo: "legacy", AmountCents: 3000})
	if err != nil || !strings.Contains(intent.CheckoutURL, "pid=10001") {
		t.Fatal("legacy resolved wrong identity", err)
	}
	if _, err = f.db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET credential_snapshot=''::bytea WHERE id=$1", pid); err == nil {
		t.Fatal("immutable credential snapshot modified")
	}
	var ciphertext []byte
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT credential_snapshot FROM payment_transactions WHERE id=$1", pid).Scan(&ciphertext); err != nil || bytes.Contains(ciphertext, []byte(cfg.EpayKey)) {
		t.Fatal("legacy secret exposed", err)
	}
	checkoutURL, err := url.Parse(intent.CheckoutURL)
	if err != nil {
		t.Fatal(err)
	}
	legacyCheckout := checkoutURL.Query()
	legacyCheckout.Set("out_trade_no", reference)
	for range 2 {
		expect(t, epayCallback(f.h, "POST", "epay_alipay", epayNotification(legacyCheckout, "legacy-trade")), 200)
	}
	var external string
	if err = f.db.Pool.QueryRow(t.Context(), "SELECT external_transaction_id FROM payment_transactions WHERE id=$1", pid).Scan(&external); err != nil || external != "10001:legacy-trade" {
		t.Fatal("legacy deduplication identity changed", external, err)
	}
}

func TestPaymentExpiryBrowserFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_BROWSER_READY_FILE") == "" {
		t.Skip("opt-in browser fixture")
	}
	var checkout url.Values
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "integration-epay-secret" {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 0))
	}))
	defer merchant.Close()
	t.Setenv("ZJMFGO_BROWSER_EPAY_API_URL", merchant.URL)
	adminBrowserFixture(t, "expiry-super", func(db *store.Store, h http.Handler) {
		f := setupConfigurationUpgradeFixture(t, db, h, false)
		cfg := epayTestConfig()
		cfg.EpayAPIURL = merchant.URL
		f.h = New(db, cfg, slog.Default()).Handler()
		iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "expiry-browser-invoice")
		checkout = epayIntent(t, f, iid, "epay_alipay", "expiry-browser-intent")
		if _, err := db.Pool.Exec(t.Context(), "UPDATE payment_transactions SET expires_at=now()-interval '1 minute' WHERE invoice_id=$1", iid); err != nil {
			t.Fatal(err)
		}
		if n, err := db.ExpirePaymentIntents(t.Context()); err != nil || n != 1 {
			t.Fatal(n, err)
		}
	})
}
