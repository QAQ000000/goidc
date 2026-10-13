package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSMSGatewayEvidencePreservesOriginalBody(t *testing.T) {
	for _, tc := range []struct {
		name, body, outcome string
		status              int
		ok, truncated       bool
	}{
		{"bom-success", "\xef\xbb\xbf" + `{"status":"200"}`, "accepted", 200, true, false},
		{"quota", `{"status":"405","msg":"短信条数已用完"}`, "business_rejected", 200, false, false},
		{"non-json", "private response", "invalid_json", 200, false, false},
		{"http-failure", "private response", "http_rejected", 502, false, false},
		{"oversize", strings.Repeat("x", (64<<10)+8), "response_too_large", 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer target.Close()
			var evidence store.SMSDeliveryEvidence
			ctx := context.WithValue(context.Background(), smsEvidenceKey{}, &evidence)
			err := (idcsmartSMS{endpoint: target.URL}).Send(ctx, store.SMSRuntimeConfig{API: "app", Key: "private-key", Sign: "sign"}, store.SMSJob{Recipient: "+8613800138000", ExpiresAt: time.Now().Add(time.Minute)})
			if (err == nil) != tc.ok || evidence.Outcome != tc.outcome || evidence.HTTPStatus != tc.status || evidence.Truncated != tc.truncated || !bytes.Equal(evidence.Response, []byte(tc.body[:min(len(tc.body), 64<<10)])) {
				t.Fatalf("unexpected evidence: status=%d outcome=%s truncated=%t error=%v", evidence.HTTPStatus, evidence.Outcome, evidence.Truncated, err)
			}
		})
	}
}

func TestSMSWithEvidenceFailuresRemainPrivate(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte("phone and code: private"))
	}))
	c := store.SMSRuntimeConfig{Provider: "generic_http_json", Enabled: true, Endpoint: target.URL, Token: "secret"}
	j := store.SMSJob{Recipient: "+8613800138000", ExpiresAt: time.Now().Add(time.Minute)}
	e, err := SendSMSWithEvidence(context.Background(), c, j)
	if err == nil || err.Error() != "SMS delivery failed" || e.Outcome != "http_rejected" || string(e.Response) != "phone and code: private" {
		t.Fatal("response capture or privacy failed")
	}
	target.Close()
	e, err = SendSMSWithEvidence(context.Background(), c, j)
	if err == nil || e.Outcome != "transport_error" || len(e.Response) != 0 || e.HTTPStatus != 0 {
		t.Fatal("transport failure was misclassified")
	}
	c.Enabled = false
	e, err = SendSMSWithEvidence(context.Background(), c, j)
	if err == nil || e.Outcome != "configuration_rejected" {
		t.Fatal("configuration failure was misclassified")
	}
}

func TestSMSWithEvidenceGenericAcceptanceSurvivesLargeBody(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(strings.Repeat("x", (64<<10)+1))) }))
	defer target.Close()
	e, err := SendSMSWithEvidence(context.Background(), store.SMSRuntimeConfig{Provider: "generic_http_json", Enabled: true, Endpoint: target.URL, Token: "secret"}, store.SMSJob{Recipient: "+8613800138000", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || e.Outcome != "accepted" || !e.Truncated || len(e.Response) != 64<<10 {
		t.Fatal("evidence changed generic HTTP success contract")
	}
}

func TestSMSGatewayEvidenceRetainsPartialRead(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		_, _ = w.Write([]byte(`{"status":200`))
	}))
	defer target.Close()
	var evidence store.SMSDeliveryEvidence
	ctx := context.WithValue(context.Background(), smsEvidenceKey{}, &evidence)
	err := (idcsmartSMS{endpoint: target.URL}).Send(ctx, store.SMSRuntimeConfig{API: "app", Key: "key", Sign: "sign"}, store.SMSJob{Recipient: "+8613800138000", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || evidence.Outcome != "read_error" || string(evidence.Response) != `{"status":200` {
		t.Fatal("partial response evidence lost")
	}
}

func TestSMSIgnoresDefaultTransportProxy(t *testing.T) {
	proxyCalls := 0
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls++; w.WriteHeader(502) }))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	old := http.DefaultTransport
	base := old.(*http.Transport).Clone()
	base.Proxy = func(*http.Request) (*url.URL, error) { return proxyURL, nil }
	http.DefaultTransport = base
	t.Cleanup(func() { http.DefaultTransport = old; base.CloseIdleConnections() })
	targetCalls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls++; w.WriteHeader(204) }))
	defer target.Close()
	e := SendSMS(context.Background(), store.SMSRuntimeConfig{Enabled: true, Provider: "generic_http_json", Endpoint: target.URL, Token: "fixture"}, store.SMSJob{Recipient: "+8613800138000", Body: "fixture", ExpiresAt: time.Now().Add(time.Minute)})
	if e != nil || proxyCalls != 0 || targetCalls != 1 {
		t.Fatalf("proxy isolation failed: error=%v proxy=%d target=%d", e, proxyCalls, targetCalls)
	}
	if base.Proxy == nil {
		t.Fatal("shared default transport mutated")
	}
}

