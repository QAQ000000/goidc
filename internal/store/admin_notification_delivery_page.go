package store

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// This projection is shared by the existing center and the filtered page. Never
// add delivery bodies, target addresses, credentials or raw provider receipts.
const notificationDeliveryProjection = `SELECT d.id,e.id AS event_id,e.user_id,e.kind AS event_kind,
 CASE WHEN d.channel='mail' THEN 'email' ELSE d.channel END AS channel,coalesce(d.instance_id,'') AS instance_id,
 CASE WHEN (d.channel='mail' AND m.id IS NULL) OR (d.channel='sms' AND s.id IS NULL) THEN 'archived'
 WHEN d.channel='mail' THEN CASE m.status WHEN 'sent' THEN 'accepted' WHEN 'sending' THEN 'sending' WHEN 'pending' THEN 'pending' WHEN 'failed' THEN 'failed' WHEN 'dead_letter' THEN 'failed' ELSE 'skipped' END
 WHEN d.channel='sms' THEN CASE s.status WHEN 'sent' THEN 'accepted' WHEN 'sending' THEN 'sending' WHEN 'pending' THEN 'pending' WHEN 'failed' THEN 'failed' WHEN 'dead_letter' THEN 'failed' ELSE 'skipped' END ELSE d.status END AS status,
 CASE WHEN (d.channel='mail' AND m.id IS NULL) OR (d.channel='sms' AND s.id IS NULL) THEN 'legacy_log_removed' ELSE d.code END AS code,
 CASE WHEN d.channel='mail' THEN coalesce(m.attempts,d.attempts) WHEN d.channel='sms' THEN coalesce(s.attempts,d.attempts) ELSE d.attempts END AS attempts,
 d.provider_message_id,d.created_at,d.expires_at
 FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id
 LEFT JOIN email_outbox m ON m.id=d.legacy_email_outbox_id LEFT JOIN sms_outbox s ON s.id=d.legacy_sms_outbox_id`

type AdminNotificationDeliveryPage struct {
	Items  []NotificationDelivery `json:"items"`
	Total  int64                  `json:"total"`
	Limit  int                    `json:"limit"`
	Offset int                    `json:"offset"`
}

// AdminNotificationDeliveriesPage lists only the unified business event catalog.
// Ticket and campaign history retain their separate, permission-aware log APIs.
func (s *Store) AdminNotificationDeliveriesPage(ctx context.Context, actor int64, filters url.Values, limit, offset int) (AdminNotificationDeliveryPage, error) {
	result := AdminNotificationDeliveryPage{Items: []NotificationDelivery{}, Limit: limit, Offset: offset}
	invalid := func() error {
		return &AccountBusinessError{400, "invalid_notification_filter", "通知历史筛选无效"}
	}
	for _, key := range []string{"event_kind", "channel", "status", "from", "to"} {
		if values, exists := filters[key]; exists && len(values) != 1 {
			return result, invalid()
		}
	}
	kind, channelName, status := strings.TrimSpace(filters.Get("event_kind")), strings.TrimSpace(filters.Get("channel")), strings.TrimSpace(filters.Get("status"))
	if _, ok := notificationEventDescriptor(kind); kind != "" && !ok {
		return result, &AccountBusinessError{400, "invalid_notification_event", "通知事件无效"}
	}
	if channelName != "" && !slices.Contains([]string{"email", "sms", "inbox", "telegram", "web_push"}, channelName) {
		return result, invalid()
	}
	if status != "" && !slices.Contains([]string{"pending", "sending", "accepted", "failed", "unknown", "skipped", "archived"}, status) {
		return result, invalid()
	}
	var start, end *time.Time
	zone := time.FixedZone("Asia/Shanghai", 8*60*60)
	for _, key := range []string{"from", "to"} {
		if value := strings.TrimSpace(filters.Get(key)); value != "" {
			date, err := time.ParseInLocation("2006-01-02", value, zone)
			if err != nil {
				return result, invalid()
			}
			if key == "from" {
				start = &date
			} else {
				date = date.AddDate(0, 0, 1)
				end = &date
			}
		}
	}
	if start != nil && end != nil && !start.Before(*end) {
		return result, invalid()
	}
	if result.Limit < 1 || result.Limit > 100 {
		result.Limit = 25
	}
	if result.Offset < 0 {
		result.Offset = 0
	}
	tx, err := s.Pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return result, err
	}
	defer tx.Rollback(ctx)
	var allowed bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM administrators a LEFT JOIN admin_roles r ON r.id=a.admin_role_id
 WHERE a.id=$1 AND a.status='active' AND (a.role='super_admin' OR (a.role='admin' AND r.active AND 'notifications.view'=ANY(r.permissions))))`, actor).Scan(&allowed); err != nil {
		return result, err
	}
	if !allowed {
		return result, &AccountBusinessError{403, "permission_denied", "没有读取通知历史的权限"}
	}
	customer, err := accountLogCustomerQuery(ctx, tx, filters, actor)
	if err != nil {
		return result, err
	}
	var customerID int64
	if customer != nil {
		customerID = *customer
	}
	catalog := []string{}
	for _, event := range NotificationEventCatalog() {
		catalog = append(catalog, event.Kind)
	}
	from := ` FROM (` + notificationDeliveryProjection + `) delivery WHERE admin_customer_allowed($1,delivery.user_id)
 AND ($2::bigint=0 OR delivery.user_id=$2) AND ($3='' OR delivery.event_kind=$3)
 AND ($4='' OR delivery.channel=$4) AND ($5='' OR delivery.status=$5)
 AND ($6::timestamptz IS NULL OR delivery.created_at >= $6) AND ($7::timestamptz IS NULL OR delivery.created_at < $7)
 AND delivery.event_kind=ANY($8::text[])`
	args := []any{actor, customerID, kind, channelName, status, start, end, catalog}
	if err = tx.QueryRow(ctx, "SELECT count(*)"+from, args...).Scan(&result.Total); err != nil {
		return result, err
	}
	rows, err := tx.Query(ctx, "SELECT delivery.*"+from+" ORDER BY delivery.id DESC LIMIT $9 OFFSET $10", append(args, result.Limit, result.Offset)...)
	if err != nil {
		return result, err
	}
	for rows.Next() {
		var item NotificationDelivery
		if err = rows.Scan(&item.ID, &item.EventID, &item.UserID, &item.EventKind, &item.Channel, &item.InstanceID, &item.Status, &item.Code, &item.Attempts, &item.ProviderMessageID, &item.CreatedAt, &item.ExpiresAt); err != nil {
			rows.Close()
			return result, err
		}
		result.Items = append(result.Items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return result, err
	}
	return result, tx.Commit(ctx)
}
