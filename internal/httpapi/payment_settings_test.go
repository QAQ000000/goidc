package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/config"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

const paymentSettingsPath = "/api/v1/admin/payment-settings"

func TestPaymentMockDisabledByDefaultAndExplicitConfiguration(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	cfg := epayTestConfig()
	cfg.PaymentMockEnabled = false
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	providers := request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, 200)
	if strings.Contains(providers.Body.String(), `"name":"mock"`) || !strings.Contains(providers.Body.String(), "epay_alipay") {
		t.Fatal("default configuration exposed mock or disabled real payment", providers.Body.String())
	}
	settings := request(f.h, "GET", paymentSettingsPath, nil, f.root, "")
	expect(t, settings, 200)
	if data(t, settings)["mock_enabled"] != false {
		t.Fatal("default settings enabled mock")
	}
	invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "mock-default-invoice")
	for _, body := range []map[string]any{{"expected_version": 1}, {"provider": "mock", "expected_version": 1}} {
		expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), body, f.customer, "mock-default-intent"), 400)
	}
	cfg.PaymentMockEnabled = true
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	providers = request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, 200)
	if !strings.Contains(providers.Body.String(), `"name":"mock"`) {
		t.Fatal("explicit environment mock opt-in ignored")
	}
	w := request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "mock-explicit-intent")
	expect(t, w, 201)
	body := map[string]any{"enabled": true, "mock_enabled": false, "api_url": "", "pid": "", "types": []string{}, "clear_key": false, "expected_version": 1}
	// Save a mock-only policy in a deployment without Epay environment credentials.
	cfg.EpayAPIURL, cfg.EpayPID, cfg.EpayKey, cfg.EpayTypes = "", "", "", nil
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	providers = request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, 200)
	if strings.Contains(providers.Body.String(), `"name":"mock"`) {
		t.Fatal("environment opted in over explicit database disable")
	}
	body["mock_enabled"], body["expected_version"] = true, 2
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	cfg.PaymentMockEnabled = false
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	providers = request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, 200)
	if !strings.Contains(providers.Body.String(), `"name":"mock"`) {
		t.Fatal("explicit database mock enable was lost")
	}
}

func TestPaymentMockMigrationPreservesConfiguredChoice(t *testing.T) {
	for _, configured := range []bool{false, true} {
		t.Run(fmt.Sprint(configured), func(t *testing.T) {
			db, _ := testServer(t)
			if _, err := db.Pool.Exec(t.Context(), `ALTER TABLE payment_settings ALTER COLUMN mock_enabled SET DEFAULT true; UPDATE payment_settings SET configured=$1,mock_enabled=true; DELETE FROM schema_migrations WHERE version='086_payment_mock_default.sql'`, pgx.QueryExecModeSimpleProtocol, configured); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(t.Context()); err != nil {
				t.Fatal(err)
			}
			var enabled bool
			if err := db.Pool.QueryRow(t.Context(), "SELECT mock_enabled FROM payment_settings").Scan(&enabled); err != nil || enabled != configured {
				t.Fatal("migration changed explicit policy or retained unsafe fallback", enabled, err)
			}
			if _, err := db.Pool.Exec(t.Context(), "DELETE FROM payment_settings; INSERT INTO payment_settings(id) VALUES(1)"); err != nil {
				t.Fatal(err)
			}
			if err := db.Pool.QueryRow(t.Context(), "SELECT mock_enabled FROM payment_settings").Scan(&enabled); err != nil || enabled {
				t.Fatal("new database row defaults to mock enabled", err)
			}
		})
	}
}

