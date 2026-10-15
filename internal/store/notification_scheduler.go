package store

import (
	"context"
	"encoding/json"
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
	if len(expiryDays) == 0 {
		return 0, nil
	}
	args := []any{limit}
	placeholders := make([]string, len(expiryDays))
	for i, d := range expiryDays {
		args = append(args, d)
		placeholders[i] = fmt.Sprintf("$%d", i+2)
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
  AND (s.next_due_at::date-CURRENT_DATE) = ANY(ARRAY[`+strings.Join(placeholders, ",")+`]::int[])
ORDER BY s.next_due_at,s.id
LIMIT $1`, args...)
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
WHERE i.status='unpaid' AND NOT i.import_review_required
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
        OR (t.channel='sms' AND u.phone_verified AND btrim(u.phone)<>'' AND EXISTS (
              SELECT 1 FROM channel_instances c WHERE c.category='sms' AND c.enabled)))
      AND NOT EXISTS (SELECT 1 FROM notification_delivery_events e
        WHERE e.invoice_id=i.id AND e.channel=t.channel AND e.kind=t.kind
          AND e.stage=0 AND e.due_date=CURRENT_DATE))
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
	if len(items) == 0 {
		return 0, nil
	}

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
	valid, err := billingCandidateStillDue(ctx, tx, &item)
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

func billingCandidateStillDue(ctx context.Context, tx pgx.Tx, item *billingNotificationCandidate) (bool, error) {
	var status string
	var reviewed bool
	var dueAt time.Time
	if err := tx.QueryRow(ctx, "SELECT status,due_at,amount_cents,import_review_required FROM invoices WHERE id=$1 FOR UPDATE", item.invoiceID).Scan(&status, &dueAt, &item.amountCents, &reviewed); err != nil {
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
		return dueAt.Before(time.Now().Add(-time.Duration(item.graceDays) * 24 * time.Hour)), nil
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
	// A mode that cannot suspend must not queue a job that is guaranteed to
	// fail: the job would exhaust its attempts while the service stayed active
	// and unpaid, and an operator would never learn about it. Record durable
	// evidence instead so the missed suspension is discoverable, then leave the
	// service untouched. Evidence is deduplicated per service/invoice so the
	// minute-level scheduler does not pile up identical rows.
	if !modeSupportsServiceAction(mode, "suspend") {
		var recorded bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM audit_events
 WHERE action='service.overdue_suspension.unsupported' AND entity_type='service' AND entity_id=$1
   AND payload->>'invoice_id'=$2)`, fmt.Sprint(serviceID), fmt.Sprint(invoiceID)).Scan(&recorded); err != nil {
			return false, err
		}
		if recorded {
			return false, nil
		}
		if err := Audit(ctx, tx, 0, "service.overdue_suspension.unsupported", "service", serviceID,
			map[string]any{"invoice_id": invoiceID, "action": "suspend", "provision_mode": mode, "reason": "delivery_mode_unsupported"}); err != nil {
			return false, err
		}
		return false, nil
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
	command, err := tx.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key)
      VALUES($1,'suspend',$2) ON CONFLICT (idempotency_key) DO NOTHING`, serviceID, key)
	if err != nil {
		return false, err
	}
	return command.RowsAffected() > 0, nil
}

// Lock the invoice before existing service/job locks. Keys also identify jobs
// queued by the original 098 implementation; manual suspension is unaffected.
// External requests remain outside the existing dispatch transaction.
func lockOverdueSuspensionInvoice(ctx context.Context, tx pgx.Tx, job ProvisionJob) (bool, error) {
	var key, action string
	if err := tx.QueryRow(ctx, "SELECT idempotency_key,action FROM provision_jobs WHERE id=$1", job.ID).Scan(&key, &action); err != nil {
		return false, err
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
      completed_at=now(),updated_at=now(),last_error='账单已支付或不再符合逾期暂停条件'
      WHERE id=$1 AND status='running' AND attempts=$2 AND external_started_at IS NULL`, job.ID, job.Attempts); err != nil {
		return err
	}
	return Audit(ctx, tx, 0, "service.overdue_suspension.cancelled", "service", job.ServiceID, map[string]any{"job_id": job.ID})
}
