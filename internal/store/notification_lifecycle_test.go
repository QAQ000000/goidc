package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestNotificationLifecycleCatalogAndTemplateVocabulary(t *testing.T) {
	catalog := NotificationEventCatalog()
	if len(catalog) != 11 {
		t.Fatalf("catalog count=%d", len(catalog))
	}
	for _, event := range catalog {
		for _, variable := range event.Variables {
			if !notificationTemplateValid(event.Kind, "{{"+variable.Key+"}}") || variable.Label == "" {
				t.Fatalf("invalid variable %+v for %s", variable, event.Kind)
			}
		}
		if notificationTemplateValid(event.Kind, "{{password}}") || notificationTemplateValid(event.Kind, "{{invoice_no") {
			t.Fatalf("unsafe template accepted for %s", event.Kind)
		}
	}
	if notificationTemplateValid("service.provisioned", "{{invoice_no}}") || notificationTemplateValid("invoice.paid", "{{domain}}") || notificationTemplateValid("unknown", "plain") {
		t.Fatal("template variables leaked between event kinds")
	}
	if got := notificationLifecycleRender("{{product_name}} {{domain}}", map[string]string{"product_name": "{{domain}}", "domain": "safe.test"}); got != "{{domain}} safe.test" {
		t.Fatalf("resource value was recursively rendered: %s", got)
	}
	catalog[0].Variables[0].Key = "password"
	if NotificationEventCatalog()[0].Variables[0].Key != "invoice_no" {
		t.Fatal("catalog caller changed policy")
	}
}

type lifecycleResources struct{ order, service, invoice int64 }

func lifecycleFixtureResources(t *testing.T, f notificationFixture, owner int64, suffix string) lifecycleResources {
	t.Helper()
	var out lifecycleResources
	var product, item int64
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.db.Pool.QueryRow(f.ctx, `INSERT INTO products(name,slug,price_cents) VALUES($1,$2,12345) RETURNING id`, "Lifecycle "+suffix, "lifecycle-"+suffix).Scan(&product))
	must(f.db.Pool.QueryRow(f.ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES($1,$2,12000,$1,'fixture') RETURNING id`, "lifecycle-order-"+suffix, owner).Scan(&out.order))
	must(f.db.Pool.QueryRow(f.ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Lifecycle','monthly',1,12345,0,12345) RETURNING id`, out.order, product).Scan(&item))
	must(f.db.Pool.QueryRow(f.ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at) VALUES($1,$2,$3,$4,'Customer service',$5,'monthly','active',clock_timestamp()+interval '7 days') RETURNING id`, owner, out.order, item, product, suffix+".example.test").Scan(&out.service))
	must(f.db.Pool.QueryRow(f.ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES($1,$2,$3,12000) RETURNING id`, "lifecycle-invoice-"+suffix, owner, out.order).Scan(&out.invoice))
	return out
}

