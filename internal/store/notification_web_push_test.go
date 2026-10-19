package store

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/channel"
	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

var webPushFixtureOnce sync.Once

func webPushFixtureKey(t *testing.T) string {
	t.Helper()
	key, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
}

type webPushFixture struct {
	notificationFixture
	key string
}

func newWebPushFixture(t *testing.T) webPushFixture {
	t.Helper()
	webPushFixtureOnce.Do(func() {
		// The store package cannot import notification (the worker imports store).
		// Protocol/factory validation is independently covered by adapter tests.
		if _, ok := channel.Lookup(channel.Push, "web_push"); !ok {
			channel.MustRegisterFactory(channel.Descriptor{ID: "web_push", Name: "Push fixture", Category: channel.Push, Fields: []channel.Field{{Key: "public_key", Label: "Public", Type: channel.Text, Required: true}, {Key: "subject", Label: "Subject", Type: channel.Text, Required: true}}}, func(any) (any, error) { return struct{}{}, nil })
		}
	})
	f := webPushFixture{notificationFixture: notificationCenterFixture(t), key: webPushFixtureKey(t)}
	f.exec(t, `INSERT INTO channel_instances(id,category,implementation_id,display_name,enabled,config) VALUES('push-fixture','push','web_push','Browser fixture',true,jsonb_build_object('public_key',$1::text,'subject','mailto:push@example.test'))`, f.key)
	return f
}
func (f webPushFixture) subscription(t *testing.T, name string) notify.PushSubscription {
	t.Helper()
	return notify.PushSubscription{Endpoint: "https://fcm.googleapis.com/fcm/send/" + name, P256DH: webPushFixtureKey(t), Auth: base64.RawURLEncoding.EncodeToString(make([]byte, 16))}
}
func (f webPushFixture) save(t *testing.T, userID int64, session string, sub notify.PushSubscription, version int64) WebPushSubscriptionInfo {
	t.Helper()
	var out WebPushSubscriptionInfo
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
		out, err = f.db.SaveWebPushSubscription(f.ctx, tx, userID, session, "push-fixture", sub, version)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}
func (f webPushFixture) ready(t *testing.T) {
	t.Helper()
	f.preference(t, f.customer, "web_push", true, 0)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationRoute(f.ctx, tx, "invoice-paid-web_push", NotificationRouteInput{Enabled: true, InstanceID: "push-fixture", ExpectedVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}
func (f webPushFixture) session() string { return fmt.Sprint("notification-session-", f.customer) }

func TestNotificationWebPushMultipleDevicesAreEncryptedAndNeverBackfill(t *testing.T) {
	f := newWebPushFixture(t)
	first := f.subscription(t, "browser-one")
	a := f.save(t, f.customer, f.session(), first, 0)
	if n := f.count(t, `SELECT count(*) FROM user_notification_preferences`); n != 0 {
		t.Fatal("registration granted notification consent")
	}
	f.ready(t)
	invoice := f.invoice(t, f.customer, "push-private-invoice")
	f.queue(t, invoice)
	b := f.save(t, f.customer, f.session(), f.subscription(t, "browser-two"), 0)
	f.queue(t, invoice)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='web_push'`); n != 1 {
		t.Fatalf("new browser backfilled an old event: %d", n)
	}
	next := f.invoice(t, f.customer, "push-current-invoice")
	f.queue(t, next)
	f.queue(t, next)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='web_push'`); n != 3 {
		t.Fatalf("per-browser event dedupe failed: %d", n)
	}
	var sealed []byte
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT subscription_ciphertext FROM notification_web_push_subscriptions WHERE id=$1`, a.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), first.Endpoint) || strings.Contains(string(sealed), first.P256DH) || strings.Contains(string(sealed), first.Auth) {
		t.Fatal("subscription capabilities leaked in stored ciphertext")
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='web_push' AND (target<>'' OR target_id IS NOT NULL OR body LIKE '%invoice%' OR body LIKE '%12345%')`); n != 0 {
		t.Fatal("delivery metadata or lock screen contains private business content")
	}
	state, err := f.db.WebPushState(f.ctx, f.customer, f.session())
	if err != nil || len(state.Instances) != 1 || len(state.Subscriptions) != 2 || !state.Subscriptions[0].Revocable {
		t.Fatalf("state=%+v err=%v", state, err)
	}
	if state.Subscriptions[0].ID != b.ID || state.Subscriptions[1].ID != a.ID {
		t.Fatal("subscription metadata lost browser identity")
	}
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil || job.PushSubscription == nil || job.PushSubscription.Endpoint != first.Endpoint || job.Target != "" {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted", Code: "push_accepted"}); err != nil {
		t.Fatal(err)
	}
}

