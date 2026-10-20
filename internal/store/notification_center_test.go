package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

var notificationTestBotOnce sync.Once

type notificationFixture struct {
	db                     *Store
	actor, customer, other int64
	ctx                    context.Context
}

func notificationCenterFixture(t *testing.T) notificationFixture {
	t.Helper()
	notificationTestBotOnce.Do(func() {
		// Store tests cannot import notification (which imports store). The
		// real Telegram factory is covered by the HTTP/adapter suites.
		if _, exists := channel.Lookup(channel.Bot, "telegram_bot"); !exists {
			channel.MustRegisterFactory(channel.Descriptor{ID: "telegram_bot", Name: "Telegram fixture", Category: channel.Bot, Fields: []channel.Field{{Key: "bot_username", Label: "Bot", Type: channel.Text, Required: true}}}, func(any) (any, error) { return struct{}{}, nil })
		}
	})
	db := isolatedTestStore(t)
	db.ProvisionEncryptionKey = make([]byte, 32)
	actor, customer := topUpFixture(t, db)
	f := notificationFixture{db: db, actor: actor, customer: customer, ctx: t.Context()}
	if err := db.Pool.QueryRow(f.ctx, `INSERT INTO users(username,email,password_hash,display_name) VALUES('notification-other','notification-other@example.test','unused','Other') RETURNING id`).Scan(&f.other); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config) VALUES('bot-fixture','bot','telegram_bot','Fixture',true,'{"bot_username":"fixture_bot"}')`)
	for _, id := range []int64{f.customer, f.other} {
		f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, fmt.Sprint("notification-session-", id), id)
	}
	return f
}

func (f notificationFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.db.Pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func (f notificationFixture) count(t *testing.T, sql string, args ...any) int {
	t.Helper()
	var count int
	if err := f.db.Pool.QueryRow(f.ctx, sql, args...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func (f notificationFixture) challenge(t *testing.T, userID int64) NotificationBindingChallenge {
	t.Helper()
	var out NotificationBindingChallenge
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
		out, err = f.db.CreateNotificationBindingChallenge(f.ctx, tx, userID, "bot-fixture", fmt.Sprint("notification-session-", userID))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f notificationFixture) consume(token, instance, chat, user string) (NotificationTarget, error) {
	var out NotificationTarget
	err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
		out, err = f.db.ConsumeNotificationBindingChallenge(f.ctx, tx, instance, token, chat, user)
		return err
	})
	return out, err
}

func (f notificationFixture) bind(t *testing.T, userID int64, chat string) NotificationTarget {
	t.Helper()
	target, err := f.consume(f.challenge(t, userID).Token, "bot-fixture", chat, chat)
	if err != nil {
		t.Fatal(err)
	}
	return target
}

func (f notificationFixture) preference(t *testing.T, userID int64, ch string, enabled bool, version int64) NotificationPreference {
	t.Helper()
	var out NotificationPreference
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
		out, err = f.db.UpdateNotificationPreference(f.ctx, tx, userID, ch, enabled, version)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func (f notificationFixture) route(t *testing.T, ch string, enabled bool, version int64) {
	t.Helper()
	instance := ""
	if ch == "telegram" {
		instance = "bot-fixture"
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationRoute(f.ctx, tx, "invoice-paid-"+ch, NotificationRouteInput{Enabled: enabled, InstanceID: instance, ExpectedVersion: version})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func (f notificationFixture) invoice(t *testing.T, userID int64, name string) int64 {
	t.Helper()
	var id int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES($1,$2,'manual','paid',12345) RETURNING id`, name, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (f notificationFixture) queue(t *testing.T, invoiceID int64) {
	t.Helper()
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.QueuePaymentSuccessNotifications(f.ctx, tx, invoiceID) }); err != nil {
		t.Fatal(err)
	}
}

func (f notificationFixture) telegramReady(t *testing.T) NotificationTarget {
	t.Helper()
	target := f.bind(t, f.customer, "123456789")
	f.preference(t, f.customer, "telegram", true, 0)
	f.route(t, "telegram", true, 1)
	return target
}