func TestSMSProtocolAndFailurePrivacy(t *testing.T) {
	calls := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer secret" {
			t.Error("missing authorization")
		}
		var body map[string]string
		if e := json.NewDecoder(r.Body).Decode(&body); e != nil || body["recipient"] != "+8613800138000" || body["message"] != "test message" {
			t.Error("invalid protocol payload")
		}
		w.WriteHeader(204)
	}))
	defer target.Close()
	c := store.SMSRuntimeConfig{Enabled: true, Provider: "generic_http_json", Endpoint: target.URL, Token: "secret"}
	j := store.SMSJob{Recipient: "+8613800138000", Body: "test message", Kind: "test", ExpiresAt: time.Now().Add(time.Minute)}
	if e := SendSMS(context.Background(), c, j); e != nil || calls != 1 {
		t.Fatal("delivery failed", e)
	}
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redirect.Close()
	c.Endpoint = redirect.URL
	if e := SendSMS(context.Background(), c, j); e == nil || calls != 1 {
		t.Fatal("redirect followed")
	}
	failure := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte("secret test message"))
	}))
	defer failure.Close()
	c.Endpoint = failure.URL
	if e := SendSMS(context.Background(), c, j); e == nil || strings.Contains(e.Error(), "secret") {
		t.Fatal("response leaked", e)
	}
	j.ExpiresAt = time.Now().Add(-time.Second)
	c.Endpoint = target.URL
	if e := SendSMS(context.Background(), c, j); e == nil || calls != 1 {
		t.Fatal("expired message sent")
	}
	j.ExpiresAt = time.Now().Add(time.Minute)
	c.Provider = "unregistered_sms_provider"
	if e := SendSMS(context.Background(), c, j); e == nil || e.Error() != "SMS delivery failed" || calls != 1 {
		t.Fatal("unregistered provider did not fail safely", e)
	}
}

func TestIdcsmartSMSProtocol(t *testing.T) {
	called := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.Header.Get("api") != "app-123" || r.Header.Get("key") != "private-key" || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			t.Errorf("unexpected idcsmart headers: method=%s api=%q key=%q content-type=%q", r.Method, r.Header.Get("api"), r.Header.Get("key"), r.Header.Get("Content-Type"))
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if r.Form.Get("to") != "13800138000" || r.Form.Get("content") != "【智简魔方】验证码：123456" || r.Form.Get("template_id") != "" || r.Form.Get("vars") != "" {
			t.Errorf("unexpected idcsmart form: %#v", r.Form)
		}
		_, _ = w.Write([]byte(`{"status":200,"msg":"ok"}`))
	}))
	defer target.Close()
	p := idcsmartSMS{endpoint: target.URL}
	err := p.Send(context.Background(), store.SMSRuntimeConfig{API: "app-123", Key: "private-key", Sign: "【智简魔方】"}, store.SMSJob{Recipient: "+8613800138000", Body: "验证码：123456", ExpiresAt: time.Now().Add(time.Minute)})
	if err != nil || !called {
		t.Fatalf("idcsmart delivery failed: %v", err)
	}
}

func TestIdcsmartSMSLegacyResponseVariants(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		wantOK         bool
	}{
		{"bom-success", "\xef\xbb\xbf" + `{"status":"200"}`, true},
		{"quota-exhausted", `{"status":"405","msg":"短信条数已用完"}`, false},
		{"gateway-rejected", `{"status":400,"msg":"private upstream details"}`, false},
		{"malformed", `{"status":200`, false},
		{"trailing-garbage", `{"status":200}invalid`, false},
		{"oversized", `{"status":200,"padding":"` + strings.Repeat("x", 64<<10) + `"}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
					return
				}
				if r.Form.Get("to") != "13800138000" || r.Form.Get("template_id") != "approved-template" {
					t.Error("domestic recipient or approved template was not passed")
				}
				_, _ = w.Write([]byte(tc.response))
			}))
			defer target.Close()
			err := (idcsmartSMS{endpoint: target.URL}).Send(context.Background(), store.SMSRuntimeConfig{API: "app", Key: "private-key", Sign: "sign"}, store.SMSJob{Recipient: "+8613800138000", Body: "验证码：123456", TemplateID: "approved-template", ExpiresAt: time.Now().Add(time.Minute)})
			if (err == nil) != tc.wantOK {
				t.Fatalf("success=%t, want %t", err == nil, tc.wantOK)
			}
			if err != nil && strings.Contains(err.Error(), "private upstream details") {
				t.Fatal("upstream response leaked")
			}
		})
	}
}

func TestIdcsmartSMSFailureDoesNotExposeResponse(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte(`{"status":500,"msg":"private upstream details"}`))
	}))
	defer target.Close()
	err := (idcsmartSMS{endpoint: target.URL}).Send(context.Background(), store.SMSRuntimeConfig{API: "app-123", Key: "private-key", Sign: "sign"}, store.SMSJob{Recipient: "+8613800138000", Body: "body", ExpiresAt: time.Now().Add(time.Minute)})
	if err == nil || strings.Contains(err.Error(), "private upstream details") || strings.Contains(err.Error(), "private-key") {
		t.Fatalf("upstream response leaked: %v", err)
	}
}

func TestIdcsmartSign(t *testing.T) {
	for _, test := range []struct{ input, want string }{
		{"智简魔方", "【智简魔方】"},
		{"【智简魔方】", "【智简魔方】"},
		{"【智简】【魔方】", "【智简魔方】"},
		{"", ""},
	} {
		if got := idcsmartSign(test.input); got != test.want {
			t.Fatalf("idcsmartSign(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestIdcsmartStatusOK(t *testing.T) {
	if !idcsmartStatusOK([]byte(`200`)) || !idcsmartStatusOK([]byte(`"200"`)) || idcsmartStatusOK([]byte(`500`)) || idcsmartStatusOK([]byte(`null`)) {
		t.Fatal("unexpected idcsmart status parsing")
	}
}