func settingsPaymentConfig() config.Config {
	return config.Config{PublicOrigin: "http://localhost:3088", PaymentMockEnabled: true, PaymentGatewaySecret: "integration-payment-secret", ProvisionEncryptionKey: []byte("01234567890123456789012345678901")}
}
func paymentSettingsBody(apiURL string, version int64) map[string]any {
	return map[string]any{"enabled": true, "mock_enabled": false, "api_url": apiURL, "pid": "10001", "types": []string{"alipay", "wxpay"}, "key": "integration-epay-secret", "expected_version": version}
}
func TestPaymentSettingsSecretsPermissionsVersionsAndAuditRollback(t *testing.T) {
	db, h := testServer(t)
	cfg := settingsPaymentConfig()
	h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	rootID, root := user(t, db, h, "payment-settings-root", "super_admin")
	_, staff := user(t, db, h, "payment-settings-staff", "admin")
	_, customer := user(t, db, h, "payment-settings-customer", "customer")
	for _, cookie := range []string{staff, customer} {
		expect(t, request(h, "GET", paymentSettingsPath, nil, cookie, ""), 403)
		expect(t, request(h, "PUT", paymentSettingsPath, paymentSettingsBody("https://merchant.example", 1), cookie, ""), 403)
	}
	expect(t, request(h, "GET", paymentSettingsPath, nil, "", ""), 401)
	secret := "zjmfgo_" + strings.Repeat("a", 64)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO api_keys(user_id,name,prefix,secret_hash) VALUES($1,'payment-settings','zjmfgo_aaaaaaaaaaaaaaaa',$2)`, rootID, tokenHash(secret)); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", paymentSettingsPath, nil)
	r.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	expect(t, w, 403)
	w = request(h, "GET", paymentSettingsPath, nil, root, "")
	expect(t, w, 200)
	if data(t, w)["configured"] != false || data(t, w)["key_configured"] != false {
		t.Fatal("fallback metadata incorrect")
	}
	body := paymentSettingsBody("https://merchant.example", 1)
	w = request(h, "PUT", paymentSettingsPath, body, root, "")
	expect(t, w, 200)
	if data(t, w)["key_configured"] != true || strings.Contains(w.Body.String(), "integration-epay-secret") {
		t.Fatal("secret exposed or missing")
	}
	var encrypted []byte
	var audit string
	if err := db.Pool.QueryRow(t.Context(), "SELECT key_ciphertext FROM payment_settings").Scan(&encrypted); err != nil || len(encrypted) == 0 || strings.Contains(string(encrypted), "integration-epay-secret") {
		t.Fatal("plaintext credential storage")
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT payload::text FROM audit_events WHERE action='payment.settings_updated' ORDER BY id DESC LIMIT 1").Scan(&audit); err != nil || strings.Contains(audit, "integration-epay-secret") || strings.Contains(audit, "key_ciphertext") {
		t.Fatal("audit secret exposed")
	}
	body["key"] = ""
	body["expected_version"] = 2
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 200)
	out, err := db.PaymentConfiguration(t.Context(), store.PaymentConfiguration{PublicOrigin: cfg.PublicOrigin})
	if err != nil || out.Key != "integration-epay-secret" {
		t.Fatal("blank key not preserved")
	}
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 409)
	body["expected_version"] = 3
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- request(h, "PUT", paymentSettingsPath, body, root, "").Code }()
	}
	wg.Wait()
	close(codes)
	successes, conflicts := 0, 0
	for code := range codes {
		if code == 200 {
			successes++
		}
		if code == 409 {
			conflicts++
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatal("configuration CAS failed", successes, conflicts)
	}
	if _, err = db.Pool.Exec(t.Context(), `CREATE FUNCTION reject_payment_settings_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='payment.settings_updated' THEN RAISE EXCEPTION 'injected failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER reject_payment_settings_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION reject_payment_settings_audit()`); err != nil {
		t.Fatal(err)
	}
	body["expected_version"] = 4
	body["pid"] = "20002"
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 500)
	out, err = db.PaymentConfiguration(t.Context(), store.PaymentConfiguration{})
	if err != nil || out.Version != 4 || out.PID != "10001" {
		t.Fatal("audit rollback failed", err)
	}
	if _, err = db.Pool.Exec(t.Context(), "ALTER TABLE audit_events DISABLE TRIGGER reject_payment_settings_audit"); err != nil {
		t.Fatal(err)
	}
	body["pid"] = "10001"
	body["enabled"] = false
	body["clear_key"] = true
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 200)
	w = request(h, "GET", paymentSettingsPath, nil, root, "")
	expect(t, w, 200)
	if data(t, w)["key_configured"] != false {
		t.Fatal("explicit clear failed")
	}
}

