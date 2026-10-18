package notification

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/module"
	"github.com/codex/zjmfgo/internal/notify"
)

// BotProvider returns delivery knowledge rather than a retryable Go error.
// In particular, an unknown result must not trigger another external write.
type BotProvider interface {
	Send(context.Context, notify.Job) notify.Result
}

var botRegistry = module.NewRegistry[BotProvider](channel.Bot)

func RegisterBotProvider(desc channel.Descriptor, factory module.Factory[BotProvider]) error {
	return botRegistry.Register(desc, factory)
}

func NewBotProvider(id string, config module.Config) (BotProvider, error) {
	provider, err := botRegistry.New(id, config)
	if err != nil || provider == nil {
		// Factory errors can contain secrets. Only fixed errors cross this boundary.
		return nil, errors.New("bot configuration unavailable")
	}
	return provider, nil
}

const telegramAPIOrigin = "https://api.telegram.org"
const telegramResponseLimit = 64 << 10

var telegramUsername = regexp.MustCompile(`(?i)^[a-z][a-z0-9_]{1,28}bot$`)
var telegramToken = regexp.MustCompile(`^[0-9]{1,32}:[A-Za-z0-9_-]{1,256}$`)
var telegramWebhookSecret = regexp.MustCompile(`^[A-Za-z0-9_-]{1,256}$`)

func ValidTelegramBotUsername(value string) bool   { return telegramUsername.MatchString(value) }
func ValidTelegramWebhookSecret(value string) bool { return telegramWebhookSecret.MatchString(value) }

func init() {
	botRegistry.MustRegister(channel.Descriptor{
		ID: "telegram_bot", Name: "Telegram Bot", Category: channel.Bot,
		Capabilities: []string{"send", "webhook"},
		Fields: []channel.Field{
			{Key: "bot_username", Label: "Bot 用户名（不含 @）", Type: channel.Text, Required: true},
			{Key: "bot_token", Label: "Bot Token", Type: channel.Text, Required: true, Secret: true},
			{Key: "webhook_secret", Label: "Webhook Secret", Type: channel.Text, Required: true, Secret: true},
		},
	}, func(config module.Config) (BotProvider, error) { return newTelegramProvider(config, nil, "") })
	channel.MustRegisterConfigValidator(channel.Bot, "telegram_bot", func(values map[string]any, _ map[string]bool) []channel.ValidationError {
		username, ok := values["bot_username"].(string)
		if !ok || !ValidTelegramBotUsername(username) {
			return []channel.ValidationError{{Field: "bot_username", Code: "invalid_bot_username"}}
		}
		return nil
	})
	channel.MustRegisterSecretValidator(channel.Bot, "telegram_bot", func(values map[string]string) []channel.ValidationError {
		var out []channel.ValidationError
		if value, exists := values["bot_token"]; exists && !telegramToken.MatchString(value) {
			out = append(out, channel.ValidationError{Field: "bot_token", Code: "invalid_bot_token"})
		}
		if value, exists := values["webhook_secret"]; exists && !ValidTelegramWebhookSecret(value) {
			out = append(out, channel.ValidationError{Field: "webhook_secret", Code: "invalid_webhook_secret"})
		}
		return out
	})
}

type telegramProvider struct {
	token         string
	webhookSecret string
	baseURL       string
	client        *http.Client
}

// The endpoint override is private and exists solely for protocol fixtures.
// User configuration never selects an HTTP destination or client transport.
func newTelegramProvider(config module.Config, client *http.Client, baseURL string) (*telegramProvider, error) {
	username, ok := config.Values["bot_username"].(string)
	token, secret := config.Secrets["bot_token"], config.Secrets["webhook_secret"]
	if !ok || !ValidTelegramBotUsername(username) || !telegramToken.MatchString(token) || !ValidTelegramWebhookSecret(secret) {
		return nil, errors.New("bot configuration invalid")
	}
	if baseURL == "" {
		baseURL = telegramAPIOrigin
	}
	var bounded http.Client
	if client != nil {
		bounded = *client
	}
	bounded.Timeout = 15 * time.Second
	bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &telegramProvider{token: token, webhookSecret: secret, baseURL: strings.TrimRight(baseURL, "/"), client: &bounded}, nil
}

type telegramResponse struct {
	OK         *bool           `json:"ok"`
	Result     json.RawMessage `json:"result"`
	ErrorCode  int             `json:"error_code"`
	Parameters struct {
		RetryAfter int64 `json:"retry_after"`
	} `json:"parameters"`
}

