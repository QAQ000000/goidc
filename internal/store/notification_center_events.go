package store

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
)

// queueUnifiedInvoicePaid observes the exact same committed payment event as
// the legacy outboxes. It links existing mail/SMS jobs and never resends them.
func (s *Store) queueUnifiedInvoicePaid(ctx context.Context, tx pgx.Tx, invoiceID, userID int64, invoiceNo string, amount int64) error {
	return s.QueueLifecycleNotification(ctx, tx, "invoice.paid", invoiceID, 0, 0, "invoice.paid:"+strconv.FormatInt(invoiceID, 10), nil)
}

// QueueLifecycleNotification observes a business fact inside its transaction.
// The referenced resources determine the recipient and all message variables;
// callers cannot override ownership, amounts, identifiers or service metadata.
// Routes and consent are sampled once, even when all routes are disabled.
func (s *Store) QueueLifecycleNotification(ctx context.Context, tx pgx.Tx, kind string, invoiceID, orderID, serviceID int64, dedupeKey string, input map[string]string) error {
	event, ok := notificationEventDescriptor(kind)
	if !ok || strings.TrimSpace(dedupeKey) == "" || len(dedupeKey) > 256 || strings.ContainsRune(dedupeKey, 0) || invoiceID < 0 || orderID < 0 || serviceID < 0 {
		return conflict("invalid_notification_event", "通知事件无效")
	}
	for key, value := range input {
		if !event.Reminder || (key != "stage" && key != "due_date") || strings.ContainsRune(value, 0) {
			return conflict("invalid_notification_payload", "通知事件变量无效")
		}
	}
	if due := input["due_date"]; due != "" {
		if _, err := time.Parse("2006-01-02", due); err != nil {
			return conflict("invalid_notification_payload", "通知提醒日期无效")
		}
	}
	stage := 0
	if value := input["stage"]; value != "" {
		var err error
		stage, err = strconv.Atoi(value)
		if err != nil || stage < 0 || stage > 365 {
			return conflict("invalid_notification_payload", "通知提醒阶段无效")
		}
	}
	if (event.Resource == "invoice" && invoiceID == 0) || (event.Resource == "order" && orderID == 0) || (event.Resource == "service" && serviceID == 0) {
		return conflict("invalid_notification_resource", "通知资源无效")
	}
	values := map[string]string{}
	var userID, invoiceUser, serviceUser, invoiceOrder, ownershipGeneration int64
	var due *time.Time
	if invoiceID > 0 {
		var number string
		var amount int64
		if err := tx.QueryRow(ctx, `SELECT user_id,coalesce(order_id,0),invoice_no,amount_cents,due_at FROM invoices WHERE id=$1`, invoiceID).Scan(&invoiceUser, &invoiceOrder, &number, &amount, &due); err != nil {
			return err
		}
		userID = invoiceUser
		values["invoice_no"], values["amount"] = number, notificationAmount(amount)
		if orderID == 0 && serviceID == 0 {
			orderID = invoiceOrder
		}
	}
	if serviceID > 0 {
		var serviceOrder int64
		var id, name, product, domain string
		if err := tx.QueryRow(ctx, `SELECT s.user_id,s.order_id,s.id::text,s.name,p.name,s.domain,s.next_due_at,service_ownership_generation(s.id) FROM services s JOIN products p ON p.id=s.product_id WHERE s.id=$1`, serviceID).Scan(&serviceUser, &serviceOrder, &id, &name, &product, &domain, &due, &ownershipGeneration); err != nil {
			return err
		}
		if (userID > 0 && userID != serviceUser) || (orderID > 0 && orderID != serviceOrder) {
			return conflict("invalid_notification_resource", "通知资源归属不一致")
		}
		// The purchase order remains the original customer's financial fact
		// after an ownership transfer. Preserve the historical purchase binding
		// until the first transfer; later service events use the current owner.
		if orderID == 0 && ownershipGeneration == 0 {
			orderID = serviceOrder
		}
		userID = serviceUser
		values["service_id"], values["service_name"], values["product_name"], values["domain"] = id, name, product, domain
		if invoiceOrder > 0 && orderID == 0 {
			// An invoice's actual order is still part of that invoice's
			// provenance and must pass the strict owner check below.
			orderID = invoiceOrder
		}
		if invoiceID > 0 && invoiceOrder != serviceOrder {
			var linked bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM invoice_items WHERE invoice_id=$1 AND service_id=$2)`, invoiceID, serviceID).Scan(&linked); err != nil {
				return err
			}
			if !linked {
				return conflict("invalid_notification_resource", "账单与服务不关联")
			}
		}
	}
	if orderID > 0 {
		var owner, amount int64
		var number string
		if err := tx.QueryRow(ctx, `SELECT user_id,order_no,total_cents FROM orders WHERE id=$1`, orderID).Scan(&owner, &number, &amount); err != nil {
			return err
		}
		if (userID > 0 && owner != userID) || (invoiceID > 0 && serviceID == 0 && invoiceOrder != orderID) {
			return conflict("invalid_notification_resource", "通知资源归属不一致")
		}
		userID = owner
		values["order_no"] = number
		if invoiceID == 0 {
			values["amount"] = notificationAmount(amount)
		}
	}
	// Keep exact instants separate from human dates. A renewal within the same
	// Shanghai calendar day must invalidate an already queued reminder as well.
	payload := map[string]string{}
	if serviceID > 0 {
		payload["ownership_generation"] = strconv.FormatInt(ownershipGeneration, 10)
		if ownershipGeneration > 0 {
			dedupeKey += ":ownership:" + strconv.FormatInt(ownershipGeneration, 10)
		}
	}
	if due != nil {
		values["due_date"] = due.In(notificationShanghai).Format("2006-01-02")
		payload["resource_due_at"] = due.UTC().Format(time.RFC3339Nano)
	}
	values["days_left"] = strconv.Itoa(stage)
	for _, variable := range event.Variables {
		payload[variable.Key] = values[variable.Key]
	}
	if event.Reminder {
		payload["stage"] = strconv.Itoa(stage)
		payload["reminder_date"] = input["due_date"]
		if payload["reminder_date"] == "" {
			payload["reminder_date"] = time.Now().In(notificationShanghai).Format("2006-01-02")
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	var eventID int64
	if err = tx.QueryRow(ctx, `INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,order_id,service_id,payload) VALUES($1,$2,$3,NULLIF($4::bigint,0),NULLIF($5::bigint,0),NULLIF($6::bigint,0),$7) ON CONFLICT(dedupe_key) DO NOTHING RETURNING id`, kind, dedupeKey, userID, invoiceID, orderID, serviceID, raw).Scan(&eventID); err == pgx.ErrNoRows {
		// Routes and consent are sampled exactly once. Replaying a payment hook
		// must not backfill an old event after a route or subscription is enabled.
		return nil
	} else if err != nil {
		return err
	}
	if err = s.linkLifecycleLegacyOutboxes(ctx, tx, eventID, kind, invoiceID, serviceID, stage); err != nil {
		return err
	}
	// Cross-day recovery must not turn a newly enabled route, subscription or
	// binding into consent for historical messages. updated_at is deliberately
	// conservative: without a full configuration history, a later edit cannot
	// prove that this route was ready at the resource's deadline. Same-day
	// reminders retain their existing current-configuration semantics.
	rows, err := tx.Query(ctx, `SELECT r.id,r.channel,coalesce(r.instance_id,''),r.version,t.subject,t.body,t.version,coalesce(p.enabled,false),coalesce(p.version,0),coalesce(b.id,0),coalesce(b.target,''),coalesce(b.version,0),coalesce(p.event_scope,'invoice.paid'),
 CASE WHEN $2='service.expired' AND
   ($3::timestamptz AT TIME ZONE 'Asia/Shanghai')::date < (now() AT TIME ZONE 'Asia/Shanghai')::date
 THEN coalesce(r.updated_at<=$3 AND t.updated_at<=$3 AND p.updated_at<=$3
   AND (r.channel='inbox' OR (b.updated_at<=$3 AND EXISTS(
     SELECT 1 FROM channel_instances c WHERE c.id=r.instance_id AND c.enabled AND c.updated_at<=$3))),false)
 ELSE true END
 FROM notification_routes r JOIN notification_message_templates t ON t.id=r.template_id
 LEFT JOIN user_notification_preferences p ON p.user_id=$1 AND p.channel=r.channel
 LEFT JOIN notification_targets b ON b.user_id=$1 AND b.instance_id=r.instance_id AND b.active
 WHERE r.event_kind=$2 AND r.channel<>'web_push' AND r.enabled AND t.active ORDER BY r.id`, userID, kind, due)
	if err != nil {
		return err
	}
	type routeSnapshot struct {
		id, channel, instance, subject, body, target, scope                 string
		routeVersion, templateVersion, prefVersion, targetID, targetVersion int64
		enabled, readyAtDeadline                                            bool
	}
	snapshots := []routeSnapshot{}
	for rows.Next() {
		var r routeSnapshot
		if err = rows.Scan(&r.id, &r.channel, &r.instance, &r.routeVersion, &r.subject, &r.body, &r.templateVersion, &r.enabled, &r.prefVersion, &r.targetID, &r.target, &r.targetVersion, &r.scope, &r.readyAtDeadline); err != nil {
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
		subject := notificationLifecycleRender(r.subject, payload)
		body := notificationLifecycleRender(r.body, payload)
		if !r.enabled {
			status, code = "skipped", "not_subscribed"
		} else if kind != "invoice.paid" && r.scope != "business" {
			status, code = "skipped", "event_scope_not_subscribed"
		} else if r.channel == "telegram" && r.targetID == 0 {
			status, code = "skipped", "target_unbound"
		} else if !r.readyAtDeadline {
			status, code = "skipped", "catchup_subscription_changed"
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
	return s.queueWebPushNotifications(ctx, tx, eventID, userID, kind, due)
}

var notificationShanghai = time.FixedZone("Asia/Shanghai", 8*60*60)
var notificationPlaceholder = regexp.MustCompile(`\{\{[a-z_]+\}\}`)

// Expand once: a resource name containing template syntax remains plain text.
func notificationLifecycleRender(value string, payload map[string]string) string {
	return notificationPlaceholder.ReplaceAllStringFunc(value, func(token string) string {
		return payload[strings.TrimSuffix(strings.TrimPrefix(token, "{{"), "}}")]
	})
}

func (s *Store) linkLifecycleLegacyOutboxes(ctx context.Context, tx pgx.Tx, eventID int64, kind string, invoiceID, serviceID int64, stage int) error {
	legacy := map[string]string{"invoice.paid": "payment_success", "service.provisioned": "service_provision_success", "service.suspended": "service_suspended", "service.expiry_reminder": "service_expiry_reminder", "order.payment_reminder": "order_payment_reminder", "invoice.overdue": "invoice_overdue_reminder"}[kind]
	if legacy == "" {
		return nil
	}
	// Both records use DEFAULT now(), PostgreSQL's exact transaction start
	// timestamp. Linking only records from this transaction identifies the old
	// outbox actually produced by this fact, without guessing a legacy date:
	// legacy CURRENT_DATE/Go dates may differ from the new Shanghai business day.
	// In particular, an old unlinked suspension cannot steal a new transition.
	_, err := tx.Exec(ctx, `INSERT INTO notification_deliveries(event_id,route_key,channel,instance_id,status,code,legacy_email_outbox_id,legacy_sms_outbox_id)
 SELECT $1,'legacy-'||n.channel,CASE WHEN n.channel='email' THEN 'mail' ELSE n.channel END,coalesce(m.channel_instance_id,s.channel_instance_id),'pending','legacy_outbox',n.email_outbox_id,n.sms_outbox_id
 FROM notification_delivery_events n LEFT JOIN email_outbox m ON m.id=n.email_outbox_id LEFT JOIN sms_outbox s ON s.id=n.sms_outbox_id
 WHERE n.kind=$2 AND (($3::bigint>0 AND n.invoice_id=$3) OR ($3=0 AND $4::bigint>0 AND n.service_id=$4))
 AND n.stage=$5 AND n.created_at=(SELECT e.created_at FROM notification_events e WHERE e.id=$1)
 AND (n.email_outbox_id IS NOT NULL OR n.sms_outbox_id IS NOT NULL)
 AND NOT EXISTS(SELECT 1 FROM notification_deliveries d WHERE (n.email_outbox_id IS NOT NULL AND d.legacy_email_outbox_id=n.email_outbox_id) OR (n.sms_outbox_id IS NOT NULL AND d.legacy_sms_outbox_id=n.sms_outbox_id))
 ON CONFLICT DO NOTHING`, eventID, legacy, invoiceID, serviceID, stage)
	return err
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