func TestPaymentSettingsDynamicAPIWorkerAndDisabledCallbacks(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	cfg := settingsPaymentConfig()
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	var checkout url.Values
	merchant := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "integration-epay-secret" {
			t.Error("incorrect query authentication")
		}
		_ = json.NewEncoder(w).Encode(queryProtocolResponse(checkout, 1))
	}))
	defer merchant.Close()
	body := paymentSettingsBody(merchant.URL, 1)
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	providers := request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	if providers.Code != 200 || !strings.Contains(providers.Body.String(), "epay_alipay") || strings.Contains(providers.Body.String(), `"name":"mock"`) {
		t.Fatal("dynamic providers incorrect", providers.Code, providers.Body.String())
	}
	iid := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "settings-runtime-invoice")
	checkout = epayIntent(t, f, iid, "epay_alipay", "settings-runtime-intent")
	callbackInvoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "settings-callback-invoice")
	callbackCheckout := epayIntent(t, f, callbackInvoice, "epay_wxpay", "settings-callback-intent")
	var pid int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT id FROM payment_transactions WHERE invoice_id=$1", iid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	body["expected_version"] = 2
	body["key"] = "replacement-secret"
	w := request(f.h, "PUT", paymentSettingsPath, body, f.root, "")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "payment_credentials_in_use" {
		t.Fatal("pending replacement allowed")
	}
	body["key"] = ""
	body["enabled"] = false
	body["mock_enabled"] = true
	body["types"] = []string{}
	body["clear_key"] = true
	w = request(f.h, "PUT", paymentSettingsPath, body, f.root, "")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "payment_credentials_in_use" {
		t.Fatal("pending payment credentials cleared for mock-only configuration")
	}
	delete(body, "clear_key")
	body["key"] = ""
	body["enabled"] = false
	body["types"] = []string{}
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	w = epayCallback(f.h, "POST", "epay_wxpay", epayNotification(callbackCheckout, "disabled-callback-trade"))
	if w.Code != 200 || w.Body.String() != "success" {
		t.Fatal("disabled pending callback failed", w.Code, w.Body.String())
	}
	w = request(f.h, "GET", fmt.Sprintf("/api/v1/invoices/%d", iid), nil, f.customer, "")
	expect(t, w, 200)
	if !strings.Contains(w.Body.String(), `"query_enabled":true`) {
		t.Fatal("disabled old query unavailable")
	}
	expect(t, request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", iid), map[string]any{"provider": "epay_alipay", "expected_version": 1}, f.customer, "settings-disabled-new"), 400)
	makeMonitoringDue(t, f.db, pid)
	fallback := store.PaymentConfiguration{PublicOrigin: cfg.PublicOrigin, MockSecret: cfg.PaymentGatewaySecret}
	if worked, err := f.db.ProcessConfiguredAutomaticPaymentCheck(t.Context(), fallback); err != nil || !worked {
		t.Fatal("worker did not dynamically load disabled old config", worked, err)
	}
	w = epayCallback(f.h, "GET", "epay_alipay", epayNotification(checkout, "query-trade"))
	if w.Code != 200 || w.Body.String() != "success" {
		t.Fatal("disabled old callback failed", w.Code, w.Body.String())
	}
	unknown := epayNotification(checkout, "unknown-trade")
	unknown.Set("out_trade_no", "epay_999_01234567890123456789012345678901")
	signEpayTest(unknown)
	w = epayCallback(f.h, "GET", "epay_alipay", unknown)
	if w.Code == 200 {
		t.Fatal("unknown callback accepted")
	}
	var activeCheck int64
	if err := f.db.Pool.QueryRow(t.Context(), `INSERT INTO payment_reconciliation_checks(payment_id,actor_id,source) VALUES($1,NULL,'worker') RETURNING id`, pid).Scan(&activeCheck); err != nil {
		t.Fatal(err)
	}
	body["expected_version"] = 3
	body["key"] = "replacement-secret"
	w = request(f.h, "PUT", paymentSettingsPath, body, f.root, "")
	expect(t, w, 409)
	if errorData(t, w)["code"] != "payment_credentials_in_use" {
		t.Fatal("active query merchant replacement allowed")
	}
	if _, err := f.db.Pool.Exec(t.Context(), "UPDATE payment_reconciliation_checks SET status='error',completed_at=clock_timestamp(),lease_until=clock_timestamp()-interval '1 second' WHERE id=$1", activeCheck); err != nil {
		t.Fatal(err)
	}
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
}

func TestPaymentSettingsMockOnlyAfterClearingMerchantKey(t *testing.T) {
	f := configurationUpgradeFixture(t, false)
	cfg := settingsPaymentConfig()
	f.h = New(f.db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	body := paymentSettingsBody("https://merchant.example", 1)
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	body["expected_version"] = 2
	body["enabled"] = false
	body["mock_enabled"] = true
	body["types"] = []string{}
	body["key"] = ""
	body["clear_key"] = true
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 200)
	body["expected_version"] = 3
	body["enabled"] = true
	delete(body, "clear_key")
	w := request(f.h, "PUT", paymentSettingsPath, body, f.root, "")
	expect(t, w, 200)
	saved := data(t, w)
	if saved["api_url"] != "https://merchant.example" || saved["pid"] != "10001" || saved["key_configured"] != false {
		t.Fatal("mock-only configuration lost retained merchant metadata")
	}
	providers := request(f.h, "GET", "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, 200)
	if !strings.Contains(providers.Body.String(), `"name":"mock"`) || strings.Contains(providers.Body.String(), "epay_") {
		t.Fatal("mock-only providers incorrect", providers.Body.String())
	}
	invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "mock-only-cleared-invoice")
	w = request(f.h, "POST", fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{"provider": "mock", "expected_version": 1}, f.customer, "mock-only-cleared-intent")
	expect(t, w, 201)
	if data(t, w)["provider"] != "mock" {
		t.Fatal("mock-only intent failed")
	}
	body["expected_version"] = 4
	for _, kind := range []string{"alipay", "wxpay", "qqpay", "bank"} {
		body["types"] = []string{kind}
		w = request(f.h, "PUT", paymentSettingsPath, body, f.root, "")
		expect(t, w, 409)
		if errorData(t, w)["code"] != "invalid_payment_settings" {
			t.Fatal("epay enabled without merchant key", kind)
		}
	}
	body["types"] = []string{}
	body["api_url"] = "http://merchant.example"
	expect(t, request(f.h, "PUT", paymentSettingsPath, body, f.root, ""), 409)
	var version int64
	if err := f.db.Pool.QueryRow(t.Context(), "SELECT version FROM payment_settings").Scan(&version); err != nil || version != 4 {
		t.Fatal("invalid mock-only updates changed configuration", err)
	}
}

