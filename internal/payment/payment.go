package payment

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	EventID               string `json:"event_id"`
	ProviderPaymentID     string `json:"payment_id"`
	InvoiceID             int64  `json:"invoice_id"`
	AmountCents           int64  `json:"amount_cents"`
	Status                string `json:"status"`
	ExternalTransactionID string `json:"external_transaction_id,omitempty"`
}

type Gateway interface {
	Name() string
	// CreateIntent is a pure local operation. It may generate a reference and
	// sign a checkout URL, but must not create an upstream payment or use network I/O.
	CreateIntent(IntentRequest) (Intent, error)
	VerifySignature(body []byte, signature string) bool
	DecodeWebhook(body []byte) (Webhook, error)
}

// MerchantScopeGateway provides a stable, non-secret identity for refund and
// reporting boundaries. It must depend only on the frozen merchant identity.
type MerchantScopeGateway interface {
	MerchantScope() (string, error)
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

// FactoryConfig retains the existing payment constructor signature. Adapters
// receive common instance values and credentials; legacy typed payloads stay
// inside their own adapter factory.
type FactoryConfig = module.Config

func New(name string, config FactoryConfig) (Gateway, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("payment gateway ID is empty")
	}
	implementation, ok := MethodOwner(name)
	if !ok {
		return nil, errors.New("payment gateway is not registered")
	}
	return NewFor(implementation, name, config)
}

func init() {
	err := registerFactory(channel.Descriptor{
		ID: "mock", Name: "模拟支付", Category: channel.Payment,
		Capabilities: []string{"create_intent", "verify_webhook", "query", "close_intent", "lookup_close"},
		Fields:       []channel.Field{{Key: "secret_source", Label: "签名密钥来源", Type: channel.Select, Options: []string{"deployment"}}},
	}, []Method{{ID: "mock", Label: "模拟支付", Simulation: true}}, mockWebhookProtocol{}, func(_ string, config FactoryConfig) (Gateway, error) {
		secret := config.Secrets["secret"]
		if secret == "" {
			secret = config.Secret
		}
		return MockGateway{Secret: []byte(secret)}, nil
	}, true)
	if err != nil {
		panic(err)
	}
}

type MockGateway struct{ Secret []byte }

func (g MockGateway) Name() string { return "mock" }

func (g MockGateway) MerchantScope() (string, error) { return "mock:test-only", nil }

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
	event, err := decodeMockWebhook(body)
	if err != nil {
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
