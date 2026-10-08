package httpapi

import (
	"context"
	"errors"
	"fmt"
	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"sync"
	"testing"
	"time"
)

func billingSchedulerFixture(t *testing.T, db *store.Store, uid, pid int64, n int, overdue bool) (int64, int64) {
	t.Helper()
	var oid, itemID, sid, iid int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('SCHED-'||gen_random_uuid()::text,$1,'pending',1234,$2,'fixture') RETURNING id`, uid, fmt.Sprintf("schedule-fixture-%d", n)).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'调度商品','monthly',1,1234,0,1234) RETURNING id`, oid, pid).Scan(&itemID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,unit_number,recurring_price_cents) VALUES($1,$2,$3,$4,'调度服务',$5,'monthly','active',now()+interval '1 month','simulation',1,1234) RETURNING id`, uid, oid, itemID, pid, fmt.Sprintf("schedule-%d.example", n)).Scan(&sid); err != nil {
		t.Fatal(err)
	}
	due := time.Now().Add(48 * time.Hour)
	if overdue {
		due = time.Now().Add(-48 * time.Hour)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,due_at) VALUES('SCHED-'||gen_random_uuid()::text,$1,$2,'unpaid',1234,$3) RETURNING id`, uid, oid, due).Scan(&iid); err != nil {
		t.Fatal(err)
	}
	return sid, iid
}

func TestBillingSchedulerDrainsLimitedBatchesAndConcurrentWorkers(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "scheduler-drain-user", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true,phone='+8613800138000' WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), "UPDATE users SET phone_verified=true WHERE id=$1", uid); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	for i := 0; i < 4; i++ {
		billingSchedulerFixture(t, db, uid, pid, i, i%2 == 0)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := db.EnqueueBillingNotifications(t.Context(), 1); err != nil {
					errs <- err
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var events, email, sms int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM notification_delivery_events WHERE kind IN ('order_payment_reminder','invoice_overdue_reminder')),(SELECT count(*) FROM email_outbox),(SELECT count(*) FROM sms_outbox)`).Scan(&events, &email, &sms); err != nil {
		t.Fatal(err)
	}
	if events != 8 || email != 4 || sms != 4 {
		t.Fatalf("batch drain events=%d email=%d sms=%d", events, email, sms)
	}
	if n, err := db.EnqueueBillingNotifications(t.Context(), 1); err != nil || n != 0 {
		t.Fatalf("replay n=%d err=%v", n, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE notification_delivery_events SET due_date=CURRENT_DATE-1 WHERE kind IN ('order_payment_reminder','invoice_overdue_reminder')`); err != nil {
		t.Fatal(err)
	}
	if n, err := db.EnqueueBillingNotifications(t.Context(), 100); err != nil || n != 8 {
		t.Fatalf("next day n=%d err=%v", n, err)
	}
}

func TestOverdueSchedulerCompletes097AndCancelsPaidJobs(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "scheduler-suspend-user", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE category='mail'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	pid := product(t, db, 10)
	first, _ := billingSchedulerFixture(t, db, uid, pid, 1, true)
	second, paidInvoice := billingSchedulerFixture(t, db, uid, pid, 2, true)
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("first batch n=%d err=%v", n, err)
	}
	if n, err := db.EnqueueOverdueServiceSuspensions(t.Context(), 1); err != nil || n != 1 {
		t.Fatalf("second batch n=%d err=%v", n, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, paidInvoice); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		job, err := db.ClaimProvisionJob(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if err = db.ProcessProvisionJob(t.Context(), *job); err != nil {
			t.Fatal(err)
		}
	}
	var state1, state2, jobState string
	var events int
	if err := db.Pool.QueryRow(t.Context(), `SELECT status FROM services WHERE id=$1`, first).Scan(&state1); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT status FROM services WHERE id=$1`, second).Scan(&state2); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT status FROM provision_jobs WHERE service_id=$1`, second).Scan(&jobState); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_delivery_events WHERE kind='service_suspended'`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if state1 != "suspended" || state2 != "active" || jobState != "cancelled" || events != 1 {
		t.Fatalf("suspend=%s paid=%s job=%s 097 events=%d", state1, state2, jobState, events)
	}
}

func TestOverdueSchedulerUsesPaymentLockOrder(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "scheduler-lock-user", "customer")
	pid := product(t, db, 10)
	sid, iid := billingSchedulerFixture(t, db, uid, pid, 1, true)
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SELECT id FROM invoices WHERE id=$1 FOR UPDATE`, iid); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		n, e := db.EnqueueOverdueServiceSuspensions(ctx, 100)
		if e == nil && n != 0 {
			e = fmt.Errorf("paid invoice queued %d", n)
		}
		done <- e
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		var waiting bool
		if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM invoices WHERE id=$1 FOR UPDATE%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scheduler did not wait for invoice")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, `SELECT id FROM services WHERE id=$1 FOR UPDATE`, sid); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, iid); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}

