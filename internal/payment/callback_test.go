package payment

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
)

func TestMockCallbackProtocolStrictLocatorAndJSONAcknowledgement(t *testing.T) {
	protocol, ok := LookupProtocol("mock")
	if !ok {
		t.Fatal("missing mock callback protocol")
	}
	body := []byte(`{"event_id":"fixture-event","payment_id":"mock_fixture","invoice_id":7,"amount_cents":1234,"status":"succeeded"}`)
	mac := hmac.New(sha256.New, []byte("mock-credential-only"))
	mac.Write(body)
	header := http.Header{"X-Payment-Signature": []string{hex.EncodeToString(mac.Sum(nil))}}
	parsed, err := protocol.Parse(CallbackRequest{Method: http.MethodPost, Header: header, Body: body})
	if err != nil || parsed.Reference != "mock_fixture" || parsed.Signature != header.Get("X-Payment-Signature") || !bytes.Equal(parsed.Body, body) || !bytes.Equal(parsed.CanonicalBody, body) {
		t.Fatalf("mock locator changed wire data: %#v %v", parsed, err)
	}
	gateway := MockGateway{Secret: []byte("mock-credential-only")}
	if !gateway.VerifySignature(parsed.Body, parsed.Signature) {
		t.Fatal("locator altered the signed payload")
	}
	if _, err := gateway.DecodeWebhook(parsed.Body); err != nil {
		t.Fatal(err)
	}
	// Locating a reference never authenticates it; the actual frozen gateway
	// must still reject an invalid signature after lookup.
	header.Set("X-Payment-Signature", "untrusted-signature")
	unsigned, err := protocol.Parse(CallbackRequest{Method: http.MethodPost, Header: header, Body: body})
	if err != nil || gateway.VerifySignature(unsigned.Body, unsigned.Signature) {
		t.Fatal("credential-free locator treated routing data as authentication")
	}
	for _, status := range []int{200, 201, 400, 401, 409, 500} {
		encoded := []byte("{\"data\":{\"fixture\":true}}\n")
		reply := protocol.Reply(status, encoded)
		if reply.Status != status || reply.ContentType != "application/json; charset=utf-8" || !bytes.Equal(reply.Body, encoded) {
			t.Fatalf("mock acknowledgement changed status or host JSON: %#v", reply)
		}
		encoded[0] = ' '
		if reply.Body[0] != '{' {
			t.Fatal("reply retained mutable host JSON")
		}
	}
	for _, invalid := range [][]byte{
		bytes.Replace(body, []byte(`"payment_id":"mock_fixture"`), []byte(`"payment_id":"first","payment_id":"second"`), 1),
		bytes.Replace(body, []byte(`"payment_id":"mock_fixture"`), []byte(`"payment_id":"first","payment_\u0069d":"second"`), 1),
		bytes.Replace(body, []byte(`"payment_id":"mock_fixture"`), []byte(`"payment_id":null`), 1),
		bytes.Replace(body, []byte(`"payment_id":"mock_fixture"`), []byte(`"payment_id":""`), 1),
		bytes.Replace(body, []byte(`"amount_cents":1234`), []byte(`"amount_cents":12.34`), 1),
		bytes.Replace(body, []byte(`"status":"succeeded"`), []byte(`"status":"succeeded","private":"private-input-marker"`), 1),
		append(bytes.Clone(body), []byte(`{}`)...), nil, []byte(`[]`), []byte(`null`), []byte(strings.Repeat("x", callbackMaxBytes+1)),
	} {
		_, err := protocol.Parse(CallbackRequest{Method: http.MethodPost, Body: invalid})
		var callbackError *CallbackError
		if !errors.As(err, &callbackError) || callbackError.Status != http.StatusBadRequest || strings.Contains(err.Error(), "private-input-marker") {
			t.Fatalf("invalid callback accepted or leaked input: %v", err)
		}
		if _, err := gateway.DecodeWebhook(invalid); err == nil {
			t.Fatal("strict locator and gateway decoder disagreed on malformed JSON")
		}
	}
	_, err = protocol.Parse(CallbackRequest{Method: http.MethodGet, Body: body})
	var callbackError *CallbackError
	if !errors.As(err, &callbackError) || callbackError.Status != http.StatusMethodNotAllowed {
		t.Fatal("mock callback accepted a non-POST request")
	}
	header["X-Payment-Signature"] = []string{"first", "second"}
	if _, err := protocol.Parse(CallbackRequest{Method: http.MethodPost, Header: header, Body: body}); err == nil {
		t.Fatal("ambiguous signature headers accepted")
	}
}

