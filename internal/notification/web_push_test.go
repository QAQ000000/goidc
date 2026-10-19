package notification

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
)

func webPushFixture(t *testing.T) (module.Config, notify.Job, *ecdh.PrivateKey) {
	t.Helper()
	vapid, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	receiver, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	if _, err = rand.Read(auth); err != nil {
		t.Fatal(err)
	}
	return module.Config{
			Values:  map[string]any{"public_key": base64.RawURLEncoding.EncodeToString(vapid.PublicKey().Bytes()), "subject": "mailto:operator@example.com"},
			Secrets: map[string]string{"private_key": base64.RawURLEncoding.EncodeToString(vapid.Bytes())},
		}, notify.Job{
			Channel: "web_push", InstanceID: "push-fixture", Target: "secret_marker_target",
			Body: "secret_marker_body", Subject: "secret_marker_subject", ExpiresAt: time.Now().Add(48 * time.Hour),
			PushSubscription: &notify.PushSubscription{
				Endpoint: "https://fcm.googleapis.com/fcm/send/secret_marker_endpoint",
				P256DH:   base64.RawURLEncoding.EncodeToString(receiver.PublicKey().Bytes()), Auth: base64.RawURLEncoding.EncodeToString(auth),
			},
		}, receiver
}

// Independent RFC 8291 receiver using crypto/ecdh and explicit HKDF steps.
// It intentionally does not call the sending dependency's crypto helpers.
func decryptWebPushFixture(t *testing.T, raw []byte, receiver *ecdh.PrivateKey, authEncoded string) []byte {
	t.Helper()
	if len(raw) < 86+16 || raw[20] != 65 || binary.BigEndian.Uint32(raw[16:20]) != 4096 {
		t.Fatal("invalid aes128gcm record header")
	}
	serverPublic, err := ecdh.P256().NewPublicKey(raw[21:86])
	if err != nil {
		t.Fatal(err)
	}
	shared, err := receiver.ECDH(serverPublic)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := base64.RawURLEncoding.DecodeString(authEncoded)
	if err != nil {
		t.Fatal(err)
	}
	extract := func(salt, input []byte) []byte {
		m := hmac.New(sha256.New, salt)
		_, _ = m.Write(input)
		return m.Sum(nil)
	}
	expand := func(prk, info []byte, count int) []byte {
		m := hmac.New(sha256.New, prk)
		_, _ = m.Write(info)
		_, _ = m.Write([]byte{1})
		return m.Sum(nil)[:count]
	}
	info := append([]byte("WebPush: info\x00"), receiver.PublicKey().Bytes()...)
	info = append(info, serverPublic.Bytes()...)
	keyingMaterial := expand(extract(auth, shared), info, 32)
	prk := extract(raw[:16], keyingMaterial)
	key := expand(prk, []byte("Content-Encoding: aes128gcm\x00"), 16)
	nonce := expand(prk, []byte("Content-Encoding: nonce\x00"), 12)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := aead.Open(nil, nonce, raw[86:], nil)
	if err != nil {
		t.Fatal("browser receiver cannot decrypt RFC 8291 payload")
	}
	plain = bytes.TrimRight(plain, "\x00")
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatal("incorrect final-record delimiter")
	}
	return plain[:len(plain)-1]
}

func verifyWebPushVAPID(t *testing.T, authorization, publicKey string) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(authorization, "vapid t="), ", k=")
	if !strings.HasPrefix(authorization, "vapid t=") || len(parts) != 2 || parts[1] != publicKey {
		t.Fatal("missing RFC 8292 authorization")
	}
	token := strings.Split(parts[0], ".")
	if len(token) != 3 {
		t.Fatal("malformed VAPID JWT")
	}
	decode := func(value string) []byte {
		data, err := base64.RawURLEncoding.DecodeString(value)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	var header struct {
		Alg string `json:"alg"`
	}
	var claims struct {
		Audience string `json:"aud"`
		Subject  string `json:"sub"`
		Expires  int64  `json:"exp"`
	}
	if json.Unmarshal(decode(token[0]), &header) != nil || header.Alg != "ES256" || json.Unmarshal(decode(token[1]), &claims) != nil {
		t.Fatal("incorrect VAPID algorithm or claims")
	}
	remaining := time.Until(time.Unix(claims.Expires, 0))
	if claims.Audience != "https://fcm.googleapis.com" || claims.Subject != "mailto:operator@example.com" || remaining <= 0 || remaining > 24*time.Hour {
		t.Fatal("incorrect VAPID contact, audience, or expiration")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), decode(publicKey))
	signature := decode(token[2])
	digest := sha256.Sum256([]byte(token[0] + "." + token[1]))
	if x == nil || len(signature) != 64 || !ecdsa.Verify(&ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])) {
		t.Fatal("invalid RFC 8292 VAPID signature")
	}
}

