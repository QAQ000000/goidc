package notify

import (
	"crypto/ecdh"
	"encoding/base64"
	"net/url"
	"strings"
)

// ValidWebPushEndpoint admits the browser vendors supported by this compiled
// adapter. A subscription URL is an opaque capability, never an arbitrary
// webhook URL. The transport separately resolves and pins public IP addresses.
func ValidWebPushEndpoint(endpoint string) bool {
	if len(endpoint) == 0 || len(endpoint) > 4096 || strings.TrimSpace(endpoint) != endpoint {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Opaque != "" || u.User != nil || u.Fragment != "" || u.Host != u.Hostname() || u.Path == "" || u.Path == "/" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "fcm.googleapis.com" || host == "updates.push.services.mozilla.com" || host == "push.services.mozilla.com" || host == "web.push.apple.com" {
		return true
	}
	// Windows uses distinct vendor-owned WNS hosts. The boundary dot prevents
	// notify.windows.com.attacker.example and attacker-notify.windows.com.
	return strings.HasSuffix(host, ".notify.windows.com") && len(host) > len(".notify.windows.com")
}

func webPushKey(value string, size int) ([]byte, bool) {
	if len(value) != base64.RawURLEncoding.EncodedLen(size) {
		return nil, false
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(value)
	return raw, err == nil && len(raw) == size && base64.RawURLEncoding.EncodeToString(raw) == value
}

func ValidWebPushPublicKey(value string) bool {
	raw, ok := webPushKey(value, 65)
	if !ok {
		return false
	}
	_, err := ecdh.P256().NewPublicKey(raw)
	return err == nil
}

func ValidWebPushSubscription(subscription PushSubscription) bool {
	if !ValidWebPushEndpoint(subscription.Endpoint) || !ValidWebPushPublicKey(subscription.P256DH) {
		return false
	}
	_, ok := webPushKey(subscription.Auth, 16)
	return ok
}
