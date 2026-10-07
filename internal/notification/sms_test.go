package notification

import (
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
}