func TestWebPushProtocolEncryptsPrivatePayloadAndSignsVAPID(t *testing.T) {
	config, job, receiver := webPushFixture(t)
	var calls atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != "POST" || r.Host != "fcm.googleapis.com" || r.URL.Path != "/fcm/send/secret_marker_endpoint" || r.Header.Get("Content-Encoding") != "aes128gcm" || r.Header.Get("Content-Type") != "application/octet-stream" {
			t.Error("incorrect Web Push HTTP protocol")
		}
		if r.Header.Get("TTL") != "86400" {
			t.Error("Web Push TTL exceeded the 24-hour cap")
		}
		verifyWebPushVAPID(t, r.Header.Get("Authorization"), config.Values["public_key"].(string))
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8193))
		if err != nil || len(raw) > 8192 || bytes.Contains(raw, []byte("账户通知")) || bytes.Contains(raw, []byte("secret_marker_body")) {
			t.Fatal("unencrypted or malformed Web Push body")
		}
		plain := decryptWebPushFixture(t, raw, receiver, job.PushSubscription.Auth)
		if string(plain) != webPushPrivatePayload || bytes.Contains(plain, []byte("secret_marker")) {
			t.Fatal("lock-screen payload leaked business content or browser credentials")
		}
		w.Header().Set("Location", "https://fcm.googleapis.com/private-provider-id")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("secret_marker_response"))
	}))
	defer server.Close()
	fixtureClient := server.Client()
	transport := fixtureClient.Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	fixtureClient.Transport = transport
	provider, err := newWebPushProvider(config, fixtureClient)
	if err != nil {
		t.Fatal(err)
	}
	result := provider.Send(context.Background(), job)
	if result != (notify.Result{Outcome: "accepted", Code: "push_accepted"}) || calls.Load() != 1 {
		t.Fatalf("push acknowledgement lost: result=%+v calls=%d", result, calls.Load())
	}
}

type webPushRoundTrip func(*http.Request) (*http.Response, error)

func TestWebPushPortalRoutesStayPrivate(t *testing.T) {
	for _, portal := range []string{"customer", "admin", "", "https://untrusted.example/"} {
		t.Run(portal, func(t *testing.T) {
			config, job, receiver := webPushFixture(t)
			job.AccountPortal = portal
			calls := 0
			provider, err := newWebPushProvider(config, &http.Client{Transport: webPushRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatal(err)
				}
				plain := decryptWebPushFixture(t, raw, receiver, job.PushSubscription.Auth)
				want := webPushPrivatePayload
				if portal == "admin" {
					want = webPushAdminPayload
				}
				if string(plain) != want || bytes.Contains(plain, []byte("secret_marker")) {
					t.Fatal("push portal changed generic content or admitted an arbitrary destination")
				}
				return &http.Response{StatusCode: http.StatusCreated, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			if result := provider.Send(context.Background(), job); result.Outcome != "accepted" || calls != 1 {
				t.Fatalf("push result=%+v calls=%d", result, calls)
			}
		})
	}
}

func (f webPushRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestWebPushProviderPreservesDeliveryKnowledgeAndBoundedRetry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		status        int
		retry         string
		outcome, code string
	}{
		{"accepted", 201, "", "accepted", "push_accepted"},
		{"gone", 410, "", "failed", "push_subscription_gone"},
		{"not_found", 404, "", "failed", "push_subscription_gone"},
		{"rejected", 403, "", "failed", "push_rejected"},
		{"rate_limit", 429, "42", "rate_limited", "push_rate_limited"},
		{"rate_limit_zero", 429, "0", "unknown", "push_rate_limit_unknown"},
		{"rate_limit_overflow", 429, "9999999999999999999999999", "unknown", "push_rate_limit_unknown"},
		{"rate_limit_excessive", 429, "86401", "unknown", "push_rate_limit_unknown"},
		{"server_failure", 503, "42", "unknown", "push_response_unknown"},
		{"timeout_response", 408, "", "unknown", "push_response_unknown"},
		{"lost_response", 0, "", "unknown", "push_transport_unknown"},
		{"redirect", 307, "", "failed", "push_rejected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, job, _ := webPushFixture(t)
			calls := 0
			client := &http.Client{Transport: webPushRoundTrip(func(r *http.Request) (*http.Response, error) {
				calls++
				if tc.status == 0 {
					return nil, errors.New("secret_marker_endpoint_and_body")
				}
				return &http.Response{StatusCode: tc.status, Header: http.Header{"Retry-After": []string{tc.retry}, "Location": []string{"https://fcm.googleapis.com/redirect-trap"}}, Body: io.NopCloser(strings.NewReader("secret_marker_response")), Request: r}, nil
			})}
			provider, err := newWebPushProvider(config, client)
			if err != nil {
				t.Fatal(err)
			}
			result := provider.Send(context.Background(), job)
			if result.Outcome != tc.outcome || result.Code != tc.code || result.ProviderMessageID != "" || calls != 1 || strings.Contains(fmt.Sprint(result), "secret_marker") {
				t.Fatalf("unexpected delivery knowledge: %+v calls=%d", result, calls)
			}
			if tc.outcome == "rate_limited" && result.RetryAfter != 42*time.Second {
				t.Fatal("valid bounded retry hint lost")
			}
		})
	}
	date := time.Now().UTC().Truncate(time.Second)
	if webPushRetryAfter(date.Add(time.Minute).Format(http.TimeFormat), date) != time.Minute || webPushRetryAfter(date.Add(-time.Second).Format(http.TimeFormat), date) != 0 {
		t.Fatal("HTTP-date retry hint incorrectly bounded")
	}
}

