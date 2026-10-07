package payment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type QueryRequest struct {
	ProviderPaymentID string
	AmountCents       int64
}

// Only these verified fields are persisted. Merchant messages and credentials
// must never enter API responses, query history, or transport error logs.
type QueryResult struct {
	ProviderPaymentID     string `json:"payment_id"`
	ExternalTransactionID string `json:"external_transaction_id"`
	AmountCents           int64  `json:"amount_cents"`
	Status                string `json:"status"`
}

type QueryGateway interface {
	Gateway
	Query(context.Context, QueryRequest) (QueryResult, error)
}

var ErrQueryUnavailable = errors.New("payment provider query unavailable")
var ErrQueryInvalid = errors.New("payment provider query result invalid")

var epayQueryTransport = func() *http.Transport {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	return transport
}()

func (g EpayGateway) Query(ctx context.Context, in QueryRequest) (QueryResult, error) {
	var out QueryResult
	if g.Validate() != nil || !epayReferencePattern.MatchString(in.ProviderPaymentID) || in.AmountCents < 1 {
		return out, ErrQueryInvalid
	}
	values := url.Values{"act": {"order"}, "pid": {g.PID}, "key": {g.Key}, "out_trade_no": {in.ProviderPaymentID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.APIURL, "/")+"/api.php?"+values.Encode(), nil)
	if err != nil {
		return out, ErrQueryUnavailable
	}
	req.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: epayQueryTransport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(req)
	if err != nil {
		// net/url errors include the URL and therefore the merchant key.
		return out, ErrQueryUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return out, ErrQueryUnavailable
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
	if err != nil || len(body) > 64<<10 {
		return out, ErrQueryInvalid
	}
	fields, err := strictQueryObject(body)
	if err != nil || queryScalar(fields["code"]) != "1" {
		return out, ErrQueryInvalid
	}
	if queryScalar(fields["pid"]) != g.PID || queryScalar(fields["type"]) != g.Type || queryScalar(fields["out_trade_no"]) != in.ProviderPaymentID {
		return out, ErrQueryInvalid
	}
	trade := queryScalar(fields["trade_no"])
	amount, err := epayAmountCents(queryScalar(fields["money"]))
	if !epayTradePattern.MatchString(trade) || err != nil || amount != in.AmountCents {
		return out, ErrQueryInvalid
	}
	status := queryScalar(fields["status"])
	if status != "0" && status != "1" {
		return out, ErrQueryInvalid
	}
	out = QueryResult{ProviderPaymentID: in.ProviderPaymentID, ExternalTransactionID: g.transactionID(trade), AmountCents: amount, Status: "pending"}
	if status == "1" {
		out.Status = "succeeded"
	}
	return out, nil
}

func queryScalar(raw json.RawMessage) string {
	if len(raw) > 0 && raw[0] == '"' {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			return value
		}
		return ""
	}
	// Number lexemes remain exact. Amount validation rejects exponents/fractions.
	return string(raw)
}

func strictQueryObject(body []byte) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrQueryInvalid
	}
	fields := map[string]json.RawMessage{}
	for decoder.More() {
		token, err = decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok {
			return nil, ErrQueryInvalid
		}
		if _, duplicate := fields[name]; duplicate {
			return nil, ErrQueryInvalid
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrQueryInvalid
		}
		fields[name] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, ErrQueryInvalid
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, ErrQueryInvalid
	}
	return fields, nil
}