func TestNotificationCenterDefaultsAndNoHistoricalBackfill(t *testing.T) {
	f := notificationCenterFixture(t)
	if err := f.db.Migrate(f.ctx); err != nil {
		t.Fatal(err)
	}
	prefs, err := f.db.NotificationPreferences(f.ctx, f.customer)
	if err != nil || len(prefs) != 3 {
		t.Fatalf("preferences=%+v err=%v", prefs, err)
	}
	for _, p := range prefs {
		if p.Enabled || p.Version != 0 {
			t.Fatalf("unexpected initial consent: %+v", p)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_routes WHERE enabled`); n != 0 {
		t.Fatalf("routes enabled by migration: %d", n)
	}
	old := f.invoice(t, f.customer, "before-routes")
	f.queue(t, old)
	f.preference(t, f.customer, "inbox", true, 0)
	f.route(t, "inbox", true, 1)
	f.queue(t, old)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries`); n != 0 {
		t.Fatalf("replayed old event backfilled %d deliveries", n)
	}
	current := f.invoice(t, f.customer, "after-routes")
	f.queue(t, current)
	f.queue(t, current)
	if n := f.count(t, `SELECT count(*) FROM notification_events`); n != 2 {
		t.Fatalf("event replay was not idempotent: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries`); n != 1 {
		t.Fatalf("current event must enqueue exactly once: %d", n)
	}
}

func TestNotificationCenterBindingIsPrivateSingleUseAndNotConsent(t *testing.T) {
	f := notificationCenterFixture(t)
	link := f.challenge(t, f.customer)
	if len(link.Token) != 48 || !strings.HasPrefix(link.DeepLink, "https://t.me/fixture_bot?start=") || time.Until(link.ExpiresAt) > 10*time.Minute+time.Second {
		t.Fatalf("invalid binding challenge: %+v", link)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_binding_challenges WHERE token_hash=$1 AND token_hash<>$2`, notificationTokenHash(link.Token), link.Token); n != 1 {
		t.Fatal("challenge must store a hash only")
	}
	for _, in := range [][3]string{{"bot-other", "123456789", "123456789"}, {"bot-fixture", "-123456", "123456"}, {"bot-fixture", "123456789", "987654321"}} {
		if _, err := f.consume(link.Token, in[0], in[1], in[2]); err == nil {
			t.Fatalf("accepted invalid binding: %+v", in)
		}
	}
	target, err := f.consume(link.Token, "bot-fixture", "123456789", "123456789")
	if err != nil || !target.Active || target.Label != "****6789" {
		t.Fatalf("binding=%+v err=%v", target, err)
	}
	if _, err = f.consume(link.Token, "bot-fixture", "123456789", "123456789"); err == nil {
		t.Fatal("consumed binding challenge accepted twice")
	}
	prefs, err := f.db.NotificationPreferences(f.ctx, f.customer)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range prefs {
		if p.Enabled {
			t.Fatal("binding silently opted user into notifications")
		}
	}
	if n := f.count(t, `SELECT count(*) FROM audit_events WHERE action='notification.target_bound' AND payload::text NOT LIKE '%123456789%' AND payload::text NOT LIKE '%'||$1||'%'`, link.Token); n != 1 {
		t.Fatalf("binding audit missing or contains target/token: %d", n)
	}
}

func TestNotificationCenterChallengesRevocationAndCurrentAuthority(t *testing.T) {
	for _, scenario := range []string{"expired", "session_revoked", "user_disabled", "bot_disabled", "schema_incompatible", "new_challenge"} {
		t.Run(scenario, func(t *testing.T) {
			f := notificationCenterFixture(t)
			link := f.challenge(t, f.customer)
			switch scenario {
			case "expired":
				f.exec(t, `UPDATE notification_binding_challenges SET expires_at=now()-interval '1 second'`)
			case "session_revoked":
				f.exec(t, `DELETE FROM sessions WHERE user_id=$1`, f.customer)
			case "user_disabled":
				f.exec(t, `UPDATE users SET status='disabled' WHERE id=$1`, f.customer)
			case "bot_disabled":
				f.exec(t, `UPDATE channel_instances SET enabled=false WHERE id='bot-fixture'`)
			case "schema_incompatible":
				f.exec(t, `UPDATE channel_instances SET schema_version=999 WHERE id='bot-fixture'`)
			case "new_challenge":
				if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
					_, err := f.db.CreateNotificationBindingChallenge(f.ctx, tx, f.customer, "bot-fixture", fmt.Sprint("notification-session-", f.customer))
					return err
				}); conflictCode(t, err) != "notification_challenge_rate_limited" {
					t.Fatal("challenge was not rate limited")
				}
				f.exec(t, `UPDATE notification_binding_challenges SET created_at=now()-interval '11 seconds'`)
				f.challenge(t, f.customer)
			}
			if _, err := f.consume(link.Token, "bot-fixture", "123456789", "123456789"); err == nil {
				t.Fatal("accepted challenge after authority was revoked")
			}
			if n := f.count(t, `SELECT count(*) FROM notification_targets`); n != 0 {
				t.Fatalf("failed binding created %d targets", n)
			}
		})
	}
}

func TestNotificationCenterConsentCASAndNoTargetOwnershipTransfer(t *testing.T) {
	f := notificationCenterFixture(t)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "telegram", true, 0)
		return err
	}); conflictCode(t, err) != "notification_binding_required" {
		t.Fatal("telegram consent did not require a target")
	}
	target := f.bind(t, f.customer, "123456789")
	pref := f.preference(t, f.customer, "telegram", true, 0)
	if pref.Version != 1 || !pref.Enabled {
		t.Fatalf("invalid initial preference: %+v", pref)
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "telegram", false, 0)
		return err
	}); conflictCode(t, err) != "version_conflict" {
		t.Fatal("stale preference version accepted")
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.UnbindNotificationTarget(f.ctx, tx, f.other, target.ID, target.Version)
	}); err == nil {
		t.Fatal("other user unbound a target")
	}
	f.exec(t, `UPDATE notification_binding_challenges SET created_at=now()-interval '11 seconds'`)
	unconsumed := f.challenge(t, f.customer)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.UnbindNotificationTarget(f.ctx, tx, f.customer, target.ID, target.Version)
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1 AND channel='telegram' AND NOT enabled AND version=2`, f.customer); n != 1 {
		t.Fatal("unbinding did not revoke consent with a new version")
	}
	if _, err := f.consume(unconsumed.Token, "bot-fixture", "123456789", "123456789"); err == nil {
		t.Fatal("unbinding did not revoke outstanding challenges")
	}
	f.exec(t, `UPDATE notification_binding_challenges SET created_at=now()-interval '11 seconds'`)
	f.bind(t, f.customer, "123456789")
	if n := f.count(t, `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1 AND channel='telegram' AND NOT enabled AND version=2`, f.customer); n != 1 {
		t.Fatal("rebinding silently restored consent")
	}
	// A new challenge for an active target also preserves existing consent.
	f.preference(t, f.customer, "telegram", true, 2)
	f.exec(t, `UPDATE notification_binding_challenges SET created_at=now()-interval '11 seconds'`)
	f.bind(t, f.customer, "123456789")
	if n := f.count(t, `SELECT count(*) FROM user_notification_preferences WHERE user_id=$1 AND channel='telegram' AND enabled AND version=3`, f.customer); n != 1 {
		t.Fatal("repeat binding changed existing consent")
	}
	current, err := f.db.NotificationTargets(f.ctx, f.customer)
	if err != nil || len(current) != 1 {
		t.Fatalf("targets=%+v err=%v", current, err)
	}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.UnbindNotificationTarget(f.ctx, tx, f.customer, current[0].ID, current[0].Version)
	}); err != nil {
		t.Fatal(err)
	}
	otherLink := f.challenge(t, f.other)
	if _, err := f.consume(otherLink.Token, "bot-fixture", "123456789", "123456789"); conflictCode(t, err) != "notification_target_in_use" {
		t.Fatal("unbound target transferred to another account")
	}
}