func TestEpayCallbackProtocolStrictFormLocatorAndLegacyAcknowledgement(t *testing.T) {
	protocol, ok := LookupProtocol("epay_alipay")
	if !ok {
		t.Fatal("missing epay callback protocol")
	}
	values := url.Values{"out_trade_no": {"epay_7_00000000000000000000000000000000"}, "money": {"12.34"}, "trade_no": {"T-fixture"}, "type": {"alipay"}, "pid": {"10001"}, "trade_status": {"TRADE_SUCCESS"}}
	header := http.Header{"Content-Type": []string{"application/x-www-form-urlencoded; charset=utf-8"}}
	wantCanonical, _ := json.Marshal(values)
	get, err := protocol.Parse(CallbackRequest{Method: http.MethodGet, RawQuery: values.Encode()})
	if err != nil {
		t.Fatal("unsigned routing locator required merchant credentials", err)
	}
	post, err := protocol.Parse(CallbackRequest{Method: http.MethodPost, Header: header, Body: []byte(values.Encode())})
	if err != nil || !reflect.DeepEqual(post, get) || get.Reference != values.Get("out_trade_no") || !bytes.Equal(get.CanonicalBody, wantCanonical) {
		t.Fatalf("form GET/POST did not normalize identically: %#v %#v %v", get, post, err)
	}
	ordered := "type=alipay&trade_status=TRADE_SUCCESS&trade_no=T-fixture&pid=10001&out_trade_no=" + values.Get("out_trade_no") + "&money=12.34"
	reordered, err := protocol.Parse(CallbackRequest{Method: http.MethodGet, RawQuery: ordered})
	if err != nil || !bytes.Equal(reordered.CanonicalBody, get.CanonicalBody) {
		t.Fatal("form ordering changed the durable callback evidence")
	}
	for _, status := range []int{200, 201, 204, 400, 401, 404, 405, 409, 500} {
		reply := protocol.Reply(status, []byte(`{"private":"host body"}`))
		wantStatus, wantBody := status, "fail"
		if status >= 200 && status < 300 {
			wantStatus, wantBody = 200, "success"
		}
		if reply.Status != wantStatus || reply.ContentType != "text/plain; charset=utf-8" || string(reply.Body) != wantBody {
			t.Fatalf("epay acknowledgement changed: %#v", reply)
		}
	}
	for name, request := range map[string]CallbackRequest{
		"method":         {Method: http.MethodPut, Header: header, Body: []byte(values.Encode())},
		"mixed_post":     {Method: http.MethodPost, Header: header, RawQuery: "out_trade_no=other", Body: []byte(values.Encode())},
		"mixed_get":      {Method: http.MethodGet, RawQuery: values.Encode(), Body: []byte("out_trade_no=other")},
		"wrong_media":    {Method: http.MethodPost, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(values.Encode())},
		"media_prefix":   {Method: http.MethodPost, Header: http.Header{"Content-Type": []string{"application/x-www-form-urlencoded-other"}}, Body: []byte(values.Encode())},
		"duplicate_ref":  {Method: http.MethodGet, RawQuery: values.Encode() + "&out_trade_no=other"},
		"duplicate_data": {Method: http.MethodGet, RawQuery: values.Encode() + "&money=99"},
		"bad_escape":     {Method: http.MethodGet, RawQuery: "out_trade_no=%ZZ"},
		"no_reference":   {Method: http.MethodGet, RawQuery: "money=12.34"},
		"large_query":    {Method: http.MethodGet, RawQuery: strings.Repeat("x", callbackMaxBytes+1)},
		"large_body":     {Method: http.MethodPost, Header: header, Body: []byte(strings.Repeat("x", callbackMaxBytes+1))},
		"large_key":      {Method: http.MethodGet, RawQuery: values.Encode() + "&" + strings.Repeat("k", 101) + "=x"},
		"large_value":    {Method: http.MethodGet, RawQuery: values.Encode() + "&private=" + strings.Repeat("x", 2001)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := protocol.Parse(request)
			var callbackError *CallbackError
			wantStatus := http.StatusBadRequest
			if name == "method" {
				wantStatus = http.StatusMethodNotAllowed
			}
			if !errors.As(err, &callbackError) || callbackError.Status != wantStatus {
				t.Fatalf("ambiguous form callback accepted: %v", err)
			}
		})
	}
}