func TestUnifiedOutboxRetryAndManualRecoveryFencesOldLease(t *testing.T) {
	for _, channel := range []string{"email", "sms"} {
		t.Run(channel, func(t *testing.T) {
			db, h := testServer(t)
			_, admin := user(t, db, h, "retry-admin", "super_admin")
			_, customer := user(t, db, h, "retry-customer", "customer")
			table := channel + "_outbox"
			var id int64
			if channel == "email" {
				if err := db.Pool.QueryRow(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,max_attempts,expires_at) VALUES('retry@example.test','subject','body','system',3,now()+interval '1 day') RETURNING id`).Scan(&id); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE id='sms-default'`); err != nil {
					t.Fatal(err)
				}
				tx, err := db.Pool.Begin(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				if err = db.QueueSMS(t.Context(), tx, "+8613800138000", "retry payload", "system", "", time.Now().Add(24*time.Hour), "sms-default"); err != nil {
					t.Fatal(err)
				}
				if err = tx.Commit(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = db.Pool.QueryRow(t.Context(), `UPDATE sms_outbox SET max_attempts=3 RETURNING id`).Scan(&id); err != nil {
					t.Fatal(err)
				}
			}
			type lease struct {
				attempt int
				at      time.Time
			}
			claim := func() (lease, error) {
				if channel == "email" {
					job, err := db.ClaimEmailJob(t.Context())
					if err != nil {
						return lease{}, err
					}
					return lease{job.Attempts, job.LockedAt}, nil
				}
				job, err := db.ClaimSMSJob(t.Context())
				if err != nil {
					return lease{}, err
				}
				return lease{job.Attempts, job.LockedAt}, nil
			}
			complete := func(l lease, fail error) error {
				if channel == "email" {
					return db.CompleteEmailJob(t.Context(), store.EmailJob{ID: id, Attempts: l.attempt, LockedAt: l.at}, fail)
				}
				return db.CompleteSMSJob(t.Context(), store.SMSJob{ID: id, Attempts: l.attempt, LockedAt: l.at}, fail)
			}
			path := fmt.Sprintf("/api/v1/admin/%s-outbox/%d/retry", channel, id)
			var old lease
			for attempt := 1; attempt <= 3; attempt++ {
				l, err := claim()
				if err != nil {
					t.Fatal(err)
				}
				if attempt == 1 {
					old = l
				}
				if l.attempt != attempt {
					t.Fatalf("attempt=%d want=%d", l.attempt, attempt)
				}
				expect(t, request(h, "POST", path, map[string]any{}, admin, ""), 409)
				if err = complete(l, errors.New("fixture failure")); err != nil {
					t.Fatal(err)
				}
				var status string
				var delay float64
				var deadAt *time.Time
				if err = db.Pool.QueryRow(t.Context(), "SELECT status,extract(epoch from available_at-now())::float8,dead_letter_at FROM "+table+" WHERE id=$1", id).Scan(&status, &delay, &deadAt); err != nil {
					t.Fatal(err)
				}
				if attempt < 3 {
					expected := float64(60 * (int64(1) << (attempt - 1)))
					if status != "pending" || delay < expected-3 || delay > expected+1 || deadAt != nil {
						t.Fatalf("retry status=%s delay=%f dead=%v", status, delay, deadAt)
					}
					if _, err = claim(); !errors.Is(err, pgx.ErrNoRows) {
						t.Fatalf("backoff not honored: %v", err)
					}
					if _, err = db.Pool.Exec(t.Context(), "UPDATE "+table+" SET available_at=now() WHERE id=$1", id); err != nil {
						t.Fatal(err)
					}
				} else if status != "dead_letter" || deadAt == nil {
					t.Fatalf("limit status=%s dead=%v", status, deadAt)
				}
			}
			expect(t, request(h, "POST", path, map[string]any{}, customer, ""), 403)
			expect(t, request(h, "POST", path, map[string]any{}, admin, ""), 200)
			fresh, err := claim()
			if err != nil {
				t.Fatal(err)
			}
			if fresh.attempt != 1 || fresh.at.Equal(old.at) {
				t.Fatalf("fresh lease=%v old=%v", fresh, old)
			}
			if err = complete(old, nil); err != nil {
				t.Fatal(err)
			}
			if err = complete(old, errors.New("late failure")); err != nil {
				t.Fatal(err)
			}
			var status string
			if err = db.Pool.QueryRow(t.Context(), "SELECT status FROM "+table+" WHERE id=$1", id).Scan(&status); err != nil || status != "sending" {
				t.Fatalf("old worker changed retry: %s %v", status, err)
			}
			if err = complete(fresh, nil); err != nil {
				t.Fatal(err)
			}
			expect(t, request(h, "POST", path, map[string]any{}, admin, ""), 409)
		})
	}
}

func TestOutboxRecoveryBackoffAndExpiredDeadLetterCleanup(t *testing.T) {
	db, _ := testServer(t)
	for _, table := range []string{"email_outbox", "sms_outbox"} {
		var id int64
		query := `INSERT INTO email_outbox(recipient,subject,body,kind,expires_at,attempts,max_attempts,status,locked_at) VALUES('recover@example.test','subject','body','system',now()+interval '1 day',2,5,'sending',now()-interval '6 minutes') RETURNING id`
		if table == "sms_outbox" {
			query = `INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,attempts,max_attempts,status,locked_at) VALUES('+8613800138000','payload','system',now()+interval '1 day',2,5,'sending',now()-interval '6 minutes') RETURNING id`
		}
		if err := db.Pool.QueryRow(t.Context(), query).Scan(&id); err != nil {
			t.Fatal(err)
		}
		recover := func() error {
			if table == "email_outbox" {
				return db.RecoverStaleEmailJobs(t.Context())
			}
			return db.RecoverStaleSMSJobs(t.Context())
		}
		if err := recover(); err != nil {
			t.Fatal(err)
		}
		var status string
		var delay float64
		var sealed []byte
		var deadAt *time.Time
		if err := db.Pool.QueryRow(t.Context(), "SELECT status,extract(epoch from available_at-now())::float8 FROM "+table+" WHERE id=$1", id).Scan(&status, &delay); err != nil {
			t.Fatal(err)
		}
		if status != "pending" || delay < 117 || delay > 121 {
			t.Fatalf("%s recovery status=%s delay=%f", table, status, delay)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE "+table+" SET status='dead_letter',dead_letter_at=now(),expires_at=now()-interval '1 minute' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		if err := recover(); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "SELECT status,body_encrypted,dead_letter_at FROM "+table+" WHERE id=$1", id).Scan(&status, &sealed, &deadAt); err != nil {
			t.Fatal(err)
		}
		if status != "expired" || len(sealed) != 0 || deadAt != nil {
			t.Fatalf("%s expired cleanup status=%s payload=%d dead=%v", table, status, len(sealed), deadAt)
		}
		if _, err := db.Pool.Exec(t.Context(), "UPDATE "+table+" SET status='pending',attempts=max_attempts,expires_at=now()+interval '1 day' WHERE id=$1", id); err != nil {
			t.Fatal(err)
		}
		if table == "email_outbox" {
			if _, err := db.ClaimEmailJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("over-limit email claimed: %v", err)
			}
		} else {
			if _, err := db.ClaimSMSJob(t.Context()); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("over-limit sms claimed: %v", err)
			}
		}
		if err := recover(); err != nil {
			t.Fatal(err)
		}
		if err := db.Pool.QueryRow(t.Context(), "SELECT status FROM "+table+" WHERE id=$1", id).Scan(&status); err != nil || status != "dead_letter" {
			t.Fatalf("%s over-limit recovery=%s err=%v", table, status, err)
		}
	}
}

func TestBillingNotificationFailureRollsBackEventAndOutbox(t *testing.T) {
	db, h := testServer(t)
	uid, _ := user(t, db, h, "scheduler-rollback-user", "customer")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET email_verified=true,phone='+8613800138000' WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE users SET phone_verified=true WHERE id=$1`, uid); err != nil {
		t.Fatal(err)
	}
	billingSchedulerFixture(t, db, uid, product(t, db, 10), 1, false)
	// SMS encryption failure occurs after the email event/row were written.
	db.ProvisionEncryptionKey = nil
	if _, err := db.EnqueueBillingNotifications(t.Context(), 100); err == nil {
		t.Fatal("missing encryption key accepted")
	}
	var count int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM notification_delivery_events)+(SELECT count(*) FROM email_outbox)+(SELECT count(*) FROM sms_outbox)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial notification commit count=%d err=%v", count, err)
	}
}
