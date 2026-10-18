package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/notification"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
)

func (s *Server) registerNotificationCenterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/me/notification-preferences", s.sessionAuth(s.notificationPreferences, false))
	mux.HandleFunc("PATCH /api/v1/me/notification-preferences", s.sessionAuth(s.notificationPreferences, false))
	mux.HandleFunc("POST /api/v1/me/notification-bindings/challenges", s.sessionAuth(s.notificationBindingChallenge, false))
	mux.HandleFunc("DELETE /api/v1/me/notification-bindings/{id}", s.sessionAuth(s.deleteNotificationBinding, false))
	mux.HandleFunc("GET /api/v1/me/notification-inbox", s.sessionAuth(s.notificationInbox, false))
	mux.HandleFunc("POST /api/v1/me/notification-inbox/{id}/read", s.sessionAuth(s.readNotificationInbox, false))
	mux.HandleFunc("GET /api/v1/admin/notification-center", s.sessionAuth(s.notificationCenter, true))
	mux.HandleFunc("PATCH /api/v1/admin/notification-center/rules/{id}", s.sessionAuth(s.updateNotificationCenterRule, true))
	mux.HandleFunc("PATCH /api/v1/admin/notification-center/templates/{id}", s.sessionAuth(s.updateNotificationCenterTemplate, true))
	mux.HandleFunc("POST /api/v1/admin/notification-center/channels/{id}/webhook", s.sessionAuth(s.configureNotificationWebhook, true))
	mux.HandleFunc("POST /api/v1/notifications/telegram/{id}/webhook", s.telegramBindingWebhook)
}

// This exception is limited to the authenticated Telegram callback route.
func notificationWebhookPath(path string) bool {
	const prefix = "/api/v1/notifications/telegram/"
	if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, "/webhook") {
		return false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), "/webhook")
	return id != "" && len(id) <= 80 && !strings.Contains(id, "/")
}

type notificationInstance struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Channel     string `json:"channel"`
	BotUsername string `json:"bot_username"`
	Version     int64  `json:"version"`
}

func (s *Server) notificationInstances(ctx context.Context) ([]notificationInstance, error) {
	rows, err := s.db.Pool.Query(ctx, `SELECT id,display_name,coalesce(config->>'bot_username',''),version FROM channel_instances WHERE category='bot' AND enabled ORDER BY sort_order,id`)
	if err != nil {
		return nil, err
	}
	items := []notificationInstance{}
	for rows.Next() {
		var item notificationInstance
		if err = rows.Scan(&item.ID, &item.Name, &item.BotUsername, &item.Version); err != nil {
			rows.Close()
			return nil, err
		}
		item.Channel = "telegram"
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	valid := []notificationInstance{}
	for _, item := range items {
		config, implementation, resolveErr := s.db.RuntimeChannelInstance(ctx, item.ID, channel.Bot)
		if resolveErr != nil || implementation != "telegram_bot" {
			continue
		}
		if _, resolveErr = notification.NewBotProvider(implementation, config); resolveErr == nil {
			valid = append(valid, item)
		}
	}
	return valid, nil
}

func (s *Server) notificationPreferences(w http.ResponseWriter, r *http.Request) {
	uid := identityFrom(r).ID
	if r.Method == http.MethodGet {
		prefs, err := s.db.NotificationPreferences(r.Context(), uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		targets, err := s.db.NotificationTargets(r.Context(), uid)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		active := make([]store.NotificationTarget, 0, len(targets))
		for _, target := range targets {
			if target.Active {
				active = append(active, target)
			}
		}
		instances, err := s.notificationInstances(r.Context())
		if err != nil {
			s.fail(w, r, err)
			return
		}
		write(w, 200, map[string]any{"channels": prefs, "targets": active, "instances": instances})
		return
	}
	var in struct {
		Channel         string `json:"channel"`
		Enabled         *bool  `json:"enabled"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if (in.Channel != "telegram" && in.Channel != "inbox") || in.Enabled == nil || in.ExpectedVersion == nil || *in.ExpectedVersion < 0 {
		writeError(w, 400, "invalid_notification_preference", "请提供有效渠道、偏好和版本")
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		out, err := s.db.UpdateNotificationPreference(r.Context(), tx, uid, in.Channel, *in.Enabled, *in.ExpectedVersion)
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.preference.updated", "user", uid, map[string]any{"channel": in.Channel, "enabled": out.Enabled, "version": out.Version}); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, out}, nil
	})
}

func (s *Server) notificationBindingChallenge(w http.ResponseWriter, r *http.Request) {
	var in struct {
		InstanceID string `json:"instance_id"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.InstanceID == "" || len(in.InstanceID) > 80 {
		writeError(w, 400, "invalid_notification_instance", "请选择通知实例")
		return
	}
	if !s.authLimit(w, r, "notification_binding", 20) {
		return
	}
	cookie, _ := r.Cookie("zjmfgo_session")
	uid := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		out, err := s.db.CreateNotificationBindingChallenge(r.Context(), tx, uid, in.InstanceID, tokenHash(cookie.Value))
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.binding.challenge_created", "user", uid, map[string]any{"instance_id": in.InstanceID}); err != nil {
			return txResponse{}, err
		}
		// The opaque challenge is returned only in the private deep link.
		return txResponse{201, map[string]any{"url": out.DeepLink, "expires_at": out.ExpiresAt}}, nil
	})
}

