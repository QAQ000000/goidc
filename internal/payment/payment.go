package payment

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
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

func init() {
	channel.MustRegister(channel.Descriptor{
		ID: "mock", Name: "模拟支付", Category: channel.Payment,
		Capabilities: []string{"create_intent", "verify_webhook"},
		Fields:       []channel.Field{{Key: "secret_source", Label: "签名密钥来源", Type: channel.Select, Options: []string{"deployment"}}},
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

func For(name, secret string, configs ...EpayConfig) (Gateway, bool) {
	if name != "mock" {
		if strings.HasPrefix(name, "epay_") && len(configs) > 0 {
			typeName := strings.TrimPrefix(name, "epay_")
			g := EpayGateway{EpayConfig: configs[0], Type: typeName}
			if g.Validate() == nil {
				return g, true
			}
		}
		return nil, false
	}
	return MockGateway{Secret: []byte(secret)}, true
}
