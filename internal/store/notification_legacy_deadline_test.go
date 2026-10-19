package store

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func legacyDeadlineFixture(t *testing.T, zone string) notificationFixture {
	t.Helper()
	f := notificationCenterFixture(t)
	original := f.db.Pool
	cfg := original.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = zone
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Pool = pool
	t.Cleanup(func() { pool.Close(); f.db.Pool = original })
	f.exec(t, `UPDATE users SET phone='+8613800000000' WHERE id=$1`, f.customer)
	f.exec(t, `UPDATE users SET email_verified=true,phone_verified=true WHERE id=$1`, f.customer)
	f.exec(t, `UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms')`)
	f.exec(t, `UPDATE notification_templates SET active=kind IN ('service_expiry_reminder','order_payment_reminder','invoice_overdue_reminder'),
 body='{{product_name}} {{days_left}} {{due_date}} {{invoice_no}}'`)
	f.exec(t, `UPDATE scheduler_task_settings SET enabled=true,settings='{"expiry_days":[7],"billing_grace_days":0}'`)
	return f
}

func legacyDeadlineSetService(t *testing.T, f notificationFixture, serviceID int64, days int) time.Time {
	t.Helper()
	var due time.Time
	if err := f.db.Pool.QueryRow(f.ctx, `UPDATE services SET next_due_at=
 (((now() AT TIME ZONE 'Asia/Shanghai')::date+$2::int)+time '00:30') AT TIME ZONE 'Asia/Shanghai'
 WHERE id=$1 RETURNING next_due_at`, serviceID, days).Scan(&due); err != nil {
		t.Fatal(err)
	}
	return due
}

func legacyDeadlineScan(t *testing.T, f notificationFixture, expected int) {
	t.Helper()
	if count, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 100); err != nil || count != expected {
		t.Fatalf("expiry queued=%d want=%d err=%v", count, expected, err)
	}
}

func legacyDeadlineClaimBoth(t *testing.T, f notificationFixture) (*EmailJob, *SMSJob) {
	t.Helper()
	mail, err := f.db.ClaimEmailJob(f.ctx)
	if err != nil {
		t.Fatal("claim reminder email", err)
	}
	sms, err := f.db.ClaimSMSJob(f.ctx)
	if err != nil {
		t.Fatal("claim reminder SMS", err)
	}
	return mail, sms
}

func legacyDeadlineActive(t *testing.T, f notificationFixture, mail *EmailJob, sms *SMSJob, wantMail, wantSMS bool) {
	t.Helper()
	if active, err := f.db.EmailJobActive(f.ctx, *mail); err != nil || active != wantMail {
		t.Fatal("email eligibility", active, wantMail, err)
	}
	if active, err := f.db.SMSJobActive(f.ctx, *sms); err != nil || active != wantSMS {
		t.Fatal("SMS eligibility", active, wantSMS, err)
	}
}

