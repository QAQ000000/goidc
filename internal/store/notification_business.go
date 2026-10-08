package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// QueuePaymentSuccessNotifications records the customer notifications in the
// same transaction that marks an invoice paid. Delivery happens later in the
// existing email/SMS workers, so provider failures cannot roll back payment.
func (s *Store) QueuePaymentSuccessNotifications(ctx context.Context, tx pgx.Tx, invoiceID int64) error {
	var userID, amount int64
	var invoiceNo, email, phone string
	var emailVerified, phoneVerified bool
	if err := tx.QueryRow(ctx, `SELECT i.user_id,i.invoice_no,i.amount_cents,u.email,u.email_verified,u.phone,u.phone_verified
FROM invoices i JOIN users u ON u.id=i.user_id WHERE i.id=$1`, invoiceID).
		Scan(&userID, &invoiceNo, &amount, &email, &emailVerified, &phone, &phoneVerified); err != nil {
		return err
	}
	amountText := fmt.Sprintf("¥%d.%02d", amount/100, amount%100)
	if emailVerified && strings.TrimSpace(email) != "" {
		if err := s.queuePaymentSuccessEmail(ctx, tx, invoiceID, userID, invoiceNo, amountText, email); err != nil {
			return err
		}
	}
	if phoneVerified && strings.TrimSpace(phone) != "" {
		if err := s.queuePaymentSuccessSMS(ctx, tx, invoiceID, userID, invoiceNo, amountText, phone); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queuePaymentSuccessEmail(ctx context.Context, tx pgx.Tx, invoiceID, userID int64, invoiceNo, amount, recipient string) error {
	var subject, body string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='payment_success'`).Scan(&subject, &body, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = renderPaymentSuccessTemplate(body, invoiceNo, amount)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date)
VALUES(NULL,$1,$2,'email','payment_success',0,CURRENT_DATE)
ON CONFLICT (invoice_id,channel,kind) WHERE invoice_id IS NOT NULL AND kind NOT IN ('order_payment_reminder','invoice_overdue_reminder') DO NOTHING
RETURNING id`, userID, invoiceID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	var outboxID int64
	if err := tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id)
VALUES($1,$2,$3,'payment_success',$4) RETURNING id`, recipient, subject, body, instanceID).Scan(&outboxID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET email_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}

func (s *Store) queuePaymentSuccessSMS(ctx context.Context, tx pgx.Tx, invoiceID, userID int64, invoiceNo, amount, recipient string) error {
	var body, templateID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT body,provider_template_id,active FROM notification_templates WHERE channel='sms' AND kind='payment_success'`).Scan(&body, &templateID, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = renderPaymentSuccessTemplate(body, invoiceNo, amount)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date)
VALUES(NULL,$1,$2,'sms','payment_success',0,CURRENT_DATE)
ON CONFLICT (invoice_id,channel,kind) WHERE invoice_id IS NOT NULL AND kind NOT IN ('order_payment_reminder','invoice_overdue_reminder') DO NOTHING
RETURNING id`, userID, invoiceID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	outboxID, err := s.queueSMSWithTemplateID(ctx, tx, recipient, body, "payment_success", "", time.Now().Add(30*24*time.Hour), templateID, instanceID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE notification_delivery_events SET sms_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}

func renderPaymentSuccessTemplate(body, invoiceNo, amount string) string {
	body = strings.ReplaceAll(body, "{{invoice_no}}", invoiceNo)
	return strings.ReplaceAll(body, "{{amount}}", amount)
}

// QueueServiceProvisionSuccessNotifications must run in the same transaction
// that makes a service active. The event key prevents duplicate messages when
// a supplier callback is followed by a periodic sync.
func (s *Store) QueueServiceProvisionSuccessNotifications(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var userID int64
	var productName, upstreamID, email, phone string
	var emailVerified, phoneVerified bool
	if err := tx.QueryRow(ctx, `SELECT s.user_id,p.name,s.upstream_id,u.email,u.email_verified,u.phone,u.phone_verified
FROM services s JOIN products p ON p.id=s.product_id JOIN users u ON u.id=s.user_id
WHERE s.id=$1 AND s.status='active'`, serviceID).
		Scan(&userID, &productName, &upstreamID, &email, &emailVerified, &phone, &phoneVerified); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if emailVerified && strings.TrimSpace(email) != "" {
		if err := s.queueServiceProvisionEmail(ctx, tx, serviceID, userID, productName, upstreamID, email); err != nil {
			return err
		}
	}
	if phoneVerified && strings.TrimSpace(phone) != "" {
		if err := s.queueServiceProvisionSMS(ctx, tx, serviceID, userID, productName, upstreamID, phone); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueServiceProvisionEmail(ctx context.Context, tx pgx.Tx, serviceID, userID int64, productName, upstreamID, recipient string) error {
	var subject, body string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='service_provision_success'`).Scan(&subject, &body, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = renderServiceProvisionTemplate(body, productName, upstreamID)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'email','service_provision_success',0,CURRENT_DATE)
