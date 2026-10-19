package store

import (
	"errors"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestNotificationWebPushOnlyScheduledReminders(t *testing.T) {
	f := newWebPushFixture(t)
	original := f.db.Pool
	cfg := original.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Pool = pool
	t.Cleanup(func() { pool.Close(); f.db.Pool = original })
	var zone string
	if err = pool.QueryRow(f.ctx, `SHOW timezone`).Scan(&zone); err != nil || zone != "UTC" {
		t.Fatal("scheduler regression must exercise a UTC database session", zone, err)
	}
	f.save(t, f.customer, f.session(), f.subscription(t, "scheduler-only-browser"), 0)
	f.exec(t, `UPDATE notification_templates SET active=false;
UPDATE notification_routes SET enabled=false;
UPDATE channel_instances SET enabled=false WHERE category IN ('mail','sms');
UPDATE scheduler_task_settings SET enabled=true,settings='{"expiry_days":[7],"billing_grace_days":0}'`)
	kinds := []string{"service.expiry_reminder", "service.expired", "order.payment_reminder", "invoice.overdue"}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if _, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "web_push", true, 0, "business"); err != nil {
			return err
		}
		for _, kind := range kinds {
			id := strings.NewReplacer(".", "-", "_", "-").Replace(kind) + "-web_push"
			// The explicit disabling update above deliberately did not advance
			// route versions; configuration still starts from migration version 1.
			if _, err := f.db.UpdateNotificationRoute(f.ctx, tx, id, NotificationRouteInput{Enabled: true, InstanceID: "push-fixture", ExpectedVersion: 1}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	upcoming := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "push-schedule-upcoming")
	expired := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "push-schedule-expired")
	unpaid := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "push-schedule-unpaid")
	overdue := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "push-schedule-overdue")
	// UTC sees the seven-day reminder's Shanghai deadline on the previous
	// calendar date. The expired item can cross midnight, so its pre-existing
	// consent/configuration is explicitly recorded before that deadline.
	f.exec(t, `UPDATE services SET next_due_at=(((now() AT TIME ZONE 'Asia/Shanghai')::date+7)+time '00:30') AT TIME ZONE 'Asia/Shanghai' WHERE id=$1`, upcoming.service)
	f.exec(t, `UPDATE services SET next_due_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, expired.service)
	f.exec(t, `UPDATE invoices SET status='cancelled' WHERE id=ANY($1)`, []int64{upcoming.invoice, expired.invoice})
	f.exec(t, `UPDATE services SET status='terminated' WHERE id=ANY($1)`, []int64{unpaid.service, overdue.service})
	f.exec(t, `UPDATE invoices SET due_at=clock_timestamp()+interval '1 day' WHERE id=$1`, unpaid.invoice)
	f.exec(t, `UPDATE invoices SET due_at=clock_timestamp()-interval '2 days' WHERE id=$1`, overdue.invoice)
	f.exec(t, `UPDATE notification_routes SET updated_at=now()-interval '10 days' WHERE channel='web_push';
UPDATE notification_message_templates SET updated_at=now()-interval '10 days' WHERE channel='web_push';
UPDATE user_notification_preferences SET updated_at=now()-interval '10 days' WHERE channel='web_push';
UPDATE notification_web_push_subscriptions SET created_at=now()-interval '10 days',updated_at=now()-interval '10 days';
UPDATE channel_instances SET updated_at=now()-interval '10 days' WHERE id='push-fixture';
UPDATE sessions SET created_at=now()-interval '10 days'`)
	for range 2 {
		if n, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 100); err != nil || n != 0 {
			t.Fatal("push-only expiry scan failed or queued a legacy transport", n, err)
		}
		if n, err := f.db.EnqueueBillingNotifications(f.ctx, 100); err != nil || n != 0 {
			t.Fatal("push-only billing scan failed or queued a legacy transport", n, err)
		}
	}
	for _, kind := range kinds {
		if n := f.count(t, `SELECT count(*) FROM notification_events WHERE kind=$1`, kind); n != 1 {
			t.Fatalf("scheduler did not generate exactly one %s fact: %d", kind, n)
		}
		if n := f.count(t, `SELECT count(*) FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.kind=$1 AND d.channel='web_push' AND d.status='pending'`, kind); n != 1 {
			t.Fatalf("scheduler did not enqueue one browser recipient for %s: %d", kind, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events e JOIN services s ON s.id=e.service_id WHERE e.kind='service.expiry_reminder' AND e.payload->>'stage'='7' AND e.payload->>'due_date'=(s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date::text AND (e.payload->>'resource_due_at')::timestamptz=s.next_due_at`); n != 1 {
		t.Fatal("push-only scheduler lost Shanghai date or exact deadline identity", n)
	}
	for range 4 {
		job, err := f.db.ClaimNotificationJob(f.ctx)
		if err != nil || job == nil || job.Channel != "web_push" || job.PushSubscription == nil {
			t.Fatal("scheduled browser reminder was not claimable", job, err)
		}
		if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted", Code: "push_accepted"}); err != nil {
			t.Fatal(err)
		}
	}
	if job, err := f.db.ClaimNotificationJob(f.ctx); job != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("repeated scheduler scans produced duplicate browser jobs", job, err)
	}
	if n := f.count(t, `SELECT (SELECT count(*) FROM email_outbox)+(SELECT count(*) FROM sms_outbox)+(SELECT count(*) FROM notification_delivery_events)+(SELECT count(*) FROM admin_inbox_messages)`); n != 0 {
		t.Fatal("push-only scheduler wrote another channel's delivery records", n)
	}
}