func TestNotificationWebPushAccountOwnershipCASAndSessionRebind(t *testing.T) {
	f := newWebPushFixture(t)
	sub := f.subscription(t, "owned-browser")
	a := f.save(t, f.customer, f.session(), sub, 0)
	f.ready(t)
	f.queue(t, f.invoice(t, f.customer, "before-push-rebind"))
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('second-device-login',$1,now()+interval '1 hour')`, f.customer)
	a2 := f.save(t, f.customer, "second-device-login", sub, a.Version)
	if a2.ID != a.ID || a2.Version != a.Version+1 {
		t.Fatal("rebind did not preserve identity and advance CAS")
	}
	for _, in := range []struct {
		uid     int64
		session string
		version int64
	}{{f.customer, f.session(), 0}, {f.other, fmt.Sprint("notification-session-", f.other), a2.Version}} {
		err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			_, err := f.db.SaveWebPushSubscription(f.ctx, tx, in.uid, in.session, "push-fixture", sub, in.version)
			return err
		})
		if err == nil {
			t.Fatal("stale or cross-account endpoint claim accepted")
		}
	}
	if job, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
		t.Fatalf("rebind revived old delivery: %+v %v", job, err)
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.DeleteWebPushSubscription(f.ctx, tx, f.other, a.ID, a2.Version) }); err == nil {
		t.Fatal("another customer revoked a subscription")
	}
	f.queue(t, f.invoice(t, f.customer, "after-push-rebind"))
	if job, err := f.db.ClaimNotificationJob(f.ctx); err != nil || job == nil {
		t.Fatalf("future event lost valid rebound subscription: %v", err)
	}
}

func TestNotificationWebPushCurrentSessionKeyAndConsentEligibility(t *testing.T) {
	for _, change := range []string{"logout", "expired_session", "disabled_user", "disabled_instance", "key_rotation", "opt_out", "unsubscribe"} {
		t.Run(change, func(t *testing.T) {
			f := newWebPushFixture(t)
			a := f.save(t, f.customer, f.session(), f.subscription(t, "eligibility-browser"), 0)
			f.ready(t)
			f.queue(t, f.invoice(t, f.customer, "queued-push"))
			switch change {
			case "logout":
				f.exec(t, `DELETE FROM sessions WHERE token_hash=$1`, f.session())
			case "expired_session":
				f.exec(t, `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, f.session())
			case "disabled_user":
				f.exec(t, `UPDATE users SET status='disabled' WHERE id=$1`, f.customer)
			case "disabled_instance":
				f.exec(t, `UPDATE channel_instances SET enabled=false WHERE id='push-fixture'`)
			case "key_rotation":
				f.exec(t, `UPDATE channel_instances SET config=jsonb_set(config,'{public_key}',to_jsonb($1::text)) WHERE id='push-fixture'`, webPushFixtureKey(t))
			case "opt_out":
				f.preference(t, f.customer, "web_push", false, 1)
			case "unsubscribe":
				if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.DeleteWebPushSubscription(f.ctx, tx, f.customer, a.ID, a.Version) }); err != nil {
					t.Fatal(err)
				}
			}
			if job, err := f.db.ClaimNotificationJob(f.ctx); !errors.Is(err, pgx.ErrNoRows) || job != nil {
				t.Fatalf("ineligible browser was claimed: %+v %v", job, err)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='web_push' AND status='skipped'`); n != 1 {
				t.Fatal("changed eligibility was not terminal")
			}
		})
	}
}

func TestNotificationWebPushProviderGoneRevokesOnlyMatchingBrowserVersion(t *testing.T) {
	for _, rebind := range []bool{false, true} {
		t.Run(fmt.Sprint("rebind-", rebind), func(t *testing.T) {
			f := newWebPushFixture(t)
			sub := f.subscription(t, "gone-browser")
			a := f.save(t, f.customer, f.session(), sub, 0)
			other := f.save(t, f.customer, f.session(), f.subscription(t, "remaining-browser"), 0)
			f.ready(t)
			f.queue(t, f.invoice(t, f.customer, "gone-push"))
			job, err := f.db.ClaimNotificationJob(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if rebind {
				a = f.save(t, f.customer, f.session(), sub, a.Version)
			}
			if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "failed", Code: "push_subscription_gone"}); err != nil {
				t.Fatal(err)
			}
			want := 0
			if rebind {
				want = 1
			}
			if n := f.count(t, `SELECT count(*) FROM notification_web_push_subscriptions WHERE id=$1 AND active`, a.ID); n != want {
				t.Fatal("410 changed a different browser version")
			}
			if n := f.count(t, `SELECT count(*) FROM notification_web_push_subscriptions WHERE id=$1 AND active`, other.ID); n != 1 {
				t.Fatal("410 revoked another device")
			}
			if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "failed", Code: "push_subscription_gone"}); err == nil {
				t.Fatal("lost lease completion was accepted twice")
			}
		})
	}
}

func TestNotificationWebPushLogoutAfterClaimAndUnknownAreTerminal(t *testing.T) {
	f := newWebPushFixture(t)
	f.save(t, f.customer, f.session(), f.subscription(t, "stale-browser"), 0)
	f.ready(t)
	f.queue(t, f.invoice(t, f.customer, "stale-push"))
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `DELETE FROM sessions WHERE token_hash=$1`, f.session())
	active, err := f.db.NotificationJobActive(f.ctx, *job)
	if err != nil || active {
		t.Fatal("logout did not revoke sending eligibility", err)
	}
	f.exec(t, `UPDATE notification_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE id=$1`, job.ID)
	if err = f.db.RecoverStaleNotificationJobs(f.ctx); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE id=$1 AND status='unknown'`, job.ID); n != 1 {
		t.Fatal("stale push lease did not remain unknown")
	}
	if next, err := f.db.ClaimNotificationJob(f.ctx); next != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("unknown push was resent", err)
	}
}

