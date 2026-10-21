package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// EnqueueServiceExpiryReminders creates the two customer-facing expiry
// reminders supported by the first scheduler batch. It only creates outbox
// rows; delivery, retries, billing and suspension remain separate concerns.
func (s *Store) EnqueueServiceExpiryReminders(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var enabled bool
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT enabled, settings->'expiry_days' FROM scheduler_task_settings WHERE id=1`).Scan(&enabled, &raw); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	var expiryDays []int
	if err := json.Unmarshal(raw, &expiryDays); err != nil || len(expiryDays) == 0 {
		expiryDays = []int{}
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `
SELECT s.id,s.user_id,s.next_due_at,p.name,u.email,u.email_verified,u.phone,u.phone_verified,
       ((s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date)::int AS days_left
FROM services s
JOIN products p ON p.id=s.product_id
JOIN users u ON u.id=s.user_id
WHERE s.status IN ('active','suspended') AND u.status='active'
  AND NOT s.import_review_required
  AND s.billing_cycle <> 'once'
  AND s.next_due_at>now()
  AND ((s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date)=ANY($2::int[])
  AND EXISTS(SELECT 1 FROM notification_templates t
    WHERE t.active AND t.kind='service_expiry_reminder'
    AND ((t.channel='email' AND u.email_verified AND btrim(u.email)<>'' AND EXISTS(
           SELECT 1 FROM channel_instances c WHERE c.category='mail' AND c.enabled))
      OR (t.channel='sms' AND u.phone_verified AND u.phone ~ '^[+][1-9][0-9]{7,14}$' AND EXISTS(
           SELECT 1 FROM channel_instances c WHERE c.category='sms' AND c.enabled)))
    AND NOT EXISTS(SELECT 1 FROM notification_delivery_events e
      WHERE e.service_id=s.id AND e.channel=t.channel AND e.kind=t.kind
       AND e.ownership_generation=service_ownership_generation(s.id)
       AND e.stage=(s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date
       AND (e.resource_due_at=s.next_due_at OR (e.resource_due_at IS NULL
        AND e.due_date BETWEEN (s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-1
                           AND (s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date+1))))
ORDER BY s.next_due_at,s.id
FOR UPDATE OF s SKIP LOCKED LIMIT $1`, limit, expiryDays)
	if err != nil {
		return 0, err
	}
	items := make([]expiryReminderCandidate, 0)
	for rows.Next() {
		var item expiryReminderCandidate
		if err = rows.Scan(&item.serviceID, &item.userID, &item.due, &item.product, &item.email, &item.emailVerified, &item.phone, &item.phoneVerified, &item.stage); err != nil {
			rows.Close()
			return 0, err
		}
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	queued := 0
	for _, item := range items {
		dueDate := item.due.In(notificationShanghai).Format("2006-01-02")
		if item.emailVerified && strings.TrimSpace(item.email) != "" {
			ok, e := s.enqueueServiceExpiryEmail(ctx, tx, item, dueDate)
			if e != nil {
				return 0, e
			}
			if ok {
				queued++
			}
		}
		if item.phoneVerified && strings.TrimSpace(item.phone) != "" {
			ok, e := s.enqueueServiceExpirySMS(ctx, tx, item, dueDate)
			if e != nil {
				return 0, e
			}
			if ok {
				queued++
			}
		}
	}
	if err = s.enqueueUnifiedServiceDeadlines(ctx, tx, limit, expiryDays); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}

type expiryReminderCandidate struct {
	serviceID, userID int64
	due               time.Time
	product, email    string
	emailVerified     bool
	phone             string
	phoneVerified     bool
	stage             int
}

func (s *Store) enqueueServiceExpiryEmail(ctx context.Context, tx pgx.Tx, item expiryReminderCandidate, dueDate string) (bool, error) {
	valid, err := serviceExpiryCandidateStillDue(ctx, tx, item, "email")
	if err != nil || !valid {
		return false, err
	}
	var subject, body, instanceID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='service_expiry_reminder'`).Scan(&subject, &body, &active); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !active {
		return false, nil
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	body = renderExpiryTemplate(body, item.product, dueDate, item.stage)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date,resource_due_at)
SELECT $1,$2,'email','service_expiry_reminder',$3,$4::date,$5
WHERE NOT EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.service_id=$1
 AND n.ownership_generation=service_ownership_generation($1)
 AND n.channel='email' AND n.kind='service_expiry_reminder' AND n.stage=$3
 AND (n.resource_due_at=$5 OR (n.resource_due_at IS NULL AND n.due_date BETWEEN $4::date-1 AND $4::date+1)))
ON CONFLICT DO NOTHING
RETURNING id`, item.serviceID, item.userID, item.stage, dueDate, item.due).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	var outboxID int64
	if err := tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,expires_at,channel_instance_id)
VALUES($1,$2,$3,'service_expiry_reminder',now()+interval '3 days',$4) RETURNING id`, item.email, subject, body, instanceID).Scan(&outboxID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET email_outbox_id=$2 WHERE id=$1", eventID, outboxID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) enqueueServiceExpirySMS(ctx context.Context, tx pgx.Tx, item expiryReminderCandidate, dueDate string) (bool, error) {
	if !ValidSMSRecipient(item.phone) {
		return false, nil
	}
	valid, err := serviceExpiryCandidateStillDue(ctx, tx, item, "sms")
	if err != nil || !valid {
		return false, err
	}
	var body string
	var templateID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT body,provider_template_id,active FROM notification_templates WHERE channel='sms' AND kind='service_expiry_reminder'`).Scan(&body, &templateID, &active); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !active {
		return false, nil
	}
	body = renderExpiryTemplate(body, item.product, dueDate, item.stage)
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date,resource_due_at)
SELECT $1,$2,'sms','service_expiry_reminder',$3,$4::date,$5
WHERE NOT EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.service_id=$1
 AND n.ownership_generation=service_ownership_generation($1)
 AND n.channel='sms' AND n.kind='service_expiry_reminder' AND n.stage=$3
 AND (n.resource_due_at=$5 OR (n.resource_due_at IS NULL AND n.due_date BETWEEN $4::date-1 AND $4::date+1)))
ON CONFLICT DO NOTHING
RETURNING id`, item.serviceID, item.userID, item.stage, dueDate, item.due).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	outboxID, err := s.queueSMSWithTemplateID(ctx, tx, item.phone, body, "service_expiry_reminder", "", time.Now().Add(72*time.Hour), templateID, instanceID)
	if err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET sms_outbox_id=$2 WHERE id=$1", eventID, outboxID); err != nil {
		return false, err
	}
	return true, nil
}

func renderExpiryTemplate(body, product, dueDate string, stage int) string {
	days := fmt.Sprintf("%d", stage)
	body = strings.ReplaceAll(body, "{{product_name}}", product)
	body = strings.ReplaceAll(body, "{{due_date}}", dueDate)
	return strings.ReplaceAll(body, "{{days_left}}", days)
}

// EnqueueBillingNotifications schedules the two billing reminders owned by
// the worker. The invoice/date/channel key makes the scan safe to repeat and
// keeps the event and its outbox row in one transaction.
func (s *Store) EnqueueBillingNotifications(ctx context.Context, limit int) (int, error) {
	return s.enqueueBillingNotifications(ctx, limit, "")
}

func (s *Store) enqueueBillingNotifications(ctx context.Context, limit int, onlyKind string) (int, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	var enabled bool
	var settings struct {
		BillingGraceDays int `json:"billing_grace_days"`
	}
	var raw []byte
	if err := s.Pool.QueryRow(ctx, `SELECT enabled, settings FROM scheduler_task_settings WHERE id=1`).Scan(&enabled, &raw); err != nil {
		return 0, err
	}
	if !enabled {
		return 0, nil
	}
	if json.Unmarshal(raw, &settings) != nil || settings.BillingGraceDays < 0 || settings.BillingGraceDays > 365 {
		settings.BillingGraceDays = 0
	}
	rows, err := s.Pool.Query(ctx, `
SELECT i.id,i.user_id,i.invoice_no,i.amount_cents,i.due_at,COALESCE(o.order_no,''),
       CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
            THEN 'order_payment_reminder' ELSE 'invoice_overdue_reminder' END,
       u.email,u.email_verified,u.phone,u.phone_verified
FROM invoices i
JOIN users u ON u.id=i.user_id
LEFT JOIN orders o ON o.id=i.order_id
WHERE i.status='unpaid' AND NOT i.import_review_required AND u.status='active'
  AND ((o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now())
       OR (i.due_at < now() - ($3::int * interval '1 day')))
  AND ($2='' OR (CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
                     THEN 'order_payment_reminder' ELSE 'invoice_overdue_reminder' END)=$2)
  AND EXISTS (
    SELECT 1 FROM notification_templates t
    WHERE t.active AND t.kind=CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
                                  THEN 'order_payment_reminder' ELSE 'invoice_overdue_reminder' END
      AND ((t.channel='email' AND u.email_verified AND btrim(u.email)<>'' AND EXISTS (
              SELECT 1 FROM channel_instances c WHERE c.category='mail' AND c.enabled))
        OR (t.channel='sms' AND u.phone_verified AND u.phone ~ '^[+][1-9][0-9]{7,14}$' AND EXISTS (
              SELECT 1 FROM channel_instances c WHERE c.category='sms' AND c.enabled)))
      AND NOT EXISTS (SELECT 1 FROM notification_delivery_events e
        WHERE e.invoice_id=i.id AND e.channel=t.channel AND e.kind=t.kind
          AND e.stage=0 AND ((e.resource_due_at IS NULL
           AND (e.created_at AT TIME ZONE 'Asia/Shanghai')::date=(now() AT TIME ZONE 'Asia/Shanghai')::date)
          OR (e.resource_due_at=i.due_at AND e.due_date=(now() AT TIME ZONE 'Asia/Shanghai')::date))))
ORDER BY i.due_at,i.id
LIMIT $1`, limit, onlyKind, settings.BillingGraceDays)
	if err != nil {
		return 0, err
	}
	items := make([]billingNotificationCandidate, 0)
	for rows.Next() {
		var item billingNotificationCandidate
		if err = rows.Scan(&item.invoiceID, &item.userID, &item.invoiceNo, &item.amountCents, &item.dueAt,
			&item.orderNo, &item.kind, &item.email, &item.emailVerified, &item.phone, &item.phoneVerified); err != nil {
			rows.Close()
			return 0, err
		}
		item.graceDays = settings.BillingGraceDays
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	sort.Slice(items, func(i, j int) bool { return items[i].invoiceID < items[j].invoiceID })
	queued := 0
	for _, item := range items {
		if item.emailVerified && strings.TrimSpace(item.email) != "" {
			ok, queueErr := s.enqueueBillingEmail(ctx, tx, item)
			if queueErr != nil {
				return 0, queueErr
			}
			if ok {
				queued++
			}
		}
		if item.phoneVerified && strings.TrimSpace(item.phone) != "" {
			ok, queueErr := s.enqueueBillingSMS(ctx, tx, item)
			if queueErr != nil {
				return 0, queueErr
			}
			if ok {
				queued++
			}
		}
	}
	if err = s.enqueueUnifiedBillingReminders(ctx, tx, limit, onlyKind, settings.BillingGraceDays); err != nil {
		return 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}

// These names make the individual scheduler responsibilities available to
// callers and tests while sharing the same transaction and idempotency key.
func (s *Store) EnqueueUnpaidOrderReminders(ctx context.Context, limit int) (int, error) {
	return s.enqueueBillingNotifications(ctx, limit, "order_payment_reminder")
}

func (s *Store) EnqueueOverdueInvoiceReminders(ctx context.Context, limit int) (int, error) {
	return s.enqueueBillingNotifications(ctx, limit, "invoice_overdue_reminder")
}

type billingNotificationCandidate struct {
	invoiceID, userID, amountCents int64
	dueAt                          time.Time
	invoiceNo, orderNo, kind       string
	graceDays                      int
	email                          string
	emailVerified                  bool
	phone                          string
	phoneVerified                  bool
}

func (s *Store) enqueueBillingEmail(ctx context.Context, tx pgx.Tx, item billingNotificationCandidate) (bool, error) {
	valid, err := billingCandidateStillDue(ctx, tx, &item)
	if err != nil || !valid {
		return false, err
	}
	if valid, err = legacyRecipientCurrent(ctx, tx, item.userID, item.email, "email"); err != nil || !valid {
		return false, err
	}
	var subject, body, instanceID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind=$1`, item.kind).Scan(&subject, &body, &active); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !active {
		return false, nil
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	body = renderBillingTemplate(body, item)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date,resource_due_at)
SELECT NULL,$1,$2,'email',$3,0,(now() AT TIME ZONE 'Asia/Shanghai')::date,$4
WHERE NOT EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.invoice_id=$2
 AND n.channel='email' AND n.kind=$3 AND n.stage=0
 AND ((n.resource_due_at IS NULL
   AND (n.created_at AT TIME ZONE 'Asia/Shanghai')::date=(now() AT TIME ZONE 'Asia/Shanghai')::date)
  OR (n.resource_due_at=$4 AND n.due_date=(now() AT TIME ZONE 'Asia/Shanghai')::date)))
ON CONFLICT DO NOTHING
RETURNING id`, item.userID, item.invoiceID, item.kind, item.dueAt).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	var outboxID int64
	if err := tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,expires_at,channel_instance_id)
VALUES($1,$2,$3,$4,now()+interval '7 days',$5) RETURNING id`, item.email, subject, body, item.kind, instanceID).Scan(&outboxID); err != nil {
		return false, err
	}
	if _, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET email_outbox_id=$2 WHERE id=$1", eventID, outboxID); err != nil {
		return false, err
	}
	return true, nil
}

func (s *Store) enqueueBillingSMS(ctx context.Context, tx pgx.Tx, item billingNotificationCandidate) (bool, error) {
	if !ValidSMSRecipient(item.phone) {
		return false, nil
	}
	valid, err := billingCandidateStillDue(ctx, tx, &item)
	if err != nil || !valid {
		return false, err
	}
	if valid, err = legacyRecipientCurrent(ctx, tx, item.userID, item.phone, "sms"); err != nil || !valid {
		return false, err
	}
	var body, templateID, instanceID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT body,provider_template_id,active FROM notification_templates WHERE channel='sms' AND kind=$1`, item.kind).Scan(&body, &templateID, &active); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if !active {
		return false, nil
	}
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	body = renderBillingTemplate(body, item)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date,resource_due_at)
SELECT NULL,$1,$2,'sms',$3,0,(now() AT TIME ZONE 'Asia/Shanghai')::date,$4
WHERE NOT EXISTS(SELECT 1 FROM notification_delivery_events n WHERE n.invoice_id=$2
 AND n.channel='sms' AND n.kind=$3 AND n.stage=0
 AND ((n.resource_due_at IS NULL
   AND (n.created_at AT TIME ZONE 'Asia/Shanghai')::date=(now() AT TIME ZONE 'Asia/Shanghai')::date)
  OR (n.resource_due_at=$4 AND n.due_date=(now() AT TIME ZONE 'Asia/Shanghai')::date)))
