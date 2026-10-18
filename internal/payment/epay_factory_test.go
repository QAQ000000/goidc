package payment

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"strings"
	"testing"
)

func epayGenericFixture() FactoryConfig {
	return FactoryConfig{
		Values:       map[string]any{"api_url": "https://merchant.example/gateway/", "pid": "10001", "types": []string{"alipay", "wxpay"}},
		Secrets:      map[string]string{"key": "fixture-merchant-key"},
		PublicOrigin: "https://billing.example",
	}
}

func TestEpayFactoryGenericConfigurationAndLegacyNamespaceRemainCompatible(t *testing.T) {
	cfg := epayGenericFixture()
	gateway, err := NewFor("epay", "epay_alipay", cfg)
	if err != nil {
		t.Fatal(err)
	}
	g := gateway.(EpayGateway)
	wantNamespace := EpayMerchantNamespace("https://merchant.example/gateway/", "10001")
	if g.TransactionNamespace != wantNamespace || g.Key != "fixture-merchant-key" || g.PublicOrigin != cfg.PublicOrigin {
		t.Fatal("generic factory lost merchant or public-origin configuration")
	}
	// Factory output cannot retain mutable input lists from an instance draft.
	cfg.Values["types"].([]string)[0] = "bank"
	if g.Types[0] != "alipay" {
		t.Fatal("gateway shares mutable instance configuration")
	}
	values := url.Values{"pid": {"10001"}, "type": {"alipay"}, "trade_no": {"fixture-trade"}, "out_trade_no": {"epay_7_00000000000000000000000000000000"}, "money": {"12.34"}, "trade_status": {"TRADE_SUCCESS"}}
	values.Set("sign", g.sign(values))
	event, err := g.DecodeWebhook([]byte(values.Encode()))
	if err != nil || event.EventID != wantNamespace+":fixture-trade" || event.ExternalTransactionID != event.EventID {
		t.Fatal("new callback lost its existing merchant transaction identity", err)
	}
	legacy := g.EpayConfig
	legacy.TransactionNamespace = ""
	legacyGateway, err := New("epay_alipay", FactoryConfig{Payload: legacy})
	if err != nil {
		t.Fatal(err)
	}
	legacyEvent, err := legacyGateway.DecodeWebhook([]byte(values.Encode()))
	if err != nil || legacyEvent.EventID != "10001:fixture-trade" || legacyEvent.ExternalTransactionID != legacyEvent.EventID {
		t.Fatal("legacy empty namespace no longer retains PID:trade", err)
	}
	for _, namespace := range []string{"", "frozen-namespace"} {
		generic := epayGenericFixture()
		generic.Values["transaction_namespace"] = namespace
		generic.Values["types"] = []any{"alipay", "wxpay"}
		gateway, err := NewFor("epay", "epay_alipay", generic)
		if err != nil || gateway.(EpayGateway).TransactionNamespace != namespace {
			t.Fatal("explicit generic namespace was replaced", err)
		}
	}
	for _, kind := range []string{"wrong_payload", "bad_types", "non_string_type", "bad_namespace", "missing_secret", "missing_origin", "disabled_method"} {
		t.Run(kind, func(t *testing.T) {
			invalid := epayGenericFixture()
			switch kind {
			case "wrong_payload":
				invalid.Payload = "private-invalid-payload"
			case "bad_types":
				invalid.Values["types"] = "alipay"
			case "non_string_type":
				invalid.Values["types"] = []any{"alipay", true}
			case "bad_namespace":
				invalid.Values["transaction_namespace"] = true
			case "missing_secret":
				invalid.Secrets = nil
			case "missing_origin":
				invalid.PublicOrigin = ""
			case "disabled_method":
				invalid.Values["types"] = []string{"wxpay"}
			}
			gateway, err := NewFor("epay", "epay_alipay", invalid)
			if err == nil || gateway != nil || strings.Contains(err.Error(), "private-invalid-payload") || strings.Contains(err.Error(), "fixture-merchant-key") {
				t.Fatal("invalid or sensitive factory configuration accepted or exposed", err)
			}
		})
	}
}

func TestPaymentMerchantScopeMatchesHistoricalCanonicalIdentity(t *testing.T) {
	scopeFor := func(canonical, pid string) string {
		sum := sha256.Sum256([]byte("epay\x00" + canonical + "\x00" + pid))
		return "epay:" + hex.EncodeToString(sum[:])
	}
	for _, tc := range []struct{ raw, canonical string }{
		{"https://PAY.EXAMPLE:443/gateway/", "https://pay.example/gateway"},
		{"http://PAY.EXAMPLE:80/gateway///", "http://pay.example/gateway"},
		{"https://pay.example:8443/a%2Fb/", "https://pay.example:8443/a%2Fb"},
		{"https://[2001:db8::1]:443/sub/", "https://[2001:db8::1]/sub"},
		{"https://[2001:db8::1]:8443/sub/", "https://[2001:db8::1]:8443/sub"},
	} {
		gateway := EpayGateway{EpayConfig: EpayConfig{APIURL: tc.raw, PID: " 10001 ", Key: "not-part-of-merchant-identity", TransactionNamespace: "also-not-part-of-scope"}}
		scope, err := gateway.MerchantScope()
		if err != nil || scope != scopeFor(tc.canonical, "10001") {
			t.Fatalf("historical merchant identity changed for %s: %s %v", tc.raw, scope, err)
		}
		gateway.Key = "rotated-secret"
		rotated, err := gateway.MerchantScope()
		if err != nil || scope != rotated {
			t.Fatal("merchant credential rotation changed the merchant identity")
		}
	}
	for _, raw := range []string{"", "ftp://merchant.example", "https://user:private@merchant.example", "https://merchant.example?key=private", "https://merchant.example#private"} {
		gateway := EpayGateway{EpayConfig: EpayConfig{APIURL: raw, PID: "10001"}}
		if scope, err := gateway.MerchantScope(); err == nil || scope != "" || strings.Contains(err.Error(), "private") {
			t.Fatal("invalid merchant identity accepted or exposed", err)
		}
	}
	if scope, err := (MockGateway{}).MerchantScope(); err != nil || scope != "mock:test-only" {
		t.Fatal("mock lost its fixed simulation scope")
	}
}