// call never returns upstream errors or response bodies: the request URL
// contains the Bot token and upstream descriptions may repeat message content.
func (p *telegramProvider) call(ctx context.Context, method string, payload any) (telegramResponse, int, bool) {
	var response telegramResponse
	body, err := json.Marshal(payload)
	if err != nil {
		return response, 0, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/bot"+p.token+"/"+method, bytes.NewReader(body))
	if err != nil {
		return response, 0, false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.client.Do(req)
	if err != nil {
		return response, 0, false
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, telegramResponseLimit+1))
	if err != nil || len(raw) > telegramResponseLimit {
		return response, resp.StatusCode, false
	}
	if err = json.Unmarshal(raw, &response); err != nil || response.OK == nil {
		return telegramResponse{}, resp.StatusCode, false
	}
	return response, resp.StatusCode, true
}

func (p *telegramProvider) Send(ctx context.Context, job notify.Job) notify.Result {
	target, err := strconv.ParseInt(job.Target, 10, 64)
	if err != nil || target <= 0 || strconv.FormatInt(target, 10) != job.Target || !utf8.ValidString(job.Body) || utf8.RuneCountInString(job.Body) < 1 || utf8.RuneCountInString(job.Body) > 4096 {
		return notify.Result{Outcome: "failed", Code: "telegram_invalid_message"}
	}
	if !job.ExpiresAt.After(time.Now()) {
		return notify.Result{Outcome: "failed", Code: "notification_expired"}
	}
	deadline := time.Now().Add(15 * time.Second)
	if job.ExpiresAt.Before(deadline) {
		deadline = job.ExpiresAt
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	response, status, decoded := p.call(ctx, "sendMessage", struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
	}{target, job.Body})
	if status == http.StatusBadRequest || status == http.StatusUnauthorized || status == http.StatusForbidden {
		return notify.Result{Outcome: "failed", Code: "telegram_rejected"}
	}
	if status >= 500 || status == 0 {
		return notify.Result{Outcome: "unknown", Code: "telegram_transport_unknown"}
	}
	if decoded && !*response.OK && (status == http.StatusTooManyRequests || response.ErrorCode == http.StatusTooManyRequests) && response.Parameters.RetryAfter > 0 && response.Parameters.RetryAfter <= int64((1<<63-1)/int64(time.Second)) {
		return notify.Result{Outcome: "rate_limited", Code: "telegram_rate_limited", RetryAfter: time.Duration(response.Parameters.RetryAfter) * time.Second}
	}
	if decoded && !*response.OK && (response.ErrorCode == http.StatusBadRequest || response.ErrorCode == http.StatusUnauthorized || response.ErrorCode == http.StatusForbidden) {
		return notify.Result{Outcome: "failed", Code: "telegram_rejected"}
	}
	if !decoded || status < 200 || status >= 300 || !*response.OK {
		return notify.Result{Outcome: "unknown", Code: "telegram_response_unknown"}
	}
	var message struct {
		MessageID int64 `json:"message_id"`
		Chat      struct {
			ID   int64  `json:"id"`
			Type string `json:"type"`
		} `json:"chat"`
	}
	if json.Unmarshal(response.Result, &message) != nil || message.MessageID <= 0 || message.Chat.ID != target || message.Chat.Type != "private" {
		return notify.Result{Outcome: "unknown", Code: "telegram_response_unknown"}
	}
	return notify.Result{Outcome: "accepted", Code: "telegram_accepted", ProviderMessageID: strconv.FormatInt(message.MessageID, 10)}
}

// ConfigureTelegramWebhook is an explicit administrative network operation.
// It is never called by channel saves, startup, or delivery retries.
func ConfigureTelegramWebhook(ctx context.Context, config module.Config, callbackURL string) error {
	provider, err := newTelegramProvider(config, nil, "")
	if err != nil {
		return errors.New("telegram webhook configuration invalid")
	}
	return provider.configureWebhook(ctx, callbackURL)
}

func (p *telegramProvider) configureWebhook(ctx context.Context, callbackURL string) error {
	u, err := url.Parse(callbackURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return errors.New("telegram webhook configuration invalid")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	response, status, decoded := p.call(ctx, "setWebhook", struct {
		URL            string   `json:"url"`
		SecretToken    string   `json:"secret_token"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{callbackURL, p.webhookSecret, []string{"message"}})
	var success bool
	if !decoded || status < 200 || status >= 300 || !*response.OK || json.Unmarshal(response.Result, &success) != nil || !success {
		return errors.New("telegram webhook configuration failed")
	}
	return nil
}
