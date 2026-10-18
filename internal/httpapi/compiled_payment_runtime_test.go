package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
)

const compiledPaymentImplementation = "fixture_payment_runtime"
const compiledPaymentMethod = "fixture_runtime_transfer"

// This adapter lives only in the HTTP test binary. Its protocol, configuration,
// two credentials and merchant identity deliberately share no Epay fields.
type compiledPaymentGateway struct{ method, realm, account, webhookKey, queryKey string }

type compiledPaymentControl struct {
	queryKey, queryStatus, transaction, closeStatus string
	queries, closes, lookups                        int
	lastOperation                                   string
}

var compiledPaymentControls = struct {
	sync.Mutex
	byReference map[string]compiledPaymentControl
}{byReference: make(map[string]compiledPaymentControl)}
var compiledPaymentSequence atomic.Uint64
var compiledPaymentRegistration sync.Once

func compiledPaymentFixtures(t *testing.T) {
	t.Helper()
	compiledPaymentRegistration.Do(func() {
		err := payment.RegisterFactory(channel.Descriptor{
			ID: compiledPaymentImplementation, Name: "Compiled fixture payment", Category: channel.Payment,
			Capabilities: []string{"create_intent", "verify_webhook", "query", "close_intent", "lookup_close"},
			Fields: []channel.Field{
				{Key: "realm", Label: "Realm", Type: channel.Text, Required: true},
				{Key: "account", Label: "Account", Type: channel.Text, Required: true},
				{Key: "webhook_key", Label: "Webhook key", Type: channel.Text, Required: true, Secret: true},
				{Key: "query_key", Label: "Query key", Type: channel.Text, Required: true, Secret: true},
			},
		}, []payment.Method{{ID: compiledPaymentMethod, Label: "Fixture transfer", Simulation: false}}, compiledPaymentProtocol{}, func(method string, config payment.FactoryConfig) (payment.Gateway, error) {
			realm, _ := config.Values["realm"].(string)
			account, _ := config.Values["account"].(string)
			if method != compiledPaymentMethod || realm == "" || account == "" || config.Secrets["webhook_key"] == "" || config.Secrets["query_key"] == "" {
				return nil, errors.New("fixture payment configuration is incomplete")
			}
			return compiledPaymentGateway{method, realm, account, config.Secrets["webhook_key"], config.Secrets["query_key"]}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	})
}

func (g compiledPaymentGateway) Name() string { return g.method }
func (g compiledPaymentGateway) MerchantScope() (string, error) {
	digest := sha256.Sum256([]byte(g.realm + "\x00" + g.account))
	return "fixture_runtime:" + hex.EncodeToString(digest[:]), nil
}
func (g compiledPaymentGateway) externalID(raw string) string {
	scope, _ := g.MerchantScope()
	return scope + ":" + raw
}
func (g compiledPaymentGateway) CreateIntent(in payment.IntentRequest) (payment.Intent, error) {
	if in.InvoiceID < 1 || in.InvoiceNo == "" || in.AmountCents < 1 {
		return payment.Intent{}, errors.New("fixture intent invalid")
	}
	reference := fmt.Sprintf("fixture-%d-%d", in.InvoiceID, compiledPaymentSequence.Add(1))
	compiledPaymentControls.Lock()
	compiledPaymentControls.byReference[reference] = compiledPaymentControl{queryKey: g.queryKey, queryStatus: "pending", transaction: "query-receipt", closeStatus: "closed"}
	compiledPaymentControls.Unlock()
	return payment.Intent{ProviderPaymentID: reference, CheckoutURL: "https://checkout.fixture.invalid/" + url.PathEscape(g.account) + "/" + reference}, nil
}
func compiledPaymentSignature(body []byte, key string) string {
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
func (g compiledPaymentGateway) VerifySignature(body []byte, signature string) bool {
	return hmac.Equal([]byte(signature), []byte(compiledPaymentSignature(body, g.webhookKey)))
}
func (g compiledPaymentGateway) DecodeWebhook(body []byte) (payment.Webhook, error) {
	fields := strings.Split(string(body), "|")
	if len(fields) != 7 || fields[6] != g.account {
		return payment.Webhook{}, errors.New("fixture callback invalid")
	}
	invoice, firstErr := strconv.ParseInt(fields[3], 10, 64)
	amount, secondErr := strconv.ParseInt(fields[4], 10, 64)
	if firstErr != nil || secondErr != nil || fields[0] == "" || fields[1] == "" || fields[2] == "" || invoice < 1 || amount < 1 || (fields[5] != "succeeded" && fields[5] != "failed") {
		return payment.Webhook{}, errors.New("fixture callback invalid")
	}
	return payment.Webhook{ProviderPaymentID: fields[0], EventID: g.externalID("event:" + fields[1]), ExternalTransactionID: g.externalID(fields[2]), InvoiceID: invoice, AmountCents: amount, Status: fields[5]}, nil
}
func (g compiledPaymentGateway) Query(ctx context.Context, in payment.QueryRequest) (payment.QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return payment.QueryResult{}, err
	}
	compiledPaymentControls.Lock()
	defer compiledPaymentControls.Unlock()
	control, exists := compiledPaymentControls.byReference[in.ProviderPaymentID]
	if !exists || control.queryKey != g.queryKey {
		return payment.QueryResult{}, payment.ErrQueryUnavailable
	}
	control.queries++
	compiledPaymentControls.byReference[in.ProviderPaymentID] = control
	return payment.QueryResult{ProviderPaymentID: in.ProviderPaymentID, AmountCents: in.AmountCents, Status: control.queryStatus, ExternalTransactionID: g.externalID(control.transaction)}, nil
}
func (g compiledPaymentGateway) Close(ctx context.Context, in payment.CloseRequest) (payment.CloseResult, error) {
	if err := ctx.Err(); err != nil {
		return payment.CloseResult{}, err
	}
	compiledPaymentControls.Lock()
	defer compiledPaymentControls.Unlock()
	control, exists := compiledPaymentControls.byReference[in.ProviderPaymentID]
	if !exists || control.queryKey != g.queryKey {
		return payment.CloseResult{}, payment.ErrCloseUnknown
	}
	control.closes++
	control.lastOperation = in.OperationID
	compiledPaymentControls.byReference[in.ProviderPaymentID] = control
	if control.closeStatus == "unknown" {
		return payment.CloseResult{}, payment.ErrCloseUnknown
	}
	return payment.CloseResult{ProviderPaymentID: in.ProviderPaymentID, AmountCents: in.AmountCents, OperationID: in.OperationID, Status: control.closeStatus}, nil
}
func (g compiledPaymentGateway) LookupClose(ctx context.Context, in payment.CloseStatusRequest) (payment.CloseStatusResult, error) {
	if err := ctx.Err(); err != nil {
		return payment.CloseStatusResult{}, err
	}
	compiledPaymentControls.Lock()
	defer compiledPaymentControls.Unlock()
	control := compiledPaymentControls.byReference[in.ProviderPaymentID]
	if control.queryKey != g.queryKey || control.lastOperation != in.OperationID {
		return payment.CloseStatusResult{}, payment.ErrCloseUnknown
	}
	control.lookups++
	compiledPaymentControls.byReference[in.ProviderPaymentID] = control
	return payment.CloseStatusResult{ProviderPaymentID: in.ProviderPaymentID, AmountCents: in.AmountCents, OperationID: in.OperationID, Status: control.closeStatus, ProviderEvidenceID: "fixture:" + in.OperationID}, nil
}

type compiledPaymentProtocol struct{}

func (compiledPaymentProtocol) Parse(in payment.CallbackRequest) (payment.CallbackPayload, error) {
	fields := strings.Split(string(in.Body), "|")
	if in.Method != http.MethodPost || in.RawQuery != "" || in.Header.Get("Content-Type") != "application/octet-stream" || len(fields) != 7 || fields[0] == "" {
		return payment.CallbackPayload{}, &payment.CallbackError{Status: http.StatusBadRequest}
	}
	canonical, _ := json.Marshal(map[string]any{"fields": fields})
	return payment.CallbackPayload{Reference: fields[0], Body: in.Body, CanonicalBody: canonical, Signature: in.Header.Get("X-Fixture-Proof")}, nil
}
func (compiledPaymentProtocol) Reply(status int, hostBody []byte) payment.CallbackReply {
	body := "fixture:rejected:" + string(hostBody)
	if status >= 200 && status < 300 {
		body = "fixture:accepted\n"
		status = http.StatusOK
	}
	return payment.CallbackReply{Status: status, ContentType: "text/plain; charset=utf-8", Body: []byte(body)}
}

type compiledPaymentFixture struct {
	db             *store.Store
	h              http.Handler
	root, customer string
	uid            int64
}
type compiledPaymentIntent struct {
	invoice, id int64
	reference   string
	checkout    string
}

func newCompiledPaymentFixture(t *testing.T) compiledPaymentFixture {
	t.Helper()
	compiledPaymentFixtures(t)
	db, h := testServer(t)
	_, root := user(t, db, h, "compiled-payment-root", "super_admin")
	uid, customer := user(t, db, h, "compiled-payment-customer", "customer")
	return compiledPaymentFixture{db, h, root, customer, uid}
}
func (f compiledPaymentFixture) instance(t *testing.T, id, account string) {
	t.Helper()
	w := request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": id, "category": "payment", "implementation_id": compiledPaymentImplementation, "display_name": id, "enabled": true,
		"config":        map[string]any{"realm": "fixture-region", "account": account},
		"secure_config": map[string]string{"webhook_key": account + "-webhook-private", "query_key": account + "-query-private"},
	}, f.root, "")
	expect(t, w, http.StatusCreated)
	if strings.Contains(w.Body.String(), "-private") {
		t.Fatal("instance response leaked payment credentials")
	}
}
func (f compiledPaymentFixture) intent(t *testing.T, instance, key string) compiledPaymentIntent {
	t.Helper()
	invoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), key+"-invoice")
	w := request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", invoice), map[string]any{
		"provider": compiledPaymentMethod, "channel_instance_id": instance, "expected_version": 1,
	}, f.customer, key+"-intent")
	expect(t, w, http.StatusCreated)
	d := data(t, w)
	return compiledPaymentIntent{invoice, int64(d["payment_id"].(float64)), d["provider_payment_id"].(string), d["checkout_url"].(string)}
}
func (f compiledPaymentFixture) rotateAndDisable(t *testing.T, instance string) {
	t.Helper()
	expect(t, request(f.h, http.MethodPut, "/api/v1/admin/channel-instances/"+instance, map[string]any{
		"display_name": "rotated " + instance, "enabled": false, "expected_version": 1,
		"config":        map[string]any{"realm": "new-region", "account": "new-account"},
		"secure_config": map[string]string{"webhook_key": "rotated-webhook-private", "query_key": "rotated-query-private"},
	}, f.root, ""), http.StatusOK)
}
func (f compiledPaymentFixture) damageSingleton(t *testing.T) {
	t.Helper()
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE payment_settings SET configured=true,key_ciphertext='\x00'::bytea; UPDATE channel_instances SET schema_version=999 WHERE id='payment-default'`); err != nil {
		t.Fatal(err)
	}
}
func compiledPaymentBody(in compiledPaymentIntent, account, event, transaction string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%s|%d|3000|succeeded|%s", in.reference, event, transaction, in.invoice, account))
}
func (f compiledPaymentFixture) callback(body []byte, key string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/api/v1/payments/"+compiledPaymentMethod+"/webhook", bytes.NewReader(body))
	r.Header.Set("Content-Type", "application/octet-stream")
	r.Header.Set("X-Fixture-Proof", compiledPaymentSignature(body, key))
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}
func compiledPaymentAccepted(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusOK || w.Body.String() != "fixture:accepted\n" || !strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("fixture callback rejected: %d %s", w.Code, w.Body.String())
	}
}
func compiledPaymentSetControl(reference string, update func(*compiledPaymentControl)) compiledPaymentControl {
	compiledPaymentControls.Lock()
	defer compiledPaymentControls.Unlock()
	control := compiledPaymentControls.byReference[reference]
	if update != nil {
		update(&control)
		compiledPaymentControls.byReference[reference] = control
	}
	return control
}
func (f compiledPaymentFixture) assertState(t *testing.T, in compiledPaymentIntent, invoice, paymentState string, version int64) {
	t.Helper()
	var savedInvoice, savedPayment string
	var savedVersion int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT i.status,p.status,i.version FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE p.id=$1`, in.id).Scan(&savedInvoice, &savedPayment, &savedVersion); err != nil || savedInvoice != invoice || savedPayment != paymentState || savedVersion != version {
		t.Fatalf("invoice/payment state: %s/%s/v%d, wanted %s/%s/v%d (%v)", savedInvoice, savedPayment, savedVersion, invoice, paymentState, version, err)
	}
}