func TestNotificationLegacyDeadlineShanghaiAndExactRenewal(t *testing.T) {
	for _, zone := range []string{"UTC", "Asia/Shanghai"} {
		t.Run(zone, func(t *testing.T) {
			f := legacyDeadlineFixture(t, zone)
			r := lifecycleFixtureResources(t, f, f.customer, "legacy-shanghai")
			due := legacyDeadlineSetService(t, f, r.service, 3)
			f.exec(t, `UPDATE scheduler_task_settings SET settings='{"expiry_days":[3],"billing_grace_days":0}'`)
			legacyDeadlineScan(t, f, 2)
			legacyDeadlineScan(t, f, 0)
			if count := f.count(t, `SELECT count(*) FROM notification_delivery_events
 WHERE service_id=$1 AND stage=3 AND resource_due_at=$2 AND due_date=$3::date`, r.service, due, due.In(notificationShanghai).Format("2006-01-02")); count != 2 {
				t.Fatal("exact deadline or Shanghai date was lost", count)
			}
			mail, sms := legacyDeadlineClaimBoth(t, f)
			if !strings.Contains(mail.Body, due.In(notificationShanghai).Format("2006-01-02")) || !strings.Contains(sms.Body, due.In(notificationShanghai).Format("2006-01-02")) {
				t.Fatal("human reminder date depends on process/session timezone")
			}
			legacyDeadlineActive(t, f, mail, sms, true, true)
			f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 microsecond' WHERE id=$1`, r.service)
			legacyDeadlineActive(t, f, mail, sms, false, false)
			if err := f.db.RecoverStaleEmailJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.db.RecoverStaleSMSJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if count := f.count(t, `SELECT (SELECT count(*) FROM email_outbox WHERE id=$1 AND status='expired')+
 (SELECT count(*) FROM sms_outbox WHERE id=$2 AND status='expired')`, mail.ID, sms.ID); count != 2 {
				t.Fatal("ineligible sending reminders did not terminate", count)
			}
			legacyDeadlineScan(t, f, 2)
			legacyDeadlineScan(t, f, 0)
			if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1`, r.service); count != 4 {
				t.Fatal("same-day extension lost its new exact deadline", count)
			}
			if _, err := f.db.Pool.Exec(f.ctx, `UPDATE notification_delivery_events SET resource_due_at=resource_due_at+interval '1 microsecond' WHERE service_id=$1`, r.service); err == nil {
				t.Fatal("queued deadline evidence was mutable")
			}
			freshMail, freshSMS := legacyDeadlineClaimBoth(t, f)
			legacyDeadlineActive(t, f, freshMail, freshSMS, true, true)
		})
	}
}

func TestNotificationLegacyDeadlinePaginationAndHistory(t *testing.T) {
	f := legacyDeadlineFixture(t, "UTC")
	var unreachable int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO users(username,email,password_hash,display_name)
 VALUES('unreachable-reminder','unreachable@example.test','unused','Unreachable') RETURNING id`).Scan(&unreachable); err != nil {
		t.Fatal(err)
	}
	blocked := lifecycleFixtureResources(t, f, unreachable, "unreachable")
	legacyDeadlineSetService(t, f, blocked.service, 7)
	var shortPhone int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO users(username,email,password_hash,display_name,phone)
 VALUES('unreachable-sms','unreachable-sms@example.test','unused','Short phone','+1234567') RETURNING id`).Scan(&shortPhone); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE users SET phone_verified=true WHERE id=$1`, shortPhone)
	short := lifecycleFixtureResources(t, f, shortPhone, "unreachable-short-sms")
	legacyDeadlineSetService(t, f, short.service, 7)
	history := lifecycleFixtureResources(t, f, f.customer, "old-date-only")
	legacyDeadlineSetService(t, f, history.service, 7)
	f.exec(t, `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date)
 SELECT s.id,s.user_id,c.channel,'service_expiry_reminder',7,(s.next_due_at AT TIME ZONE 'UTC')::date
 FROM services s CROSS JOIN (VALUES('email'),('sms')) c(channel) WHERE s.id=$1`, history.service)
	for n := 0; n < 6; n++ {
		r := lifecycleFixtureResources(t, f, f.customer, fmt.Sprintf("legacy-batch-%d", n))
		legacyDeadlineSetService(t, f, r.service, 7)
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			for range 8 {
				if _, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil {
					errCh <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE resource_due_at IS NOT NULL`); count != 12 {
		t.Fatal("unreachable/already-processed prefix blocked batch progress or duplicate enqueue", count)
	}
	if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1 AND resource_due_at IS NULL`, history.service); count != 2 {
		t.Fatal("legacy dates were guessed or re-enqueued", count)
	}
	legacyDeadlineScan(t, f, 0)
}

func TestNotificationLegacyDeadlineInvalidationBeforeSending(t *testing.T) {
	for _, item := range []struct {
		name, mutation string
		mail, sms      bool
	}{
		{"terminated", `UPDATE services SET status='terminated' WHERE id=$1`, false, false},
		{"review", `UPDATE services SET import_review_required=true WHERE id=$1`, false, false},
		{"one_time", `UPDATE services SET billing_cycle='once' WHERE id=$1`, false, false},
		{"changed_deadline", `UPDATE services SET next_due_at=next_due_at+interval '1 microsecond' WHERE id=$1`, false, false},
		{"changed_email", `UPDATE users SET email='changed@example.test' WHERE id=(SELECT user_id FROM services WHERE id=$1)`, false, true},
		{"unverified_phone", `UPDATE users SET phone_verified=false WHERE id=(SELECT user_id FROM services WHERE id=$1)`, true, false},
		{"disabled_user", `UPDATE users SET status='disabled' WHERE id=(SELECT user_id FROM services WHERE id=$1)`, false, false},
		{"disabled_templates", `UPDATE notification_templates SET active=false WHERE kind='service_expiry_reminder' AND $1::bigint>0`, false, false},
		{"disabled_instances", `UPDATE channel_instances SET enabled=false WHERE category IN ('mail','sms') AND $1::bigint>0`, false, false},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := legacyDeadlineFixture(t, "UTC")
			r := lifecycleFixtureResources(t, f, f.customer, "invalidation")
			legacyDeadlineSetService(t, f, r.service, 7)
			legacyDeadlineScan(t, f, 2)
			mail, sms := legacyDeadlineClaimBoth(t, f)
			legacyDeadlineActive(t, f, mail, sms, true, true)
			f.exec(t, item.mutation, r.service)
			legacyDeadlineActive(t, f, mail, sms, item.mail, item.sms)
			if err := f.db.RecoverStaleEmailJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.db.RecoverStaleSMSJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			for _, ch := range []struct {
				table string
				id    int64
				valid bool
			}{{"email_outbox", mail.ID, item.mail}, {"sms_outbox", sms.ID, item.sms}} {
				var status string
				if err := f.db.Pool.QueryRow(f.ctx, "SELECT status FROM "+ch.table+" WHERE id=$1", ch.id).Scan(&status); err != nil || (status == "expired") == ch.valid {
					t.Fatal("stale reminder lease remained eligible", ch.table, status, ch.valid, err)
				}
			}
		})
	}
}

func TestNotificationLegacyBillingShanghaiAndInvalidation(t *testing.T) {
	t.Run("historical_UTC_date_uses_observed_business_day", func(t *testing.T) {
		f := legacyDeadlineFixture(t, "UTC")
		var today, yesterday int64
		for _, target := range []struct {
			name   string
			id     *int64
			age    int
			atTime string
		}{{"today", &today, 0, "00:30"}, {"yesterday", &yesterday, 1, "20:00"}} {
			if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents,due_at)
 VALUES($1,$2,'manual',100,now()-interval '2 days') RETURNING id`, "legacy-observed-"+target.name, f.customer).Scan(target.id); err != nil {
				t.Fatal(err)
			}
			// Both old UTC DATE values are yesterday. Their actual scan times
			// belong to different Shanghai days; only today's fact suppresses.
			f.exec(t, `INSERT INTO notification_delivery_events(user_id,invoice_id,channel,kind,stage,due_date,created_at)
 SELECT $1,$2,c.channel,'invoice_overdue_reminder',0,(now() AT TIME ZONE 'Asia/Shanghai')::date-1,
  (((now() AT TIME ZONE 'Asia/Shanghai')::date-$3::int)+$4::time) AT TIME ZONE 'Asia/Shanghai'
 FROM (VALUES('email'),('sms')) c(channel)`, f.customer, *target.id, target.age, target.atTime)
		}
		if count, err := f.db.EnqueueOverdueInvoiceReminders(f.ctx, 1); err != nil || count != 2 {
			t.Fatal("historical dates blocked current billing day or duplicated today's fact", count, err)
		}
		if count, err := f.db.EnqueueOverdueInvoiceReminders(f.ctx, 1); err != nil || count != 0 {
			t.Fatal("billing history replay duplicated today's reminder", count, err)
		}
		if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE invoice_id=$1 AND resource_due_at IS NOT NULL`, today); count != 0 {
			t.Fatal("UTC date-only history was resent for the same Shanghai business day", count)
		}
		if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE invoice_id=$1 AND resource_due_at IS NOT NULL`, yesterday); count != 2 {
			t.Fatal("yesterday's actual scan suppressed today's reminder", count)
		}
	})
	for _, item := range []struct{ name, mutation string }{
		{"paid", `UPDATE invoices SET status='paid',paid_at=clock_timestamp() WHERE id=$1`},
		{"cancelled", `UPDATE invoices SET status='cancelled' WHERE id=$1`},
		{"deadline_changed", `UPDATE invoices SET due_at=due_at+interval '1 microsecond' WHERE id=$1`},
		{"review", `UPDATE invoices SET import_review_required=true WHERE id=$1`},
	} {
		t.Run(item.name, func(t *testing.T) {
			f := legacyDeadlineFixture(t, "UTC")
			var invoice int64
			var due time.Time
			if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents,due_at)
 VALUES('legacy-billing',$1,'automatic',100,
 (((now() AT TIME ZONE 'Asia/Shanghai')::date-2)+time '00:30') AT TIME ZONE 'Asia/Shanghai')
 RETURNING id,due_at`, f.customer).Scan(&invoice, &due); err != nil {
				t.Fatal(err)
			}
			if count, err := f.db.EnqueueOverdueInvoiceReminders(f.ctx, 100); err != nil || count != 2 {
				t.Fatal("billing queue", count, err)
			}
			if count := f.count(t, `SELECT count(*) FROM notification_delivery_events
 WHERE invoice_id=$1 AND resource_due_at=$2 AND due_date=(now() AT TIME ZONE 'Asia/Shanghai')::date`, invoice, due); count != 2 {
				t.Fatal("billing business day or source deadline lost", count)
			}
			mail, sms := legacyDeadlineClaimBoth(t, f)
			if !strings.Contains(mail.Body, due.In(notificationShanghai).Format("2006-01-02")) {
				t.Fatal("billing body uses UTC date")
			}
			legacyDeadlineActive(t, f, mail, sms, true, true)
			f.exec(t, item.mutation, invoice)
			legacyDeadlineActive(t, f, mail, sms, false, false)
			if err := f.db.RecoverStaleEmailJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if err := f.db.RecoverStaleSMSJobs(f.ctx); err != nil {
				t.Fatal(err)
			}
			if job, err := f.db.ClaimEmailJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
				t.Fatal("obsolete billing reminder reached email adapter", job, err)
			}
			if job, err := f.db.ClaimSMSJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
				t.Fatal("obsolete billing reminder reached SMS adapter", job, err)
			}
		})
	}
}