func TestWebPushProviderRejectsExpiredOrInvalidSubscriptionsBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*notify.Job)
		code   string
	}{
		{"expired", func(j *notify.Job) { j.ExpiresAt = time.Now().Add(-time.Second) }, "notification_expired"},
		{"nil", func(j *notify.Job) { j.PushSubscription = nil }, "push_invalid_subscription"},
		{"untrusted_host", func(j *notify.Job) { j.PushSubscription.Endpoint = "https://attacker.example/secret" }, "push_invalid_subscription"},
		{"bad_keys", func(j *notify.Job) { j.PushSubscription.Auth = "invalid" }, "push_invalid_subscription"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, job, _ := webPushFixture(t)
			calls := 0
			provider, err := newWebPushProvider(config, &http.Client{Transport: webPushRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("must not send")
			})})
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&job)
			result := provider.Send(context.Background(), job)
			if result.Outcome != "failed" || result.Code != tc.code || calls != 0 {
				t.Fatalf("ineligible subscription sent: %+v calls=%d", result, calls)
			}
		})
	}
	config, job, _ := webPushFixture(t)
	job.ExpiresAt = time.Now().Add(20 * time.Second)
	var ttl int
	provider, _ := newWebPushProvider(config, &http.Client{Transport: webPushRoundTrip(func(r *http.Request) (*http.Response, error) {
		ttl, _ = strconv.Atoi(r.Header.Get("TTL"))
		deadline, ok := r.Context().Deadline()
		if !ok || deadline.After(job.ExpiresAt) || time.Until(deadline) > 15*time.Second {
			t.Error("network call outlives bounded job deadline")
		}
		return &http.Response{StatusCode: 201, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
	})})
	provider.Send(context.Background(), job)
	if ttl < 18 || ttl > 20 {
		t.Fatalf("TTL did not respect job lifetime: %d", ttl)
	}
}

func TestWebPushRegistryAndConfigurationKeepCredentialsLocal(t *testing.T) {
	config, _, _ := webPushFixture(t)
	desc, ok := channel.Lookup(channel.Push, "web_push")
	if !ok || !channel.HasFactory(channel.Push, "web_push") || !channel.ValidCategory(channel.Push) {
		t.Fatal("compiled Web Push factory missing")
	}
	if _, err := NewPushProvider("web_push", config); err != nil {
		t.Fatal("matching VAPID keys rejected")
	}
	other, _, _ := webPushFixture(t)
	config.Secrets["private_key"] = other.Secrets["private_key"]
	if _, err := NewPushProvider("web_push", config); err == nil || strings.Contains(err.Error(), config.Secrets["private_key"]) {
		t.Fatal("mismatched VAPID keys accepted or exposed")
	}
	for _, subject := range []string{"operator@example.com", "mailto:operator@example.com?body=secret", "http://example.com", "https://user:password@example.com", "mailto:Name <operator@example.com>", "mailto:operator@example.com\r\n"} {
		if validWebPushSubject(subject) {
			t.Fatal("malformed VAPID contact accepted")
		}
	}
	config.Values["subject"] = "bad"
	config.Values["public_key"] = "bad"
	if _, problems := channel.ValidateConfiguration(desc, config.Values, map[string]bool{"private_key": true}, true); len(problems) != 2 {
		t.Fatal("public VAPID key and contact validation did not reject malformed configuration")
	}
	validation := channel.ValidateSecretValues(desc, map[string]string{"private_key": "secret_marker"})
	if len(validation) != 1 || validation[0].Code != "invalid_vapid_private_key" || strings.Contains(fmt.Sprint(validation), "secret_marker") {
		t.Fatal("invalid secret was not checked locally")
	}
	if problems := channel.ValidateSecretValues(desc, nil); len(problems) != 0 {
		t.Fatal("partial configuration update required unrelated credentials")
	}
	provider, _ := newWebPushProvider(other, nil)
	transport := provider.client.Transport.(*http.Transport)
	if transport.Proxy != nil || transport.DialContext == nil || provider.client.Timeout != 15*time.Second || provider.client.CheckRedirect == nil {
		t.Fatal("production push transport lacks network boundary")
	}
}

