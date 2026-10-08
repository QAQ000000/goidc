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
ON CONFLICT (invoice_id,channel,kind) WHERE invoice_id IS NOT NULL DO NOTHING
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
ON CONFLICT (invoice_id,channel,kind) WHERE invoice_id IS NOT NULL DO NOTHING
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
