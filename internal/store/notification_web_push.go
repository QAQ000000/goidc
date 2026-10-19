package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/notify"
	"github.com/codex/zjmfgo/internal/secure"
	"github.com/jackc/pgx/v5"
)

type WebPushInstance struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Version   int64  `json:"version"`
}
type WebPushSubscriptionInfo struct {
	ID             int64  `json:"id"`
	InstanceID     string `json:"instance_id"`
	EndpointHash   string `json:"endpoint_hash"`
	Version        int64  `json:"version"`
	CurrentSession bool   `json:"current_session"`
	Active         bool   `json:"active"`
	Revocable      bool   `json:"revocable"`
}
type WebPushAccountState struct {
	Instances     []WebPushInstance         `json:"instances"`
	Subscriptions []WebPushSubscriptionInfo `json:"subscriptions"`
}

// The session is intentionally checked through EXISTS rather than a foreign
// key: removing/expiring a login immediately makes its subscription ineligible,
// while retained metadata can explain why a browser needs to subscribe again.
const webPushSubscriptionCurrent = `s.active AND EXISTS(SELECT 1 FROM sessions ss WHERE ss.token_hash=s.session_token_hash AND ss.user_id=s.user_id AND ss.expires_at>clock_timestamp())
 AND EXISTS(SELECT 1 FROM channel_instances c WHERE c.id=s.instance_id AND c.enabled AND c.category='push' AND c.implementation_id='web_push' AND c.config->>'public_key'=s.vapid_public_key)`

func (s *Store) webPushInstance(ctx context.Context, tx pgx.Tx, instanceID string) (WebPushInstance, error) {
	var out WebPushInstance
	rec, err := scanChannelRecord(tx.QueryRow(ctx, "SELECT "+channelInstanceColumns+" FROM channel_instances WHERE id=$1 FOR SHARE", instanceID))
	if err != nil {
		return out, conflict("notification_push_unavailable", "浏览器推送渠道不可用")
	}
	cfg, implementation, err := s.runtimeChannelRecord(rec, channel.Push)
	if err != nil || implementation != "web_push" {
		return out, conflict("notification_push_unavailable", "浏览器推送渠道不可用")
	}
	key, _ := cfg.Values["public_key"].(string)
	if !notify.ValidWebPushPublicKey(key) {
		return out, conflict("notification_push_unavailable", "浏览器推送密钥无效")
	}
	if _, err = channel.New(channel.Push, implementation, cfg); err != nil {
		return out, conflict("notification_push_unavailable", "浏览器推送密钥配置无效")
	}
	return WebPushInstance{ID: rec.item.ID, Name: rec.item.DisplayName, PublicKey: key, Version: rec.item.Version}, nil
}

