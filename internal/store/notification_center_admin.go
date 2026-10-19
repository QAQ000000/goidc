package store

import (
	"context"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/jackc/pgx/v5"
)

type NotificationRoute struct {
	ID         string `json:"id"`
	EventKind  string `json:"event_kind"`
	Channel    string `json:"channel"`
	InstanceID string `json:"instance_id"`
	TemplateID string `json:"template_id"`
	Enabled    bool   `json:"enabled"`
	Version    int64  `json:"version"`
}
type NotificationRouteInput struct {
	Enabled         bool   `json:"enabled"`
	InstanceID      string `json:"instance_id"`
	TemplateID      string `json:"template_id"`
	ExpectedVersion int64  `json:"expected_version"`
}
type NotificationMessageTemplate struct {
	ID        string `json:"id"`
	EventKind string `json:"event_kind"`
	Channel   string `json:"channel"`
	Subject   string `json:"subject"`
	Body      string `json:"body"`
	Active    bool   `json:"active"`
	Version   int64  `json:"version"`
}
type NotificationMessageTemplateInput struct {
	Subject         string `json:"subject"`
	Body            string `json:"body"`
	Active          bool   `json:"active"`
	ExpectedVersion int64  `json:"expected_version"`
}

func (s *Store) NotificationRoutes(ctx context.Context) ([]NotificationRoute, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,event_kind,channel,coalesce(instance_id,''),template_id,enabled,version FROM notification_routes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationRoute{}
	for rows.Next() {
		var r NotificationRoute
		if err = rows.Scan(&r.ID, &r.EventKind, &r.Channel, &r.InstanceID, &r.TemplateID, &r.Enabled, &r.Version); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) UpdateNotificationRoute(ctx context.Context, tx pgx.Tx, id string, in NotificationRouteInput) (NotificationRoute, error) {
	var out NotificationRoute
	err := tx.QueryRow(ctx, `SELECT id,event_kind,channel,coalesce(instance_id,''),template_id,enabled,version FROM notification_routes WHERE id=$1 FOR UPDATE`, id).Scan(&out.ID, &out.EventKind, &out.Channel, &out.InstanceID, &out.TemplateID, &out.Enabled, &out.Version)
	if err != nil {
		return out, err
	}
	if in.ExpectedVersion != out.Version {
		return out, conflict("version_conflict", "通知路由已变更，请重新加载")
	}
	if in.TemplateID == "" {
		in.TemplateID = out.TemplateID
	}
	if in.InstanceID == "" {
		in.InstanceID = out.InstanceID
	}
	if out.Channel == "inbox" && in.InstanceID != "" {
		return out, conflict("invalid_notification_route", "站内通知不使用外部渠道实例")
	}
	if in.Enabled && out.Channel == "telegram" {
		rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR SHARE", in.InstanceID))
		if err != nil {
			return out, conflict("notification_bot_unavailable", "通知机器人不可用")
		}
		desc, ok := channel.Lookup(channel.Category("bot"), rec.item.ImplementationID)
		if !ok || !rec.item.Enabled || rec.item.Category != "bot" || rec.item.ImplementationID != "telegram_bot" || rec.item.SchemaVersion != desc.SchemaVersion || !channel.HasFactory(desc.Category, desc.ID) {
			return out, conflict("notification_bot_unavailable", "通知机器人不可用")
		}
		_, validation := channel.ValidateConfiguration(desc, rec.item.Config, credentialFlags(rec, desc), true)
		if len(validation) > 0 {
			return out, conflict("notification_bot_unavailable", "通知机器人配置无效")
		}
	}
	if in.Enabled && out.Channel == "web_push" {
		if _, err = s.webPushInstance(ctx, tx, in.InstanceID); err != nil {
			return out, err
		}
	}
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_message_templates WHERE id=$1 AND event_kind=$2 AND channel=$3 AND (active OR NOT $4))`, in.TemplateID, out.EventKind, out.Channel, in.Enabled).Scan(&valid); err != nil {
		return out, err
	}
	if !valid {
		return out, conflict("invalid_notification_template", "通知模板不可用")
	}
	err = tx.QueryRow(ctx, `UPDATE notification_routes SET enabled=$2,instance_id=NULLIF($3,''),template_id=$4,version=version+1,updated_at=now() WHERE id=$1 RETURNING enabled,coalesce(instance_id,''),template_id,version`, id, in.Enabled, in.InstanceID, in.TemplateID).Scan(&out.Enabled, &out.InstanceID, &out.TemplateID, &out.Version)
	return out, err
}

func (s *Store) NotificationTemplates(ctx context.Context) ([]NotificationMessageTemplate, error) {
	rows, err := s.Pool.Query(ctx, `SELECT id,event_kind,channel,subject,body,active,version FROM notification_message_templates ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationMessageTemplate{}
	for rows.Next() {
		var t NotificationMessageTemplate
		if err = rows.Scan(&t.ID, &t.EventKind, &t.Channel, &t.Subject, &t.Body, &t.Active, &t.Version); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateNotificationTemplate(ctx context.Context, tx pgx.Tx, id string, in NotificationMessageTemplateInput) (NotificationMessageTemplate, error) {
	var out NotificationMessageTemplate
	if len([]rune(strings.TrimSpace(in.Subject))) < 1 || len([]rune(in.Subject)) > 240 || len([]rune(strings.TrimSpace(in.Body))) < 1 || len([]rune(in.Body)) > 4000 || strings.ContainsAny(in.Subject+in.Body, "\x00") {
		return out, conflict("invalid_notification_template", "通知模板内容无效")
	}
	// Each event has its own small, non-executable vocabulary. Read the event
	// from the stored template rather than accepting a caller-supplied kind.
	var kind, channelName string
	if err := tx.QueryRow(ctx, `SELECT event_kind,channel FROM notification_message_templates WHERE id=$1 FOR UPDATE`, id).Scan(&kind, &channelName); err != nil {
		return out, err
	}
	if channelName == "web_push" && (in.Subject != notificationWebPushSubject || in.Body != notificationWebPushBody) {
		return out, conflict("invalid_notification_template", "浏览器锁屏通知使用固定通用提示，请只调整启用状态")
	}
	for _, value := range []string{in.Subject, in.Body} {
		if !notificationTemplateValid(kind, value) {
			return out, conflict("invalid_notification_template", "通知模板含不支持的变量")
		}
	}
	err := tx.QueryRow(ctx, `UPDATE notification_message_templates SET subject=$2,body=$3,active=$4,version=version+1,updated_at=now() WHERE id=$1 AND version=$5 RETURNING id,event_kind,channel,subject,body,active,version`, id, in.Subject, in.Body, in.Active, in.ExpectedVersion).Scan(&out.ID, &out.EventKind, &out.Channel, &out.Subject, &out.Body, &out.Active, &out.Version)
	if err == pgx.ErrNoRows {
		return out, conflict("version_conflict", "通知模板已变更，请重新加载")
	}
	return out, err
}

type NotificationInboxItem struct {
	ID        int64      `json:"id"`
	Subject   string     `json:"subject"`
	Body      string     `json:"body"`
	Status    string     `json:"status"`
	CreatedAt time.Time  `json:"created_at"`
	ReadAt    *time.Time `json:"read_at"`
}

func (s *Store) UserNotificationInbox(ctx context.Context, userID int64, limit, offset int) ([]NotificationInboxItem, error) {
	if limit < 1 || limit > 101 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `SELECT m.id,m.subject,m.body,m.status,m.created_at,m.read_at FROM admin_inbox_messages m JOIN users u ON u.id=m.user_id WHERE m.user_id=$1 AND u.status='active' ORDER BY m.id DESC LIMIT $2 OFFSET $3`, userID, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationInboxItem{}
	for rows.Next() {
		var m NotificationInboxItem
		if err = rows.Scan(&m.ID, &m.Subject, &m.Body, &m.Status, &m.CreatedAt, &m.ReadAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func (s *Store) MarkNotificationInboxRead(ctx context.Context, tx pgx.Tx, userID, id int64) error {
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE admin_inbox_messages SET status='read',read_at=coalesce(read_at,now()) WHERE id=$1 AND user_id=$2 AND status IN ('sent','read')`, id, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return conflict("notification_message_not_found", "通知不存在")
	}
	return nil
}

type NotificationDelivery struct {
	ID                int64     `json:"id"`
	EventID           int64     `json:"event_id"`
	UserID            int64     `json:"user_id"`
	EventKind         string    `json:"event_kind"`
	Channel           string    `json:"channel"`
	InstanceID        string    `json:"instance_id"`
	Status            string    `json:"status"`
	Code              string    `json:"code"`
	Attempts          int       `json:"attempts"`
	ProviderMessageID string    `json:"provider_message_id"`
	CreatedAt         time.Time `json:"created_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

func (s *Store) NotificationDeliveriesForAdmin(ctx context.Context, actorID int64, limit, offset int, eventKinds ...string) ([]NotificationDelivery, error) {
	kind := ""
	if len(eventKinds) > 1 {
		return nil, conflict("invalid_notification_event", "通知事件无效")
	}
	if len(eventKinds) == 1 {
		kind = eventKinds[0]
		if _, ok := notificationEventDescriptor(kind); kind != "" && !ok {
			return nil, conflict("invalid_notification_event", "通知事件无效")
		}
	}
	return s.notificationDeliveries(ctx, actorID, 0, limit, offset, kind)
}
func (s *Store) NotificationDeliveries(ctx context.Context, userID int64, limit, offset int) ([]NotificationDelivery, error) {
	return s.notificationDeliveries(ctx, 0, userID, limit, offset, "")
}
func (s *Store) notificationDeliveries(ctx context.Context, actorID, userID int64, limit, offset int, kind string) ([]NotificationDelivery, error) {
	if limit < 1 || limit > 101 {
		limit = 20
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.Pool.Query(ctx, `SELECT d.id,e.id,e.user_id,e.kind,
 CASE WHEN d.channel='mail' THEN 'email' ELSE d.channel END,coalesce(d.instance_id,''),
 CASE WHEN (d.channel='mail' AND m.id IS NULL) OR (d.channel='sms' AND s.id IS NULL) THEN 'archived'
 WHEN d.channel='mail' THEN CASE m.status WHEN 'sent' THEN 'accepted' WHEN 'sending' THEN 'sending' WHEN 'pending' THEN 'pending' WHEN 'failed' THEN 'failed' WHEN 'dead_letter' THEN 'failed' ELSE 'skipped' END
 WHEN d.channel='sms' THEN CASE s.status WHEN 'sent' THEN 'accepted' WHEN 'sending' THEN 'sending' WHEN 'pending' THEN 'pending' WHEN 'failed' THEN 'failed' WHEN 'dead_letter' THEN 'failed' ELSE 'skipped' END ELSE d.status END,
 CASE WHEN (d.channel='mail' AND m.id IS NULL) OR (d.channel='sms' AND s.id IS NULL) THEN 'legacy_log_removed' ELSE d.code END,
 CASE WHEN d.channel='mail' THEN coalesce(m.attempts,d.attempts) WHEN d.channel='sms' THEN coalesce(s.attempts,d.attempts) ELSE d.attempts END,
 d.provider_message_id,d.created_at,d.expires_at
 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id
 LEFT JOIN email_outbox m ON m.id=d.legacy_email_outbox_id LEFT JOIN sms_outbox s ON s.id=d.legacy_sms_outbox_id
 WHERE (($1>0 AND admin_customer_allowed($1,e.user_id)) OR ($2>0 AND e.user_id=$2 AND EXISTS(SELECT 1 FROM users u WHERE u.id=$2 AND u.status='active'))) AND ($5='' OR e.kind=$5) ORDER BY d.id DESC LIMIT $3 OFFSET $4`, actorID, userID, limit, offset, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationDelivery{}
	for rows.Next() {
		var d NotificationDelivery
		if err = rows.Scan(&d.ID, &d.EventID, &d.UserID, &d.EventKind, &d.Channel, &d.InstanceID, &d.Status, &d.Code, &d.Attempts, &d.ProviderMessageID, &d.CreatedAt, &d.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