func (s *Server) deleteNotificationBinding(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpectedVersion int64 `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.ExpectedVersion < 1 {
		writeError(w, 400, "expected_version_required", "请提供绑定版本")
		return
	}
	uid := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		if err := s.db.UnbindNotificationTarget(r.Context(), tx, uid, id, in.ExpectedVersion); err != nil {
			return txResponse{}, err
		}
		if err := store.Audit(r.Context(), tx, uid, "notification.binding.removed", "notification_target", id, map[string]any{"version": in.ExpectedVersion + 1}); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, map[string]bool{"unbound": true}}, nil
	})
}

func (s *Server) notificationInbox(w http.ResponseWriter, r *http.Request) {
	limit, offset := pagination(r)
	items, err := s.db.UserNotificationInbox(r.Context(), identityFrom(r).ID, limit+1, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	write(w, 200, map[string]any{"items": items, "has_more": hasMore, "limit": limit, "offset": offset})
}

func (s *Server) readNotificationInbox(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	uid := identityFrom(r).ID
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.LockFileCredential(requestFileContext(r), tx, uid); err != nil {
			return txResponse{}, err
		}
		if err := s.db.MarkNotificationInboxRead(r.Context(), tx, uid, id); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, map[string]bool{"read": true}}, nil
	})
}

func (s *Server) notificationCenter(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.NotificationRoutes(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	templates, err := s.db.NotificationTemplates(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	instances, err := s.notificationInstances(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	limit, offset := pagination(r)
	deliveries, err := s.db.NotificationDeliveriesForAdmin(r.Context(), identityFrom(r).ID, limit+1, offset)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	hasMore := len(deliveries) > limit
	if hasMore {
		deliveries = deliveries[:limit]
	}
	channels := []map[string]any{{"channel": "email", "name": "邮件（兼容队列）"}, {"channel": "sms", "name": "短信（兼容队列）"}, {"channel": "telegram", "name": "Telegram", "requires_binding": true}, {"channel": "inbox", "name": "站内通知"}}
	write(w, 200, map[string]any{"channels": channels, "rules": rules, "templates": templates, "instances": instances, "deliveries": deliveries, "has_more": hasMore, "limit": limit, "offset": offset})
}

func (s *Server) updateNotificationCenterRule(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Enabled         *bool   `json:"enabled"`
		InstanceID      *string `json:"instance_id"`
		TemplateID      string  `json:"template_id"`
		ExpectedVersion int64   `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Enabled == nil || in.ExpectedVersion < 1 || in.TemplateID == "" {
		writeError(w, 400, "invalid_notification_route", "请提供路由开关、模板和版本")
		return
	}
	instance := ""
	if in.InstanceID != nil {
		instance = *in.InstanceID
	}
	uid := identityFrom(r).ID
	id := r.PathValue("id")
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.CheckAdminCreationPermission(requestFileContext(r), tx, uid, "notifications"); err != nil {
			return txResponse{}, err
		}
		out, err := s.db.UpdateNotificationRoute(r.Context(), tx, id, store.NotificationRouteInput{Enabled: *in.Enabled, InstanceID: instance, TemplateID: in.TemplateID, ExpectedVersion: in.ExpectedVersion})
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.route.updated", "user", uid, map[string]any{"route_id": id, "enabled": *in.Enabled, "version": in.ExpectedVersion + 1}); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, out}, nil
	})
}

func (s *Server) updateNotificationCenterTemplate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Subject         string `json:"subject"`
		Body            string `json:"body"`
		Active          *bool  `json:"active"`
		ExpectedVersion int64  `json:"expected_version"`
	}
	if !decode(w, r, &in) {
		return
	}
	if in.Active == nil || in.ExpectedVersion < 1 {
		writeError(w, 400, "invalid_notification_template", "请提供模板状态和版本")
		return
	}
	uid := identityFrom(r).ID
	id := r.PathValue("id")
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.CheckAdminCreationPermission(requestFileContext(r), tx, uid, "notifications"); err != nil {
			return txResponse{}, err
		}
		out, err := s.db.UpdateNotificationTemplate(r.Context(), tx, id, store.NotificationMessageTemplateInput{Subject: in.Subject, Body: in.Body, Active: *in.Active, ExpectedVersion: in.ExpectedVersion})
		if err != nil {
			return txResponse{}, err
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.template.updated", "user", uid, map[string]any{"template_id": id, "version": in.ExpectedVersion + 1}); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, out}, nil
	})
}

