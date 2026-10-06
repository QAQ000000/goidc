package payment

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type EpayConfig struct {
	APIURL, PID, Key, PublicOrigin string
	Types                          []string
}
type EpayGateway struct {
	EpayConfig
	Type string
}

func (g EpayGateway) Name() string { return "epay_" + g.Type }

// HTTP is accepted only for isolated loopback test gateways.
func validGatewayURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == "" &&
		(u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1")))
}

func ValidEpayType(value string) bool {
	return value == "alipay" || value == "wxpay" || value == "qqpay" || value == "bank"
}

func (g EpayGateway) Validate() error {
	if !validGatewayURL(g.APIURL) || !validGatewayURL(g.PublicOrigin) || !regexp.MustCompile(`^[0-9]{1,20}$`).MatchString(g.PID) || len(g.Key) < 8 || !ValidEpayType(g.Type) {
		return errors.New("invalid epay configuration")
	}
	for _, value := range g.Types {
		if value == g.Type {
			return nil
		}
	}
	return errors.New("epay payment type disabled")
}

func (g EpayGateway) sign(values url.Values) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		if key != "sign" && key != "sign_type" && values.Get(key) != "" {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+values.Get(key))
	}
	// MD5 is the legacy gateway protocol, not a password hashing algorithm.
	sum := md5.Sum([]byte(strings.Join(parts, "&") + g.Key))
	return hex.EncodeToString(sum[:])
}

func (g EpayGateway) CreateIntent(req IntentRequest) (Intent, error) {
	if err := g.Validate(); err != nil {
		return Intent{}, err
	}
	if req.InvoiceID < 1 || req.InvoiceNo == "" || req.AmountCents < 1 || req.AmountCents > 10000000000 {
		return Intent{}, errors.New("invalid epay invoice")
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return Intent{}, err
	}
	id := fmt.Sprintf("epay_%d_%s", req.InvoiceID, hex.EncodeToString(raw))
	origin := strings.TrimRight(g.PublicOrigin, "/")
	values := url.Values{"pid": {g.PID}, "type": {g.Type}, "notify_url": {origin + "/api/v1/payments/" + g.Name() + "/webhook"}, "return_url": {fmt.Sprintf("%s/console/invoices/%d", origin, req.InvoiceID)}, "out_trade_no": {id}, "name": {"账单 " + req.InvoiceNo}, "money": {fmt.Sprintf("%d.%02d", req.AmountCents/100, req.AmountCents%100)}}
	values.Set("sign", g.sign(values))
	values.Set("sign_type", "MD5")
	return Intent{ProviderPaymentID: id, CheckoutURL: strings.TrimRight(g.APIURL, "/") + "/submit.php?" + values.Encode()}, nil
}

func epayValues(body []byte) (url.Values, error) {
	if len(body) == 0 || len(body) > 64<<10 {
		return nil, errors.New("invalid epay payload")
	}
	values, err := url.ParseQuery(string(body))
	if err != nil {
		return nil, err
	}
	for key, items := range values {
		if len(items) != 1 || len(key) > 100 || len(items[0]) > 2000 {
			return nil, errors.New("duplicate or oversized epay parameter")
		}
	}
	return values, nil
}

func (g EpayGateway) VerifySignature(body []byte, _ string) bool {
	values, err := epayValues(body)
	if err != nil || g.Key == "" || values.Get("pid") != g.PID || values.Get("type") != g.Type || (values.Get("sign_type") != "" && values.Get("sign_type") != "MD5") {
		return false
	}
	return hmac.Equal([]byte(strings.ToLower(values.Get("sign"))), []byte(g.sign(values)))
}

var epayAmountPattern = regexp.MustCompile(`^(0|[1-9][0-9]{0,8})(?:\.([0-9]{1,2}))?$`)
var epayReferencePattern = regexp.MustCompile(`^epay_([1-9][0-9]{0,18})_[0-9a-f]{32}$`)
var epayTradePattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,120}$`)

func epayAmountCents(value string) (int64, error) {
	amount := epayAmountPattern.FindStringSubmatch(value)
	if len(amount) == 0 {
		return 0, errors.New("invalid epay amount")
	}
	whole, _ := strconv.ParseInt(amount[1], 10, 64)
	fraction, _ := strconv.ParseInt((amount[2] + "00")[:2], 10, 64)
	cents := whole*100 + fraction
	if cents < 1 || cents > 10000000000 {
		return 0, errors.New("invalid epay amount")
	}
	return cents, nil
}

func (g EpayGateway) DecodeWebhook(body []byte) (Webhook, error) {
	if !g.VerifySignature(body, "") {
		return Webhook{}, errors.New("invalid epay signature")
	}
	values, _ := epayValues(body)
	cents, amountErr := epayAmountCents(values.Get("money"))
	reference := epayReferencePattern.FindStringSubmatch(values.Get("out_trade_no"))
	if amountErr != nil || len(reference) == 0 || !epayTradePattern.MatchString(values.Get("trade_no")) || values.Get("trade_status") != "TRADE_SUCCESS" {
		return Webhook{}, errors.New("invalid epay callback fields")
	}
	invoiceID, err := strconv.ParseInt(reference[1], 10, 64)
	if err != nil || invoiceID < 1 || cents < 1 || cents > 10000000000 {
		return Webhook{}, errors.New("invalid epay amount or invoice")
	}
	return Webhook{EventID: g.PID + ":" + values.Get("trade_no"), ProviderPaymentID: values.Get("out_trade_no"), InvoiceID: invoiceID, AmountCents: cents, Status: "succeeded"}, nil
}