func TestCompiledPaymentCatalogAndInstanceIsolation(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-pay-a", "account-a")
	f.instance(t, "compiled-pay-b", "account-b")
	providers := request(f.h, http.MethodGet, "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, http.StatusOK)
	seen := map[string]bool{}
	for _, item := range listData(t, providers) {
		if item["name"] == compiledPaymentMethod {
			seen[item["instance_id"].(string)] = true
		}
	}
	if !seen["compiled-pay-a"] || !seen["compiled-pay-b"] || strings.Contains(providers.Body.String(), "-private") {
		t.Fatalf("compiled instances absent or credentials exposed: %s", providers.Body.String())
	}
	a := f.intent(t, "compiled-pay-a", "compiled-a")
	b := f.intent(t, "compiled-pay-b", "compiled-b")
	if !strings.Contains(a.checkout, "/account-a/") || !strings.Contains(b.checkout, "/account-b/") || a.reference == b.reference {
		t.Fatal("selected payment instance did not reach its factory")
	}
	path := fmt.Sprintf("/api/v1/invoices/%d/payments", a.invoice)
	replayBody := map[string]any{"provider": compiledPaymentMethod, "channel_instance_id": "compiled-pay-a", "expected_version": 1}
	replay := request(f.h, http.MethodPost, path, replayBody, f.customer, "compiled-a-intent")
	expect(t, replay, http.StatusOK)
	if data(t, replay)["payment_id"] != float64(a.id) {
		t.Fatal("payment replay created a second transaction")
	}
	replayBody["channel_instance_id"] = "compiled-pay-b"
	expect(t, request(f.h, http.MethodPost, path, replayBody, f.customer, "compiled-a-intent"), http.StatusConflict)
	_, staff := user(t, f.db, f.h, "compiled-payment-reader", "admin")
	expect(t, request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{}, staff, ""), http.StatusForbidden)
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM payment_transactions WHERE invoice_id=$1`, a.invoice).Scan(&count); err != nil || count != 1 {
		t.Fatal("idempotency did not retain one transaction", count, err)
	}
}

func TestCompiledPaymentHistoricalCallbackUsesFrozenCredentials(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-history-a", "history-a")
	f.instance(t, "compiled-history-b", "history-b")
	a := f.intent(t, "compiled-history-a", "history-a")
	b := f.intent(t, "compiled-history-b", "history-b")
	var snapshot []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT credential_snapshot FROM payment_transactions WHERE id=$1`, a.id).Scan(&snapshot); err != nil || len(snapshot) == 0 {
		t.Fatal("payment credentials were not frozen", err)
	}
	f.rotateAndDisable(t, "compiled-history-a")
	f.damageSingleton(t)
	body := compiledPaymentBody(a, "history-a", "history-event-a", "history-receipt-a")
	for _, key := range []string{"rotated-webhook-private", "history-b-webhook-private"} {
		expect(t, f.callback(body, key), http.StatusUnauthorized)
	}
	compiledPaymentAccepted(t, f.callback(body, "history-a-webhook-private"))
	compiledPaymentAccepted(t, f.callback(compiledPaymentBody(b, "history-b", "history-event-b", "history-receipt-b"), "history-b-webhook-private"))
	f.assertState(t, a, "paid", "succeeded", 2)
	f.assertState(t, b, "paid", "succeeded", 2)
	var after []byte
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT credential_snapshot FROM payment_transactions WHERE id=$1`, a.id).Scan(&after); err != nil || !bytes.Equal(snapshot, after) {
		t.Fatal("rotation or callback changed historical ciphertext", err)
	}
}

func TestCompiledPaymentHistoricalQueryAndExpiry(t *testing.T) {
	for _, source := range []string{"manual", "automatic"} {
		t.Run(source, func(t *testing.T) {
			f := newCompiledPaymentFixture(t)
			f.instance(t, "compiled-query", "query-account")
			in := f.intent(t, "compiled-query", "compiled-query-"+source)
			compiledPaymentSetControl(in.reference, func(control *compiledPaymentControl) { control.queryStatus = "succeeded" })
			if _, err := f.db.Pool.Exec(t.Context(), `UPDATE payment_transactions SET expires_at=clock_timestamp()-interval '1 minute',next_reconcile_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, in.id); err != nil {
				t.Fatal(err)
			}
			if _, err := f.db.ExpirePaymentIntents(t.Context()); err != nil {
				t.Fatal(err)
			}
			f.assertState(t, in, "unpaid", "expired", 1)
			f.rotateAndDisable(t, "compiled-query")
			f.damageSingleton(t)
			if source == "manual" {
				w := request(f.h, http.MethodPost, queryPath(in.invoice, in.id, false), nil, f.customer, "")
				expect(t, w, http.StatusOK)
				if data(t, w)["status"] != "succeeded" {
					t.Fatal("historical manual query did not settle")
				}
			} else {
				worked, err := f.db.ProcessConfiguredAutomaticPaymentCheck(t.Context(), store.PaymentConfiguration{})
				if err != nil || !worked {
					t.Fatalf("historical automatic query: worked=%v err=%v", worked, err)
				}
			}
			if control := compiledPaymentSetControl(in.reference, nil); control.queries != 1 {
				t.Fatal("query did not use the original second credential", control.queries)
			}
			f.assertState(t, in, "paid", "succeeded", 2)
			compiledPaymentAccepted(t, f.callback(compiledPaymentBody(in, "query-account", "query-late-callback", "query-receipt"), "query-account-webhook-private"))
			var settlements int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=$1::bigint::text`, in.invoice).Scan(&settlements); err != nil || settlements != 1 {
				t.Fatal("query and late callback settled twice", settlements, err)
			}
		})
	}
}

func TestCompiledPaymentSchemaAndConfigurationErrors(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	_, err := payment.NewFor(compiledPaymentImplementation, compiledPaymentMethod, payment.FactoryConfig{Values: map[string]any{"realm": "fixture"}, Secrets: map[string]string{"webhook_key": "constructor-private"}})
	if err == nil || strings.Contains(err.Error(), "constructor-private") {
		t.Fatal("invalid constructor config succeeded or leaked credentials")
	}
	missing := request(f.h, http.MethodPost, "/api/v1/admin/channel-instances", map[string]any{
		"id": "compiled-missing", "category": "payment", "implementation_id": compiledPaymentImplementation, "display_name": "Missing second key", "enabled": true,
		"config": map[string]any{"realm": "fixture-region", "account": "schema-account"}, "secure_config": map[string]string{"webhook_key": "constructor-private"},
	}, f.root, "")
	expect(t, missing, http.StatusConflict)
	if strings.Contains(missing.Body.String(), "constructor-private") {
		t.Fatal("configuration error leaked a credential")
	}
	f.instance(t, "compiled-schema", "schema-account")
	old := f.intent(t, "compiled-schema", "compiled-schema-old")
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE channel_instances SET schema_version=2 WHERE id='compiled-schema'`); err != nil {
		t.Fatal(err)
	}
	newInvoice := manualInvoiceCreate(t, f.h, f.root, manualInvoiceInput(f.uid), "compiled-schema-new")
	rejected := request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/payments", newInvoice), map[string]any{
		"provider": compiledPaymentMethod, "channel_instance_id": "compiled-schema", "expected_version": 1,
	}, f.customer, "compiled-schema-new-intent")
	expect(t, rejected, http.StatusConflict)
	if !strings.Contains(rejected.Body.String(), "channel_schema_incompatible") {
		t.Fatal("schema mismatch had no explicit contract error")
	}
	providers := request(f.h, http.MethodGet, "/api/v1/payment-providers", nil, f.customer, "")
	expect(t, providers, http.StatusOK)
	if strings.Contains(providers.Body.String(), "compiled-schema") {
		t.Fatal("incompatible instance remained selectable")
	}
	var count int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM payment_transactions WHERE invoice_id=$1`, newInvoice).Scan(&count); err != nil || count != 0 {
		t.Fatal("schema mismatch persisted a payment", count, err)
	}
	compiledPaymentAccepted(t, f.callback(compiledPaymentBody(old, "schema-account", "schema-history", "schema-receipt"), "schema-account-webhook-private"))
	f.assertState(t, old, "paid", "succeeded", 2)
}

func TestCompiledPaymentCallbackValidationAndReceiptIdentity(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-validation", "validation-account")
	in := f.intent(t, "compiled-validation", "compiled-validation")
	body := compiledPaymentBody(in, "validation-account", "validation-event", "receipt-one")
	for _, invalid := range []struct {
		name string
		body []byte
		key  string
		code int
	}{
		{"bad_signature", body, "wrong-private", http.StatusUnauthorized},
		{"wrong_amount", bytes.Replace(body, []byte("|3000|"), []byte("|3001|"), 1), "validation-account-webhook-private", http.StatusConflict},
		{"wrong_invoice", bytes.Replace(body, []byte(fmt.Sprintf("|%d|3000|", in.invoice)), []byte(fmt.Sprintf("|%d|3000|", in.invoice+999)), 1), "validation-account-webhook-private", http.StatusConflict},
		{"wrong_account", bytes.Replace(body, []byte("|validation-account"), []byte("|another-account"), 1), "validation-account-webhook-private", http.StatusBadRequest},
		{"unknown_reference", bytes.Replace(body, []byte(in.reference), []byte("fixture-missing-reference"), 1), "validation-account-webhook-private", http.StatusUnauthorized},
		{"malformed_protocol", []byte("not-a-fixture-message"), "validation-account-webhook-private", http.StatusBadRequest},
	} {
		t.Run(invalid.name, func(t *testing.T) {
			expect(t, f.callback(invalid.body, invalid.key), invalid.code)
			f.assertState(t, in, "unpaid", "pending", 1)
			var events int
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM payment_webhook_events WHERE provider=$1`, compiledPaymentMethod).Scan(&events); err != nil || events != 0 {
				t.Fatal("invalid callback left an event", events, err)
			}
		})
	}
	compiledPaymentAccepted(t, f.callback(body, "validation-account-webhook-private"))
	compiledPaymentAccepted(t, f.callback(body, "validation-account-webhook-private"))
	expect(t, f.callback(bytes.Replace(body, []byte("receipt-one"), []byte("receipt-two"), 1), "validation-account-webhook-private"), http.StatusConflict)
	changedEvent := compiledPaymentBody(in, "validation-account", "validation-different-event", "receipt-two")
	expect(t, f.callback(changedEvent, "validation-account-webhook-private"), http.StatusConflict)
	compiledPaymentAccepted(t, f.callback(compiledPaymentBody(in, "validation-account", "validation-second-notice", "receipt-one"), "validation-account-webhook-private"))
	other := f.intent(t, "compiled-validation", "compiled-validation-other")
	conflicting := f.callback(compiledPaymentBody(other, "validation-account", "validation-reused-receipt", "receipt-one"), "validation-account-webhook-private")
	expect(t, conflicting, http.StatusConflict)
	f.assertState(t, other, "unpaid", "pending", 1)
	f.assertState(t, in, "paid", "succeeded", 2)
	var settlements, events int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM audit_events WHERE action='payment.webhook.succeeded' AND entity_id=$1::bigint::text),(SELECT count(*) FROM payment_webhook_events WHERE provider=$2)`, in.invoice, compiledPaymentMethod).Scan(&settlements, &events); err != nil || settlements != 1 || events != 2 {
		t.Fatal("callback retry/identity facts inconsistent", settlements, events, err)
	}
}

func TestCompiledPaymentRefundMerchantScopes(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-refund-a", "refund-a")
	f.instance(t, "compiled-refund-b", "refund-b")
	f.instance(t, "compiled-refund-a2", "refund-a")
	_, reviewer := externalRefundActor(t, f.db, f.h, "compiled-refund-reviewer", []string{"external_refunds.view", "external_refunds.review"})
	var scopes []string
	for index, instance := range []string{"compiled-refund-a", "compiled-refund-b", "compiled-refund-a2"} {
		account := "refund-a"
		if index == 1 {
			account = "refund-b"
		}
		in := f.intent(t, instance, fmt.Sprintf("compiled-refund-%d", index))
		receipt := "same-raw-receipt"
		if index == 2 {
			receipt = "other-raw-receipt"
		}
		event := "shared-raw-event"
		if index == 2 {
			event = "other-raw-event"
		}
		compiledPaymentAccepted(t, f.callback(compiledPaymentBody(in, account, event, receipt), account+"-webhook-private"))
		if index < 2 {
			f.rotateAndDisable(t, instance)
		}
		body := map[string]any{
			"payment_id": in.id, "amount_cents": 3000, "method": "original_route", "provider_refund_id": "shared-raw-payout-id",
			"received_at": time.Now().UTC().Format(time.RFC3339Nano), "evidence_ref": "test://compiled/refund", "evidence_sha256": strings.Repeat("a", 64), "note": "isolated compiled merchant refund", "expected_version": 2,
		}
		w := request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds", in.invoice), body, f.root, fmt.Sprintf("compiled-refund-create-%d", index))
		if index == 2 {
			expect(t, w, http.StatusConflict)
			continue
		}
		expect(t, w, http.StatusCreated)
		refund := data(t, w)["refund"].(map[string]any)
		confirm := fmt.Sprintf("/api/v1/admin/invoices/%d/external-refunds/%.0f/confirm", in.invoice, refund["id"])
		expect(t, request(f.h, http.MethodPost, confirm, map[string]any{"expected_version": 1, "note": "independent compiled receipt verification"}, reviewer, fmt.Sprintf("compiled-refund-confirm-%d", index)), http.StatusCreated)
		var scope string
		if err := f.db.Pool.QueryRow(t.Context(), `SELECT merchant_scope FROM external_refunds WHERE payment_id=$1`, in.id).Scan(&scope); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(scope, "fixture_runtime:") || strings.Contains(w.Body.String(), "-private") {
			t.Fatal("refund did not use the adapter's frozen merchant scope")
		}
		scopes = append(scopes, scope)
	}
	if len(scopes) != 2 || scopes[0] == scopes[1] {
		t.Fatal("different merchants were conflated", scopes)
	}
	var claims int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM external_refund_payout_claims WHERE provider_refund_id='shared-raw-payout-id'`).Scan(&claims); err != nil || claims != 2 {
		t.Fatal("refund payout claims did not preserve the two merchant identities", claims, err)
	}
}