func (s *Store) WebPushState(ctx context.Context, userID int64, sessionHash string) (WebPushAccountState, error) {
	out := WebPushAccountState{Instances: []WebPushInstance{}, Subscriptions: []WebPushSubscriptionInfo{}}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return out, err
	}
	defer tx.Rollback(ctx)
	var valid bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users u JOIN sessions ss ON ss.user_id=u.id WHERE u.id=$1 AND u.status='active' AND ss.token_hash=$2 AND ss.expires_at>clock_timestamp())`, userID, sessionHash).Scan(&valid); err != nil {
		return out, err
	}
	if !valid {
		return out, conflict("notification_session_expired", "登录会话已失效")
	}
	rows, err := tx.Query(ctx, `SELECT id FROM channel_instances WHERE category='push' AND implementation_id='web_push' AND enabled ORDER BY sort_order,id`)
	if err != nil {
		return out, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return out, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return out, err
	}
	for _, id := range ids {
		item, e := s.webPushInstance(ctx, tx, id)
		if e == nil {
			out.Instances = append(out.Instances, item)
		}
	}
	rows, err = tx.Query(ctx, `SELECT s.id,s.instance_id,s.endpoint_hash,s.version,s.session_token_hash=$2,`+webPushSubscriptionCurrent+`,s.active FROM notification_web_push_subscriptions s WHERE s.user_id=$1 AND (s.active OR s.id IN (SELECT old.id FROM notification_web_push_subscriptions old WHERE old.user_id=$1 AND NOT old.active ORDER BY old.id DESC LIMIT 20)) ORDER BY s.active DESC,s.id DESC`, userID, sessionHash)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var info WebPushSubscriptionInfo
		if err = rows.Scan(&info.ID, &info.InstanceID, &info.EndpointHash, &info.Version, &info.CurrentSession, &info.Active, &info.Revocable); err != nil {
			rows.Close()
			return out, err
		}
		out.Subscriptions = append(out.Subscriptions, info)
	}
	rows.Close()
	return out, rows.Err()
}

func (s *Store) webPushCrypt(userID int64, endpointHash string, raw []byte, seal bool) ([]byte, error) {
	if len(s.ProvisionEncryptionKey) != 32 {
		return nil, conflict("channel_encryption_unconfigured", "渠道凭据加密密钥未配置")
	}
	aad := []byte(fmt.Sprintf("zjmfgo:web-push-subscription:v1:%d:%s", userID, endpointHash))
	if seal {
		return secure.Seal(s.ProvisionEncryptionKey, aad, raw)
	}
	plain, err := secure.Open(s.ProvisionEncryptionKey, aad, raw)
	if err != nil {
		return nil, conflict("notification_push_subscription_unavailable", "浏览器订阅不可用")
	}
	return plain, nil
}

func (s *Store) SaveWebPushSubscription(ctx context.Context, tx pgx.Tx, userID int64, sessionHash, instanceID string, subscription notify.PushSubscription, expectedVersion int64) (WebPushSubscriptionInfo, error) {
	var out WebPushSubscriptionInfo
	if expectedVersion < 0 || !notify.ValidWebPushSubscription(subscription) {
		return out, conflict("invalid_web_push_subscription", "浏览器订阅格式或推送地址无效")
	}
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return out, err
	}
	var id int64
	if err := tx.QueryRow(ctx, `SELECT user_id FROM sessions WHERE user_id=$1 AND token_hash=$2 AND expires_at>clock_timestamp() FOR SHARE`, userID, sessionHash).Scan(&id); err != nil {
		return out, conflict("notification_session_expired", "登录会话已失效")
	}
	instance, err := s.webPushInstance(ctx, tx, instanceID)
	if err != nil {
		return out, err
	}
	hash := notificationTokenHash(subscription.Endpoint)
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,718232))`, hash); err != nil {
		return out, err
	}
	var owner, version int64
	err = tx.QueryRow(ctx, `SELECT user_id,version FROM notification_web_push_subscriptions WHERE endpoint_hash=$1 FOR UPDATE`, hash).Scan(&owner, &version)
	if err != nil && err != pgx.ErrNoRows {
		return out, err
	}
	if err == nil && owner != userID {
		return out, conflict("web_push_endpoint_owned", "此浏览器订阅已属于其他账户，请先在浏览器重新订阅")
	}
	if version != expectedVersion {
		return out, conflict("version_conflict", "浏览器订阅已变更，请重新加载")
	}
	var total int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM notification_web_push_subscriptions WHERE user_id=$1 AND active AND endpoint_hash<>$2`, userID, hash).Scan(&total); err != nil {
		return out, err
	}
	if total >= 20 {
		return out, conflict("notification_push_subscription_limit", "最多订阅20个浏览器，请先移除旧订阅")
	}
	raw, err := json.Marshal(subscription)
	if err != nil {
		return out, err
	}
	sealed, err := s.webPushCrypt(userID, hash, raw, true)
	if err != nil {
		return out, err
	}
	err = tx.QueryRow(ctx, `INSERT INTO notification_web_push_subscriptions(user_id,instance_id,endpoint_hash,subscription_ciphertext,session_token_hash,vapid_public_key)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(endpoint_hash) DO UPDATE SET instance_id=excluded.instance_id,subscription_ciphertext=excluded.subscription_ciphertext,session_token_hash=excluded.session_token_hash,vapid_public_key=excluded.vapid_public_key,active=true,version=notification_web_push_subscriptions.version+1,updated_at=now()
 RETURNING id,instance_id,endpoint_hash,version,active`, userID, instanceID, hash, sealed, sessionHash, instance.PublicKey).Scan(&out.ID, &out.InstanceID, &out.EndpointHash, &out.Version, &out.Active)
	out.CurrentSession = true
	out.Revocable = true
	return out, err
}

func (s *Store) DeleteWebPushSubscription(ctx context.Context, tx pgx.Tx, userID, id, expectedVersion int64) error {
	if err := activeNotificationUser(ctx, tx, userID); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE notification_web_push_subscriptions SET active=false,version=version+1,updated_at=now() WHERE id=$1 AND user_id=$2 AND version=$3 AND active`, id, userID, expectedVersion)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return conflict("version_conflict", "浏览器订阅已变更或不属于当前账户")
	}
	return nil
}

func (s *Store) loadWebPushSubscription(ctx context.Context, tx pgx.Tx, deliveryID int64) (*notify.PushSubscription, error) {
	var userID int64
	var hash string
	var sealed []byte
	err := tx.QueryRow(ctx, `SELECT s.user_id,s.endpoint_hash,s.subscription_ciphertext FROM notification_web_push_subscriptions s JOIN notification_deliveries d ON d.push_subscription_id=s.id WHERE d.id=$1 FOR SHARE OF s`, deliveryID).Scan(&userID, &hash, &sealed)
	if err != nil {
		return nil, err
	}
	raw, err := s.webPushCrypt(userID, hash, sealed, false)
	if err != nil {
		return nil, err
	}
	var out notify.PushSubscription
	if json.Unmarshal(raw, &out) != nil || !notify.ValidWebPushSubscription(out) || notificationTokenHash(out.Endpoint) != hash {
		return nil, conflict("notification_push_subscription_unavailable", "浏览器订阅不可用")
	}
	return &out, nil
}
