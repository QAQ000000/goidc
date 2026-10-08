package httpapi

import (
	"context"
	"strings"
	"testing"
)

func TestPaymentSuccessNotificationIsTransactionalAndIdempotent(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "payment-notification-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE id='mail-smtp-default'`); err != nil {
		t.Fatal(err)
	}
	var invoiceID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,invoice_kind,amount_cents)
VALUES('M-NOTIFY',$1,NULL,'manual',1234) RETURNING id`, uid).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	queue := func() error {
		tx, err := db.Pool.Begin(context.Background())
		if err != nil {
			return err
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(context.Background(), "UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1", invoiceID); err != nil {
			return err
		}
		if err = db.QueuePaymentSuccessNotifications(context.Background(), tx, invoiceID); err != nil {
			return err
		}
		return tx.Commit(context.Background())
	}
	if err := queue(); err != nil {
		t.Fatal(err)
	}
	if err := queue(); err != nil {
		t.Fatal(err)
	}
	var events, outbox int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM notification_delivery_events WHERE invoice_id=$1 AND channel='email'", invoiceID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("payment notification events=%d err=%v", events, err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM email_outbox WHERE kind='payment_success' AND recipient='payment-notification-customer@example.test'").Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("payment notification outbox=%d err=%v", outbox, err)
	}
	var body string
	if err := db.Pool.QueryRow(t.Context(), "SELECT body FROM email_outbox WHERE kind='payment_success' LIMIT 1").Scan(&body); err != nil || body == "" || !containsAll(body, "M-NOTIFY", "¥12.34") {
		t.Fatalf("payment notification body=%q err=%v", body, err)
	}
}

func containsAll(value string, parts ...string) bool {
	for _, part := range parts {
		if !strings.Contains(value, part) {
			return false
		}
	}
	return true
}
