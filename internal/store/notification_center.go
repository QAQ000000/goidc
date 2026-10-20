package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/jackc/pgx/v5"
)

type NotificationPreference struct {
	Channel         string `json:"channel"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	EventScope      string `json:"event_scope"`
	Version         int64  `json:"version"`
	RequiresBinding bool   `json:"requires_binding"`
}

type NotificationTarget struct {
	ID         int64  `json:"id"`
	Channel    string `json:"channel"`
	InstanceID string `json:"instance_id"`
	Label      string `json:"label"`
	Target     string `json:"-"`
	Active     bool   `json:"active"`
	Version    int64  `json:"version"`
}

type NotificationBindingChallenge struct {
	Token     string    `json:"token"`
	DeepLink  string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

func notificationChannelName(channelName string) string {
	if channelName == "telegram" {
		return "Telegram"
	}
	if channelName == "web_push" {
		return "浏览器通知"
	}
	return "站内通知"
}

func activeNotificationUser(ctx context.Context, tx pgx.Tx, userID int64) error {
	var id int64
	table, err := authenticationTable(ctx, tx, userID)
	if err != nil {
		return conflict("notification_user_unavailable", "用户不可用")
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE id=$1 AND status='active' FOR UPDATE`, userID).Scan(&id); err != nil {
		return conflict("notification_user_unavailable", "用户不可用")
	}
	return nil
}

func (s *Store) NotificationPreferences(ctx context.Context, userID int64) ([]NotificationPreference, error) {
	rows, err := s.Pool.Query(ctx, `SELECT c.channel,coalesce(p.enabled,false),coalesce(p.version,0),coalesce(p.event_scope,'invoice.paid') FROM account_identities u CROSS JOIN (VALUES('telegram'),('inbox'),('web_push')) c(channel) LEFT JOIN user_notification_preferences p ON p.user_id=u.id AND p.channel=c.channel WHERE u.id=$1 AND u.status='active' ORDER BY c.channel`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationPreference{}
	for rows.Next() {
		var p NotificationPreference
		if err = rows.Scan(&p.Channel, &p.Enabled, &p.Version, &p.EventScope); err != nil {
			return nil, err
		}
		p.Name = notificationChannelName(p.Channel)
		p.RequiresBinding = p.Channel == "telegram" || p.Channel == "web_push"
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) UpdateNotificationPreference(ctx context.Context, tx pgx.Tx, userID int64, channelName string, enabled bool, expectedVersion int64, eventScope ...string) (NotificationPreference, error) {
	out := NotificationPreference{Channel: channelName, Name: notificationChannelName(channelName), RequiresBinding: channelName == "telegram" || channelName == "web_push", EventScope: "invoice.paid"}
	if channelName != "telegram" && channelName != "inbox" && channelName != "web_push" || expectedVersion < 0 || len(eventScope) > 1 || (len(eventScope) == 1 && eventScope[0] != "" && eventScope[0] != "invoice.paid" && eventScope[0] != "business") {
		return out, conflict("invalid_notification_preference", "通知偏好无效")
	}
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return out, err
	}
	var version int64
	err := tx.QueryRow(ctx, `SELECT version,event_scope FROM user_notification_preferences WHERE user_id=$1 AND channel=$2 FOR UPDATE`, userID, channelName).Scan(&version, &out.EventScope)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	if version != expectedVersion {
		return out, conflict("version_conflict", "通知偏好已变更，请重新加载")
	}
	if len(eventScope) == 1 && eventScope[0] != "" {
		out.EventScope = eventScope[0]
	}
	if enabled && channelName == "telegram" {
		rows, queryErr := tx.Query(ctx, `SELECT t.instance_id FROM notification_targets t WHERE t.user_id=$1 AND t.active ORDER BY t.instance_id`, userID)
		if queryErr != nil {
			return out, queryErr
		}
		instances := []string{}
		for rows.Next() {
			var instance string
			if err = rows.Scan(&instance); err != nil {
				rows.Close()
				return out, err
			}
			instances = append(instances, instance)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return out, err
		}
		exists := false
		for _, instance := range instances {
			if _, err = notificationBindingBot(ctx, tx, instance); err == nil {
				exists = true
				break
			}
		}
		if !exists {
			return out, conflict("notification_binding_required", "请先绑定通知目标")
		}
	}
	if enabled && channelName == "web_push" {
		var exists bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_web_push_subscriptions s WHERE s.user_id=$1 AND `+webPushSubscriptionCurrent+`)`, userID).Scan(&exists); err != nil {
			return out, err
		}
		if !exists {
			return out, conflict("notification_binding_required", "请先在浏览器订阅通知")
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO user_notification_preferences(user_id,channel,enabled,event_scope) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,channel) DO UPDATE SET enabled=excluded.enabled,event_scope=excluded.event_scope,version=user_notification_preferences.version+1,updated_at=now() RETURNING enabled,version,event_scope`, userID, channelName, enabled, out.EventScope).Scan(&out.Enabled, &out.Version, &out.EventScope)
	return out, err
}

