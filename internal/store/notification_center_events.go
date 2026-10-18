package store

import (
	"context"
	"encoding/json"
	"strconv"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// queueUnifiedInvoicePaid observes the exact same committed payment event as
// the legacy outboxes. It links existing mail/SMS jobs and never resends them.
func (s *Store) queueUnifiedInvoicePaid(ctx context.Context, tx pgx.Tx, invoiceID, userID int64, invoiceNo string, amount int64) error {
	payload := map[string]string{"invoice_no": invoiceNo, "amount": notificationAmount(amount)}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var eventID int64
	if err = tx.QueryRow(ctx, `INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload) VALUES('invoice.paid',$1,$2,$3,$4) ON CONFLICT(dedupe_key) DO NOTHING RETURNING id`, "invoice.paid:"+strconv.FormatInt(invoiceID, 10), userID, invoiceID, raw).Scan(&eventID); err == pgx.ErrNoRows {
		// Routes and consent are sampled exactly once. Replaying a payment hook
		// must not backfill an old event after a route or subscription is enabled.
		return nil
	} else if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO notification_deliveries(event_id,route_key,channel,instance_id,status,code,legacy_email_outbox_id,legacy_sms_outbox_id)
 SELECT $1,'legacy-'||n.channel,CASE WHEN n.channel='email' THEN 'mail' ELSE n.channel END,coalesce(m.channel_instance_id,s.channel_instance_id),'pending','legacy_outbox',n.email_outbox_id,n.sms_outbox_id FROM notification_delivery_events n LEFT JOIN email_outbox m ON m.id=n.email_outbox_id LEFT JOIN sms_outbox s ON s.id=n.sms_outbox_id WHERE n.invoice_id=$2 AND n.kind='payment_success' AND (n.email_outbox_id IS NOT NULL OR n.sms_outbox_id IS NOT NULL) ON CONFLICT(event_id,route_key) DO NOTHING`, eventID, invoiceID); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT r.id,r.channel,coalesce(r.instance_id,''),r.version,t.subject,t.body,t.version,coalesce(p.enabled,false),coalesce(p.version,0),coalesce(b.id,0),coalesce(b.target,''),coalesce(b.version,0) FROM notification_routes r JOIN notification_message_templates t ON t.id=r.template_id LEFT JOIN user_notification_preferences p ON p.user_id=$1 AND p.channel=r.channel LEFT JOIN notification_targets b ON b.user_id=$1 AND b.instance_id=r.instance_id AND b.active WHERE r.event_kind='invoice.paid' AND r.enabled AND t.active ORDER BY r.id`, userID)
	if err != nil {
		return err
	}
	type routeSnapshot struct {
		id, channel, instance, subject, body, target                        string
		routeVersion, templateVersion, prefVersion, targetID, targetVersion int64
		enabled                                                             bool
	}
	snapshots := []routeSnapshot{}
	for rows.Next() {
		var r routeSnapshot
		if err = rows.Scan(&r.id, &r.channel, &r.instance, &r.routeVersion, &r.subject, &r.body, &r.templateVersion, &r.enabled, &r.prefVersion, &r.targetID, &r.target, &r.targetVersion); err != nil {
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
		subject := notificationRender(r.subject, payload)
		body := notificationRender(r.body, payload)
		if !r.enabled {
			status, code = "skipped", "not_subscribed"
		} else if r.channel == "telegram" && r.targetID == 0 {
			status, code = "skipped", "target_unbound"
		}
		if !notificationMessageValid(r.channel, subject, body) {
			// A syntactically valid template can expand beyond provider/storage
			// limits. Record a terminal result without aborting payment or
			// leaving a poison row at the front of the delivery queue.
			status, code, subject, body = "failed", "rendered_message_invalid", "", ""
		}
		if r.channel == "inbox" {
			r.target = strconv.FormatInt(userID, 10)
		}
		if _, err = tx.Exec(ctx, `INSERT INTO notification_deliveries(event_id,route_key,route_id,route_version,template_version,preference_version,target_id,target_version,channel,instance_id,target,subject,body,status,code) VALUES($1,$2,$2,$3,$4,$5,NULLIF($6,0),$7,$8,NULLIF($9,''),$10,$11,$12,$13,$14) ON CONFLICT(event_id,route_key) DO NOTHING`, eventID, r.id, r.routeVersion, r.templateVersion, r.prefVersion, r.targetID, r.targetVersion, r.channel, r.instance, r.target, subject, body, status, code); err != nil {
			return err
		}
	}
	return nil
}

func notificationMessageValid(channelName, subject, body string) bool {
	if !utf8.ValidString(subject) || !utf8.ValidString(body) {
		return false
	}
	bodyLimit := 100000
	if channelName == "telegram" {
		bodyLimit = 4096
	}
	return utf8.RuneCountInString(subject) >= 1 && utf8.RuneCountInString(subject) <= 240 && utf8.RuneCountInString(body) >= 1 && utf8.RuneCountInString(body) <= bodyLimit
}