func TestNotificationWebPushCrossDayCatchupRequiresExistingBrowserConsent(t *testing.T) {
	f := newWebPushFixture(t)
	old := f.save(t, f.customer, f.session(), f.subscription(t, "old-browser"), 0)
	recent := f.save(t, f.customer, f.session(), f.subscription(t, "new-browser"), 0)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "web_push", true, 0, "business")
		if err != nil {
			return err
		}
		_, err = f.db.UpdateNotificationRoute(f.ctx, tx, "service-expired-web_push", NotificationRouteInput{Enabled: true, InstanceID: "push-fixture", ExpectedVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
	resources := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "push-catchup")
	// Always on the previous Shanghai day and within the 72 hour catchup range.
	due := time.Now().In(notificationShanghai).AddDate(0, 0, -1).Truncate(time.Second)
	f.exec(t, `UPDATE services SET next_due_at=$1 WHERE id=$2`, due, resources.service)
	f.exec(t, `UPDATE notification_routes SET updated_at=$1 WHERE channel='web_push'`, due.Add(-time.Hour))
	f.exec(t, `UPDATE notification_message_templates SET updated_at=$1 WHERE channel='web_push'`, due.Add(-time.Hour))
	f.exec(t, `UPDATE user_notification_preferences SET updated_at=$1 WHERE user_id=$2 AND channel='web_push'`, due.Add(-time.Hour), f.customer)
	f.exec(t, `UPDATE channel_instances SET updated_at=$1 WHERE id='push-fixture'`, due.Add(-time.Hour))
	f.exec(t, `UPDATE sessions SET created_at=$1 WHERE token_hash=$2`, due.Add(-time.Hour), f.session())
	f.exec(t, `UPDATE notification_web_push_subscriptions SET created_at=$1,updated_at=$1 WHERE id=$2`, due.Add(-time.Hour), old.ID)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.QueueLifecycleNotification(f.ctx, tx, "service.expired", 0, 0, resources.service, "push-catchup-event", nil)
	}); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE push_subscription_id=$1 AND status='pending'`, old.ID); n != 1 {
		t.Fatal("pre-deadline browser missed catchup")
	}
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE push_subscription_id=$1 AND code='catchup_subscription_changed'`, recent.ID); n != 1 {
		t.Fatal("post-deadline browser received history")
	}
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil || job == nil {
		t.Fatal("valid historical browser was not claimable", err)
	}
}

