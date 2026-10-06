package payment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestEpayQueryVerifiedResponseAndWire(t *testing.T) {
	ref := "epay_9_01234567890123456789012345678901"
	for _, status := range []any{0, "1"} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				if r.Method != "GET" || r.URL.Path != "/api.php" || q.Get("act") != "order" || q.Get("pid") != "10001" || q.Get("key") != "unit-merchant-secret" || q.Get("out_trade_no") != ref || len(q) != 4 {
					t.Error("query did not use V1 merchant-order protocol")
					w.WriteHeader(400)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"code": 1, "pid": "10001", "type": "alipay", "out_trade_no": ref, "trade_no": "query-123", "money": "30.01", "status": status, "msg": "untrusted-merchant-text"})
			}))
			defer server.Close()
			g := EpayGateway{EpayConfig: EpayConfig{APIURL: server.URL, PublicOrigin: "http://localhost:3088", PID: "10001", Key: "unit-merchant-secret", Types: []string{"alipay"}}, Type: "alipay"}
			result, err := g.Query(t.Context(), QueryRequest{ProviderPaymentID: ref, AmountCents: 3001})
			want := "pending"
			if fmt.Sprint(status) == "1" {
				want = "succeeded"
			}
			if err != nil || result.Status != want || result.AmountCents != 3001 || result.ExternalTransactionID != "10001:query-123" {
				t.Fatal("invalid query normalization", result, err)
			}
			encoded, _ := json.Marshal(result)
			if strings.Contains(string(encoded), "secret") || strings.Contains(string(encoded), "untrusted") {
				t.Fatal("provider credential/message exposed")
			}
		})
	}
}

func TestEpayQueryRejectsAmbiguousAndMismatchedResponses(t *testing.T) {
	ref := "epay_9_01234567890123456789012345678901"
	base := `{"code":1,"pid":"10001","type":"alipay","out_trade_no":"` + ref + `","trade_no":"query-123","money":"30.01","status":1}`
	cases := []string{
		strings.Replace(base, `"pid":"10001"`, `"pid":"99999"`, 1),
		strings.Replace(base, `"type":"alipay"`, `"type":"wxpay"`, 1),
		strings.Replace(base, ref, "epay_10_01234567890123456789012345678901", 1),
		strings.Replace(base, `"money":"30.01"`, `"money":"30.001"`, 1),
		strings.Replace(base, `"money":"30.01"`, `"money":3.001e1`, 1),
		strings.Replace(base, `"money":"30.01"`, `"money":"29.99"`, 1),
		strings.Replace(base, `"status":1`, `"status":2`, 1),
		strings.Replace(base, `"status":1`, `"status":false`, 1),
		strings.Replace(base, `"status":1`, `"status":0,"status":1`, 1),
		strings.Replace(base, `"code":1`, `"code":-1`, 1),
		base + `{}`, "[]", "<html>failure</html>", strings.Repeat("x", 65537),
	}
	for i, body := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(body)) }))
			defer server.Close()
			g := EpayGateway{EpayConfig: EpayConfig{APIURL: server.URL, PublicOrigin: "http://localhost:3088", PID: "10001", Key: "unit-merchant-secret", Types: []string{"alipay"}}, Type: "alipay"}
			if _, err := g.Query(t.Context(), QueryRequest{ProviderPaymentID: ref, AmountCents: 3001}); !errors.Is(err, ErrQueryInvalid) {
				t.Fatal("invalid result accepted", err)
			}
		})
	}
}

func TestEpayQueryNoRedirectTLSVerificationAndRedactedTimeout(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, http.StatusFound) }))
	defer redirect.Close()
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("untrusted certificate accepted") }))
	defer tls.Close()
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer slow.Close()
	for _, endpoint := range []string{redirect.URL, tls.URL, slow.URL} {
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		g := EpayGateway{EpayConfig: EpayConfig{APIURL: endpoint, PublicOrigin: "http://localhost:3088", PID: "10001", Key: "unit-merchant-secret", Types: []string{"alipay"}}, Type: "alipay"}
		_, err := g.Query(ctx, QueryRequest{ProviderPaymentID: "epay_9_01234567890123456789012345678901", AmountCents: 3001})
		cancel()
		if !errors.Is(err, ErrQueryUnavailable) || strings.Contains(err.Error(), "unit-merchant-secret") || strings.Contains(err.Error(), endpoint) {
			t.Fatal("unsafe provider failure", err)
		}
	}
	if redirected.Load() != 0 {
		t.Fatal("query followed redirect")
	}
}

func TestEpayQueryDoesNotUseDownloadProxy(t *testing.T) {
	var calls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(502)
	}))
	defer proxy.Close()
	t.Setenv("HTTPS_PROXY", proxy.URL)
	t.Setenv("https_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	t.Setenv("no_proxy", "")
	g := EpayGateway{EpayConfig: EpayConfig{APIURL: "https://merchant.invalid", PublicOrigin: "http://localhost:3088", PID: "10001", Key: "unit-merchant-secret", Types: []string{"alipay"}}, Type: "alipay"}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := g.Query(ctx, QueryRequest{ProviderPaymentID: "epay_9_01234567890123456789012345678901", AmountCents: 3001}); !errors.Is(err, ErrQueryUnavailable) || calls.Load() != 0 {
		t.Fatal("merchant query used download proxy", err, calls.Load())
	}
}