func TestNotificationCenterLegacyLinksNeverEnterNewWorker(t *testing.T) {
	f := notificationCenterFixture(t)
	f.exec(t, `UPDATE users SET phone='+8613800000000' WHERE id=$1`, f.customer)
	f.exec(t, `UPDATE users SET email_verified=true,phone_verified=true WHERE id=$1`, f.customer)
	f.exec(t, `UPDATE channel_instances SET enabled=true WHERE category IN ('mail','sms')`)
	invoiceID := f.invoice(t, f.customer, "legacy-paid")
	f.queue(t, invoiceID)
	f.queue(t, invoiceID)
	for _, table := range []string{"email_outbox", "sms_outbox"} {
		if n := f.count(t, `SELECT count(*) FROM `+table+` WHERE kind='payment_success'`); n != 1 {
			t.Fatalf("legacy outbox %s duplicated or missing: %d", table, n)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel IN ('mail','sms')`); n != 2 {
		t.Fatalf("legacy links missing: %d", n)
	}
	if job, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
		t.Fatalf("legacy outbox entered new worker: job=%+v err=%v", job, err)
	}
	f.exec(t, `UPDATE email_outbox SET status='sent',attempts=3`)
	metadata, err := f.db.NotificationDeliveries(f.ctx, f.customer, 20, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range metadata {
		if d.Channel == "email" && (d.Status != "accepted" || d.Attempts != 3) {
			t.Fatalf("legacy status is not live: %+v", d)
		}
	}
	// Existing retention must remain possible; orphaned references cannot
	// cause the unified worker to create a replacement delivery.
	f.exec(t, `DELETE FROM email_outbox`)
	f.exec(t, `DELETE FROM sms_outbox`)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE legacy_email_outbox_id IS NULL AND legacy_sms_outbox_id IS NULL`); n != 2 {
		t.Fatalf("retention did not clear legacy links: %d", n)
	}
	metadata, err = f.db.NotificationDeliveries(f.ctx, f.customer, 20, 0)
	if err != nil || len(metadata) != 2 {
		t.Fatalf("archived metadata=%+v err=%v", metadata, err)
	}
	for _, d := range metadata {
		if d.Status != "archived" || d.Code != "legacy_log_removed" {
			t.Fatalf("archived legacy log mislabeled: %+v", d)
		}
	}
}

func TestNotificationCenterInboxAtomicDeliveryFrozenTemplateAndOwnership(t *testing.T) {
	f := notificationCenterFixture(t)
	f.preference(t, f.customer, "inbox", true, 0)
	f.route(t, "inbox", true, 1)
	invoiceID := f.invoice(t, f.customer, "frozen-inbox")
	f.queue(t, invoiceID)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationTemplate(f.ctx, tx, "invoice-paid-inbox", NotificationMessageTemplateInput{Subject: "New title", Body: "New {{invoice_no}}", Active: true, ExpectedVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if job, err := f.db.ClaimNotificationJob(f.ctx); job != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("inbox leaked to external worker: %+v %v", job, err)
	}
	messages, err := f.db.UserNotificationInbox(f.ctx, f.customer, 20, 0)
	if err != nil || len(messages) != 1 || messages[0].Subject != "付款成功" || !strings.Contains(messages[0].Body, "frozen-inbox") || !strings.Contains(messages[0].Body, "¥123.45") {
		t.Fatalf("frozen inbox=%+v err=%v", messages, err)
	}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.MarkNotificationInboxRead(f.ctx, tx, f.other, messages[0].ID) }); err == nil {
		t.Fatal("other user read a private inbox message")
	}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.MarkNotificationInboxRead(f.ctx, tx, f.customer, messages[0].ID) }); err != nil {
		t.Fatal(err)
	}
	f.queue(t, invoiceID)
	_, _ = f.db.ClaimNotificationJob(f.ctx)
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages`); n != 1 {
		t.Fatalf("inbox replay created %d copies", n)
	}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationTemplate(f.ctx, tx, "invoice-paid-inbox", NotificationMessageTemplateInput{Subject: "Unsupported", Body: "{{password}}", Active: true, ExpectedVersion: 2})
		return err
	}); conflictCode(t, err) != "invalid_notification_template" {
		t.Fatal("unknown template variable accepted")
	}
}

func TestNotificationCenterClaimRechecksCurrentConsentAndOwnership(t *testing.T) {
	for _, scenario := range []string{"opt_out_and_back_in", "unbind_and_rebind", "bot_disabled", "invoice_owner_changed", "route_disabled_and_reenabled", "template_inactive", "user_disabled"} {
		t.Run(scenario, func(t *testing.T) {
			f := notificationCenterFixture(t)
			target := f.telegramReady(t)
			invoiceID := f.invoice(t, f.customer, "revoked-before-claim")
			f.queue(t, invoiceID)
			switch scenario {
			case "opt_out_and_back_in":
				f.preference(t, f.customer, "telegram", false, 1)
				f.preference(t, f.customer, "telegram", true, 2)
			case "unbind_and_rebind":
				if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
					return f.db.UnbindNotificationTarget(f.ctx, tx, f.customer, target.ID, target.Version)
				}); err != nil {
					t.Fatal(err)
				}
				f.exec(t, `UPDATE notification_binding_challenges SET created_at=now()-interval '11 seconds'`)
				f.bind(t, f.customer, "123456789")
			case "bot_disabled":
				f.exec(t, `UPDATE channel_instances SET enabled=false WHERE id='bot-fixture'`)
			case "invoice_owner_changed":
				// Simulate pre-187 malformed ownership with a scoped fixture bypass;
				// the production trigger must reject the same change first.
				if _, err := f.db.Pool.Exec(f.ctx, "UPDATE invoices SET user_id=$2 WHERE id=$1", invoiceID, f.other); err == nil {
					t.Fatal("invoice owner guard allowed a transfer")
				}
				if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
					if _, err := tx.Exec(f.ctx, "ALTER TABLE invoices DISABLE TRIGGER invoice_referral_owner"); err != nil {
						return err
					}
					if _, err := tx.Exec(f.ctx, "UPDATE invoices SET user_id=$2 WHERE id=$1", invoiceID, f.other); err != nil {
						return err
					}
					_, err := tx.Exec(f.ctx, "ALTER TABLE invoices ENABLE TRIGGER invoice_referral_owner")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			case "route_disabled_and_reenabled":
				f.route(t, "telegram", false, 2)
				f.route(t, "telegram", true, 3)
			case "template_inactive":
				f.exec(t, `UPDATE notification_message_templates SET active=false WHERE channel='telegram'`)
			case "user_disabled":
				f.exec(t, `UPDATE users SET status='disabled' WHERE id=$1`, f.customer)
			}
			if job, err := f.db.ClaimNotificationJob(f.ctx); job != nil || !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("ineligible delivery claimed: %+v %v", job, err)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='skipped'`); n != 1 {
				t.Fatalf("revoked delivery was not terminally skipped: %d", n)
			}
		})
	}
}

