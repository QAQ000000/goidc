package payment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
)

type IntentRequest struct {
	InvoiceID   int64
	InvoiceNo   string
	AmountCents int64
}

type Intent struct {
	ProviderPaymentID string
	CheckoutURL       string
}

type Webhook struct {
	EventID           string `json:"event_id"`
	ProviderPaymentID string `json:"payment_id"`
	InvoiceID         int64  `json:"invoice_id"`
	AmountCents       int64  `json:"amount_cents"`
	Status            string `json:"status"`
}

type Gateway interface {
	Name() string
	CreateIntent(IntentRequest) (Intent, error)
	VerifySignature(body []byte, signature string) bool
	DecodeWebhook(body []byte) (Webhook, error)
}

// CloseGateway is intentionally stricter than QueryGateway. Implementations
// must return a provider-confirmed terminal close result; a local expiry,
// pending response, timeout, or 404 is not a close. The operation ID is
// stable for retries so a provider can make its close request idempotent.
type CloseRequest struct {
	ProviderPaymentID string
	AmountCents       int64
	OperationID       string
}

type CloseResult struct {
	ProviderPaymentID     string `json:"payment_id"`
	ExternalTransactionID string `json:"external_transaction_id"`
	AmountCents           int64  `json:"amount_cents"`
	OperationID           string `json:"operation_id"`
	Status                string `json:"status"`
}

type CloseGateway interface {
	QueryGateway
	Close(context.Context, CloseRequest) (CloseResult, error)
}

// gatewayRegistry is the payment-specific typed view over the common
// compiled-in module registry. Database channel instances select an ID, while
// this registry supplies the executable implementation.
var gatewayRegistry = module.NewRegistry[Gateway](channel.Payment)

// FactoryConfig is kept as a typed compatibility boundary for existing
// payment callers. New adapters may use Values/Secret from module.Config and
// leave Epay empty.
type FactoryConfig = module.Config

type epayFactoryConfig struct {
	Config EpayConfig
	Type   string
}

func New(name string, config FactoryConfig) (Gateway, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("payment gateway ID is empty")
	}
	implementation := name
	if strings.HasPrefix(name, "epay_") {
		implementation = "epay"
		cfg, ok := config.Payload.(EpayConfig)
		if !ok {
			return nil, errors.New("invalid epay factory configuration")
		}
		config.Payload = epayFactoryConfig{Config: cfg, Type: strings.TrimPrefix(name, "epay_")}
	}
	return gatewayRegistry.New(implementation, config)
}

func init() {
	gatewayRegistry.MustRegister(channel.Descriptor{
		ID: "mock", Name: "模拟支付", Category: channel.Payment,
		Capabilities: []string{"create_intent", "verify_webhook", "query", "close_intent", "lookup_close"},
		Fields:       []channel.Field{{Key: "secret_source", Label: "签名密钥来源", Type: channel.Select, Options: []string{"deployment"}}},
	}, func(config FactoryConfig) (Gateway, error) {
		return MockGateway{Secret: []byte(config.Secret)}, nil
	})
}

type MockGateway struct{ Secret []byte }

func (g MockGateway) Name() string { return "mock" }

func (g MockGateway) CreateIntent(req IntentRequest) (Intent, error) {
	if req.InvoiceNo == "" || req.AmountCents < 0 {
		return Intent{}, errors.New("invalid payment intent")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Intent{}, err
	}
	id := "mock_" + hex.EncodeToString(raw)
	return Intent{ProviderPaymentID: id, CheckoutURL: "mock://pay/" + id}, nil
}

func (g MockGateway) VerifySignature(body []byte, signature string) bool {
	if len(g.Secret) == 0 || signature == "" {
		return false
	}
	mac := hmac.New(sha256.New, g.Secret)
	mac.Write(body)
	want := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(strings.ToLower(strings.TrimSpace(signature))), []byte(want))
}

func (g MockGateway) DecodeWebhook(body []byte) (Webhook, error) {
	var event Webhook
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&event); err != nil {
		return event, fmt.Errorf("invalid webhook: %w", err)
	}
	if event.EventID == "" || event.ProviderPaymentID == "" || event.InvoiceID < 1 || event.AmountCents < 0 {
		return event, errors.New("webhook fields are invalid")
	}
	if event.Status != "succeeded" && event.Status != "failed" {
		return event, errors.New("webhook status is invalid")
	}
	return event, nil
}

func (g MockGateway) Query(ctx context.Context, req QueryRequest) (QueryResult, error) {
	if err := ctx.Err(); err != nil {
		return QueryResult{}, err
	}
	if req.ProviderPaymentID == "" || req.AmountCents < 1 {
		return QueryResult{}, ErrQueryInvalid
	}
	return QueryResult{ProviderPaymentID: req.ProviderPaymentID, AmountCents: req.AmountCents, Status: "pending", ExternalTransactionID: "mock-query:" + req.ProviderPaymentID}, nil
}

func (g MockGateway) Close(ctx context.Context, req CloseRequest) (CloseResult, error) {
	if err := ctx.Err(); err != nil {
		return CloseResult{}, ErrCloseUnknown
	}
	if req.ProviderPaymentID == "" || req.AmountCents < 1 || req.OperationID == "" {
		return CloseResult{}, ErrCloseInvalid
	}
	result := CloseResult{ProviderPaymentID: req.ProviderPaymentID, OperationID: req.OperationID, AmountCents: req.AmountCents, Status: "closed"}
	g.recordCloseEvidence(result)
	return result, nil
}

func For(name, secret string, configs ...EpayConfig) (Gateway, bool) {
	config := FactoryConfig{Secret: secret}
	if len(configs) > 0 {
		config.Payload = configs[0]
	}
	gateway, err := New(name, config)
	return gateway, err == nil
}
