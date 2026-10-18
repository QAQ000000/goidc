package notification

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
)

func telegramFixtureConfig() module.Config {
	return module.Config{Values: map[string]any{"bot_username": "example_bot"}, Secrets: map[string]string{"bot_token": "123456:token_DO_NOT_LOG", "webhook_secret": "webhook_DO_NOT_LOG"}}
}

func telegramFixtureJob() notify.Job {
	return notify.Job{ID: 1, LeaseToken: "fixture-lease", Channel: "telegram", InstanceID: "bot-fixture", Target: "987654321", Body: "你好 <b>plain</b> 🙂", ExpiresAt: time.Now().Add(time.Minute)}
}

func TestTelegramCompiledRegistryAndLocalValidation(t *testing.T) {
	desc, found := channel.Lookup(channel.Bot, "telegram_bot")
	if !found || !channel.HasFactory(channel.Bot, "telegram_bot") || desc.SchemaVersion != 1 || desc.InstanceMode != "channel" || len(desc.Fields) != 3 || len(channel.SecretKeys(desc)) != 2 {
		t.Fatalf("invalid compiled Bot descriptor: %+v", desc)
	}
	if !channel.ValidCategory(channel.Bot) || channel.ValidCategory(channel.Category("remote_code")) {
		t.Fatal("invalid public category contract")
	}
	if _, err := NewBotProvider("telegram_bot", telegramFixtureConfig()); err != nil {
		t.Fatal(err)
	}
	for _, username := range []string{"@example_bot", "bot", "ab_bot ", "示例bot", "a" + strings.Repeat("b", 29) + "bot", "a-bot"} {
		cfg := telegramFixtureConfig()
		cfg.Values["bot_username"] = username
		if _, errs := channel.ValidateConfiguration(desc, cfg.Values, map[string]bool{"bot_token": true, "webhook_secret": true}, true); len(errs) == 0 {
			t.Fatalf("accepted invalid bot username %q", username)
		}
		if _, err := NewBotProvider("telegram_bot", cfg); err == nil {
			t.Fatal("factory ignored invalid username")
		}
	}
	for _, username := range []string{"abBOT", "a" + strings.Repeat("b", 28) + "bot", "example_bot"} {
		if !ValidTelegramBotUsername(username) {
			t.Fatalf("rejected valid username %q", username)
		}
	}
	for _, field := range []string{"bot_token", "webhook_secret"} {
		for _, value := range []string{"", "secret?DO_NOT_LOG", "secret\nDO_NOT_LOG", strings.Repeat("x", 257)} {
			cfg := telegramFixtureConfig()
			cfg.Secrets[field] = value
			if errs := channel.ValidateSecretValues(desc, map[string]string{field: value}); len(errs) != 1 || errs[0].Field != field || strings.Contains(errs[0].Code, "DO_NOT_LOG") {
				t.Fatalf("unsafe or missing validation: %+v", errs)
			}
			if _, err := NewBotProvider("telegram_bot", cfg); err == nil || strings.Contains(err.Error(), "DO_NOT_LOG") {
				t.Fatal("factory accepted invalid credential or exposed its value")
			}
		}
		cfg := telegramFixtureConfig()
		delete(cfg.Secrets, field)
		if _, err := NewBotProvider("telegram_bot", cfg); err == nil {
			t.Fatal("factory accepted missing required credential")
		}
	}
	if errs := channel.ValidateSecretValues(desc, nil); len(errs) != 0 {
		t.Fatal("partial credential update required unrelated secrets")
	}
	if !ValidTelegramWebhookSecret(strings.Repeat("A", 256)) {
		t.Fatal("valid maximum-length webhook secret rejected")
	}
}