func TestNotificationCenterActiveRecheckLeaseRecoveryAndUnknownNeverRetry(t *testing.T) {
	f := notificationCenterFixture(t)
	f.telegramReady(t)
	f.queue(t, f.invoice(t, f.customer, "lease-fence"))
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil || job == nil || job.Channel != "telegram" || job.Target != "123456789" {
		t.Fatalf("claim=%+v err=%v", job, err)
	}
	if active, err := f.db.NotificationJobActive(f.ctx, *job); err != nil || !active {
		t.Fatalf("fresh lease not active: %v %v", active, err)
	}
	f.preference(t, f.customer, "telegram", false, 1)
	if active, err := f.db.NotificationJobActive(f.ctx, *job); err != nil || active {
		t.Fatalf("opt-out after claim still active: %v %v", active, err)
	}
	f.exec(t, `UPDATE notification_deliveries SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID)
	if err = f.db.RecoverStaleNotificationJobs(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted"}); conflictCode(t, err) != "notification_lease_lost" {
		t.Fatal("stale worker completion was not fenced")
	}
	if n := f.count(t, `SELECT count(*) FROM notification_delivery_attempts WHERE status='unknown' AND code='lease_expired'`); n != 1 {
		t.Fatalf("stale attempt not marked unknown: %d", n)
	}
	if newJob, err := f.db.ClaimNotificationJob(f.ctx); newJob != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("unknown delivery automatically retried: %+v %v", newJob, err)
	}
}

func TestNotificationCenterRetriesOnlyExplicitRateLimitAndStopsAtFive(t *testing.T) {
	f := notificationCenterFixture(t)
	f.telegramReady(t)
	f.queue(t, f.invoice(t, f.customer, "rate-limit"))
	for attempt := 1; attempt <= 5; attempt++ {
		job, err := f.db.ClaimNotificationJob(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "rate_limited", RetryAfter: 10 * time.Second}); err != nil {
			t.Fatal(err)
		}
		if attempt < 5 {
			if early, err := f.db.ClaimNotificationJob(f.ctx); early != nil || !errors.Is(err, pgx.ErrNoRows) {
				t.Fatalf("retry ignored provider delay: %+v %v", early, err)
			}
			f.exec(t, `UPDATE notification_deliveries SET next_attempt_at=now()-interval '1 second' WHERE id=$1`, job.ID)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='failed' AND attempts=5`); n != 1 {
		t.Fatalf("rate limit attempts were not capped: %d", n)
	}
	if job, err := f.db.ClaimNotificationJob(f.ctx); job != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("sixth attempt allowed: %+v %v", job, err)
	}
	for _, outcome := range []string{"accepted", "failed", "unknown", "retry"} {
		f.queue(t, f.invoice(t, f.customer, "terminal-"+outcome))
		job, err := f.db.ClaimNotificationJob(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: outcome, Code: "unsafe token with spaces"}); err != nil {
			t.Fatal(err)
		}
		if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status IN ('accepted','failed','unknown') AND code IN ('provider_error','invalid_provider_outcome')`, job.ID); n != 1 {
			t.Fatalf("terminal outcome mishandled: %s", outcome)
		}
	}
}

func TestNotificationCenterConcurrentClaimsExactlyOnce(t *testing.T) {
	f := notificationCenterFixture(t)
	f.telegramReady(t)
	for i := 0; i < 8; i++ {
		f.queue(t, f.invoice(t, f.customer, fmt.Sprintf("concurrent-%d", i)))
	}
	var wg sync.WaitGroup
	results := make(chan *notify.Job, 12)
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			job, err := f.db.ClaimNotificationJob(f.ctx)
			if err != nil && !errors.Is(err, pgx.ErrNoRows) {
				errs <- err
			}
			if job != nil {
				results <- job
			}
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	seen := map[int64]bool{}
	for job := range results {
		if seen[job.ID] {
			t.Fatalf("delivery %d claimed twice", job.ID)
		}
		seen[job.ID] = true
	}
	if len(seen) != 8 {
		t.Fatalf("got %d unique claims, want 8", len(seen))
	}
}

func TestNotificationCenterOversizedRenderedTemplateCannotPoisonQueue(t *testing.T) {
	f := notificationCenterFixture(t)
	f.preference(t, f.customer, "inbox", true, 0)
	f.route(t, "inbox", true, 1)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationTemplate(f.ctx, tx, "invoice-paid-inbox", NotificationMessageTemplateInput{Subject: strings.Repeat("{{invoice_no}}", 17), Body: "Paid {{amount}}", Active: true, ExpectedVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	f.queue(t, f.invoice(t, f.customer, "invoice-number-longer-than-fourteen-characters"))
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='failed' AND code='rendered_message_invalid'`); n != 1 {
		t.Fatalf("oversized rendered message was not terminal: %d", n)
	}
	f.queue(t, f.invoice(t, f.customer, "short"))
	// Defend against already queued invalid rows as well as fresh enqueues.
	f.exec(t, `UPDATE notification_deliveries SET subject=repeat('x',241) WHERE status='pending'`)
	f.queue(t, f.invoice(t, f.customer, "later"))
	if job, err := f.db.ClaimNotificationJob(f.ctx); job != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("poison queue stalled: %+v %v", job, err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='failed' AND code='rendered_message_invalid'`); n != 2 {
		t.Fatalf("old invalid delivery was not terminal: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE subject=repeat('later',17)`); n != 1 {
		t.Fatalf("valid message behind poison row was not delivered: %d", n)
	}
}

