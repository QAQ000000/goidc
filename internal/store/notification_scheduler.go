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
