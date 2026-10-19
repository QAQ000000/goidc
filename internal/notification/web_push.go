package notification

import (
	"context"
	"crypto/ecdh"
	"crypto/subtle"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"net"
	"net/http"
	"net/mail"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
)

// PushProvider is the typed compiled push transport boundary. Acceptance only
// means the browser vendor accepted the encrypted message for delivery.
type PushProvider interface {
	Send(context.Context, notify.Job) notify.Result
}

var pushRegistry = module.NewRegistry[PushProvider](channel.Push)

func RegisterPushProvider(desc channel.Descriptor, factory module.Factory[PushProvider]) error {
	return pushRegistry.Register(desc, factory)
}

func NewPushProvider(id string, config module.Config) (PushProvider, error) {
	provider, err := pushRegistry.New(id, config)
	if err != nil || provider == nil {
		return nil, errors.New("push configuration unavailable")
	}
	return provider, nil
}

func ValidWebPushPublicKey(value string) bool { return notify.ValidWebPushPublicKey(value) }
func ValidWebPushSubscription(sub notify.PushSubscription) bool {
	return notify.ValidWebPushSubscription(sub)
}

func webPushPrivateKey(value string) (*ecdh.PrivateKey, bool) {
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(raw) != 32 || base64.RawURLEncoding.EncodeToString(raw) != value {
		return nil, false
	}
	key, err := ecdh.P256().NewPrivateKey(raw)
	return key, err == nil
}

func validWebPushSubject(value string) bool {
	if len(value) > 2048 || strings.TrimSpace(value) != value {
		return false
	}
	if strings.HasPrefix(value, "mailto:") {
		address := strings.TrimPrefix(value, "mailto:")
		parsed, err := mail.ParseAddress(address)
		return err == nil && parsed.Address == address && parsed.Name == "" && !strings.ContainsAny(address, "?\r\n")
	}
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == "" && u.Opaque == ""
}

func init() {
	pushRegistry.MustRegister(channel.Descriptor{
		ID: "web_push", Name: "浏览器 Web Push", Category: channel.Push,
		Capabilities: []string{"send"},
		Fields: []channel.Field{
			{Key: "public_key", Label: "VAPID 公钥（Base64URL）", Type: channel.Text, Required: true},
			{Key: "private_key", Label: "VAPID 私钥（Base64URL）", Type: channel.Text, Required: true, Secret: true},
			{Key: "subject", Label: "联系地址（https:// 或 mailto:）", Type: channel.Text, Required: true},
		},
	}, func(config module.Config) (PushProvider, error) { return newWebPushProvider(config, nil) })
	channel.MustRegisterConfigValidator(channel.Push, "web_push", func(values map[string]any, _ map[string]bool) []channel.ValidationError {
		var out []channel.ValidationError
		publicKey, _ := values["public_key"].(string)
		if !notify.ValidWebPushPublicKey(publicKey) {
			out = append(out, channel.ValidationError{Field: "public_key", Code: "invalid_vapid_public_key"})
		}
		subject, _ := values["subject"].(string)
		if !validWebPushSubject(subject) {
			out = append(out, channel.ValidationError{Field: "subject", Code: "invalid_vapid_subject"})
		}
		return out
	})
	channel.MustRegisterSecretValidator(channel.Push, "web_push", func(values map[string]string) []channel.ValidationError {
		if value, exists := values["private_key"]; exists {
			if _, ok := webPushPrivateKey(value); !ok {
				return []channel.ValidationError{{Field: "private_key", Code: "invalid_vapid_private_key"}}
			}
		}
		return nil
	})
}

type webPushProvider struct {
	publicKey, privateKey, subject string
	client                         *http.Client
}

func newWebPushProvider(config module.Config, fixtureClient *http.Client) (*webPushProvider, error) {
	publicKey, _ := config.Values["public_key"].(string)
	subject, _ := config.Values["subject"].(string)
	privateKey := config.Secrets["private_key"]
	private, ok := webPushPrivateKey(privateKey)
	if !ok || !notify.ValidWebPushPublicKey(publicKey) || !validWebPushSubject(subject) {
		return nil, errors.New("push configuration invalid")
	}
	public, _ := base64.RawURLEncoding.DecodeString(publicKey)
	if subtle.ConstantTimeCompare(public, private.PublicKey().Bytes()) != 1 {
		return nil, errors.New("push configuration invalid")
	}
	// webpush-go prefixes mailto: itself for non-HTTPS contact addresses.
	subject = strings.TrimPrefix(subject, "mailto:")
	client := &http.Client{Transport: &http.Transport{
		Proxy: nil, DialContext: webPushDialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 10 * time.Second,
		IdleConnTimeout: 30 * time.Second, MaxIdleConns: 8, MaxIdleConnsPerHost: 2,
	}}
	// Only in-package protocol fixtures may substitute transport. No persisted
	// configuration or environment variable can select an arbitrary endpoint.
	if fixtureClient != nil {
		copyClient := *fixtureClient
		client = &copyClient
	}
	client.Timeout = 15 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &webPushProvider{publicKey: publicKey, privateKey: privateKey, subject: subject, client: client}, nil
}

