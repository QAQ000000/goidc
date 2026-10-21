package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Dates below deliberately cross Shanghai midnight while PostgreSQL sessions
// stay in UTC. Historical settings simulate consent recorded before downtime,
// rather than making a new subscription retroactively receive old events.
func notificationCatchupFixture(t *testing.T, channels ...string) notificationFixture {
	t.Helper()
	f := notificationCenterFixture(t)
	cfg := f.db.Pool.Config()
	cfg.ConnConfig.RuntimeParams["timezone"] = "UTC"
	pool, err := pgxpool.NewWithConfig(f.ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	f.db.Pool.Close()
	f.db.Pool = pool
	f.exec(t, `UPDATE notification_templates SET active=false`)
	for _, ch := range channels {
		if ch == "telegram" {
			f.bind(t, f.customer, "123456789")
		}
		lifecycleBusinessPreference(t, f, ch, 0)
		lifecycleRoute(t, f, "service.expired", ch)
		lifecycleRoute(t, f, "service.expiry_reminder", ch)
	}
	f.exec(t, `UPDATE notification_routes SET updated_at=now()-interval '10 days';
 UPDATE notification_message_templates SET updated_at=now()-interval '10 days';
 UPDATE user_notification_preferences SET updated_at=now()-interval '10 days';
 UPDATE notification_targets SET created_at=now()-interval '10 days',updated_at=now()-interval '10 days';
 UPDATE channel_instances SET updated_at=now()-interval '10 days'`)
	var timezone string
	if err = f.db.Pool.QueryRow(f.ctx, `SHOW timezone`).Scan(&timezone); err != nil || timezone != "UTC" {
		t.Fatalf("test must use UTC sessions: timezone=%q err=%v", timezone, err)
	}
	return f
}

func notificationCatchupService(t *testing.T, f notificationFixture, suffix, dueSQL string) lifecycleResources {
	t.Helper()
	r := lifecycleFixtureResources(t, f, f.customer, suffix)
	f.exec(t, `UPDATE services SET next_due_at=`+dueSQL+` WHERE id=$1`, r.service)
	return r
}

func notificationCatchupScan(t *testing.T, f notificationFixture, limit int) {
	t.Helper()
	count, err := f.db.EnqueueServiceExpiryReminders(f.ctx, limit)
	if err != nil || count != 0 {
		t.Fatalf("scan failed or changed legacy outbox count: count=%d err=%v", count, err)
	}
}

func TestNotificationCatchupBoundedWindowAndShanghaiDates(t *testing.T) {
	f := notificationCatchupFixture(t, "inbox")
	yesterday := notificationCatchupService(t, f, "yesterday", `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '00:30') AT TIME ZONE 'Asia/Shanghai'`)
	recent := notificationCatchupService(t, f, "recent", `clock_timestamp()-interval '71 hours'`)
	old := notificationCatchupService(t, f, "too-old", `clock_timestamp()-interval '73 hours'`)
	today := notificationCatchupService(t, f, "today", `clock_timestamp()-interval '1 microsecond'`)
	upcoming := notificationCatchupService(t, f, "upcoming", `(((now() AT TIME ZONE 'Asia/Shanghai')::date+7)+time '00:30') AT TIME ZONE 'Asia/Shanghai'`)
	missedStage := notificationCatchupService(t, f, "missed-stage", `(((now() AT TIME ZONE 'Asia/Shanghai')::date+6)+time '00:30') AT TIME ZONE 'Asia/Shanghai'`)
	for range 3 {
		notificationCatchupScan(t, f, 100)
	}
	for _, r := range []lifecycleResources{yesterday, recent, today} {
		if n := f.count(t, `SELECT count(*) FROM notification_events e JOIN services s ON s.id=e.service_id
 WHERE e.kind='service.expired' AND e.service_id=$1 AND e.payload->>'stage'='0'
 AND e.dedupe_key='service.expired:'||s.id::text||':'||(extract(epoch FROM s.next_due_at)*1000000)::bigint::text||':0'
 AND e.payload->>'due_date'=(s.next_due_at AT TIME ZONE 'Asia/Shanghai')::date::text`, r.service); n != 1 {
			t.Fatalf("expiry catchup missing, repeated or wrong date: service=%d count=%d", r.service, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=ANY($1)`, []int64{old.service, missedStage.service}); n != 0 {
		t.Fatalf("out-of-window expiry or obsolete reminder stage backfilled=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE kind='service.expiry_reminder' AND service_id=$1 AND payload->>'stage'='7'`, upcoming.service); n != 1 {
		t.Fatalf("current Shanghai reminder stage=%d", n)
	}
	if _, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("inbox must stay internal: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1`, f.customer); n != 4 {
		t.Fatalf("eligible catchup and current reminder delivered=%d", n)
	}
}

func TestNotificationCatchupConcurrentBatchDrainAndRestart(t *testing.T) {
	f := notificationCatchupFixture(t, "inbox")
	for i := range 8 {
		r := notificationCatchupService(t, f, fmt.Sprintf("batch-%d", i), `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai'`)
		if i%2 == 0 {
			f.exec(t, `UPDATE services SET status='suspended' WHERE id=$1`, r.service)
		}
	}
	var wg sync.WaitGroup
	errorsCh := make(chan error, 3)
	for range 3 {
		wg.Go(func() {
			for range 10 {
				if count, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil {
					errorsCh <- err
					return
				} else if count != 0 {
					errorsCh <- fmt.Errorf("legacy count changed: %d", count)
					return
				}
			}
		})
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE kind='service.expired'`); n != 8 {
		t.Fatalf("concurrent small batches did not drain exactly once: %d", n)
	}
	// A fresh pool and Store recreate worker-local state and must retain the
	// persisted identity; no in-memory watermark can be required for correctness.
	pool, err := pgxpool.NewWithConfig(f.ctx, f.db.Pool.Config())
	if err != nil {
		t.Fatal(err)
	}
	restarted := &Store{Pool: pool}
	t.Cleanup(restarted.Close)
	if count, err := restarted.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil || count != 0 {
		t.Fatal(count, err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE kind='service.expired'`); n != 8 {
		t.Fatalf("restart recreated historical expiry events=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='pending'`); n != 8 {
		t.Fatalf("catchup deliveries=%d", n)
	}
}

func TestNotificationCatchupRecognizesExistingExpiryFacts(t *testing.T) {
	f := notificationCatchupFixture(t, "inbox")
	statuses := []string{"pending", "accepted", "skipped", "failed", "unknown"}
	var changedService int64
	for i, status := range statuses {
		r := notificationCatchupService(t, f, "existing-"+status, `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai'`)
		changedService = r.service
		var due time.Time
		if err := f.db.Pool.QueryRow(f.ctx, `SELECT next_due_at FROM services WHERE id=$1`, r.service).Scan(&due); err != nil {
			t.Fatal(err)
		}
		key := fmt.Sprintf("service.expired:%d:%d:0", r.service, due.UnixMicro())
		if i > 0 {
			key = "historical-key-" + status
		}
		lifecycleQueue(t, f, r, "service.expired", key, map[string]string{"stage": "0"})
		f.exec(t, `UPDATE notification_deliveries SET status=$1 WHERE event_id=(SELECT id FROM notification_events WHERE dedupe_key=$2)`, status, key)
	}
	for range 3 {
		notificationCatchupScan(t, f, 1)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE kind='service.expired'`); n != len(statuses) {
		t.Fatalf("legacy identity or terminal result recreated=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries`); n != len(statuses) {
		t.Fatalf("historical expiry delivery was backfilled=%d", n)
	}
	// A distinct precise deadline on the same Shanghai day is a new fact;
	// date-only identity must not suppress it because an older result exists.
	f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 second' WHERE id=$1`, changedService)
	for range 2 {
		notificationCatchupScan(t, f, 100)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind='service.expired'`, changedService); n != 2 {
		t.Fatalf("new precise deadline was lost or duplicated=%d", n)
	}
}

func TestNotificationCatchupRechecksCurrentServiceState(t *testing.T) {
	f := notificationCatchupFixture(t, "inbox")
	scenarios := []string{"renewed", "exact-due-changed", "ownership", "terminated", "import-review", "once", "window-expired"}
	resources := make(map[string]lifecycleResources)
	for _, scenario := range scenarios {
		resources[scenario] = notificationCatchupService(t, f, "state-"+scenario, `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai'`)
	}
	notificationCatchupScan(t, f, 100)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='pending'`); n != len(scenarios) {
		t.Fatalf("catchup was ineligible before state changes=%d", n)
	}
	for scenario, r := range resources {
		switch scenario {
		case "renewed":
			f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1`, r.service)
		case "exact-due-changed":
			f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 second' WHERE id=$1`, r.service)
		case "ownership":
			lifecycleTransfer(t, f, r, f.other, "catchup-stale-owner-transfer")
		case "terminated":
			f.exec(t, `UPDATE services SET status='terminated' WHERE id=$1`, r.service)
		case "import-review":
			f.exec(t, `UPDATE services SET import_review_required=true WHERE id=$1`, r.service)
		case "once":
			f.exec(t, `UPDATE services SET billing_cycle='once' WHERE id=$1`, r.service)
		case "window-expired":
			// Move both live and frozen deadlines together, so exact-instant
			// mismatch cannot accidentally satisfy the 72-hour stale check.
			f.exec(t, `UPDATE services SET next_due_at=clock_timestamp()-interval '73 hours' WHERE id=$1`, r.service)
			f.exec(t, `UPDATE notification_events SET payload=jsonb_set(payload,'{resource_due_at}',to_jsonb((SELECT next_due_at FROM services WHERE id=$1))) WHERE service_id=$1`, r.service)
		}
	}
	if _, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale catchup reached adapter: %v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='skipped' AND code='eligibility_changed'`); n != len(scenarios)-1 {
		t.Fatalf("stale expiry catchup not suppressed=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='skipped' AND code='service_owner_changed'`); n != 1 {
		t.Fatalf("ownership transfer did not terminate the old catchup delivery=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages`); n != 0 {
		t.Fatalf("obsolete catchup inbox messages=%d", n)
	}
	for _, scenario := range []string{"active-exact-due-changed", "active-window-expired", "same-day-snapshot-after-midnight"} {
		t.Run(scenario, func(t *testing.T) {
			activeFixture := notificationCatchupFixture(t, "telegram")
			dueSQL := `clock_timestamp()-interval '71 hours'`
			if scenario == "same-day-snapshot-after-midnight" {
				// Fresh settings are valid for a current-day event. Crossing
				// midnight later must not retroactively apply catchup consent.
				activeFixture.exec(t, `UPDATE notification_routes SET updated_at=now();
 UPDATE notification_message_templates SET updated_at=now();
 UPDATE user_notification_preferences SET updated_at=now();
 UPDATE notification_targets SET updated_at=now();
 UPDATE channel_instances SET updated_at=now()`)
				dueSQL = `clock_timestamp()-interval '1 microsecond'`
			}
			r := notificationCatchupService(t, activeFixture, scenario, dueSQL)
			notificationCatchupScan(t, activeFixture, 100)
			job, err := activeFixture.db.ClaimNotificationJob(activeFixture.ctx)
			if err != nil || job == nil {
				t.Fatalf("eligible expiry was not claimed: job=%+v err=%v", job, err)
			}
			if active, err := activeFixture.db.NotificationJobActive(activeFixture.ctx, *job); err != nil || !active {
				t.Fatalf("newly claimed expiry is inactive: active=%v err=%v", active, err)
			}
			switch scenario {
			case "active-exact-due-changed":
				activeFixture.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 second' WHERE id=$1`, r.service)
			case "active-window-expired":
				activeFixture.exec(t, `UPDATE services SET next_due_at=clock_timestamp()-interval '73 hours' WHERE id=$1`, r.service)
				activeFixture.exec(t, `UPDATE notification_events SET payload=jsonb_set(payload,'{resource_due_at}',to_jsonb((SELECT next_due_at FROM services WHERE id=$1))) WHERE service_id=$1`, r.service)
			case "same-day-snapshot-after-midnight":
				activeFixture.exec(t, `UPDATE services SET next_due_at=(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai' WHERE id=$1`, r.service)
				activeFixture.exec(t, `UPDATE notification_events SET payload=jsonb_set(payload,'{resource_due_at}',to_jsonb((SELECT next_due_at FROM services WHERE id=$1))) WHERE service_id=$1`, r.service)
			}
			wantActive := scenario == "same-day-snapshot-after-midnight"
			if active, err := activeFixture.db.NotificationJobActive(activeFixture.ctx, *job); err != nil || active != wantActive {
				t.Fatalf("active recheck missed %s: active=%v want=%v err=%v", scenario, active, wantActive, err)
			}
			if n := activeFixture.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='sending' AND lease_until>clock_timestamp() AND expires_at>clock_timestamp()`, job.ID); n != 1 {
				t.Fatal("test invalidation must not depend on lease or delivery expiry")
			}
		})
	}
}

func TestNotificationCatchupDoesNotBackfillNewSubscriptions(t *testing.T) {
	for _, scenario := range []string{"route", "template", "preference", "binding", "instance"} {
		t.Run(scenario, func(t *testing.T) {
			channel := "inbox"
			if scenario == "binding" || scenario == "instance" {
				channel = "telegram"
			}
			f := notificationCatchupFixture(t, channel)
			r := notificationCatchupService(t, f, "late-"+scenario, `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai'`)
			switch scenario {
			case "route":
				f.exec(t, `UPDATE notification_routes SET updated_at=now() WHERE event_kind='service.expired'`)
			case "template":
				f.exec(t, `UPDATE notification_message_templates SET updated_at=now() WHERE event_kind='service.expired'`)
			case "preference":
				f.exec(t, `UPDATE user_notification_preferences SET updated_at=now() WHERE user_id=$1`, f.customer)
			case "binding":
				f.exec(t, `UPDATE notification_targets SET created_at=now(),updated_at=now() WHERE user_id=$1`, f.customer)
			case "instance":
				f.exec(t, `UPDATE channel_instances SET updated_at=now() WHERE id='bot-fixture'`)
			}
			for range 2 {
				notificationCatchupScan(t, f, 100)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind='service.expired'`, r.service); n != 1 {
				t.Fatalf("historical fact must be sampled once=%d", n)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='skipped' AND code='catchup_subscription_changed'`); n != 1 {
				t.Fatalf("new %s retroactively subscribed historical expiry: skipped=%d", scenario, n)
			}
			if _, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("historical subscription reached adapter: %v", err)
			}
			if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages`); n != 0 {
				t.Fatalf("new subscription received historical inbox=%d", n)
			}
		})
	}
}

func TestNotificationCatchupDisabledRoutesAndPerChannelConsent(t *testing.T) {
	f := notificationCatchupFixture(t, "inbox", "telegram")
	r := notificationCatchupService(t, f, "channels", `(((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai'`)
	f.exec(t, `UPDATE notification_routes SET enabled=false WHERE event_kind='service.expired'`)
	notificationCatchupScan(t, f, 100)
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1`, r.service); n != 0 {
		t.Fatalf("disabled routes generated unused events=%d", n)
	}
	// Only inbox was enabled before downtime. Telegram becomes available now.
	f.exec(t, `UPDATE notification_routes SET enabled=true,updated_at=CASE WHEN channel='telegram' THEN now() ELSE now()-interval '10 days' END WHERE event_kind='service.expired'`)
	notificationCatchupScan(t, f, 100)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='inbox' AND status='pending'`); n != 1 {
		t.Fatalf("eligible historical channel was suppressed=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='telegram' AND status='skipped' AND code='catchup_subscription_changed'`); n != 1 {
		t.Fatalf("newly enabled channel received historical event=%d", n)
	}
	// A later correction cannot reanimate the already sampled skipped channel.
	f.exec(t, `UPDATE notification_routes SET updated_at=now()-interval '10 days' WHERE event_kind='service.expired'`)
	notificationCatchupScan(t, f, 100)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='telegram' AND status='pending'`); n != 0 {
		t.Fatalf("historical skipped delivery was revived=%d", n)
	}
	if _, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1`, f.customer); n != 1 {
		t.Fatalf("eligible inbox delivery=%d", n)
	}
}
