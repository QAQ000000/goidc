package store

import (
	"context"
	"fmt"
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
	rows, err := s.Pool.Query(ctx, `
SELECT s.id,s.user_id,s.next_due_at,p.name,u.email,u.email_verified,u.phone,u.phone_verified,
       (s.next_due_at::date-CURRENT_DATE)::int AS days_left
FROM services s
JOIN products p ON p.id=s.product_id
JOIN users u ON u.id=s.user_id
WHERE s.status IN ('active','suspended')
  AND NOT s.import_review_required
  AND s.billing_cycle <> 'once'
  AND s.next_due_at IS NOT NULL
  AND (s.next_due_at::date-CURRENT_DATE) IN (1,7)
ORDER BY s.next_due_at,s.id
LIMIT $1`, limit)
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
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	queued := 0
	for _, item := range items {
		dueDate := item.due.Format("2006-01-02")
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
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	body = renderExpiryTemplate(body, item.product, dueDate, item.stage)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'email','service_expiry_reminder',$3,$4::date)
ON CONFLICT (service_id,channel,kind,stage,due_date) DO NOTHING
RETURNING id`, item.serviceID, item.userID, item.stage, dueDate).Scan(&eventID); err != nil {
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
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'sms','service_expiry_reminder',$3,$4::date)
ON CONFLICT (service_id,channel,kind,stage,due_date) DO NOTHING
RETURNING id`, item.serviceID, item.userID, item.stage, dueDate).Scan(&eventID); err != nil {
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
	rows, err := s.Pool.Query(ctx, `
SELECT i.id,i.user_id,i.invoice_no,i.amount_cents,i.due_at,COALESCE(o.order_no,''),
       CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
            THEN 'order_payment_reminder' ELSE 'invoice_overdue_reminder' END,
       u.email,u.email_verified,u.phone,u.phone_verified
FROM invoices i
JOIN users u ON u.id=i.user_id
LEFT JOIN orders o ON o.id=i.order_id
WHERE i.status='unpaid' AND NOT i.import_review_required
  AND ((o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now())
       OR (i.due_at<now()))
  AND ($2='' OR (CASE WHEN o.id IS NOT NULL AND o.status='pending' AND i.due_at>=now()
                     THEN 'order_payment_reminder' ELSE 'invoice_overdue_reminder' END)=$2)
ORDER BY i.due_at,i.id
LIMIT $1`, limit, onlyKind)
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
		items = append(items, item)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	if len(items) == 0 {
		return 0, nil
	}

	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
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
	email                          string
	emailVerified                  bool
	phone                          string
	phoneVerified                  bool
}

func (s *Store) enqueueBillingEmail(ctx context.Context, tx pgx.Tx, item billingNotificationCandidate) (bool, error) {
	valid, err := billingCandidateStillDue(ctx, tx, item)
	if err != nil || !valid {
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
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date)
VALUES(NULL,$1,$2,'email',$3,0,CURRENT_DATE)
ON CONFLICT (invoice_id,channel,kind,stage,due_date)
WHERE invoice_id IS NOT NULL AND kind IN ('order_payment_reminder','invoice_overdue_reminder') DO NOTHING
RETURNING id`, item.userID, item.invoiceID, item.kind).Scan(&eventID); err != nil {
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
	valid, err := billingCandidateStillDue(ctx, tx, item)
	if err != nil || !valid {
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
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date)
VALUES(NULL,$1,$2,'sms',$3,0,CURRENT_DATE)
ON CONFLICT (invoice_id,channel,kind,stage,due_date)
WHERE invoice_id IS NOT NULL AND kind IN ('order_payment_reminder','invoice_overdue_reminder') DO NOTHING
RETURNING id`, item.userID, item.invoiceID, item.kind).Scan(&eventID); err != nil {
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

func billingCandidateStillDue(ctx context.Context, tx pgx.Tx, item billingNotificationCandidate) (bool, error) {
	var status string
	var dueAt time.Time
	if err := tx.QueryRow(ctx, "SELECT status,due_at FROM invoices WHERE id=$1 FOR UPDATE", item.invoiceID).Scan(&status, &dueAt); err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	if status != "unpaid" {
		return false, nil
	}
	if item.kind == "invoice_overdue_reminder" {
		return dueAt.Before(time.Now()), nil
	}
	var orderStatus string
	if err := tx.QueryRow(ctx, "SELECT COALESCE(o.status,'') FROM invoices i LEFT JOIN orders o ON o.id=i.order_id WHERE i.id=$1", item.invoiceID).Scan(&orderStatus); err != nil {
		return false, err
	}
	if orderStatus != "pending" || !dueAt.After(time.Now()) {
		return false, nil
	}
	return true, nil
}

func renderBillingTemplate(body string, item billingNotificationCandidate) string {
	amount := fmt.Sprintf("¥%d.%02d", item.amountCents/100, item.amountCents%100)
	body = strings.ReplaceAll(body, "{{order_no}}", item.orderNo)
	body = strings.ReplaceAll(body, "{{invoice_no}}", item.invoiceNo)
	body = strings.ReplaceAll(body, "{{amount}}", amount)
	return strings.ReplaceAll(body, "{{due_date}}", item.dueAt.Format("2006-01-02"))
}

// EnqueueOverdueServiceSuspensions applies the billing grace period and only
// creates suspend jobs. The service status and customer notification are left
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
	rows, err := tx.Query(ctx, `
SELECT s.id,COALESCE((SELECT i.id FROM invoices i
 WHERE i.status='unpaid' AND i.due_at<now()-interval '24 hours'
   AND (i.order_id=s.order_id OR EXISTS(
     SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
       AND ii.item_type IN ('renew','service','upgrade')))
 ORDER BY i.due_at,i.id LIMIT 1),0)
FROM services s
WHERE s.status='active' AND NOT s.import_review_required
  AND EXISTS (SELECT 1 FROM invoices i
    WHERE i.status='unpaid' AND i.due_at<now()-interval '24 hours'
      AND (i.order_id=s.order_id OR EXISTS(
        SELECT 1 FROM invoice_items ii WHERE ii.invoice_id=i.id AND ii.service_id=s.id
          AND ii.item_type IN ('renew','service','upgrade'))))
  AND NOT EXISTS (SELECT 1 FROM provision_jobs j WHERE j.service_id=s.id
                  AND j.status IN ('pending','running','waiting_manual','waiting_verification'))
ORDER BY s.id
LIMIT $1
FOR UPDATE OF s SKIP LOCKED`, limit)
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
	if err = rows.Err(); err != nil {
		rows.Close()
		return 0, err
	}
	rows.Close()
	queued := 0
	for _, candidate := range candidates {
		var status string
		var dueAt time.Time
		if err = tx.QueryRow(ctx, "SELECT status,due_at FROM invoices WHERE id=$1 FOR UPDATE", candidate.invoiceID).Scan(&status, &dueAt); err != nil {
			return 0, err
		}
		if status != "unpaid" || !dueAt.Before(time.Now().Add(-24*time.Hour)) {
			continue
		}
		key := fmt.Sprintf("overdue-suspend-service-%d-invoice-%d", candidate.serviceID, candidate.invoiceID)
		command, insertErr := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key)
VALUES($1,'suspend',$2) ON CONFLICT (idempotency_key) DO NOTHING`, candidate.serviceID, key)
		if insertErr != nil {
			return 0, insertErr
		}
		queued += int(command.RowsAffected())
	}
	if err = tx.Commit(ctx); err != nil {
		return 0, err
	}
	return queued, nil
}