ON CONFLICT DO NOTHING
RETURNING id`, item.userID, item.invoiceID, item.kind, item.dueAt).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	outboxID, err := s.queueSMSWithTemplateID(ctx, tx, item.phone, body, item.kind, "", time.Now().Add(7*24*time.Hour), templateID, instanceID)
	if err != nil {
		return false, err
	}
	if _, err = tx.Exec(ctx, "UPDATE notification_delivery_events SET sms_outbox_id=$2 WHERE id=$1", eventID, outboxID); err != nil {
		return false, err
	}
	return true, nil
}

func billingCandidateStillDue(ctx context.Context, tx pgx.Tx, item *billingNotificationCandidate) (bool, error) {
	var status string
	var reviewed bool
	var dueAt, observedAt time.Time
	if err := tx.QueryRow(ctx, "SELECT status,due_at,amount_cents,import_review_required,now() FROM invoices WHERE id=$1 FOR UPDATE", item.invoiceID).Scan(&status, &dueAt, &item.amountCents, &reviewed, &observedAt); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if status != "unpaid" || reviewed {
		return false, nil
	}
	item.dueAt = dueAt
	if item.kind == "invoice_overdue_reminder" {
		return dueAt.Before(observedAt.Add(-time.Duration(item.graceDays) * 24 * time.Hour)), nil
	}
	var orderStatus string
	if err := tx.QueryRow(ctx, "SELECT COALESCE(o.status,'') FROM invoices i LEFT JOIN orders o ON o.id=i.order_id WHERE i.id=$1", item.invoiceID).Scan(&orderStatus); err != nil {
		return false, err
	}
	if orderStatus != "pending" || !dueAt.After(observedAt) {
		return false, nil
	}
	return true, nil
}

func renderBillingTemplate(body string, item billingNotificationCandidate) string {
	amount := fmt.Sprintf("¥%d.%02d", item.amountCents/100, item.amountCents%100)
	body = strings.ReplaceAll(body, "{{order_no}}", item.orderNo)
	body = strings.ReplaceAll(body, "{{invoice_no}}", item.invoiceNo)
	body = strings.ReplaceAll(body, "{{amount}}", amount)
	return strings.ReplaceAll(body, "{{due_date}}", item.dueAt.In(notificationShanghai).Format("2006-01-02"))
}

// The unified scans are intentionally independent of legacy address, template
// and channel availability. A Telegram-only customer must not disappear from
// the candidate set. Legacy outboxes and their affected counts remain intact.
func (s *Store) enqueueUnifiedServiceDeadlines(ctx context.Context, tx pgx.Tx, limit int, stages []int) error {
	rows, err := tx.Query(ctx, `SELECT s.id,s.next_due_at,c.stage,c.kind,
 (s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date::text
 FROM services s CROSS JOIN LATERAL (SELECT
  CASE WHEN s.next_due_at<=now() THEN 0 ELSE
   ((s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date)::int END AS stage,
  CASE WHEN s.next_due_at<=now() THEN 'service.expired' ELSE 'service.expiry_reminder' END AS kind
 ) c
 WHERE s.status IN ('active','suspended') AND NOT s.import_review_required
 AND s.billing_cycle<>'once' AND s.next_due_at IS NOT NULL
 AND ((s.next_due_at>now() AND
       ((s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date)=ANY($2::int[]))
      OR (s.next_due_at<=now() AND s.next_due_at>=now()-interval '72 hours'))
 AND EXISTS(SELECT 1 FROM notification_routes r JOIN notification_message_templates m ON m.id=r.template_id
  WHERE r.event_kind=c.kind AND r.enabled AND m.active)
 AND NOT EXISTS(SELECT 1 FROM notification_events e WHERE e.dedupe_key=
  c.kind||':'||s.id::text||':'||(extract(epoch FROM s.next_due_at)*1000000)::bigint::text||':'||c.stage::text
   ||CASE WHEN service_ownership_generation(s.id)>0
     THEN ':ownership:'||service_ownership_generation(s.id)::text ELSE '' END
  OR (c.kind='service.expired' AND e.kind=c.kind AND e.service_id=s.id
      AND coalesce(e.payload->>'ownership_generation','0')=service_ownership_generation(s.id)::text
      AND (e.payload->>'resource_due_at')::timestamptz=s.next_due_at))
 ORDER BY s.id FOR UPDATE OF s SKIP LOCKED LIMIT $1`, limit, stages)
	if err != nil {
		return err
	}
	type deadline struct {
		id            int64
		due           time.Time
		stage         int
		kind, dueDate string
	}
	items := []deadline{}
	for rows.Next() {
		var item deadline
		if err = rows.Scan(&item.id, &item.due, &item.stage, &item.kind, &item.dueDate); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		key := fmt.Sprintf("%s:%d:%d:%d", item.kind, item.id, item.due.UnixMicro(), item.stage)
		if err = s.QueueLifecycleNotification(ctx, tx, item.kind, 0, 0, item.id, key, map[string]string{"stage": fmt.Sprint(item.stage), "due_date": item.dueDate}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) enqueueUnifiedBillingReminders(ctx context.Context, tx pgx.Tx, limit int, onlyKind string, graceDays int) error {
	rows, err := tx.Query(ctx, `WITH candidates AS (
 SELECT i.id,i.due_at,coalesce(i.order_id,0) AS order_id,
 CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
 THEN 'order.payment_reminder' ELSE 'invoice.overdue' END AS kind,
 (now() AT TIME ZONE 'Asia/Shanghai')::date::text AS reminder_date
 FROM invoices i LEFT JOIN orders o ON o.id=i.order_id
 WHERE i.status='unpaid' AND NOT i.import_review_required
 AND ((o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now())
      OR i.due_at<now()-($3::int*interval '1 day'))
 ) SELECT i.id,c.order_id,c.kind,c.reminder_date,c.due_at
 FROM candidates c JOIN invoices i ON i.id=c.id
 WHERE ($2='' OR ($2='order_payment_reminder' AND c.kind='order.payment_reminder')
                 OR ($2='invoice_overdue_reminder' AND c.kind='invoice.overdue'))
 AND EXISTS(SELECT 1 FROM notification_routes r JOIN notification_message_templates m ON m.id=r.template_id
  WHERE r.event_kind=c.kind AND r.enabled AND m.active)
 AND NOT EXISTS(SELECT 1 FROM notification_events e WHERE e.dedupe_key=c.kind||':'||i.id::text||':'||(extract(epoch FROM c.due_at)*1000000)::bigint::text||':'||c.reminder_date)
 ORDER BY i.id FOR UPDATE OF i SKIP LOCKED LIMIT $1`, limit, onlyKind, graceDays)
	if err != nil {
		return err
	}
	type reminder struct {
		invoiceID, orderID int64
		kind, date         string
		due                time.Time
	}
	items := []reminder{}
	for rows.Next() {
		var item reminder
		if err = rows.Scan(&item.invoiceID, &item.orderID, &item.kind, &item.date, &item.due); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, item := range items {
		key := fmt.Sprintf("%s:%d:%d:%s", item.kind, item.invoiceID, item.due.UnixMicro(), item.date)
		if err = s.QueueLifecycleNotification(ctx, tx, item.kind, item.invoiceID, item.orderID, 0, key, map[string]string{"stage": "0", "due_date": item.date}); err != nil {
			return err
		}
	}
	return nil
}

// EnqueueOverdueServiceSuspensions applies the billing grace period and only
// creates lifecycle jobs, including recovery of proven system suspensions.
// The service status and customer notification are left
// to ProcessProvisionJob, preserving the existing external-call boundary.
func (s *Store) EnqueueOverdueServiceSuspensions(ctx context.Context, limit int) (int, error) {
	if limit < 1 || limit > 500 {
		limit = 100
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	// Keep the batch atomic. Lock every invoice before any service, matching
	// payment settlement and avoiding invoice/service lock inversions.
	rows, err := tx.Query(ctx, `
SELECT s.id,i.id FROM services s
JOIN invoices i ON i.status='unpaid' AND NOT i.import_review_required
  AND i.due_at<now()-interval '24 hours'
  AND (i.order_id=s.order_id OR EXISTS (
    SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
      AND ii.item_type IN ('renew','service','upgrade')))
WHERE s.status='active' AND NOT s.import_review_required
  AND NOT EXISTS (SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id
                  AND j.status IN ('pending','running','waiting_manual','waiting_verification'))
  AND NOT EXISTS (SELECT 1 FROM provision_jobs j WHERE j.idempotency_key=
      'overdue-suspend-service-'||s.id::text||'-invoice-'||i.id::text)
ORDER BY i.id,s.id LIMIT $1`, limit)
	if err != nil {
		return 0, err
	}
	var candidates []struct{ serviceID, invoiceID int64 }
	for rows.Next() {
		var candidate struct{ serviceID, invoiceID int64 }
		if err = rows.Scan(&candidate.serviceID, &candidate.invoiceID); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	for _, candidate := range candidates {
		var invoiceID int64
		if err = tx.QueryRow(ctx, "SELECT id FROM invoices WHERE id=$1 FOR UPDATE", candidate.invoiceID).Scan(&invoiceID); err != nil {
			return 0, err
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].serviceID < candidates[j].serviceID })
	queued := 0
	for _, candidate := range candidates {
		ok, err := s.enqueueOverdueServiceSuspension(ctx, tx, candidate.serviceID, candidate.invoiceID)
		if err != nil {
			return 0, err
		}
		if ok {
			queued++
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	if queued < limit {
		expired, expiryErr := s.enqueueExpiredSupplierSuspensions(ctx, limit-queued)
		if expiryErr != nil {
			return queued, expiryErr
		}
		queued += expired
	}
	if queued < limit {
		recovered, recoveryErr := s.enqueuePaidSupplierRecoveries(ctx, limit-queued)
		if recoveryErr != nil {
			return queued, recoveryErr
		}
		queued += recovered
	}
	return queued, nil
}

func (s *Store) enqueueOverdueServiceSuspension(ctx context.Context, tx pgx.Tx, serviceID, invoiceID int64) (bool, error) {
	var due bool
	if err := tx.QueryRow(ctx, `SELECT status='unpaid' AND NOT import_review_required
      AND due_at<clock_timestamp()-interval '24 hours'
      FROM invoices WHERE id=$1 FOR UPDATE`, invoiceID).Scan(&due); err != nil {
		return false, err
	}
	if !due {
		return false, nil
	}
	var eligible bool
	var mode string
	if err := tx.QueryRow(ctx, `SELECT s.status='active' AND NOT s.import_review_required
      AND EXISTS (SELECT 1 FROM invoices i WHERE i.id=$2 AND (i.order_id=s.order_id OR EXISTS (
        SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
          AND ii.item_type IN ('renew','service','upgrade'))))
      AND NOT EXISTS (SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id
          AND j.status IN ('pending','running','waiting_manual','waiting_verification')),
      s.provision_mode
      FROM services s WHERE s.id=$1 FOR UPDATE`, serviceID, invoiceID).Scan(&eligible, &mode); err != nil {
		return false, err
	}
	if !eligible {
		return false, nil
	}
	manualLifecycle := false
	if mode == "supplier" {
		caps, capErr := s.supplierServiceActionCapabilities(ctx, tx, serviceID)
		if capErr != nil {
			// A missing or disabled binding must become visible work, while an SQL
			// failure still rolls back the entire batch rather than being swallowed.
			var businessErr *Conflict
			if !errors.As(capErr, &businessErr) && !errors.Is(capErr, pgx.ErrNoRows) {
				return false, capErr
			}
		}
		manualLifecycle = capErr != nil || !caps.Suspend
	}
	// Re-read after the service lock: another scheduler may have inserted a
	// job while this transaction waited, without changing the service row.
	var busy bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM provision_jobs WHERE service_id=$1
        AND status IN ('pending','running','waiting_manual','waiting_verification'))`, serviceID).Scan(&busy); err != nil {
		return false, err
	}
	if busy {
		return false, nil
	}
	key := fmt.Sprintf("overdue-suspend-service-%d-invoice-%d", serviceID, invoiceID)
	status, message := "pending", ""
	payloadMap := map[string]any{}
	if mode == "supplier" {
		payloadMap["system_overdue_suspension"] = true
		payloadMap["billing_invoice_id"] = invoiceID
	}
	if manualLifecycle {
		status, message = "waiting_manual", supplierLifecycleManualMessage
		payloadMap["manual_lifecycle"] = true
	}
	payload, _ := json.Marshal(payloadMap)
	command, err := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,last_error,request_payload)
      VALUES($1,'suspend',$2,$3,$4,$5::jsonb) ON CONFLICT (idempotency_key) DO NOTHING`, serviceID, key, status, message, payload)
	if err != nil {
		return false, err
	}
	if command.RowsAffected() > 0 && manualLifecycle {
		if err = Audit(ctx, tx, 0, "supplier.lifecycle.manual_requested", "service", serviceID, map[string]any{"invoice_id": invoiceID, "action": "suspend", "source": "overdue"}); err != nil {
			return false, err
		}
	}
	return command.RowsAffected() > 0, nil
}

// Lock the invoice before existing service/job locks. Keys also identify jobs
// queued by the original 098 implementation; manual suspension is unaffected.
// External requests remain outside the existing dispatch transaction.
func lockOverdueSuspensionInvoice(ctx context.Context, tx pgx.Tx, job ProvisionJob) (bool, error) {
	var key, action string
	var payload []byte
	if err := tx.QueryRow(ctx, "SELECT idempotency_key,action,request_payload FROM provision_jobs WHERE id=$1", job.ID).Scan(&key, &action, &payload); err != nil {
		return false, err
	}
	if action == "resume" && strings.HasPrefix(key, "paid-resume-service-") {
		return lockPaidSupplierRecoveryInvoice(ctx, tx, job, key, payload)
	}
	if action == "suspend" && strings.HasPrefix(key, "expiry-suspend-service-") {
		return supplierExpirySuspensionEligible(ctx, tx, job, key, payload)
	}
	if action != "suspend" || !strings.HasPrefix(key, "overdue-suspend-service-") {
		return true, nil
	}
	var serviceID, invoiceID int64
	if _, err := fmt.Sscanf(key, "overdue-suspend-service-%d-invoice-%d", &serviceID, &invoiceID); err != nil || serviceID != job.ServiceID || invoiceID < 1 || key != fmt.Sprintf("overdue-suspend-service-%d-invoice-%d", serviceID, invoiceID) {
		return false, fmt.Errorf("invalid scheduled suspension key")
	}
	var due bool
	err := tx.QueryRow(ctx, `SELECT i.status='unpaid' AND NOT i.import_review_required
      AND i.due_at<clock_timestamp()-interval '24 hours'
      AND EXISTS (SELECT 1 FROM services s WHERE s.id=$2 AND (i.order_id=s.order_id OR EXISTS (
          SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
            AND ii.item_type IN ('renew','service','upgrade'))))
      FROM invoices i WHERE i.id=$1 FOR UPDATE`, invoiceID, serviceID).Scan(&due)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	return due, err
}

// Called only after the normal service/job lease checks under their row locks.
func cancelObsoleteOverdueSuspension(ctx context.Context, tx pgx.Tx, job ProvisionJob) error {
	if _, err := tx.Exec(ctx, `UPDATE provision_jobs SET status='cancelled',locked_at=NULL,
      completed_at=now(),updated_at=now(),last_error='账单或暂停来源已变化，不再符合自动生命周期条件'
      WHERE id=$1 AND status='running' AND attempts=$2 AND external_started_at IS NULL`, job.ID, job.Attempts); err != nil {
		return err
	}
	return Audit(ctx, tx, 0, "service.overdue_suspension.cancelled", "service", job.ServiceID, map[string]any{"job_id": job.ID})
}
