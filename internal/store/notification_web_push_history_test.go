package store

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

func TestNotificationWebPushHistoryDeduplicatesPushOnlyFactsAndWhitelistsPayload(t *testing.T) {
	f := newWebPushFixture(t)
	a := f.save(t, f.customer, f.session(), f.subscription(t, "history-one"), 0)
	f.save(t, f.customer, f.session(), f.subscription(t, "history-two"), 0)
	f.ready(t)
	acceptedInvoice := f.invoice(t, f.customer, "history-accepted")
	f.queue(t, acceptedInvoice)
	for i := 0; i < 2; i++ {
		job, err := f.db.ClaimNotificationJob(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted", Code: "push_accepted"}); err != nil {
			t.Fatal(err)
		}
	}
	// Internal metadata, arbitrary future keys, and another event's public
	// vocabulary must never widen this event's customer-visible vocabulary.
	f.exec(t, `UPDATE notification_events SET payload=payload||'{"resource_due_at":"private-deadline","session":"private-session","endpoint":"https://private-endpoint.test/token","domain":"other-event-value","future_internal":{"credential":"private-nested"}}' WHERE invoice_id=$1`, acceptedInvoice)
	items, err := f.db.WebPushNotificationHistory(f.ctx, f.customer, 20, 0)
	if err != nil || len(items) != 1 || items[0].Kind != "invoice.paid" || items[0].Name != "付款成功" || items[0].CreatedAt.IsZero() {
		t.Fatal("two browsers must produce one readable business fact", items, err)
	}
	if len(items[0].Variables) != 2 || items[0].Variables[0].Key != "invoice_no" || items[0].Variables[0].Value != "history-accepted" || items[0].Variables[0].Label != "账单号" || items[0].Variables[1].Value != "¥123.45" {
		t.Fatal("history lost the event's public facts", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil || strings.Contains(string(encoded), "private-") || strings.Contains(string(encoded), "other-event-value") || strings.Contains(string(encoded), "resource_due_at") {
		t.Fatal("history exposed internal or unrelated payload keys", err)
	}
	unknownInvoice := f.invoice(t, f.customer, "history-uncertain")
	f.queue(t, unknownInvoice)
	job, err := f.db.ClaimNotificationJob(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "unknown", Code: "push_receipt_lost"}); err != nil {
		t.Fatal(err)
	}
	// The other device remains pending for the same event. An uncertain push
	// still refers to a real business fact, without claiming human receipt.
	f.queue(t, f.invoice(t, f.customer, "history-pending"))
	f.preference(t, f.customer, "web_push", false, 1)
	f.queue(t, f.invoice(t, f.customer, "history-skipped"))
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.DeleteWebPushSubscription(f.ctx, tx, f.customer, a.ID, a.Version)
	}); err != nil {
		t.Fatal(err)
	}
	items, err = f.db.WebPushNotificationHistory(f.ctx, f.customer, 20, 0)
	if err != nil || len(items) != 2 || items[0].Variables[0].Value != "history-uncertain" || items[1].Variables[0].Value != "history-accepted" {
		t.Fatal("history must survive opt-out, exclude pending/skipped, and order recent facts first", items, err)
	}
	if other, err := f.db.WebPushNotificationHistory(f.ctx, f.other, 20, 0); err != nil || len(other) != 0 {
		t.Fatal("another customer could read push history", other, err)
	}
	page, err := f.db.WebPushNotificationHistory(f.ctx, f.customer, 1, 1)
	if err != nil || len(page) != 1 || page[0].ID != items[1].ID {
		t.Fatal("history offset pagination lost event identity", page, err)
	}
	page, err = f.db.WebPushNotificationHistory(f.ctx, f.customer, 10000, -1)
	if err != nil || len(page) != 2 {
		t.Fatal("invalid pagination was not bounded and normalized", page, err)
	}
	if n := f.count(t, `SELECT count(*) FROM admin_inbox_messages`); n != 0 {
		t.Fatal("push history silently created inbox messages", n)
	}
	if n := f.count(t, `SELECT count(*) FROM user_notification_preferences WHERE channel='inbox'`); n != 0 {
		t.Fatal("push history silently changed inbox consent", n)
	}
}

func TestNotificationWebPushHistoryRechecksCurrentOwnershipOfEveryResource(t *testing.T) {
	for _, changed := range []string{"invoice", "order", "service", "inactive_user"} {
		t.Run(changed, func(t *testing.T) {
			f := newWebPushFixture(t)
			f.save(t, f.customer, f.session(), f.subscription(t, "resource-history"), 0)
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
				if _, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, "web_push", true, 0, "business"); err != nil {
					return err
				}
				_, err := f.db.UpdateNotificationRoute(f.ctx, tx, "service-renewed-web_push", NotificationRouteInput{Enabled: true, InstanceID: "push-fixture", ExpectedVersion: 1})
				return err
			}); err != nil {
				t.Fatal(err)
			}
			resources := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "history-"+changed)
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
				return f.db.QueueLifecycleNotification(f.ctx, tx, "service.renewed", resources.invoice, resources.order, resources.service, "history-resource-fact", nil)
			}); err != nil {
				t.Fatal(err)
			}
			job, err := f.db.ClaimNotificationJob(f.ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted", Code: "push_accepted"}); err != nil {
				t.Fatal(err)
			}
			before, err := f.db.WebPushNotificationHistory(f.ctx, f.customer, 20, 0)
			if err != nil || len(before) != 1 || before[0].Kind != "service.renewed" {
				t.Fatal("service notification history is missing before ownership changes", before, err)
			}
			switch changed {
			case "invoice":
				f.exec(t, `UPDATE invoices SET user_id=$1 WHERE id=$2`, f.other, resources.invoice)
			case "order":
				f.exec(t, `UPDATE orders SET user_id=$1 WHERE id=$2`, f.other, resources.order)
			case "service":
				f.exec(t, `UPDATE services SET user_id=$1 WHERE id=$2`, f.other, resources.service)
			case "inactive_user":
				f.exec(t, `UPDATE users SET status='disabled' WHERE id=$1`, f.customer)
			}
			after, err := f.db.WebPushNotificationHistory(f.ctx, f.customer, 20, 0)
			if err != nil || len(after) != 0 {
				t.Fatal("historical snapshot remained visible after ownership or account changed", after, err)
			}
			other, err := f.db.WebPushNotificationHistory(f.ctx, f.other, 20, 0)
			if err != nil || len(other) != 0 {
				t.Fatal("resource transfer exposed the old owner's notification history to its new owner", other, err)
			}
		})
	}
}
