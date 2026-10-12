package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/store"
)

// SMSProvider implements an explicitly registered, compiled-in module protocol.
type SMSProvider interface {
	Send(context.Context, store.SMSRuntimeConfig, store.SMSJob) error
}

var smsRegistry = module.NewRegistry[SMSProvider](channel.SMS)

func RegisterSMSProvider(name string, p SMSProvider) error {
	return RegisterSMSProviderDescriptor(channel.Descriptor{ID: name, Name: name, Category: channel.SMS, Capabilities: []string{"send"}}, p)
}

func RegisterSMSProviderDescriptor(desc channel.Descriptor, p SMSProvider) error {
	if desc.ID == "" || p == nil || desc.Category != channel.SMS {
		return errors.New("invalid SMS provider registration")
	}
	if err := smsRegistry.Register(desc, func(module.Config) (SMSProvider, error) { return p, nil }); err != nil {
		return err
	}
	return nil
}

const idcsmartDefaultEndpoint = "http://api1.idcsmart.com/smsapi.php?action=send"

func init() {
	if err := RegisterSMSProviderDescriptor(channel.Descriptor{
		ID: "generic_http_json", Name: "通用 HTTP JSON 短信", Category: channel.SMS,
		Capabilities: []string{"send"},
		Fields: []channel.Field{
			{Key: "endpoint", Label: "HTTPS 接口", Type: channel.Text, Required: true},
			{Key: "token", Label: "访问令牌", Type: channel.Text, Required: true, Secret: true},
		},
	}, genericHTTPSMS{}); err != nil {
		panic(err)
	}
	if err := RegisterSMSProviderDescriptor(channel.Descriptor{
		ID: "idcsmart", Name: "智简魔方", Category: channel.SMS,
		Capabilities: []string{"send"},
		Fields: []channel.Field{
			{Key: "api", Label: "应用 ID", Type: channel.Text, Required: true},
			{Key: "sign", Label: "短信 SignName", Type: channel.Text, Required: true},
			{Key: "key", Label: "应用秘钥", Type: channel.Text, Required: true, Secret: true},
		},
	}, idcsmartSMS{endpoint: idcsmartDefaultEndpoint}); err != nil {
		panic(err)
	}
}
func SendSMS(ctx context.Context, c store.SMSRuntimeConfig, j store.SMSJob) error {
	validConfig := c.Provider == "idcsmart" && c.API != "" && c.Sign != "" && c.Key != ""
	if c.Provider != "idcsmart" {
		validConfig = store.ValidSMSEndpoint(c.Endpoint) && c.Token != ""
	}
	p, resolveErr := smsRegistry.New(c.Provider, module.Config{Payload: c})
	if !c.Enabled || resolveErr != nil || p == nil || !validConfig || !store.ValidSMSRecipient(j.Recipient) || !j.ExpiresAt.After(time.Now()) {
		return errors.New("SMS delivery failed")
	}
	if e := p.Send(ctx, c, j); e != nil {
		return errors.New("SMS delivery failed")
	}
	return nil
}

type genericHTTPSMS struct{}

// idcsmartSMS implements the compiled-in 智简魔方 official SMS module. The
// default instance has a fixed upstream URL; the endpoint field is only used
// by package tests to point at an httptest server.
type idcsmartSMS struct {
	endpoint string
}

func (p idcsmartSMS) Send(ctx context.Context, c store.SMSRuntimeConfig, j store.SMSJob) error {
	if c.API == "" || c.Key == "" || c.Sign == "" {
		return errors.New("invalid idcsmart configuration")
	}
	endpoint := p.endpoint
	if endpoint == "" {
		endpoint = idcsmartDefaultEndpoint
	}
	if p.endpoint == idcsmartDefaultEndpoint {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "http" || u.Host != "api1.idcsmart.com" || u.Path != "/smsapi.php" || u.Query().Get("action") != "send" {
			return errors.New("invalid idcsmart endpoint")
		}
	}

	form := url.Values{}
	form.Set("to", j.Recipient)
	form.Set("content", idcsmartSign(c.Sign)+j.Body)
	form.Set("template_id", j.TemplateID)
	form.Set("vars", "")
	deadline := time.Now().Add(15 * time.Second)
	if j.ExpiresAt.Before(deadline) {
		deadline = j.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("api", c.API)
	req.Header.Set("key", c.Key)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	transport := http.DefaultTransport
	if base, ok := http.DefaultTransport.(*http.Transport); ok {
		cloned := base.Clone()
		cloned.Proxy = nil
		transport = cloned
	} else {
		transport = &http.Transport{}
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.New("idcsmart request failed")
	}
	var result struct {
		Status json.RawMessage `json:"status"`
	}
	limited := io.LimitReader(resp.Body, 64<<10)
	if err = json.NewDecoder(limited).Decode(&result); err != nil || !idcsmartStatusOK(result.Status) {
		return errors.New("idcsmart request failed")
	}
	return nil
}

func idcsmartStatusOK(raw json.RawMessage) bool {
	status := strings.TrimSpace(string(raw))
	status = strings.Trim(status, `"`)
	value, err := strconv.Atoi(status)
	return err == nil && value == 200
}

func idcsmartSign(sign string) string {
	sign = strings.TrimSpace(sign)
	sign = strings.ReplaceAll(sign, "【", "")
	sign = strings.ReplaceAll(sign, "】", "")
	if sign == "" {
		return ""
	}
	return "【" + sign + "】"
}

var _ SMSProvider = idcsmartSMS{}

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