func maskedNotificationTarget(target string) string {
	if len(target) < 5 {
		return "****"
	}
	return "****" + target[len(target)-4:]
}

func (s *Store) NotificationTargets(ctx context.Context, userID int64) ([]NotificationTarget, error) {
	rows, err := s.Pool.Query(ctx, `SELECT t.id,t.channel,t.instance_id,t.target,t.active,t.version FROM notification_targets t JOIN account_identities u ON u.id=t.user_id WHERE t.user_id=$1 AND u.status='active' ORDER BY t.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationTarget{}
	for rows.Next() {
		var t NotificationTarget
		if err = rows.Scan(&t.ID, &t.Channel, &t.InstanceID, &t.Target, &t.Active, &t.Version); err != nil {
			return nil, err
		}
		t.Label = maskedNotificationTarget(t.Target)
		out = append(out, t)
	}
	return out, rows.Err()
}

func notificationToken() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
func notificationTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

var notificationBotUsername = regexp.MustCompile(`(?i)^[a-z][a-z0-9_]{1,28}bot$`)
var notificationPrivateID = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)

func (s *Store) CreateNotificationBindingChallenge(ctx context.Context, tx pgx.Tx, userID int64, instanceID, sessionTokenHash string) (NotificationBindingChallenge, error) {
	var out NotificationBindingChallenge
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return out, err
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE token_hash=$1 AND user_id=$2 AND expires_at>clock_timestamp() FOR SHARE`, sessionTokenHash, userID).Scan(&sessionUser); err != nil {
		return out, conflict("notification_session_expired", "登录会话已失效")
	}
	username, err := notificationBindingBot(ctx, tx, instanceID)
	if err != nil {
		return out, err
	}
	var recent bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_binding_challenges WHERE user_id=$1 AND created_at>clock_timestamp()-interval '10 seconds')`, userID).Scan(&recent); err != nil {
		return out, err
	}
	if recent {
		return out, conflict("notification_challenge_rate_limited", "请稍后重新生成绑定链接")
	}
	if _, err := tx.Exec(ctx, `UPDATE notification_binding_challenges SET revoked_at=clock_timestamp() WHERE user_id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, userID); err != nil {
		return out, err
	}
	token, err := notificationToken()
	if err != nil {
		return out, err
	}
	if err = tx.QueryRow(ctx, `INSERT INTO notification_binding_challenges(token_hash,user_id,instance_id,session_token_hash,expires_at) VALUES($1,$2,$3,$4,clock_timestamp()+interval '10 minutes') RETURNING expires_at`, notificationTokenHash(token), userID, instanceID, sessionTokenHash).Scan(&out.ExpiresAt); err != nil {
		return out, err
	}
	out.Token = token
	out.DeepLink = "https://t.me/" + username + "?start=" + token
	return out, nil
}