func TestTelegramSendPlainTextAndStrictAcceptance(t *testing.T) {
	tests := []struct {
		name, body, outcome string
		status              int
		retry               time.Duration
	}{
		{"accepted", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321,"type":"private"}}}`, "accepted", 200, 0},
		{"missing_ok", `{"result":{"message_id":77,"chat":{"id":987654321,"type":"private"}}}`, "unknown", 200, 0},
		{"missing_message", `{"ok":true,"result":{"chat":{"id":987654321,"type":"private"}}}`, "unknown", 200, 0},
		{"zero_message", `{"ok":true,"result":{"message_id":0,"chat":{"id":987654321,"type":"private"}}}`, "unknown", 200, 0},
		{"group_chat", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321,"type":"group"}}}`, "unknown", 200, 0},
		{"missing_chat_type", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321}}}`, "unknown", 200, 0},
		{"wrong_chat", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654322,"type":"private"}}}`, "unknown", 200, 0},
		{"missing_chat", `{"ok":true,"result":{"message_id":77}}`, "unknown", 200, 0},
		{"fraction_message", `{"ok":true,"result":{"message_id":1.5,"chat":{"id":987654321,"type":"private"}}}`, "unknown", 200, 0},
		{"string_chat", `{"ok":true,"result":{"message_id":77,"chat":{"id":"987654321","type":"private"}}}`, "unknown", 200, 0},
		{"malformed", `{"ok":true`, "unknown", 200, 0},
		{"trailing_json", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321,"type":"private"}}}{}`, "unknown", 200, 0},
		{"large", strings.Repeat("x", telegramResponseLimit+1), "unknown", 200, 0},
		{"rejected_400", `{"ok":false,"error_code":400,"description":"token_DO_NOT_LOG"}`, "failed", 400, 0},
		{"rejected_403", `{"ok":false,"error_code":403}`, "failed", 403, 0},
		{"rejected_code", `{"ok":false,"error_code":400}`, "failed", 200, 0},
		{"rate_limited", `{"ok":false,"error_code":429,"parameters":{"retry_after":42}}`, "rate_limited", 429, 42 * time.Second},
		{"rate_limit_missing_delay", `{"ok":false,"error_code":429}`, "unknown", 429, 0},
		{"rate_limit_negative", `{"ok":false,"error_code":429,"parameters":{"retry_after":-1}}`, "unknown", 429, 0},
		{"rate_limit_overflow", `{"ok":false,"error_code":429,"parameters":{"retry_after":9223372036854775807}}`, "unknown", 429, 0},
		{"server_failure", `{"ok":true,"result":{"message_id":77,"chat":{"id":987654321,"type":"private"}}}`, "unknown", 503, 0},
		{"unclassified_rejection", `{"ok":false,"error_code":409}`, "unknown", 409, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/bot123456:token_DO_NOT_LOG/sendMessage" || r.Header.Get("Content-Type") != "application/json" {
					t.Error("unexpected Telegram request contract")
				}
				var payload map[string]json.RawMessage
				if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 2 || string(payload["chat_id"]) != "987654321" {
					t.Error("request included formatting/options or invalid chat identity")
				}
				var text string
				if json.Unmarshal(payload["text"], &text) != nil || text != telegramFixtureJob().Body {
					t.Error("plain text changed")
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			provider, err := newTelegramProvider(telegramFixtureConfig(), server.Client(), server.URL)
			if err != nil {
				t.Fatal(err)
			}
			result := provider.Send(context.Background(), telegramFixtureJob())
			if result.Outcome != tc.outcome || result.RetryAfter != tc.retry || strings.Contains(result.Code, "DO_NOT_LOG") {
				t.Fatalf("result %+v, want %s/%s", result, tc.outcome, tc.retry)
			}
			if (result.ProviderMessageID == "77") != (tc.outcome == "accepted") {
				t.Fatal("unverified provider message ID was retained")
			}
		})
	}
}

type telegramRoundTripper func(*http.Request) (*http.Response, error)

func (f telegramRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestTelegramDeadlineLengthRedirectAndTransportSafety(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Timeout: time.Hour, Transport: telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		deadline, ok := r.Context().Deadline()
		if !ok || time.Until(deadline) > 15*time.Second || r.URL.Host != "api.telegram.org" {
			t.Error("unbounded deadline or configurable endpoint")
		}
		return nil, errors.New("transport leaked /bot123456:token_DO_NOT_LOG/sendMessage")
	})}
	cfg := telegramFixtureConfig()
	cfg.Values["endpoint"] = "https://attacker.invalid"
	p, err := newTelegramProvider(cfg, client, "")
	if err != nil || p.client.Timeout > 15*time.Second {
		t.Fatal("invalid bounded client")
	}
	job := telegramFixtureJob()
	job.Body = strings.Repeat("🙂", 4096)
	if result := p.Send(context.Background(), job); result.Outcome != "unknown" || strings.Contains(result.Code, "DO_NOT_LOG") || calls.Load() != 1 {
		t.Fatal("4096 Unicode characters rejected or transport error exposed")
	}
	for _, modify := range []func(*notify.Job){
		func(j *notify.Job) { j.Body += "🙂" },
		func(j *notify.Job) { j.Body = "" },
		func(j *notify.Job) { j.Body = string([]byte{0xff}) },
		func(j *notify.Job) { j.Target = "@some_user" },
		func(j *notify.Job) { j.Target = "0" },
		func(j *notify.Job) { j.Target = "-100123456" },
		func(j *notify.Job) { j.ExpiresAt = time.Now().Add(-time.Second) },
	} {
		invalid := job
		modify(&invalid)
		if result := p.Send(context.Background(), invalid); result.Outcome != "failed" || calls.Load() != 1 {
			t.Fatal("invalid or expired message reached the network")
		}
	}
	var leaked atomic.Bool
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Store(true) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()
	p, _ = newTelegramProvider(telegramFixtureConfig(), redirect.Client(), redirect.URL)
	if result := p.Send(context.Background(), telegramFixtureJob()); result.Outcome != "unknown" || leaked.Load() {
		t.Fatal("redirect followed or classified as a safe retry")
	}
	client = &http.Client{Transport: telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
		deadline, _ := r.Context().Deadline()
		if time.Until(deadline) > time.Second {
			t.Error("job TTL did not bound request deadline")
		}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})}
	p, _ = newTelegramProvider(telegramFixtureConfig(), client, "")
	job = telegramFixtureJob()
	job.ExpiresAt = time.Now().Add(20 * time.Millisecond)
	if result := p.Send(context.Background(), job); result.Outcome != "unknown" {
		t.Fatal("uncertain timeout was retryable")
	}
}

func TestTelegramWebhookExplicitConfigurationContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bot123456:token_DO_NOT_LOG/setWebhook" {
			t.Error("wrong explicit webhook operation")
		}
		var payload struct {
			URL     string   `json:"url"`
			Secret  string   `json:"secret_token"`
			Updates []string `json:"allowed_updates"`
		}
		if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.URL != "https://goidc.example/api/telegram/webhook" || payload.Secret != "webhook_DO_NOT_LOG" || len(payload.Updates) != 1 || payload.Updates[0] != "message" {
			t.Error("webhook callback authentication/update contract changed")
		}
		_, _ = io.WriteString(w, `{"ok":true,"result":true}`)
	}))
	defer server.Close()
	provider, _ := newTelegramProvider(telegramFixtureConfig(), server.Client(), server.URL)
	if err := provider.configureWebhook(context.Background(), "https://goidc.example/api/telegram/webhook"); err != nil {
		t.Fatal(err)
	}
	for _, callback := range []string{"http://goidc.example/callback", "https://user:secret@goidc.example/callback", "https://goidc.example/callback#fragment", "bad"} {
		if err := provider.configureWebhook(context.Background(), callback); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid callback accepted or reflected")
		}
	}
	for _, raw := range []string{`{"ok":false,"description":"token_DO_NOT_LOG"}`, `{"ok":true,"result":false}`, `{"ok":true,"result":{}}`, `not json`} {
		provider.client.Transport = telegramRoundTripper(func(r *http.Request) (*http.Response, error) {
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(raw)), Header: make(http.Header)}, nil
		})
		if err := provider.configureWebhook(context.Background(), "https://goidc.example/callback"); err == nil || strings.Contains(err.Error(), "DO_NOT_LOG") {
			t.Fatal("webhook accepted unconfirmed result or exposed response")
		}
	}
}
