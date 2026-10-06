package payment

import (
	"crypto/md5"
	"fmt"
	"net/url"
	"strings"
	"testing"
)

func TestEpayGatewayBuildsLegacySignedPaymentLink(t *testing.T) {
	g := EpayGateway{EpayConfig: EpayConfig{APIURL: "https://pay.example/", PID: "10001", Key: "test-secret", PublicOrigin: "https://zjmf.example", Types: []string{"alipay"}}, Type: "alipay"}
	intent, err := g.CreateIntent(IntentRequest{InvoiceID: 42, InvoiceNo: "I-42", AmountCents: 1234})
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(intent.CheckoutURL)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	if !epayReferencePattern.MatchString(q.Get("out_trade_no")) || q.Get("money") != "12.34" || q.Get("sign_type") != "MD5" {
		t.Fatalf("unexpected query: %s", u.RawQuery)
	}
	if !strings.HasPrefix(intent.CheckoutURL, "https://pay.example/submit.php?") || q.Get("sign") == "" {
		t.Fatal(intent.CheckoutURL)
	}
	if !g.VerifySignature([]byte(q.Encode()), q.Get("sign")) {
		t.Fatal("generated signature did not verify")
	}
	canonical := "money=12.34&name=账单 I-42&notify_url=https://zjmf.example/api/v1/payments/epay_alipay/webhook&out_trade_no=" + q.Get("out_trade_no") + "&pid=10001&return_url=https://zjmf.example/console/invoices/42&type=alipaytest-secret"
	if q.Get("sign") != fmt.Sprintf("%x", md5.Sum([]byte(canonical))) {
		t.Fatal("legacy signing contract mismatch")
	}
	if strings.Contains(intent.CheckoutURL, g.Key) {
		t.Fatal("gateway key exposed")
	}
}

func TestEpayRejectsAmbiguousAndUnsafeInput(t *testing.T) {
	g := EpayGateway{EpayConfig: EpayConfig{APIURL: "https://pay.example", PID: "10001", Key: "test-secret", PublicOrigin: "https://zjmf.example", Types: []string{"alipay"}}, Type: "alipay"}
	for _, raw := range []string{"http://pay.example", "https://user:pass@pay.example", "https://pay.example?key=secret", "javascript:alert(1)"} {
		copy := g
		copy.APIURL = raw
		if copy.Validate() == nil {
			t.Fatal("unsafe gateway URL accepted", raw)
		}
	}
	if _, ok := For("epay_unknown", "", g.EpayConfig); ok {
		t.Fatal("unknown provider enabled")
	}
	if _, ok := For("epay_wxpay", "", g.EpayConfig); ok {
		t.Fatal("disabled provider enabled")
	}
	q := url.Values{"pid": {"10001"}, "type": {"alipay"}, "trade_no": {"T-1"}, "out_trade_no": {"epay_42_00000000000000000000000000000000"}, "money": {"12.34"}, "trade_status": {"TRADE_SUCCESS"}}
	q.Set("sign", g.sign(q))
	q.Add("money", "99.99")
	if g.VerifySignature([]byte(q.Encode()), "") {
		t.Fatal("duplicate callback parameter accepted")
	}
}

func TestEpayGatewayDecodesSignedCallback(t *testing.T) {
	g := EpayGateway{EpayConfig: EpayConfig{Key: "test-secret", PID: "10001"}, Type: "alipay"}
	values := url.Values{"pid": {"10001"}, "type": {"alipay"}, "trade_no": {"T-42"}, "out_trade_no": {"epay_42_00000000000000000000000000000000"}, "money": {"12.34"}, "trade_status": {"TRADE_SUCCESS"}}
	values.Set("sign", g.sign(values))
	event, err := g.DecodeWebhook([]byte(values.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	if event.InvoiceID != 42 || event.ProviderPaymentID != values.Get("out_trade_no") || event.AmountCents != 1234 || event.Status != "succeeded" {
		t.Fatalf("unexpected callback: %+v", event)
	}
	values.Set("money", "12.345")
	values.Set("sign", g.sign(values))
	if _, err := g.DecodeWebhook([]byte(values.Encode())); err == nil {
		t.Fatal("fractional cent callback accepted")
	}
}