func lifecycleRoute(t *testing.T, f notificationFixture, kind, channelName string) {
	t.Helper()
	id := strings.NewReplacer(".", "-", "_", "-").Replace(kind) + "-" + channelName
	instance := ""
	if channelName == "telegram" {
		instance = "bot-fixture"
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationRoute(f.ctx, tx, id, NotificationRouteInput{Enabled: true, InstanceID: instance, ExpectedVersion: 1})
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

func lifecycleQueue(t *testing.T, f notificationFixture, r lifecycleResources, kind, key string, payload map[string]string) {
	t.Helper()
	invoice, order, service := int64(0), int64(0), int64(0)
	switch {
	case kind == "order.created":
		order = r.order
	case strings.HasPrefix(kind, "service."):
		service = r.service
	default:
		invoice = r.invoice
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.QueueLifecycleNotification(f.ctx, tx, kind, invoice, order, service, key, payload)
	}); err != nil {
		t.Fatal(err)
	}
}

func lifecycleBusinessPreference(t *testing.T, f notificationFixture, ch string, version int64) NotificationPreference {
	t.Helper()
	var out NotificationPreference
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) (err error) {
		out, err = f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, ch, true, version, "business")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNotificationLifecycleDefaultsScopeAndNoHistoricalBackfill(t *testing.T) {
	f := notificationCenterFixture(t)
	r := lifecycleFixtureResources(t, f, f.customer, "scope")
	if n := f.count(t, `SELECT count(*) FROM notification_routes`); n != 2*len(NotificationEventCatalog()) {
		t.Fatalf("routes=%d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM notification_routes WHERE enabled`); n != 0 {
		t.Fatalf("new routes enabled=%d", n)
	}
	lifecycleQueue(t, f, r, "order.created", "historical-order", nil)
	lifecycleRoute(t, f, "order.created", "inbox")
	f.preference(t, f.customer, "inbox", true, 0)
	lifecycleQueue(t, f, r, "order.created", "payment-only-scope", nil)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE code='event_scope_not_subscribed'`); n != 1 {
		t.Fatalf("scope skipped=%d", n)
	}
	lifecycleBusinessPreference(t, f, "inbox", 1)
	lifecycleQueue(t, f, r, "order.created", "historical-order", nil)
	lifecycleQueue(t, f, r, "order.created", "payment-only-scope", nil)
	if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='pending'`); n != 0 {
		t.Fatalf("historical events backfilled=%d", n)
	}
	lifecycleQueue(t, f, r, "order.created", "new-order", nil)
	_, err := f.db.ClaimNotificationJob(f.ctx)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("inbox claim err=%v", err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1 AND body LIKE '%¥120.00%'`, f.customer); n != 1 {
		t.Fatalf("net order amount or inbox count incorrect=%d", n)
	}
	// An old client cannot silently change business consent while toggling it.
	pref := f.preference(t, f.customer, "inbox", false, 2)
	if pref.EventScope != "business" {
		t.Fatalf("old client changed scope: %+v", pref)
	}
}

func TestNotificationLifecycleResourcesAndTemplates(t *testing.T) {
	f := notificationCenterFixture(t)
	r := lifecycleFixtureResources(t, f, f.customer, "resources")
	other := lifecycleFixtureResources(t, f, f.other, "other-resources")
	lifecycleBusinessPreference(t, f, "inbox", 0)
	lifecycleRoute(t, f, "service.provisioned", "inbox")
	lifecycleQueue(t, f, r, "service.provisioned", "provisioned", nil)
	_, err := f.db.ClaimNotificationJob(f.ctx)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages WHERE user_id=$1 AND body LIKE '%resources.example.test%'`, f.customer); n != 1 {
		t.Fatalf("resource recipient count=%d", n)
	}
	for _, tc := range []struct {
		kind                    string
		invoice, order, service int64
		payload                 map[string]string
	}{
		{"service.provisioned", other.invoice, 0, r.service, nil},
		{"service.provisioned", 0, other.order, r.service, nil},
		{"order.created", 0, r.order, 0, map[string]string{"amount": "¥0.01"}},
		{"service.expiry_reminder", 0, 0, r.service, map[string]string{"due_date": "invalid"}},
		{"unknown.event", 0, r.order, 0, nil},
	} {
		err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			return f.db.QueueLifecycleNotification(f.ctx, tx, tc.kind, tc.invoice, tc.order, tc.service, "rejected", tc.payload)
		})
		if err == nil {
			t.Fatalf("accepted invalid resource/payload: %+v", tc)
		}
	}
	if n := f.count(t, `SELECT count(*) FROM notification_events WHERE dedupe_key='rejected'`); n != 0 {
		t.Fatalf("rejected events persisted=%d", n)
	}
	err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationTemplate(f.ctx, tx, "service-provisioned-inbox", NotificationMessageTemplateInput{Subject: "{{invoice_no}}", Body: "{{domain}}", Active: true, ExpectedVersion: 1})
		return err
	})
	if err == nil {
		t.Fatal("cross-event variable accepted")
	}
	filtered, err := f.db.NotificationDeliveriesForAdmin(f.ctx, f.actor, 20, 0, "service.provisioned")
	if err != nil || len(filtered) != 1 || filtered[0].EventKind != "service.provisioned" {
		t.Fatalf("filtered=%+v err=%v", filtered, err)
	}
}