func (s *Store) ConsumeNotificationBindingChallenge(ctx context.Context, tx pgx.Tx, instanceID, token, chatID, telegramUserID string) (NotificationTarget, error) {
	var out NotificationTarget
	if len(token) != 48 || !notificationPrivateID.MatchString(chatID) || chatID != telegramUserID {
		return out, conflict("notification_binding_invalid", "绑定请求无效")
	}
	var userID int64
	hash := notificationTokenHash(token)
	if err := tx.QueryRow(ctx, `SELECT user_id FROM notification_binding_challenges WHERE token_hash=$1`, hash).Scan(&userID); err != nil {
		return out, conflict("notification_binding_invalid", "绑定链接无效或已失效")
	}
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return out, err
	}
	var sessionUser int64
	if err := tx.QueryRow(ctx, `SELECT s.user_id FROM sessions s JOIN notification_binding_challenges b ON b.session_token_hash=s.token_hash WHERE b.token_hash=$1 AND s.user_id=$2 AND s.expires_at>clock_timestamp() AND b.instance_id=$3 AND b.expires_at>clock_timestamp() AND b.consumed_at IS NULL AND b.revoked_at IS NULL FOR SHARE OF s`, hash, userID, instanceID).Scan(&sessionUser); err != nil {
		return out, conflict("notification_binding_invalid", "绑定链接无效或已失效")
	}
	if _, err := notificationBindingBot(ctx, tx, instanceID); err != nil {
		return out, err
	}
	var valid bool
	if err := tx.QueryRow(ctx, `SELECT b.instance_id=$2 AND b.expires_at>clock_timestamp() AND b.consumed_at IS NULL AND b.revoked_at IS NULL FROM notification_binding_challenges b WHERE b.token_hash=$1 FOR UPDATE`, hash, instanceID).Scan(&valid); err != nil || !valid {
		return out, conflict("notification_binding_invalid", "绑定链接无效或已失效")
	}
	// Serialize even when this target has never had a row: two accounts must
	// not race to claim the same private chat or transfer its ownership.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,718231))`, instanceID+":"+chatID); err != nil {
		return out, err
	}
	var owner int64
	err := tx.QueryRow(ctx, `SELECT user_id FROM notification_targets WHERE instance_id=$1 AND target=$2 FOR UPDATE`, instanceID, chatID).Scan(&owner)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	if err == nil && owner != userID {
		return out, conflict("notification_target_in_use", "通知目标已被绑定，不能转移")
	}
	var other bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM notification_targets WHERE user_id=$1 AND instance_id=$2 AND active AND target<>$3)`, userID, instanceID, chatID).Scan(&other); err != nil {
		return out, err
	}
	if other {
		return out, conflict("notification_target_in_use", "请先解除原通知目标绑定")
	}
	err = tx.QueryRow(ctx, `INSERT INTO notification_targets(user_id,channel,instance_id,target) VALUES($1,'telegram',$2,$3) ON CONFLICT(instance_id,target) DO UPDATE SET active=true,version=notification_targets.version+1,updated_at=now() WHERE notification_targets.user_id=excluded.user_id RETURNING id,channel,instance_id,target,active,version`, userID, instanceID, chatID).Scan(&out.ID, &out.Channel, &out.InstanceID, &out.Target, &out.Active, &out.Version)
	if err != nil {
		return out, err
	}
	out.Label = maskedNotificationTarget(out.Target)
	if _, err = tx.Exec(ctx, `UPDATE notification_binding_challenges SET consumed_at=clock_timestamp() WHERE token_hash=$1`, hash); err != nil {
		return out, err
	}
	err = Audit(ctx, tx, userID, "notification.target_bound", "notification_target", out.ID, map[string]any{"instance_id": instanceID, "channel": "telegram"})
	return out, err
}

func notificationBindingBot(ctx context.Context, tx pgx.Tx, instanceID string) (string, error) {
	rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR SHARE", instanceID))
	if err != nil {
		return "", conflict("notification_bot_unavailable", "通知机器人不可用")
	}
	desc, ok := channel.Lookup(channel.Category("bot"), rec.item.ImplementationID)
	if !ok || rec.item.Category != "bot" || rec.item.ImplementationID != "telegram_bot" || !rec.item.Enabled || rec.item.SchemaVersion != desc.SchemaVersion || !channel.HasFactory(desc.Category, desc.ID) {
		return "", conflict("notification_bot_unavailable", "通知机器人不可用")
	}
	_, validation := channel.ValidateConfiguration(desc, rec.item.Config, credentialFlags(rec, desc), true)
	username, _ := rec.item.Config["bot_username"].(string)
	if len(validation) > 0 || !notificationBotUsername.MatchString(username) {
		return "", conflict("notification_bot_unavailable", "通知机器人配置无效")
	}
	return username, nil
}

func (s *Store) UnbindNotificationTarget(ctx context.Context, tx pgx.Tx, userID, id, expectedVersion int64) error {
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE notification_targets SET active=false,version=version+1,updated_at=now() WHERE id=$1 AND user_id=$2 AND version=$3 AND active`, id, userID, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return conflict("version_conflict", "通知目标已变更或不属于当前用户")
	}
	// A later binding starts without consent. Incrementing the preference
	// version also permanently invalidates messages queued before unbinding.
	if _, err = tx.Exec(ctx, `INSERT INTO user_notification_preferences(user_id,channel,enabled) VALUES($1,'telegram',false) ON CONFLICT(user_id,channel) DO UPDATE SET enabled=false,version=user_notification_preferences.version+1,updated_at=now()`, userID); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE notification_binding_challenges SET revoked_at=clock_timestamp() WHERE user_id=$1 AND consumed_at IS NULL AND revoked_at IS NULL`, userID)
	return err
}

func notificationRender(value string, payload map[string]string) string {
	for k, v := range payload {
		value = strings.ReplaceAll(value, "{{"+k+"}}", v)
	}
	return value
}
func notificationAmount(cents int64) string { return fmt.Sprintf("¥%d.%02d", cents/100, cents%100) }