func TestCompiledPaymentTrustedCloseAndLateReceipt(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-close", "close-account")
	in := f.intent(t, "compiled-close", "compiled-close")
	f.rotateAndDisable(t, "compiled-close")
	f.damageSingleton(t)
	w := request(f.h, http.MethodPost, closePath(in.invoice, in.id), map[string]any{"expected_version": 1}, f.root, "compiled-close-operation")
	expect(t, w, http.StatusOK)
	f.assertState(t, in, "unpaid", "closed", 2)
	expect(t, request(f.h, http.MethodPost, closePath(in.invoice, in.id), map[string]any{"expected_version": 1}, f.root, "compiled-close-operation"), http.StatusOK)
	if control := compiledPaymentSetControl(in.reference, nil); control.closes != 1 {
		t.Fatal("close replay repeated the external operation", control.closes)
	}
	body := compiledPaymentBody(in, "close-account", "closed-late-event", "closed-late-receipt")
	compiledPaymentAccepted(t, f.callback(body, "close-account-webhook-private"))
	compiledPaymentAccepted(t, f.callback(body, "close-account-webhook-private"))
	f.assertState(t, in, "unpaid", "closed", 2)
	for _, operation := range []string{"pay", "pay-credit"} {
		expect(t, request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/%s", in.invoice, operation), map[string]any{"expected_version": 2}, f.customer, "compiled-anomaly-"+operation), http.StatusConflict)
	}
	var anomalies, amount int
	var external string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*),coalesce(sum(amount_cents),0),coalesce(min(external_transaction_id),'') FROM payment_close_anomalies WHERE payment_id=$1`, in.id).Scan(&anomalies, &amount, &external); err != nil || anomalies != 1 || amount != 3000 || !strings.HasPrefix(external, "fixture_runtime:") {
		t.Fatal("late money was not retained once with merchant identity", anomalies, amount, external, err)
	}
	read := request(f.h, http.MethodGet, closePath(in.invoice, in.id), nil, f.root, "")
	expect(t, read, http.StatusOK)
	if data(t, read)["close_supported"] != true || strings.Contains(read.Body.String(), "-private") {
		t.Fatal("historical close capability was lost or leaked credentials")
	}
	var anchor int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT min(id) FROM payment_close_anomalies WHERE payment_id=$1`, in.id).Scan(&anchor); err != nil {
		t.Fatal(err)
	}
	base, create := anomalyPaths(in.invoice, anchor)
	refund := request(f.h, http.MethodPost, create, paymentAnomalyInput(anchor, "compiled-late-payout"), f.root, "compiled-late-refund")
	expect(t, refund, http.StatusCreated)
	_, reviewer := externalRefundActor(t, f.db, f.h, "compiled-anomaly-reviewer", []string{"invoices.view", "payment_anomalies.view", "payment_anomalies.review"})
	confirm := fmt.Sprintf("%s/payment-anomaly-refunds/%.0f/confirm", base, data(t, refund)["refund"].(map[string]any)["id"])
	expect(t, request(f.h, http.MethodPost, confirm, map[string]any{"expected_version": 1, "expected_group_version": anchor, "note": "independent late receipt refund review"}, reviewer, "compiled-late-refund-review"), http.StatusCreated)
	f.assertState(t, in, "unpaid", "closed", 2)
	var resolved bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT NOT payment_close_anomaly_unresolved($1)`, anchor).Scan(&resolved); err != nil || !resolved {
		t.Fatal("compiled merchant anomaly refund did not resolve the frozen money", err)
	}
}

func TestCompiledPaymentUnknownCloseAndIndependentHistoricalLookup(t *testing.T) {
	f := newCompiledPaymentFixture(t)
	f.instance(t, "compiled-close-unknown", "unknown-account")
	in := f.intent(t, "compiled-close-unknown", "compiled-close-unknown")
	compiledPaymentSetControl(in.reference, func(control *compiledPaymentControl) { control.closeStatus = "unknown" })
	for _, key := range []string{"compiled-unknown-operation", "compiled-unknown-operation"} {
		expect(t, request(f.h, http.MethodPost, closePath(in.invoice, in.id), map[string]any{"expected_version": 1}, f.root, key), http.StatusBadGateway)
	}
	expect(t, request(f.h, http.MethodPost, closePath(in.invoice, in.id), map[string]any{"expected_version": 1}, f.root, "compiled-unknown-new-key"), http.StatusConflict)
	expect(t, request(f.h, http.MethodPost, fmt.Sprintf("/api/v1/invoices/%d/pay", in.invoice), map[string]any{"expected_version": 1}, f.customer, "compiled-unknown-balance"), http.StatusConflict)
	f.assertState(t, in, "unpaid", "pending", 1)
	if control := compiledPaymentSetControl(in.reference, nil); control.closes != 1 {
		t.Fatal("unknown close was sent again", control.closes)
	}
	var state string
	var operations int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT count(*),min(status) FROM payment_close_operations WHERE payment_id=$1`, in.id).Scan(&operations, &state); err != nil || operations != 1 || state != "unknown" {
		t.Fatal("unknown close operation was replaced", operations, state, err)
	}
	// A later provider-confirmed lookup may be reviewed independently. It must
	// use the frozen keys without reissuing Close or consulting today's default.
	f.rotateAndDisable(t, "compiled-close-unknown")
	f.damageSingleton(t)
	compiledPaymentSetControl(in.reference, func(control *compiledPaymentControl) { control.closeStatus = "closed" })
	_, verifier := externalRefundActor(t, f.db, f.h, "compiled-close-verifier", []string{"invoices.view", "payments.close_verify"})
	_, reviewer := externalRefundActor(t, f.db, f.h, "compiled-close-reviewer", []string{"invoices.view", "payments.close_review"})
	var operationID int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM payment_close_operations WHERE payment_id=$1`, in.id).Scan(&operationID); err != nil {
		t.Fatal(err)
	}
	lookup := request(f.h, http.MethodPost, verificationPath(in.invoice, in.id), map[string]any{"close_operation_id": operationID, "expected_version": 1}, verifier, "compiled-historical-lookup")
	expect(t, lookup, http.StatusCreated)
	verification := data(t, lookup)["verification"].(map[string]any)
	if verification["status"] != "awaiting_review" {
		t.Fatal("trusted historical lookup was not available for review", lookup.Body.String())
	}
	f.assertState(t, in, "unpaid", "pending", 1)
	confirm := fmt.Sprintf("%s/%.0f/confirm", verificationPath(in.invoice, in.id), verification["id"])
	expect(t, request(f.h, http.MethodPost, confirm, map[string]any{"expected_version": 1, "note": "independent historical close evidence"}, reviewer, "compiled-close-independent-review"), http.StatusCreated)
	f.assertState(t, in, "unpaid", "closed", 2)
	var unresolved bool
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,payment_close_operation_unresolved(id) FROM payment_close_operations WHERE id=$1`, operationID).Scan(&state, &unresolved); err != nil || state != "unknown" || unresolved {
		t.Fatal("review rewrote the historical unknown operation", state, unresolved, err)
	}
	if control := compiledPaymentSetControl(in.reference, nil); control.closes != 1 || control.lookups != 1 {
		t.Fatal("historical verification repeated Close or lost its frozen key", control.closes, control.lookups)
	}
}