func TestNotificationLifecycleSuppressesStaleRemindersAndOwnership(t *testing.T) {
	for _, scenario := range []string{"paid", "renewed", "same-day-due-change", "resumed", "ownership"} {
		t.Run(scenario, func(t *testing.T) {
			f := notificationCenterFixture(t)
			r := lifecycleFixtureResources(t, f, f.customer, "stale")
			lifecycleBusinessPreference(t, f, "inbox", 0)
			kind := "service.expiry_reminder"
			payload := map[string]string{"stage": "7"}
			switch scenario {
			case "paid":
				kind, payload = "invoice.overdue", nil
				f.exec(t, `UPDATE invoices SET due_at=clock_timestamp()-interval '1 day' WHERE id=$1`, r.invoice)
			case "resumed":
				kind, payload = "service.suspended", nil
				f.exec(t, `UPDATE services SET status='suspended' WHERE id=$1`, r.service)
			case "ownership":
				kind, payload = "service.provisioned", nil
			}
			lifecycleRoute(t, f, kind, "inbox")
			lifecycleQueue(t, f, r, kind, "stale-reminder", payload)
			var initiallyEligible bool
			if err := f.db.Pool.QueryRow(f.ctx, `SELECT `+notificationEligible+` FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.dedupe_key='stale-reminder'`).Scan(&initiallyEligible); err != nil || !initiallyEligible {
				t.Fatalf("reminder was already ineligible before state change: eligible=%v err=%v", initiallyEligible, err)
			}
			switch scenario {
			case "paid":
				f.exec(t, `UPDATE invoices SET status='paid' WHERE id=$1`, r.invoice)
			case "renewed":
				f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1`, r.service)
			case "same-day-due-change":
				f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 second' WHERE id=$1`, r.service)
			case "resumed":
				f.exec(t, `UPDATE services SET status='active' WHERE id=$1`, r.service)
			case "ownership":
				f.exec(t, `UPDATE services SET user_id=$2 WHERE id=$1`, r.service, f.other)
			}
			_, err := f.db.ClaimNotificationJob(f.ctx)
			if !errors.Is(err, pgx.ErrNoRows) {
				t.Fatal(err)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE status='skipped' AND code='eligibility_changed'`); n != 1 {
				t.Fatalf("stale delivery not suppressed=%d", n)
			}
			if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages`); n != 0 {
				t.Fatalf("stale inbox persisted=%d", n)
			}
		})
	}
}

func TestNotificationLifecycleTelegramOnlyAndRechecksScope(t *testing.T) {
	f := notificationCenterFixture(t)
	r := lifecycleFixtureResources(t, f, f.customer, "telegram")
	f.bind(t, f.customer, "123456789")
	lifecycleBusinessPreference(t, f, "telegram", 0)
	lifecycleRoute(t, f, "order.created", "telegram")
	lifecycleQueue(t, f, r, "order.created", "telegram-order", nil)
	lifecycleQueue(t, f, r, "order.created", "telegram-order", nil)
	if n := f.count(t, `SELECT (SELECT count(*) FROM email_outbox)+(SELECT count(*) FROM sms_outbox)`); n != 0 {
		t.Fatalf("generic event sent legacy messages=%d", n)
	}
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil || job == nil || job.Channel != "telegram" || !strings.Contains(job.Body, "¥120.00") {
		t.Fatalf("job=%+v err=%v", job, err)
	}
	if active, err := f.db.NotificationJobActive(f.ctx, *job); err != nil || !active {
		t.Fatalf("active=%v err=%v", active, err)
	}
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "telegram", true, 1, "invoice.paid")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if active, err := f.db.NotificationJobActive(f.ctx, *job); err != nil || active {
		t.Fatalf("scope downgrade kept active=%v err=%v", active, err)
	}
	// Routes are independent: Telegram needs neither a verified address nor
	// an email/SMS implementation, including when every event is enabled.
	for index, event := range NotificationEventCatalog() {
		if event.Kind == "invoice.paid" || event.Kind == "order.created" {
			continue
		}
		lifecycleRoute(t, f, event.Kind, "telegram")
		payload := map[string]string(nil)
		if event.Kind == "service.expiry_reminder" {
			payload = map[string]string{"stage": "7", "due_date": time.Now().In(notificationShanghai).AddDate(0, 0, 7).Format("2006-01-02")}
		}
		lifecycleQueue(t, f, r, event.Kind, fmt.Sprint("all-events-", index), payload)
	}
	if n := f.count(t, `SELECT count(DISTINCT kind) FROM notification_events`); n != 10 {
		t.Fatalf("generic event kinds=%d", n)
	}
}