func TestNotificationCenterCompletionWaitingPastLeaseIsFenced(t *testing.T) {
	f := notificationCenterFixture(t)
	f.telegramReady(t)
	f.queue(t, f.invoice(t, f.customer, "lease-wait"))
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	blocker, err := f.db.Pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer blocker.Rollback(f.ctx)
	if _, err = blocker.Exec(f.ctx, `UPDATE notification_deliveries SET lease_until=clock_timestamp()+interval '200 milliseconds' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	go func() { completed <- f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted"}) }()
	if _, err = blocker.Exec(f.ctx, `SELECT pg_sleep(0.3)`); err != nil {
		t.Fatal(err)
	}
	if err = blocker.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-completed; conflictCode(t, err) != "notification_lease_lost" {
		t.Fatal("completion accepted after waiting past lease expiry")
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='accepted'`); n != 0 {
		t.Fatal("expired completion mutated delivery state")
	}
}

func TestNotificationCenterRollbackAndAdminMetadataScope(t *testing.T) {
	f := notificationCenterFixture(t)
	link := f.challenge(t, f.customer)
	sentinel := errors.New("rollback fixture")
	err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if _, err := f.db.ConsumeNotificationBindingChallenge(f.ctx, tx, "bot-fixture", link.Token, "123456789", "123456789"); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || f.count(t, `SELECT count(*) FROM notification_targets`) != 0 || f.count(t, `SELECT count(*) FROM audit_events WHERE action='notification.target_bound'`) != 0 {
		t.Fatal("binding/audit did not roll back atomically")
	}
	if _, err = f.consume(link.Token, "bot-fixture", "123456789", "123456789"); err != nil {
		t.Fatal("rolled-back challenge was consumed", err)
	}
	rollbackInvoice := f.invoice(t, f.customer, "rollback-paid-event")
	err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if err := f.db.QueuePaymentSuccessNotifications(f.ctx, tx, rollbackInvoice); err != nil {
			return err
		}
		return sentinel
	})
	if !errors.Is(err, sentinel) || f.count(t, `SELECT count(*) FROM notification_events`) != 0 {
		t.Fatal("payment notification event did not roll back atomically")
	}
	f.preference(t, f.customer, "inbox", true, 0)
	f.preference(t, f.other, "inbox", true, 0)
	f.route(t, "inbox", true, 1)
	f.queue(t, f.invoice(t, f.customer, "scope-owned"))
	f.queue(t, f.invoice(t, f.other, "scope-other"))
	var staff int64
	var roleID int64
	if err = f.db.Pool.QueryRow(f.ctx, `INSERT INTO admin_roles(name,permissions) VALUES('notification fixture',ARRAY['reports.view']) RETURNING id`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}
	if err = f.db.Pool.QueryRow(f.ctx, `INSERT INTO administrators(username,email,password_hash,display_name,role,admin_role_id,customer_scope) VALUES('notification-staff','notification-staff@example.test','unused','Staff','admin',$1,'owned') RETURNING id`, roleID).Scan(&staff); err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE users SET sales_owner_id=$2 WHERE id=$1`, f.customer, staff)
	deliveries, err := f.db.NotificationDeliveriesForAdmin(f.ctx, staff, 20, 0)
	if err != nil || len(deliveries) != 1 || deliveries[0].UserID != f.customer {
		t.Fatalf("customer scope leaked: %+v %v", deliveries, err)
	}
	deliveries, err = f.db.NotificationDeliveriesForAdmin(f.ctx, f.actor, 20, 0)
	if err != nil || len(deliveries) != 2 {
		t.Fatalf("superadmin scope=%+v err=%v", deliveries, err)
	}
	// HTTP asks for page size + 1 to derive has_more; the store must accept
	// 101 instead of silently reverting a full 100-row page to 20 rows.
	f.exec(t, `INSERT INTO admin_inbox_messages(user_id,subject,body) SELECT $1,'page','fixture' FROM generate_series(1,101)`, f.customer)
	items, err := f.db.UserNotificationInbox(f.ctx, f.customer, 101, 0)
	if err != nil || len(items) != 101 {
		t.Fatalf("101-row pagination=%d err=%v", len(items), err)
	}
	f.exec(t, `INSERT INTO notification_deliveries(event_id,route_key,channel,status) SELECT e.id,'page-'||n,'inbox','failed' FROM notification_events e CROSS JOIN generate_series(1,101) n WHERE e.user_id=$1`, f.customer)
	page, err := f.db.NotificationDeliveries(f.ctx, f.customer, 101, 0)
	if err != nil || len(page) != 101 {
		t.Fatalf("101-row delivery pagination=%d err=%v", len(page), err)
	}
}
