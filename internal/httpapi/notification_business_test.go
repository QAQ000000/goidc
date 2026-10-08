package httpapi

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/store"
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

func TestServiceProvisionSuccessNotificationIsQueuedOnce(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "provision-notification-customer", "customer")
	_, admin := user(t, db, h, "provision-notification-admin", "super_admin")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE id='mail-smtp-default'`); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 1)
	version, itemID := add(t, h, customer, pid, 1)
	order := request(h, "POST", "/api/v1/orders", store.CheckoutInput{CartVersion: version, ItemIDs: []int64{itemID}}, customer, "provision-notification-order")
	expect(t, order, 201)
	invoiceID := int64(data(t, order)["invoice_id"].(float64))
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/admin/customers/%d/balance", uid), map[string]any{"amount_cents": 2000}, admin, "provision-notification-topup"), 201)
	expect(t, request(h, "POST", fmt.Sprintf("/api/v1/invoices/%d/pay", invoiceID), map[string]any{}, customer, "provision-notification-pay"), 201)
	job, err := db.ClaimProvisionJob(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
		t.Fatal(err)
	}
	var events, outbox int
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1 AND channel='email' AND kind='service_provision_success'`, job.ServiceID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("provision notification events=%d err=%v", events, err)
	}
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM email_outbox WHERE kind='service_provision_success' AND recipient='provision-notification-customer@example.test'`).Scan(&outbox); err != nil || outbox != 1 {
		t.Fatalf("provision notification outbox=%d err=%v", outbox, err)
	}
	if err = db.ProcessProvisionJob(t.Context(), *job); err == nil {
		t.Fatal("reprocessed provision job unexpectedly succeeded")
	}
	if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1 AND kind='service_provision_success'`, job.ServiceID).Scan(&events); err != nil || events != 1 {
		t.Fatalf("duplicate provision event count=%d err=%v", events, err)
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