func TestNotificationLegacyDeadlineRejectsElapsedStage(t *testing.T) {
	f := legacyDeadlineFixture(t, "UTC")
	r := lifecycleFixtureResources(t, f, f.customer, "elapsed-stage")
	due := legacyDeadlineSetService(t, f, r.service, 7)
	var mailID, smsID int64
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if err := tx.QueryRow(f.ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,expires_at,channel_instance_id)
 SELECT email,'Eight days left','Old stage reminder','service_expiry_reminder',now()+interval '1 day','mail-smtp-default'
 FROM users WHERE id=$1 RETURNING id`, f.customer).Scan(&mailID); err != nil {
			return err
		}
		var err error
		smsID, err = f.db.queueSMSWithTemplateID(f.ctx, tx, "+8613800000000", "Eight days left", "service_expiry_reminder", "", time.Now().Add(24*time.Hour), "", "sms-default")
		if err != nil {
			return err
		}
		_, err = tx.Exec(f.ctx, `INSERT INTO notification_delivery_events
 (service_id,user_id,channel,kind,stage,due_date,resource_due_at,email_outbox_id,sms_outbox_id,created_at)
 SELECT $1,$2,c.channel,'service_expiry_reminder',8,($3::timestamptz AT TIME ZONE 'Asia/Shanghai')::date,$3,
 CASE WHEN c.channel='email' THEN $4::bigint END,CASE WHEN c.channel='sms' THEN $5::bigint END,now()-interval '1 day'
 FROM (VALUES('email'),('sms')) c(channel)`, r.service, f.customer, due, mailID, smsID)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if job, err := f.db.ClaimEmailJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
		t.Fatal("same deadline with elapsed stage reached email adapter", job, err)
	}
	if job, err := f.db.ClaimSMSJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
		t.Fatal("same deadline with elapsed stage reached SMS adapter", job, err)
	}
	if count := f.count(t, `SELECT (SELECT count(*) FROM email_outbox WHERE id=$1 AND status='expired' AND attempts=0)+
 (SELECT count(*) FROM sms_outbox WHERE id=$2 AND status='expired' AND attempts=0)`, mailID, smsID); count != 2 {
		t.Fatal("elapsed stage was retried or stayed pending", count)
	}
}

func TestNotificationLegacyDeadlineUpgrade172PreservesFacts(t *testing.T) {
	db := supplierExpirySchema171(t)
	applyNotificationUpgradeMigration(t, db, supplierExpiryMigration172)
	assertNotificationUpgradeBinaryGate(t, db, "172")
	actor, customer := topUpFixture(t, db)
	f := notificationFixture{db: db, actor: actor, customer: customer, ctx: t.Context()}
	for _, kind := range []string{"service_expiry_reminder", "order_payment_reminder", "invoice_overdue_reminder"} {
		for _, status := range []string{"pending", "sending", "dead_letter", "sent"} {
			r := lifecycleFixtureResources(t, f, customer, kind+"-"+status)
			for _, channelName := range []string{"email", "sms"} {
				var outbox int64
				if channelName == "email" {
					if err := db.Pool.QueryRow(f.ctx, `INSERT INTO email_outbox(recipient,subject,body,kind,status,attempts,expires_at,locked_at,dead_letter_at)
 VALUES('old@example.test','Original subject','Original body',$1,$2,2,now()+interval '1 day',
 CASE WHEN $2='sending' THEN now() END,CASE WHEN $2='dead_letter' THEN now() END) RETURNING id`, kind, status).Scan(&outbox); err != nil {
						t.Fatal(err)
					}
				} else if err := db.Pool.QueryRow(f.ctx, `INSERT INTO sms_outbox(recipient,body_encrypted,kind,status,attempts,expires_at,locked_at,dead_letter_at)
 VALUES('+8613800000000','old-opaque-body'::bytea,$1,$2,2,now()+interval '1 day',
 CASE WHEN $2='sending' THEN now() END,CASE WHEN $2='dead_letter' THEN now() END) RETURNING id`, kind, status).Scan(&outbox); err != nil {
					t.Fatal(err)
				}
				column := "email_outbox_id"
				if channelName == "sms" {
					column = "sms_outbox_id"
				}
				var service, invoice any
				stage := 0
				if kind == "service_expiry_reminder" {
					service, stage = r.service, 7
				} else {
					invoice = r.invoice
				}
				f.exec(t, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date,`+column+`)
 VALUES($1,$2,$3,$4,$5,$6,(now() AT TIME ZONE 'Asia/Shanghai')::date,$7)`, service, customer, invoice, channelName, kind, stage, outbox)
			}
		}
	}
	f.exec(t, `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('other@example.test','Other','Keep original','system');
 INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at) VALUES('+8613800000000','untouched'::bytea,'system',now()+interval '1 day')`)
	snapshot := func() string {
		t.Helper()
		var out string
		if err := db.Pool.QueryRow(f.ctx, `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
 'events',(SELECT jsonb_agg(to_jsonb(x)-'resource_due_at' ORDER BY id) FROM notification_delivery_events x),
 'mail',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM email_outbox x WHERE status='sent' OR kind='system'),
 'sms',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM sms_outbox x WHERE status='sent' OR kind='system'))::text`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	oldMigrations := supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration172)
	if err := db.CheckSchema(f.ctx); err == nil {
		t.Fatal("runtime accepted schema172 without deadline migration")
	}
	if err := db.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSchema(f.ctx); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before || supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration172) != oldMigrations {
		t.Fatal("upgrade rewrote historical business facts, sent content, unaffected jobs or predecessor checksums")
	}
	if count := f.count(t, `SELECT (SELECT count(*) FROM email_outbox WHERE status='expired')+
 (SELECT count(*) FROM sms_outbox WHERE status='expired')`); count != 18 {
		t.Fatal("unproven legacy reminder jobs were not conservatively expired", count)
	}
	if count := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE resource_due_at IS NOT NULL`); count != 0 {
		t.Fatal("upgrade inferred a deadline from current resources", count)
	}
	if err := db.Migrate(f.ctx); err != nil || snapshot() != before {
		t.Fatal("repeat migration changed preserved facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