// Every DNS answer must be public. Connections use the checked literal IP,
// retaining the original request host for TLS certificate verification.
func webPushDialContext(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return webPushDial(ctx, network, address, net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func webPushDial(ctx context.Context, network, address string,
	resolve func(context.Context, string, string) ([]netip.Addr, error),
	dial func(context.Context, string, string) (net.Conn, error),
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port != "443" || !notify.ValidWebPushEndpoint("https://"+host+"/endpoint") {
		return nil, errors.New("push destination rejected")
	}
	addresses, err := resolve(ctx, "ip", host)
	if err != nil || len(addresses) == 0 {
		return nil, errors.New("push destination unavailable")
	}
	for _, address := range addresses {
		if !webPushPublicAddress(address) {
			return nil, errors.New("push destination rejected")
		}
	}
	for _, address := range addresses {
		if connection, err := dial(ctx, network, net.JoinHostPort(address.String(), port)); err == nil {
			return connection, nil
		}
	}
	return nil, errors.New("push destination unavailable")
}

var webPushDeniedNetworks = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/96"), netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"), netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"),
}

func webPushPublicAddress(address netip.Addr) bool {
	address = address.Unmap()
	if !address.IsValid() || !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, network := range webPushDeniedNetworks {
		if network.Contains(address) {
			return false
		}
	}
	return true
}

const webPushPrivatePayload = `{"title":"账户通知","body":"您有新的账户通知，请登录查看。","url":"/console/account"}`
const webPushAdminPayload = `{"title":"账户通知","body":"您有新的账户通知，请登录查看。","url":"/admin/account"}`

func (p *webPushProvider) Send(ctx context.Context, job notify.Job) notify.Result {
	if job.PushSubscription == nil || !notify.ValidWebPushSubscription(*job.PushSubscription) {
		return notify.Result{Outcome: "failed", Code: "push_invalid_subscription"}
	}
	now := time.Now()
	ttl := int(job.ExpiresAt.Sub(now) / time.Second)
	if ttl < 1 {
		return notify.Result{Outcome: "failed", Code: "notification_expired"}
	}
	if ttl > 24*60*60 {
		ttl = 24 * 60 * 60
	}
	deadline := now.Add(15 * time.Second)
	if job.ExpiresAt.Before(deadline) {
		deadline = job.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	sub := job.PushSubscription
	payload := webPushPrivatePayload
	if job.AccountPortal == "admin" {
		payload = webPushAdminPayload
	}
	response, err := webpush.SendNotificationWithContext(ctx, []byte(payload), &webpush.Subscription{
		Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256DH, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient: p.client, Subscriber: p.subject, TTL: ttl,
		VAPIDPublicKey: p.publicKey, VAPIDPrivateKey: p.privateKey,
	})
	if err != nil || response == nil {
		return notify.Result{Outcome: "unknown", Code: "push_transport_unknown"}
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return notify.Result{Outcome: "accepted", Code: "push_accepted"}
	case response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone:
		return notify.Result{Outcome: "failed", Code: "push_subscription_gone"}
	case response.StatusCode == http.StatusTooManyRequests:
		if retry := webPushRetryAfter(response.Header.Get("Retry-After"), now); retry > 0 {
			return notify.Result{Outcome: "rate_limited", Code: "push_rate_limited", RetryAfter: retry}
		}
		return notify.Result{Outcome: "unknown", Code: "push_rate_limit_unknown"}
	case response.StatusCode >= 500 || response.StatusCode == http.StatusRequestTimeout:
		return notify.Result{Outcome: "unknown", Code: "push_response_unknown"}
	default:
		return notify.Result{Outcome: "failed", Code: "push_rejected"}
	}
}

func webPushRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.ParseInt(value, 10, 32); err == nil && seconds > 0 && seconds <= 24*60*60 {
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := date.Sub(now)
		if delay > 0 && delay <= 24*time.Hour {
			return delay
		}
	}
	return 0
}