ON CONFLICT (service_id,channel,kind) WHERE invoice_id IS NULL AND kind='service_provision_success' DO NOTHING
RETURNING id`, serviceID, userID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	var outboxID int64
	if err := tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id)
VALUES($1,$2,$3,'service_provision_success',$4) RETURNING id`, recipient, subject, body, instanceID).Scan(&outboxID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET email_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}

func (s *Store) queueServiceProvisionSMS(ctx context.Context, tx pgx.Tx, serviceID, userID int64, productName, upstreamID, recipient string) error {
	var body, templateID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT body,provider_template_id,active FROM notification_templates WHERE channel='sms' AND kind='service_provision_success'`).Scan(&body, &templateID, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = renderServiceProvisionTemplate(body, productName, upstreamID)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'sms','service_provision_success',0,CURRENT_DATE)
ON CONFLICT (service_id,channel,kind) WHERE invoice_id IS NULL AND kind='service_provision_success' DO NOTHING
RETURNING id`, serviceID, userID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	outboxID, err := s.queueSMSWithTemplateID(ctx, tx, recipient, body, "service_provision_success", "", time.Now().Add(30*24*time.Hour), templateID, instanceID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE notification_delivery_events SET sms_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}

func renderServiceProvisionTemplate(body, productName, upstreamID string) string {
	body = strings.ReplaceAll(body, "{{product_name}}", productName)
	return strings.ReplaceAll(body, "{{upstream_id}}", upstreamID)
}

// QueueServiceSuspendedNotifications records the customer-facing transition
// after the service row has reached suspended inside the caller transaction.
func (s *Store) QueueServiceSuspendedNotifications(ctx context.Context, tx pgx.Tx, serviceID int64) error {
	var userID int64
	var productName, email, phone string
	var emailVerified, phoneVerified bool
	if err := tx.QueryRow(ctx, `SELECT s.user_id,p.name,u.email,u.email_verified,u.phone,u.phone_verified
FROM services s JOIN products p ON p.id=s.product_id JOIN users u ON u.id=s.user_id
WHERE s.id=$1 AND s.status='suspended'`, serviceID).
		Scan(&userID, &productName, &email, &emailVerified, &phone, &phoneVerified); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if emailVerified && strings.TrimSpace(email) != "" {
		if err := s.queueServiceSuspendedEmail(ctx, tx, serviceID, userID, productName, email); err != nil {
			return err
		}
	}
	if phoneVerified && strings.TrimSpace(phone) != "" {
		if err := s.queueServiceSuspendedSMS(ctx, tx, serviceID, userID, productName, phone); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) queueServiceSuspendedEmail(ctx context.Context, tx pgx.Tx, serviceID, userID int64, productName, recipient string) error {
	var subject, body string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT subject,body,active FROM notification_templates WHERE channel='email' AND kind='service_suspended'`).Scan(&subject, &body, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='mail' AND implementation_id='smtp' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = strings.ReplaceAll(body, "{{product_name}}", productName)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'email','service_suspended',0,CURRENT_DATE)
ON CONFLICT (service_id,channel,kind,stage,due_date) DO NOTHING
RETURNING id`, serviceID, userID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	var outboxID int64
	if err := tx.QueryRow(ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,channel_instance_id)
VALUES($1,$2,$3,'service_suspended',$4) RETURNING id`, recipient, subject, body, instanceID).Scan(&outboxID); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, "UPDATE notification_delivery_events SET email_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}

func (s *Store) queueServiceSuspendedSMS(ctx context.Context, tx pgx.Tx, serviceID, userID int64, productName, recipient string) error {
	var body, templateID string
	var active bool
	if err := tx.QueryRow(ctx, `SELECT body,provider_template_id,active FROM notification_templates WHERE channel='sms' AND kind='service_suspended'`).Scan(&body, &templateID, &active); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	if !active {
		return nil
	}
	var instanceID string
	if err := tx.QueryRow(ctx, `SELECT id FROM channel_instances WHERE category='sms' AND enabled=true ORDER BY sort_order,id LIMIT 1`).Scan(&instanceID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	body = strings.ReplaceAll(body, "{{product_name}}", productName)
	var eventID int64
	if err := tx.QueryRow(ctx, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
VALUES($1,$2,'sms','service_suspended',0,CURRENT_DATE)
ON CONFLICT (service_id,channel,kind,stage,due_date) DO NOTHING
RETURNING id`, serviceID, userID).Scan(&eventID); err != nil {
		if err == pgx.ErrNoRows {
			return nil
		}
		return err
	}
	outboxID, err := s.queueSMSWithTemplateID(ctx, tx, recipient, body, "service_suspended", "", time.Now().Add(30*24*time.Hour), templateID, instanceID)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "UPDATE notification_delivery_events SET sms_outbox_id=$2 WHERE id=$1", eventID, outboxID)
	return err
}