func TestWebPushTransportRejectsPrivateDNSAndPinsPublicDestinations(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.168.0.1", "192.0.2.1", "198.18.0.1", "198.51.100.1", "203.0.113.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "64:ff9b::a00:1", "2002:7f00:1::", "2001:db8::1"} {
		if webPushPublicAddress(netip.MustParseAddr(address)) {
			t.Fatalf("non-public IP accepted: %s", address)
		}
	}
	for _, address := range []string{"142.250.1.1", "2001:4860:4860::8888", "2606:4700:4700::1111"} {
		if !webPushPublicAddress(netip.MustParseAddr(address)) {
			t.Fatalf("public push IP rejected: %s", address)
		}
	}
	for _, addresses := range [][]netip.Addr{
		{netip.MustParseAddr("127.0.0.1")},
		{netip.MustParseAddr("142.250.1.1"), netip.MustParseAddr("10.0.0.1")},
	} {
		calls := 0
		_, err := webPushDial(context.Background(), "tcp", "fcm.googleapis.com:443", func(context.Context, string, string) ([]netip.Addr, error) { return addresses, nil }, func(context.Context, string, string) (net.Conn, error) {
			calls++
			return nil, errors.New("must not dial")
		})
		if err == nil || calls != 0 {
			t.Fatal("private or mixed public/private DNS answer reached network")
		}
	}
	var dialed string
	_, _ = webPushDial(context.Background(), "tcp", "fcm.googleapis.com:443", func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("142.250.1.1")}, nil
	}, func(_ context.Context, _, address string) (net.Conn, error) {
		dialed = address
		return nil, errors.New("fixture")
	})
	if dialed != "142.250.1.1:443" {
		t.Fatal("transport re-resolved hostname after validation")
	}
}

type pushCenterFixtureStore struct{ *centerFixtureStore }

func (s *pushCenterFixtureStore) RuntimeChannelInstance(_ context.Context, id string, category channel.Category) (module.Config, string, error) {
	s.resolutions++
	if id != "push-fixture" || category != channel.Push {
		return module.Config{}, "", errors.New("wrong push category")
	}
	return module.Config{}, s.implementation, s.resolveErr
}

func TestWebPushCenterDispatchRechecksEligibilityAndDoesNotReplayUnknown(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(strconv.FormatBool(revoke), func(t *testing.T) {
			_, job, _ := webPushFixture(t)
			provider := &centerFixtureProvider{result: notify.Result{Outcome: "unknown", Code: "push_transport_unknown"}}
			id := fmt.Sprintf("push_fixture_%d", centerFactorySequence.Add(1))
			if err := RegisterPushProvider(channel.Descriptor{ID: id, Name: "Push fixture", Category: channel.Push}, func(module.Config) (PushProvider, error) { return provider, nil }); err != nil {
				t.Fatal(err)
			}
			db := &pushCenterFixtureStore{&centerFixtureStore{jobs: []*notify.Job{&job}, implementation: id}}
			if revoke {
				db.active = []bool{true, false}
			}
			count, err := DrainNotificationCenter(context.Background(), db, 10)
			if err != nil || count != 1 || len(db.completed) != 1 || db.checks != 2 || db.resolutions != 1 {
				t.Fatal("push worker dispatch bypassed category or lease check")
			}
			if revoke && (provider.calls != 0 || db.completed[0].Code != "eligibility_changed") || !revoke && (provider.calls != 1 || db.completed[0].Outcome != "unknown") {
				t.Fatal("push worker ignored revocation or unknown knowledge")
			}
			if _, err = DrainNotificationCenter(context.Background(), db, 10); err != nil || !revoke && provider.calls != 1 {
				t.Fatal("unknown Web Push delivery retried")
			}
		})
	}
}
