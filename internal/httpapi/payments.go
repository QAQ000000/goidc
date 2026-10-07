package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/codex/zjmfgo/internal/payment"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func (s *Server) createPayment(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var input struct {
		Provider        string `json:"provider"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Provider = strings.TrimSpace(strings.ToLower(input.Provider))
	if input.Provider == "" {
		input.Provider = "mock"
	}
	configuration, err := s.paymentConfiguration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	gateway, ok := configuration.Gateway(input.Provider, false)
	if !ok {
		writeError(w, 400, "unsupported_payment_provider", "支付渠道不支持")
		return
	}
	key := r.Header.Get("Idempotency-Key")
	details, err := s.db.InvoicePaymentDetails(r.Context(), identityFrom(r).ID, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	expected := input.ExpectedVersion
	if expected == 0 && details.InvoiceKind != "manual" {
		expected = details.Version
	}
	intent, err := gateway.CreateIntent(payment.IntentRequest{InvoiceID: details.InvoiceID, InvoiceNo: details.InvoiceNo, AmountCents: details.AmountCents})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	result, err := s.db.CreatePaymentIntent(store.WithPaymentConfigurationVersion(r.Context(), configuration.Version), identityFrom(r).ID, id, gateway.Name(), intent.ProviderPaymentID, intent.CheckoutURL, key, expected, details.Version)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	write(w, status, result)
}

func (s *Server) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	configuration, err := s.paymentConfiguration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	gateway, ok := configuration.Gateway(provider, true)
	if !ok {
		writeError(w, 404, "payment_provider_not_found", "支付渠道不存在")
		return
	}
	if strings.HasPrefix(provider, "epay_") {
		s.epayWebhook(w, r, gateway)
		return
	}
	if r.Method != http.MethodPost {
		writeError(w, 405, "method_not_allowed", "回调需要 POST")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		writeError(w, 400, "invalid_webhook", "回调内容无效")
		return
	}
	if !gateway.VerifySignature(body, r.Header.Get("X-Payment-Signature")) {
		writeError(w, 401, "invalid_payment_signature", "支付回调签名无效")
		return
	}
	event, err := gateway.DecodeWebhook(body)
	if err != nil {
		writeError(w, 400, "invalid_webhook", "回调内容无效")
		return
	}
	result, err := s.db.ProcessPaymentWebhook(r.Context(), provider, event.EventID, event.ProviderPaymentID, event.InvoiceID, event.AmountCents, event.Status, body)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	status := http.StatusOK
	if !result.Replayed {
		status = http.StatusCreated
	}
	write(w, status, result)
}

func (s *Server) paymentProviders(w http.ResponseWriter, r *http.Request) {
	configuration, err := s.paymentConfiguration(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	items := []map[string]any{}
	if _, ok := configuration.Gateway("mock", false); ok {
		items = append(items, map[string]any{"name": "mock", "label": "模拟支付", "simulation": true})
	}
	labels := map[string]string{"alipay": "支付宝", "wxpay": "微信支付", "qqpay": "QQ 钱包", "bank": "网银支付"}
	for _, value := range configuration.Types {
		if _, ok := configuration.Gateway("epay_"+value, false); ok {
			items = append(items, map[string]any{"name": "epay_" + value, "label": labels[value], "simulation": false})
		}
	}
	write(w, 200, items)
}

func (s *Server) epayWebhook(w http.ResponseWriter, r *http.Request, gateway payment.Gateway) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fail := func(status int) { w.WriteHeader(status); _, _ = io.WriteString(w, "fail") }
	var payload []byte
	if r.Method == http.MethodGet {
		payload = []byte(r.URL.RawQuery)
	} else {
		if len(r.URL.RawQuery) > 0 || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
			fail(400)
			return
		}
		var err error
		payload, err = io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			fail(400)
			return
		}
	}
	if !gateway.VerifySignature(payload, "") {
		fail(401)
		return
	}
	event, err := gateway.DecodeWebhook(payload)
	if err != nil {
		fail(400)
		return
	}
	values, err := url.ParseQuery(string(payload))
	if err != nil {
		fail(400)
		return
	}
	// Canonical decoded parameters make retries independent of form ordering.
	raw, _ := json.Marshal(values)
	if _, err = s.db.ProcessPaymentWebhook(r.Context(), gateway.Name(), event.EventID, event.ProviderPaymentID, event.InvoiceID, event.AmountCents, event.Status, raw, event.EventID); err != nil {
		var conflict *store.Conflict
		var pg *pgconn.PgError
		if errors.As(err, &conflict) || errors.Is(err, pgx.ErrNoRows) || (errors.As(err, &pg) && pg.Code == "23505") {
			fail(409)
		} else {
			s.log.Error("payment callback settlement failed", "provider", gateway.Name(), "invoice_id", event.InvoiceID, "error", err)
			fail(500)
		}
		return
	}
	_, _ = io.WriteString(w, "success")
}
