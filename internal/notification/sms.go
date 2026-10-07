package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"github.com/codex/zjmfgo/internal/store"
	"net/http"
	"sync"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
)

// SMSProvider implements an explicitly registered, compiled-in module protocol.
type SMSProvider interface {
	Send(context.Context, store.SMSRuntimeConfig, store.SMSJob) error
}

var smsRegistry = struct {
	sync.RWMutex
	providers map[string]SMSProvider
}{providers: make(map[string]SMSProvider)}

func RegisterSMSProvider(name string, p SMSProvider) error {
	smsRegistry.Lock()
	defer smsRegistry.Unlock()
	if name == "" || p == nil || smsRegistry.providers[name] != nil {
		return errors.New("invalid SMS provider registration")
	}
	smsRegistry.providers[name] = p
	store.RegisterSMSProviderName(name)
	return nil
}
func init() { _ = RegisterSMSProvider("generic_http_json", genericHTTPSMS{}) }

func init() {
	channel.MustRegister(channel.Descriptor{
		ID: "generic_http_json", Name: "通用 HTTP JSON 短信", Category: channel.SMS,
		Capabilities: []string{"send"},
		Fields: []channel.Field{
			{Key: "endpoint", Label: "HTTPS 接口", Type: channel.Text, Required: true},
			{Key: "token", Label: "访问令牌", Type: channel.Text, Required: true, Secret: true},
		},
	})
}
func SendSMS(ctx context.Context, c store.SMSRuntimeConfig, j store.SMSJob) error {
	smsRegistry.RLock()
	p := smsRegistry.providers[c.Provider]
	smsRegistry.RUnlock()
	if !c.Enabled || p == nil || !store.ValidSMSEndpoint(c.Endpoint) || c.Token == "" || !store.ValidSMSRecipient(j.Recipient) || !j.ExpiresAt.After(time.Now()) {
		return errors.New("SMS delivery failed")
	}
	if e := p.Send(ctx, c, j); e != nil {
		return errors.New("SMS delivery failed")
	}
	return nil
}

type genericHTTPSMS struct{}

func (genericHTTPSMS) Send(ctx context.Context, c store.SMSRuntimeConfig, j store.SMSJob) error {
	raw, e := json.Marshal(map[string]string{"recipient": j.Recipient, "message": j.Body, "kind": j.Kind})
	if e != nil {
		return e
	}
	deadline := time.Now().Add(15 * time.Second)
	if j.ExpiresAt.Before(deadline) {
		deadline = j.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint, bytes.NewReader(raw))
	if e != nil {
		return e
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := base.Clone()
		cloned.Proxy = nil
		transport = cloned
	} else {
		transport = &http.Transport{}
	}
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	resp, e := client.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("SMS delivery failed")
	}
	return nil
}
