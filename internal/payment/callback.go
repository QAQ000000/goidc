package payment

import (
	"bytes"
	"encoding/json"
	"errors"
	"mime"
	"net/http"
)

const callbackMaxBytes = 64 << 10

type CallbackRequest struct {
	Method   string
	Header   http.Header
	RawQuery string
	Body     []byte
}

// CallbackPayload is untrusted routing input, not a settlement event. The host
// must resolve the referenced transaction's frozen gateway, verify its
// signature, decode its event and match the transaction before any write.
type CallbackPayload struct {
	Reference     string
	Body          []byte
	CanonicalBody []byte
	Signature     string
}

type CallbackReply struct {
	Status      int
	ContentType string
	Body        []byte
}

// Protocol parsing and replies are credential-free, bounded local operations.
// Reply receives JSON already encoded by the host, keeping store types and
// response envelopes outside the payment package.
type WebhookProtocol interface {
	Parse(CallbackRequest) (CallbackPayload, error)
	Reply(status int, jsonBody []byte) CallbackReply
}

type CallbackError struct{ Status int }

func (*CallbackError) Error() string { return "invalid payment callback request" }

type mockWebhookProtocol struct{}

func (mockWebhookProtocol) Parse(request CallbackRequest) (CallbackPayload, error) {
	if request.Method != http.MethodPost {
		return CallbackPayload{}, &CallbackError{Status: http.StatusMethodNotAllowed}
	}
	if len(request.RawQuery) > callbackMaxBytes {
		return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
	}
	event, err := decodeMockWebhook(request.Body)
	if err != nil || event.ProviderPaymentID == "" || len(event.ProviderPaymentID) > 2000 || len(request.Header.Values("X-Payment-Signature")) > 1 {
		return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
	}
	return CallbackPayload{
		Reference: event.ProviderPaymentID, Body: bytes.Clone(request.Body),
		CanonicalBody: bytes.Clone(request.Body), Signature: request.Header.Get("X-Payment-Signature"),
	}, nil
}

func (mockWebhookProtocol) Reply(status int, jsonBody []byte) CallbackReply {
	return CallbackReply{Status: status, ContentType: "application/json; charset=utf-8", Body: bytes.Clone(jsonBody)}
}

func decodeMockWebhook(body []byte) (Webhook, error) {
	var event Webhook
	if len(body) == 0 || len(body) > callbackMaxBytes {
		return event, errors.New("invalid payment callback payload")
	}
	// The token pass rejects duplicate keys and trailing JSON. The typed pass
	// also rejects unknown fields and non-integral or overflowing amounts.
	if _, err := strictQueryObject(body); err != nil {
		return event, errors.New("invalid payment callback payload")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&event); err != nil {
		return event, errors.New("invalid payment callback payload")
	}
	return event, nil
}

type epayWebhookProtocol struct{}

func (epayWebhookProtocol) Parse(request CallbackRequest) (CallbackPayload, error) {
	if len(request.Body) > callbackMaxBytes || len(request.RawQuery) > callbackMaxBytes {
		return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
	}
	var body []byte
	switch request.Method {
	case http.MethodGet:
		if len(request.Body) != 0 {
			return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
		}
		body = []byte(request.RawQuery)
	case http.MethodPost:
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/x-www-form-urlencoded" || len(request.Header.Values("Content-Type")) != 1 || request.RawQuery != "" {
			return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
		}
		body = request.Body
	default:
		return CallbackPayload{}, &CallbackError{Status: http.StatusMethodNotAllowed}
	}
	values, err := epayValues(body)
	if err != nil || values.Get("out_trade_no") == "" {
		return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
	}
	canonical, err := json.Marshal(values)
	if err != nil {
		return CallbackPayload{}, &CallbackError{Status: http.StatusBadRequest}
	}
	return CallbackPayload{Reference: values.Get("out_trade_no"), Body: bytes.Clone(body), CanonicalBody: canonical}, nil
}

func (epayWebhookProtocol) Reply(status int, _ []byte) CallbackReply {
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		return CallbackReply{Status: http.StatusOK, ContentType: "text/plain; charset=utf-8", Body: []byte("success")}
	}
	return CallbackReply{Status: status, ContentType: "text/plain; charset=utf-8", Body: []byte("fail")}
}