func TestNotificationLifecycleLegacyLinksOnlyCurrentTransaction(t *testing.T) {
	for _, kind := range []string{"service.suspended", "service.expiry_reminder", "invoice.overdue"} {
		t.Run(kind, func(t *testing.T) {
			f := notificationCenterFixture(t)
			r := lifecycleFixtureResources(t, f, f.customer, "legacy-current")
			legacy, invoiceID, serviceID, stage := "service_suspended", int64(0), r.service, 0
			payload := map[string]string(nil)
			switch kind {
			case "service.expiry_reminder":
				legacy, stage = "service_expiry_reminder", 7
				payload = map[string]string{"stage": "7"}
			case "invoice.overdue":
				legacy, invoiceID, serviceID = "invoice_overdue_reminder", r.invoice, 0
			}
			var oldID, currentID int64
			insertLegacy := func(tx pgx.Tx, current bool) error {
				var outbox int64
				if err := tx.QueryRow(f.ctx, `INSERT INTO email_outbox(recipient,subject,body,kind) VALUES('fixture@example.test','Legacy','Legacy body',$1) RETURNING id`, legacy).Scan(&outbox); err != nil {
					return err
				}
				_, err := tx.Exec(f.ctx, `INSERT INTO notification_delivery_events(service_id,user_id,invoice_id,channel,kind,stage,due_date,email_outbox_id,created_at)
 VALUES(NULLIF($1::bigint,0),$2,NULLIF($3::bigint,0),'email',$4,$5,CURRENT_DATE-CASE WHEN $7::boolean THEN 0 ELSE 1 END,$6,now()-CASE WHEN $7 THEN interval '0 days' ELSE interval '1 day' END)`, serviceID, f.customer, invoiceID, legacy, stage, outbox, current)
				if current {
					currentID = outbox
				} else {
					oldID = outbox
				}
				return err
			}
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return insertLegacy(tx, false) }); err != nil {
				t.Fatal(err)
			}
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
				if _, err := tx.Exec(f.ctx, `SET LOCAL TIME ZONE 'UTC'`); err != nil {
					return err
				}
				if err := insertLegacy(tx, true); err != nil {
					return err
				}
				if kind != "service.suspended" {
					// Deliberately use the next Shanghai business date while the old
					// row uses UTC CURRENT_DATE. Linking must use the exact old row,
					// without assuming these dates are interchangeable.
					var businessDate string
					if err := tx.QueryRow(f.ctx, `SELECT (CURRENT_DATE+1)::text`).Scan(&businessDate); err != nil {
						return err
					}
					if payload == nil {
						payload = map[string]string{}
					}
					payload["due_date"] = businessDate
				}
				return f.db.QueueLifecycleNotification(f.ctx, tx, kind, invoiceID, 0, serviceID, "current-legacy", payload)
			}); err != nil {
				t.Fatal(err)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE legacy_email_outbox_id=$1`, currentID); n != 1 {
				t.Fatalf("current legacy row not linked=%d", n)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE legacy_email_outbox_id=$1`, oldID); n != 0 {
				t.Fatalf("historical row linked=%d", n)
			}
			// A later fact without a new old outbox must not attach either row.
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
				return f.db.QueueLifecycleNotification(f.ctx, tx, kind, invoiceID, 0, serviceID, "later-without-legacy", payload)
			}); err != nil {
				t.Fatal(err)
			}
			if n := f.count(t, `SELECT count(*) FROM notification_deliveries WHERE channel='mail'`); n != 1 {
				t.Fatalf("old outbox reattached=%d", n)
			}
			if n := f.count(t, `SELECT count(*) FROM email_outbox`); n != 2 {
				t.Fatalf("linking resent old chain=%d", n)
			}
		})
	}
}