func TestNotificationWebPushInvalidSubscriptionsAndUnboundConsentAreRejected(t *testing.T) {
	f := newWebPushFixture(t)
	for _, endpoint := range []string{"http://fcm.googleapis.com/token", "https://127.0.0.1/push", "https://push.attacker.test/token"} {
		sub := f.subscription(t, "invalid")
		sub.Endpoint = endpoint
		if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			_, err := f.db.SaveWebPushSubscription(f.ctx, tx, f.customer, f.session(), "push-fixture", sub, 0)
			return err
		}); err == nil {
			t.Fatal("unsafe endpoint was accepted")
		}
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "web_push", true, 0)
		return err
	}); err == nil {
		t.Fatal("consent without an eligible browser was accepted")
	}
	for i := 0; i < 20; i++ {
		f.save(t, f.customer, f.session(), f.subscription(t, fmt.Sprint("limited-", i)), 0)
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.SaveWebPushSubscription(f.ctx, tx, f.customer, f.session(), "push-fixture", f.subscription(t, "over-limit"), 0)
		return err
	}); err == nil {
		t.Fatal("per-account browser subscription cap was exceeded")
	}
	f.exec(t, `UPDATE sessions SET expires_at=now()-interval '1 second' WHERE token_hash=$1`, f.session())
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('manage-old-browsers',$1,now()+interval '1 hour')`, f.customer)
	state, err := f.db.WebPushState(f.ctx, f.customer, "manage-old-browsers")
	if err != nil || len(state.Subscriptions) != 20 || state.Subscriptions[0].Active || !state.Subscriptions[0].Revocable {
		t.Fatal("old-session devices cannot be managed", state, err)
	}
	a := state.Subscriptions[0]
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.DeleteWebPushSubscription(f.ctx, tx, f.customer, a.ID, a.Version) }); err != nil {
		t.Fatal("expired-session subscription could not be revoked", err)
	}
}

func TestNotificationWebPushUpgrade173PreservesExistingConsentAndQueuedFacts(t *testing.T) {
	db := supplierExpirySchema171(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name > supplierExpiryMigration171 && name < "174_" {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var exists bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('notification_web_push_subscriptions') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &exists); err != nil || !strings.HasPrefix(boundary, "173_") || exists {
		t.Fatal("fixture did not originate at schema173", boundary, exists, err)
	}
	actor, customer := topUpFixture(t, db)
	f := notificationFixture{db: db, ctx: t.Context(), actor: actor, customer: customer}
	f.exec(t, `UPDATE notification_routes SET enabled=true,version=8 WHERE id='invoice-paid-inbox'; UPDATE notification_message_templates SET subject='Preserved subject',version=6 WHERE id='invoice-paid-inbox'`)
	f.exec(t, `INSERT INTO user_notification_preferences(user_id,channel,enabled,version,event_scope) VALUES($1,'inbox',true,5,'business')`, customer)
	invoice := f.invoice(t, customer, "schema173-original-invoice")
	f.exec(t, `WITH e AS(INSERT INTO notification_events(kind,dedupe_key,user_id,invoice_id,payload) VALUES('invoice.paid','schema173-event',$1::bigint,$2::bigint,'{}') RETURNING id)
