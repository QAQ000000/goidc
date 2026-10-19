package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

func serviceExpiryCandidateStillDue(ctx context.Context, tx pgx.Tx, item expiryReminderCandidate, channelName string) (bool, error) {
	address := "u.email_verified AND u.email=$5"
	recipient := item.email
	if channelName == "sms" {
		address, recipient = "u.phone_verified AND u.phone=$5", item.phone
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM services s JOIN users u ON u.id=s.user_id
 WHERE s.id=$1 AND s.user_id=$2 AND s.next_due_at=$3 AND s.next_due_at>now()
 AND s.status IN ('active','suspended') AND NOT s.import_review_required AND s.billing_cycle<>'once'
 AND (s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date-(now() AT TIME ZONE 'Asia/Shanghai')::date=$4
 AND u.status='active' AND `+address+`)`, item.serviceID, item.userID, item.due, item.stage, recipient).Scan(&valid)
	return valid, err
}

func legacyRecipientCurrent(ctx context.Context, tx pgx.Tx, userID int64, recipient, channelName string) (bool, error) {
	address := "email_verified AND email=$2"
	if channelName == "sms" {
		address = "phone_verified AND phone=$2"
	}
	var valid bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND status='active' AND `+address+`)`, userID, recipient).Scan(&valid)
	return valid, err
}

// Only the three scheduler reminder families use this additional fence.
// Verification, ticket and other legacy jobs retain their established rules.
// Names are private constants supplied by the email/SMS entry points.
func legacyDeadlineEligibleSQL(alias, channelName string) string {
	column, address, category := "email_outbox_id", "u.email_verified AND u.email="+alias+".recipient", "mail"
	if channelName == "sms" {
		column, address, category = "sms_outbox_id", "u.phone_verified AND u.phone="+alias+".recipient", "sms"
	}
	return fmt.Sprintf(`(%[1]s.kind NOT IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder') OR EXISTS(
 SELECT 1 FROM notification_delivery_events n JOIN users u ON u.id=n.user_id
 JOIN channel_instances c ON c.id=%[1]s.channel_instance_id
 JOIN notification_templates t ON t.channel='%[2]s' AND t.kind=n.kind
 WHERE n.%[3]s=%[1]s.id AND n.channel='%[2]s' AND n.kind=%[1]s.kind
 AND n.resource_due_at IS NOT NULL AND u.status='active' AND %[4]s
 AND c.category='%[5]s' AND c.enabled AND t.active
 AND CASE WHEN n.kind='service_expiry_reminder' THEN EXISTS(
  SELECT 1 FROM services s WHERE s.id=n.service_id AND s.user_id=n.user_id
   AND s.status IN ('active','suspended') AND NOT s.import_review_required
   AND s.billing_cycle<>'once' AND s.next_due_at=n.resource_due_at
   AND s.next_due_at>clock_timestamp()
   AND (s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date
       -(clock_timestamp() AT TIME ZONE 'Asia/Shanghai')::date=n.stage)
 ELSE EXISTS(SELECT 1 FROM invoices i LEFT JOIN orders o ON o.id=i.order_id
  WHERE i.id=n.invoice_id AND i.user_id=n.user_id AND i.status='unpaid'
   AND NOT i.import_review_required AND i.due_at=n.resource_due_at
   AND n.due_date=(clock_timestamp() AT TIME ZONE 'Asia/Shanghai')::date
   AND ((n.kind='order_payment_reminder' AND o.status='pending' AND i.due_at>=clock_timestamp())
     OR (n.kind='invoice_overdue_reminder' AND i.due_at<clock_timestamp()))) END))`, alias, channelName, column, address, category)
}

// Invalid reminders must reach a terminal state before stale-lease recovery.
// Otherwise Active=false would leave a sending row to be retried every lease.
func (s *Store) expireLegacyDeadlineJobs(ctx context.Context, table string) error {
	channelName := "email"
	body := "body='[expired]',body_encrypted=''::bytea"
	if table == "sms_outbox" {
		channelName, body = "sms", "body_encrypted=''::bytea"
	}
	_, err := s.Pool.Exec(ctx, fmt.Sprintf(`UPDATE %[1]s o SET status='expired',%[2]s,
 locked_at=NULL,dead_letter_at=NULL,last_error='deadline eligibility changed'
 WHERE o.status IN ('pending','sending','dead_letter')
 AND o.kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder')
 AND NOT %[3]s`, table, body, legacyDeadlineEligibleSQL("o", channelName)))
	return err
}