func TestPaymentSettingsValidationFallbackAndStaleIntent(t *testing.T) {
	db, h := testServer(t)
	cfg := epayTestConfig()
	cfg.ProvisionEncryptionKey = []byte("01234567890123456789012345678901")
	h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	_, root := user(t, db, h, "payment-settings-validation", "super_admin")
	w := request(h, "GET", paymentSettingsPath, nil, root, "")
	expect(t, w, 200)
	if data(t, w)["configured"] != false || data(t, w)["key_configured"] != true || strings.Contains(w.Body.String(), cfg.EpayKey) {
		t.Fatal("environment fallback incorrect")
	}
	body := paymentSettingsBody("http://merchant.example", 1)
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 409)
	body["api_url"] = "https://merchant.example"
	body["key"] = ""
	expect(t, request(h, "PUT", paymentSettingsPath, body, root, ""), 200)
	fallback := store.PaymentConfiguration{PublicOrigin: cfg.PublicOrigin}
	configured, err := db.PaymentConfiguration(t.Context(), fallback)
	if err != nil || configured.Key != cfg.EpayKey {
		t.Fatal("environment key migration failed", err)
	}
	// A payment prepared before this save cannot commit against the new configuration.
	_, err = db.CreatePaymentIntent(store.WithPaymentConfigurationVersion(t.Context(), 1), 1, 1, "epay_alipay", "not-used", "", "stale-intent-key", 1)
	var conflict *store.Conflict
	if !errors.As(err, &conflict) || conflict.Code != "version_conflict" {
		t.Fatal("stale intent accepted", err)
	}
	_, err = db.ClaimAutomaticPaymentCheck(store.WithPaymentConfigurationVersion(t.Context(), 1), []string{"epay_alipay"})
	if !errors.As(err, &conflict) || conflict.Code != "version_conflict" {
		t.Fatal("worker reserved a query with stale merchant configuration", err)
	}
	cfg.ProvisionEncryptionKey = nil
	h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	expect(t, request(h, "GET", paymentSettingsPath, nil, root, ""), 409)
	// Separate fresh schema proves an unconfigured encryption key never persists a secret.
	db2, h2 := testServer(t)
	_, root2 := user(t, db2, h2, "payment-settings-no-encryption", "super_admin")
	db2.ProvisionEncryptionKey = nil
	expect(t, request(h2, "PUT", paymentSettingsPath, paymentSettingsBody("https://merchant.example", 1), root2, ""), 409)
	var configuredFlag bool
	if err = db2.Pool.QueryRow(t.Context(), "SELECT configured FROM payment_settings").Scan(&configuredFlag); err != nil || configuredFlag {
		t.Fatal("unencrypted credential persisted")
	}
}

func TestPaymentSettingsSessionRecheckedAfterWaiting(t *testing.T) {
	db, h := testServer(t)
	cfg := settingsPaymentConfig()
	h = New(db, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler()
	_, root := user(t, db, h, "payment-settings-waiting", "super_admin")
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Second)
	defer cancel()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err = tx.Exec(ctx, "SELECT id FROM payment_settings WHERE id=1 FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	codes := make(chan int, 1)
	go func() {
		codes <- request(h, "PUT", paymentSettingsPath, paymentSettingsBody("https://merchant.example", 1), root, "").Code
	}()
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE 'SELECT configured,enabled,mock_enabled,api_url,pid,types,key_ciphertext,version,updated_at FROM payment_settings%FOR UPDATE')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = db.Pool.Exec(ctx, "DELETE FROM sessions WHERE token_hash=$1", tokenHash(strings.TrimPrefix(root, "zjmfgo_session="))); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-codes:
		if code != 401 {
			t.Fatal("revoked session mutation allowed", code)
		}
	case <-ctx.Done():
		t.Fatal("waiting mutation stalled")
	}
	var version int64
	if err = db.Pool.QueryRow(ctx, "SELECT version FROM payment_settings").Scan(&version); err != nil || version != 1 {
		t.Fatal("revoked session settings changed")
	}
}