INSERT INTO notification_deliveries(event_id,route_key,route_id,route_version,template_version,preference_version,channel,target,subject,body)
SELECT id,'invoice-paid-inbox','invoice-paid-inbox',8,6,5,'inbox',$1::bigint::text,'Preserved subject','Preserved body' FROM e`, customer, invoice)
	snapshot := func() string {
		t.Helper()
		var out string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
'instances',(SELECT jsonb_agg(to_jsonb(c) ORDER BY id) FROM channel_instances c),
'routes',(SELECT jsonb_agg(to_jsonb(r) ORDER BY id) FROM notification_routes r WHERE channel<>'web_push'),
'templates',(SELECT jsonb_agg(to_jsonb(m) ORDER BY id) FROM notification_message_templates m WHERE channel<>'web_push'),
'preferences',(SELECT jsonb_agg(to_jsonb(p) ORDER BY user_id,channel) FROM user_notification_preferences p),
'events',(SELECT jsonb_agg(to_jsonb(e) ORDER BY id) FROM notification_events e),
'deliveries',(SELECT jsonb_agg(to_jsonb(d)-'push_subscription_id' ORDER BY id) FROM notification_deliveries d))::text`).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := snapshot()
	oldMigrations := supplierExpiryUpgradeMigrations(t, db, boundary)
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current binary accepted schema173")
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != snapshot() || oldMigrations != supplierExpiryUpgradeMigrations(t, db, boundary) {
		t.Fatal("push migration changed existing facts, consent, or migration checksums")
	}
	if n := f.count(t, `SELECT count(*) FROM notification_routes WHERE channel='web_push' AND NOT enabled`); n != len(NotificationEventCatalog()) {
		t.Fatal("new push routes were enabled or incomplete", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_web_push_subscriptions`); n != 0 {
		t.Fatal("migration invented browser subscriptions")
	}
	if err = db.Migrate(t.Context()); err != nil || before != snapshot() {
		t.Fatal("repeated migration was not idempotent", err)
	}
	if job, err := db.ClaimNotificationJob(t.Context()); job != nil || !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal("pre-upgrade inbox was misrouted externally", err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1 AND subject='Preserved subject' AND body='Preserved body'`, customer); n != 1 {
		t.Fatal("pre-upgrade queued inbox lost its frozen content")
	}
}

func TestNotificationWebPushTemplatesKeepLockScreenPrivateAndStateUsesCAS(t *testing.T) {
	f := newWebPushFixture(t)
	if n := f.count(t, `SELECT count(*) FROM notification_message_templates WHERE channel='web_push' AND subject='账户通知' AND body='您有新的账户通知，请登录查看。'`); n != len(NotificationEventCatalog()) {
		t.Fatal("migration did not give every event a private lock screen template", n)
	}
	for _, in := range []NotificationMessageTemplateInput{
		{Subject: "账单 {{invoice_no}}", Body: "金额 {{amount}}", Active: true, ExpectedVersion: 1},
		{Subject: "账户通知", Body: "请登录查看", Active: true, ExpectedVersion: 1},
		{Subject: "支付成功", Body: "您有新的账户通知，请登录查看。", Active: false, ExpectedVersion: 1},
	} {
		if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			_, err := f.db.UpdateNotificationTemplate(f.ctx, tx, "invoice-paid-web_push", in)
			return err
		}); err == nil {
			t.Fatal("editable lock screen content was accepted")
		}
	}
	setActive := func(active bool, version int64) (NotificationMessageTemplate, error) {
		var out NotificationMessageTemplate
		err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
			out, err = f.db.UpdateNotificationTemplate(f.ctx, tx, "invoice-paid-web_push", NotificationMessageTemplateInput{Subject: "账户通知", Body: "您有新的账户通知，请登录查看。", Active: active, ExpectedVersion: version})
			return err
		})
		return out, err
	}
	if out, err := setActive(false, 1); err != nil || out.Active || out.Version != 2 {
		t.Fatal("fixed template cannot be disabled", out, err)
	}
	if _, err := setActive(true, 1); err == nil {
		t.Fatal("stale template state overwrote a later update")
	}
	if out, err := setActive(true, 2); err != nil || !out.Active || out.Version != 3 {
		t.Fatal("fixed template cannot be re-enabled using current CAS", out, err)
	}
}