func (s *Server) configureNotificationWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	uid := identityFrom(r).ID
	origin, err := url.Parse(s.cfg.PublicOrigin)
	if err != nil || origin.Scheme != "https" || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" && origin.Path != "/" {
		writeError(w, 400, "notification_webhook_https_required", "请先配置公网 HTTPS 站点地址")
		return
	}
	// The intent survives an ambiguous external result or a later audit failure.
	err = s.db.WithTx(r.Context(), func(tx pgx.Tx) error {
		if err := store.CheckAdminCreationPermission(requestFileContext(r), tx, uid, "settings"); err != nil {
			return err
		}
		return store.Audit(r.Context(), tx, uid, "notification.webhook.requested", "user", uid, map[string]any{"instance_id": id})
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.withTx(w, r, func(tx pgx.Tx) (txResponse, error) {
		if err := store.CheckAdminCreationPermission(requestFileContext(r), tx, uid, "settings"); err != nil {
			return txResponse{}, err
		}
		var instanceID string
		if err := tx.QueryRow(r.Context(), `SELECT id FROM channel_instances WHERE id=$1 AND category='bot' AND implementation_id='telegram_bot' AND enabled FOR SHARE`, id).Scan(&instanceID); err != nil {
			return txResponse{}, err
		}
		config, implementation, err := s.db.RuntimeChannelInstanceTx(r.Context(), tx, id, channel.Bot)
		if err != nil || implementation != "telegram_bot" {
			return txResponse{}, &txHTTPError{409, "notification_bot_unavailable", "通知实例不可用"}
		}
		callback := strings.TrimRight(origin.String(), "/") + "/api/v1/notifications/telegram/" + url.PathEscape(id) + "/webhook"
		if err = notification.ConfigureTelegramWebhook(r.Context(), config, callback); err != nil {
			return txResponse{}, &txHTTPError{502, "notification_webhook_unconfirmed", "平台未确认设置结果，请检查后重试"}
		}
		if err = store.Audit(r.Context(), tx, uid, "notification.webhook.configured", "user", uid, map[string]any{"instance_id": id}); err != nil {
			return txResponse{}, err
		}
		return txResponse{200, map[string]bool{"configured": true}}, nil
	})
}

func (s *Server) telegramBindingWebhook(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	config, implementation, err := s.db.RuntimeChannelInstance(r.Context(), id, channel.Bot)
	if err != nil || implementation != "telegram_bot" {
		writeError(w, 401, "invalid_notification_webhook", "回调校验失败")
		return
	}
	want := sha256.Sum256([]byte(config.Secrets["webhook_secret"]))
	got := sha256.Sum256([]byte(r.Header.Get("X-Telegram-Bot-Api-Secret-Token")))
	if config.Secrets["webhook_secret"] == "" || subtle.ConstantTimeCompare(want[:], got[:]) != 1 {
		writeError(w, 401, "invalid_notification_webhook", "回调校验失败")
		return
	}
	if !s.authLimit(w, r, "notification_webhook:"+id, 120) {
		return
	}
	var update struct {
		Message *struct {
			From *struct {
				ID    int64 `json:"id"`
				IsBot bool  `json:"is_bot"`
			} `json:"from"`
			Chat struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			} `json:"chat"`
			Text string `json:"text"`
		} `json:"message"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	if err = decoder.Decode(&update); err != nil {
		writeError(w, 400, "invalid_notification_update", "回调内容无效")
		return
	}
	var extra any
	if err = decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		writeError(w, 400, "invalid_notification_update", "回调内容无效")
		return
	}
	message := update.Message
	if message == nil || message.From == nil || message.From.IsBot || message.Chat.Type != "private" || message.From.ID <= 0 || message.From.ID != message.Chat.ID {
		write(w, 200, map[string]bool{"ok": true})
		return
	}
	parts := strings.Fields(message.Text)
	if len(parts) != 2 || parts[0] != "/start" {
		write(w, 200, map[string]bool{"ok": true})
		return
	}
	chatID := strconv.FormatInt(message.Chat.ID, 10)
	err = s.db.WithTx(r.Context(), func(tx pgx.Tx) error {
		if _, err := s.db.ConsumeNotificationBindingChallenge(r.Context(), tx, id, parts[1], chatID, chatID); err != nil {
			return err
		}
		// Validate the current secret under the instance lock. A concurrent
		// rotation must roll back both target creation and token consumption.
		current, implementation, err := s.db.RuntimeChannelInstanceTx(r.Context(), tx, id, channel.Bot)
		if err != nil {
			return err
		}
		currentHash := sha256.Sum256([]byte(current.Secrets["webhook_secret"]))
		if implementation != "telegram_bot" || current.Secrets["webhook_secret"] == "" || subtle.ConstantTimeCompare(currentHash[:], got[:]) != 1 {
			return &store.Conflict{Code: "notification_binding_invalid", Message: "回调校验失败"}
		}
		return nil
	})
	if err != nil {
		var conflict *store.Conflict
		if !errors.As(err, &conflict) {
			// A raw DB error could include the challenge or external identity.
			s.log.Error("notification binding persistence failed")
			writeError(w, 503, "notification_binding_unavailable", "请稍后重试")
			return
		}
	}
	// Invalid, expired and replayed challenges do not reveal account state.
	write(w, 200, map[string]bool{"ok": true})
}
