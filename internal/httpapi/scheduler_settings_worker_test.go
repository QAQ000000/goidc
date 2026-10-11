package httpapi

import (
	"context"
	"github.com/codex/zjmfgo/internal/store"
	"testing"
)

func TestSchedulerConfiguredExpiryDays(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "expiry-reminder-customer", "customer")
	admin, _ := user(t, db, h, "expiry-reminder-admin", "super_admin")
	var sessionHash string
	if err := db.Pool.QueryRow(context.Background(), "SELECT token_hash FROM sessions WHERE user_id=$1", admin).Scan(&sessionHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.UpdateSMTPSettings(t.Context(), admin, sessionHash, store.SMTPSettings{
		Enabled: true, Host: "smtp.example.test", Port: 25, From: "notify@example.test", TLSMode: "none",
	}, nil, false, 1, store.SMTPRuntimeConfig{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET email_verified=true WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	var orderID, itemID, serviceID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES('EXP-'||replace(gen_random_uuid()::text,'-',''),$1,'active',1333,'expiry-reminder-order','fixture') RETURNING id`, uid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'商品','monthly',1,1234,99,1333) RETURNING id`, orderID, pid).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,unit_number,recurring_price_cents)
VALUES($1,$2,$3,$4,'商品','expiry-reminder.example','monthly','active',CURRENT_DATE+interval '7 days'+interval '12 hours','simulation',1,1234) RETURNING id`, uid, orderID, itemID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	for _, cfg := range []string{`{"expiry_days":[],"billing_grace_days":0}`, `{"expiry_days":[3],"billing_grace_days":0}`} {
		if _, err := db.Pool.Exec(t.Context(), "UPDATE scheduler_task_settings SET settings=$1::jsonb", cfg); err != nil {
			t.Fatal(err)
		}
		n, err := db.EnqueueServiceExpiryReminders(t.Context(), 100)
		if err != nil || n != 0 {
			t.Fatalf("unconfigured expiry queued=%d err=%v", n, err)
		}
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE scheduler_task_settings SET settings='{"expiry_days":[7,1],"billing_grace_days":0}'`); err != nil {
		t.Fatal(err)
	}
	queued, err := db.EnqueueServiceExpiryReminders(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("first schedule queued=%d err=%v", queued, err)
	}
	queued, err = db.EnqueueServiceExpiryReminders(t.Context(), 100)
	if err != nil || queued != 0 {
		t.Fatalf("duplicate schedule queued=%d err=%v", queued, err)
	}
	var count int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM email_outbox WHERE kind='service_expiry_reminder'").Scan(&count); err != nil || count != 1 {
		t.Fatalf("email outbox count=%d err=%v", count, err)
	}
	if _, err = db.Pool.Exec(t.Context(), "UPDATE services SET next_due_at=CURRENT_DATE+interval '1 day'+interval '12 hours' WHERE id=$1", serviceID); err != nil {
		t.Fatal(err)
	}
	queued, err = db.EnqueueServiceExpiryReminders(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("second stage queued=%d err=%v", queued, err)
	}
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM notification_delivery_events WHERE service_id=$1", serviceID).Scan(&count); err != nil || count != 2 {
		t.Fatalf("delivery event count=%d err=%v", count, err)
	}
}

func TestSchedulerConfiguredBillingGrace(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "billing-scheduler-customer", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE id='mail-smtp-default'`); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	var orderID, itemID, serviceID, invoiceID int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
VALUES('BILL-'||replace(gen_random_uuid()::text,'-',''),$1,'pending',1000,'billing-scheduler-order','fixture') RETURNING id`, uid).Scan(&orderID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
VALUES($1,$2,'商品','monthly',1,1000,0,1000) RETURNING id`, orderID, pid).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,unit_number,recurring_price_cents)
VALUES($1,$2,$3,$4,'商品','billing-scheduler.example','monthly','active',now()+interval '1 month','simulation',1,1000) RETURNING id`, uid, orderID, itemID, pid).Scan(&serviceID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at)
VALUES('BILL-'||replace(gen_random_uuid()::text,'-',''),$1,$2,'unpaid',1000,now()+interval '2 days') RETURNING id`, uid, orderID).Scan(&invoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE scheduler_task_settings SET settings='{"expiry_days":[],"billing_grace_days":3}'`); err != nil {
		t.Fatal(err)
	}
	n, err := db.EnqueueOverdueInvoiceReminders(t.Context(), 100)
	if err != nil || n != 0 {
		t.Fatalf("future invoice queued=%d err=%v", n, err)
	}
	queued, err := db.EnqueueUnpaidOrderReminders(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("order reminder queued=%d err=%v", queued, err)
	}
	queued, err = db.EnqueueUnpaidOrderReminders(t.Context(), 100)
	if err != nil || queued != 0 {
		t.Fatalf("duplicate order reminder queued=%d err=%v", queued, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE invoices SET due_at=now()-interval '2 days' WHERE id=$1`, invoiceID); err != nil {
		t.Fatal(err)
	}
	n, err = db.EnqueueOverdueInvoiceReminders(t.Context(), 100)
	if err != nil || n != 0 {
		t.Fatalf("within grace queued=%d err=%v", n, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `UPDATE scheduler_task_settings SET settings='{"expiry_days":[],"billing_grace_days":1}'`); err != nil {
		t.Fatal(err)
	}
	queued, err = db.EnqueueOverdueInvoiceReminders(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("overdue reminder queued=%d err=%v", queued, err)
	}
	queued, err = db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
	if err != nil || queued != 1 {
		t.Fatalf("suspension queued=%d err=%v", queued, err)
	}
	queued, err = db.EnqueueOverdueServiceSuspensions(t.Context(), 100)
	if err != nil || queued != 0 {
		t.Fatalf("duplicate suspension queued=%d err=%v", queued, err)
	}
	var action, status string
	if err = db.Pool.QueryRow(t.Context(), `SELECT action,status FROM provision_jobs WHERE service_id=$1`, serviceID).Scan(&action, &status); err != nil || action != "suspend" || status != "pending" {
		t.Fatalf("suspension job action=%s status=%s err=%v", action, status, err)
	}
}