func TestCompiledPaymentAuditFailureRollsBackFacts(t *testing.T) {
	for _, source := range []string{"callback", "query", "close"} {
		t.Run(source, func(t *testing.T) {
			f := newCompiledPaymentFixture(t)
			f.instance(t, "compiled-rollback", "rollback-account")
			in := f.intent(t, "compiled-rollback", "compiled-rollback-"+source)
			action := "payment.webhook.succeeded"
			if source == "query" {
				action = "payment.reconciliation.completed"
				compiledPaymentSetControl(in.reference, func(control *compiledPaymentControl) { control.queryStatus = "succeeded" })
			} else if source == "close" {
				action = "payment.intent.closed"
			}
			if _, err := f.db.Pool.Exec(t.Context(), `CREATE FUNCTION compiled_payment_reject_audit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.action='`+action+`' THEN RAISE EXCEPTION 'isolated compiled payment audit failure'; END IF; RETURN NEW; END $$; CREATE TRIGGER compiled_payment_reject_audit BEFORE INSERT ON audit_events FOR EACH ROW EXECUTE FUNCTION compiled_payment_reject_audit()`); err != nil {
				t.Fatal(err)
			}
			var w *httptest.ResponseRecorder
			switch source {
			case "callback":
				w = f.callback(compiledPaymentBody(in, "rollback-account", "rollback-event", "rollback-receipt"), "rollback-account-webhook-private")
			case "query":
				w = request(f.h, http.MethodPost, queryPath(in.invoice, in.id, false), nil, f.customer, "")
			case "close":
				w = request(f.h, http.MethodPost, closePath(in.invoice, in.id), map[string]any{"expected_version": 1}, f.root, "compiled-rollback-close")
			}
			expect(t, w, http.StatusInternalServerError)
			f.assertState(t, in, "unpaid", "pending", 1)
			var events int
			var external string
			if err := f.db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM payment_webhook_events WHERE provider=$2),external_transaction_id FROM payment_transactions WHERE id=$1`, in.id, compiledPaymentMethod).Scan(&events, &external); err != nil || events != 0 || external != "" {
				t.Fatal("audit failure retained settlement facts", events, external, err)
			}
			if source == "close" {
				var status string
				if err := f.db.Pool.QueryRow(t.Context(), `SELECT status FROM payment_close_operations WHERE payment_id=$1`, in.id).Scan(&status); err != nil || status != "unknown" || compiledPaymentSetControl(in.reference, nil).closes != 1 {
					t.Fatal("uncommitted provider close did not remain unknown", status, err)
				}
			} else if source == "callback" {
				if _, err := f.db.Pool.Exec(t.Context(), `DROP TRIGGER compiled_payment_reject_audit ON audit_events`); err != nil {
					t.Fatal(err)
				}
				compiledPaymentAccepted(t, f.callback(compiledPaymentBody(in, "rollback-account", "rollback-event", "rollback-receipt"), "rollback-account-webhook-private"))
				f.assertState(t, in, "paid", "succeeded", 2)
			}
		})
	}
}

// Keep the alternate protocol's transport boundary explicit: it never performs
// an HTTP request, and the raw pipe payload is converted to valid JSON before
// it reaches jsonb settlement evidence.
var _ payment.Gateway = compiledPaymentGateway{}
var _ payment.QueryGateway = compiledPaymentGateway{}
var _ payment.CloseGateway = compiledPaymentGateway{}
var _ payment.CloseStatusGateway = compiledPaymentGateway{}
var _ payment.MerchantScopeGateway = compiledPaymentGateway{}
var _ payment.WebhookProtocol = compiledPaymentProtocol{}
