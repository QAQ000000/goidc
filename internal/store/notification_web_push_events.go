package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

const notificationWebPushSubject = "账户通知"
const notificationWebPushBody = "您有新的账户通知，请登录查看。"

// Each browser is a separate recipient and delivery identity. A replay never
// reaches this function, so a later subscription cannot backfill an old event.
func (s *Store) queueWebPushNotifications(ctx context.Context, tx pgx.Tx, eventID, userID int64, kind string, due *time.Time) error {
	rows, err := tx.Query(ctx, `SELECT r.id,coalesce(r.instance_id,''),r.version,t.version,coalesce(p.enabled,false),coalesce(p.version,0),coalesce(p.event_scope,'invoice.paid'),coalesce(s.id,0),coalesce(s.version,0),
 CASE WHEN $2='service.expired' AND ($3::timestamptz AT TIME ZONE 'Asia/Shanghai')::date<(now() AT TIME ZONE 'Asia/Shanghai')::date
 THEN coalesce(r.updated_at<=$3 AND t.updated_at<=$3 AND p.updated_at<=$3 AND s.updated_at<=$3
 AND EXISTS(SELECT 1 FROM channel_instances c WHERE c.id=r.instance_id AND c.updated_at<=$3)
 AND EXISTS(SELECT 1 FROM sessions ss WHERE ss.token_hash=s.session_token_hash AND ss.created_at<=$3),false) ELSE true END
 FROM notification_routes r JOIN notification_message_templates t ON t.id=r.template_id
 LEFT JOIN user_notification_preferences p ON p.user_id=$1 AND p.channel='web_push'
 LEFT JOIN notification_web_push_subscriptions s ON s.user_id=$1 AND s.instance_id=r.instance_id AND `+webPushSubscriptionCurrent+`
 WHERE r.channel='web_push' AND r.event_kind=$2 AND r.enabled AND t.active ORDER BY r.id,s.id`, userID, kind, due)
	if err != nil {
		return err
	}
	type snapshot struct {
		id, instance, scope                                                             string
		routeVersion, templateVersion, prefVersion, subscriptionID, subscriptionVersion int64
		enabled, ready                                                                  bool
	}
	snapshots := []snapshot{}
	for rows.Next() {
		var r snapshot
		if err = rows.Scan(&r.id, &r.instance, &r.routeVersion, &r.templateVersion, &r.enabled, &r.prefVersion, &r.scope, &r.subscriptionID, &r.subscriptionVersion, &r.ready); err != nil {
			rows.Close()
			return err
		}
		snapshots = append(snapshots, r)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, r := range snapshots {
		status, code := "pending", ""
		switch {
		case !r.enabled:
			status, code = "skipped", "not_subscribed"
		case kind != "invoice.paid" && r.scope != "business":
			status, code = "skipped", "event_scope_not_subscribed"
		case r.subscriptionID == 0:
			status, code = "skipped", "target_unbound"
		case !r.ready:
			status, code = "skipped", "catchup_subscription_changed"
		}
		// Push intentionally carries no business identifiers, amounts, or rendered
		// template content: lock screens can be visible to people other than users.
		routeKey := fmt.Sprintf("%s:browser:%d", r.id, r.subscriptionID)
		if _, err = tx.Exec(ctx, `INSERT INTO notification_deliveries(event_id,route_key,route_id,route_version,template_version,preference_version,target_version,push_subscription_id,channel,instance_id,subject,body,status,code)
  VALUES($1,$2,$3,$4,$5,$6,$7,NULLIF($8,0),'web_push',NULLIF($9,''),'账户通知','您有新的账户通知，请登录查看。',$10,$11) ON CONFLICT(event_id,route_key) DO NOTHING`, eventID, routeKey, r.id, r.routeVersion, r.templateVersion, r.prefVersion, r.subscriptionVersion, r.subscriptionID, r.instance, status, code); err != nil {
			return err
		}
	}
	return nil
}
