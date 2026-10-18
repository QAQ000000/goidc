package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
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
		Provider          string `json:"provider"`
		ChannelInstanceID string `json:"channel_instance_id"`
		ExpectedVersion   int64  `json:"expected_version"`
	}
	if !decode(w, r, &input) {
		return
	}
	input.Provider = strings.TrimSpace(strings.ToLower(input.Provider))
	if input.Provider == "" {
		input.Provider = "mock"
	}
	var configuration store.PaymentConfiguration
	var err error
	if strings.TrimSpace(input.ChannelInstanceID) != "" {
		configuration, err = s.db.PaymentConfigurationForInstance(r.Context(), strings.TrimSpace(input.ChannelInstanceID), s.paymentFallback())
	} else {
		configuration, err = s.paymentConfiguration(r.Context())
	}
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
	paymentContext := store.WithPaymentConfiguration(r.Context(), configuration)
	if details.InvoiceKind == "topup" {
		if bearerCredential(r) != "" {
			writeError(w, 403, "session_required", "充值需要客户浏览器会话")
			return
		}
		cookie, e := r.Cookie("zjmfgo_session")
		if e != nil {
			writeError(w, 401, "unauthorized", "请先登录")
			return
		}
		paymentContext = store.WithTopupSession(paymentContext, tokenHash(cookie.Value))
	}
	if strings.TrimSpace(input.ChannelInstanceID) != "" {
		paymentContext = store.WithPaymentChannelInstance(paymentContext, strings.TrimSpace(input.ChannelInstanceID))
	}
	result, err := s.db.CreatePaymentIntent(paymentContext, identityFrom(r).ID, id, gateway.Name(), intent.ProviderPaymentID, intent.CheckoutURL, key, expected, details.Version)
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

// A callback locator is untrusted. It can only select an existing transaction;
// signature verification and decoding always use that transaction's snapshot.
func (s *Server) paymentWebhook(w http.ResponseWriter, r *http.Request) {
	provider := strings.TrimSpace(strings.ToLower(r.PathValue("provider")))
	protocol, ok := payment.LookupProtocol(provider)
	if !ok {
		writeError(w, 404, "payment_provider_not_found", "支付渠道不存在")
		return
	}
	reply := func(status int, value any) {
		body, _ := json.Marshal(value)
		response := protocol.Reply(status, append(body, '\n'))
		w.Header().Set("Content-Type", response.ContentType)
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}
	fail := func(status int, code, message string) {
		reply(status, map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	if len(r.URL.RawQuery) > 64<<10 {
		fail(400, "invalid_webhook", "回调内容无效")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		fail(400, "invalid_webhook", "回调内容无效")
		return
	}
	payload, err := protocol.Parse(payment.CallbackRequest{Method: r.Method, Header: r.Header, RawQuery: r.URL.RawQuery, Body: body})
	if err != nil || payload.Reference == "" || len(payload.Reference) > 512 || len(payload.Body) > 64<<10 || len(payload.CanonicalBody) == 0 || len(payload.CanonicalBody) > 256<<10 {
		status := http.StatusBadRequest
		var callbackError *payment.CallbackError
		if errors.As(err, &callbackError) && callbackError.Status == http.StatusMethodNotAllowed {
			status = http.StatusMethodNotAllowed
		}
		fail(status, "invalid_webhook", "回调内容无效")
		return
	}
	var canonical map[string]json.RawMessage
	if err := json.Unmarshal(payload.CanonicalBody, &canonical); err != nil || canonical == nil {
		fail(400, "invalid_webhook", "回调证据格式无效")
		return
	}
	gateway, ok, err := s.db.PaymentTransactionGateway(r.Context(), provider, payload.Reference, s.paymentFallback())
	if err != nil || !ok || gateway == nil || gateway.Name() != provider || !gateway.VerifySignature(payload.Body, payload.Signature) {
		fail(401, "invalid_payment_signature", "支付回调签名无效")
		return
	}
	event, err := gateway.DecodeWebhook(payload.Body)
	if err != nil || event.ProviderPaymentID != payload.Reference {
		fail(400, "invalid_webhook", "回调内容无效")
		return
	}
	method, _ := payment.MethodInfo(provider)
	// Every real payment needs an independently identified, merchant-scoped
	// external transaction. Event IDs identify delivery, not financial identity.
	if !method.Simulation && event.ExternalTransactionID == "" {
		fail(400, "invalid_webhook", "回调缺少渠道流水号")
		return
	}
	var external []string
	if event.ExternalTransactionID != "" {
		external = append(external, event.ExternalTransactionID)
	}
	result, err := s.db.ProcessPaymentWebhook(r.Context(), provider, event.EventID, event.ProviderPaymentID, event.InvoiceID, event.AmountCents, event.Status, payload.CanonicalBody, external...)
	if err != nil {
		var conflict *store.Conflict
		var pg *pgconn.PgError
		switch {
		case errors.As(err, &conflict):
			fail(409, conflict.Code, conflict.Message)
		case errors.Is(err, pgx.ErrNoRows), errors.As(err, &pg) && (pg.Code == "23505" || pg.Code == "23503" || pg.Code == "23514"):
			fail(409, "payment_callback_conflict", "支付回调与交易记录不匹配")
		default:
			s.log.Error("payment callback settlement failed", "provider", provider, "invoice_id", event.InvoiceID, "error", err)
			fail(500, "internal_error", "请求处理失败")
		}
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
	}
	reply(status, map[string]any{"data": result})
}

func (s *Server) paymentProviders(w http.ResponseWriter, r *http.Request) {
	items := []map[string]any{}
	instances, err := s.db.ListChannelInstances(r.Context(), "payment")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	for _, instance := range instances {
		if !instance.Enabled && instance.ID != "payment-mock-default" {
			continue
		}
		configuration, resolveErr := s.db.PaymentConfigurationForInstance(r.Context(), instance.ID, s.paymentFallback())
		if resolveErr != nil {
			continue
		}
		for _, method := range payment.Methods(instance.ImplementationID) {
			if _, ok := configuration.Gateway(method.ID, false); !ok {
				continue
			}
			label := method.Label + " · " + instance.DisplayName
			if method.Simulation {
				label = instance.DisplayName
			}
			items = append(items, map[string]any{"name": method.ID, "label": label, "instance_id": instance.ID, "simulation": method.Simulation})
		}
	}
	write(w, 200, items)
}
