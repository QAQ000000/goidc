package store

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/notify"
	"github.com/jackc/pgx/v5"
)

type ownershipCompatibilityFixture struct {
	notificationFixture
	resources lifecycleResources
	adminCtx  context.Context
}

func ownershipCompatibilityResources(t *testing.T, f notificationFixture, name string) ownershipCompatibilityFixture {
	t.Helper()
	r := lifecycleFixtureResources(t, f, f.customer, name)
	f.exec(t, `UPDATE orders SET status='active',review_status='approved' WHERE id=$1`, r.order)
	f.exec(t, `UPDATE invoices SET status='paid',paid_at=clock_timestamp() WHERE id=$1`, r.invoice)
	f.exec(t, `UPDATE services SET provision_mode='simulation',upstream_id='ownership-local-resource' WHERE id=$1`, r.service)
	f.exec(t, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('ownership-compat-admin',$1,clock_timestamp()+interval '1 hour')`, f.actor)
	return ownershipCompatibilityFixture{f, r, WithStorageCredential(f.ctx, "ownership-compat-admin", false)}
}

func (f ownershipCompatibilityFixture) transfer(t *testing.T, target int64, key string) AdminServiceTransferResult {
	t.Helper()
	var owner, version, generation int64
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT user_id,version,service_ownership_generation(id) FROM services WHERE id=$1`, f.resources.service).Scan(&owner, &version, &generation); err != nil {
		t.Fatal(err)
	}
	in := AdminServiceTransferInput{ExpectedVersion: version, ExpectedOwnerGeneration: &generation, SourceCustomerID: owner, TargetCustomerID: target,
		CredentialHandoverConfirmed: true, Reason: "verified ownership compatibility", HandoverEvidence: "isolated fixture credential handover"}
	quote, err := f.db.QuoteAdminServiceTransfer(f.adminCtx, f.actor, f.resources.service, in)
	if err != nil {
		t.Fatal("transfer quote", err)
	}
	in.QuoteHash = quote.QuoteHash
	out, err := f.db.TransferAdminService(f.adminCtx, f.actor, f.resources.service, key, in)
	if err != nil {
		t.Fatal("transfer commit", err)
	}
	return out
}

func TestServiceOwnershipNotificationPeriodsAndStrictAssociations(t *testing.T) {
	n := notificationCenterFixture(t)
	f := ownershipCompatibilityResources(t, n, "ownership-notifications")
	push := webPushFixtureWithNotification(t, n)
	for index, owner := range []int64{f.customer, f.other} {
		f.bind(t, owner, fmt.Sprint(1800000000+index))
		push.save(t, owner, fmt.Sprint("notification-session-", owner), push.subscription(t, fmt.Sprint("ownership-browser-", owner)), 0)
		for _, channel := range []string{"inbox", "telegram", "web_push"} {
			if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
				_, err := f.db.UpdateNotificationPreference(f.ctx, tx, owner, channel, true, 0, "business")
				return err
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.exec(t, `UPDATE notification_routes SET enabled=true,instance_id=CASE channel WHEN 'telegram' THEN 'bot-fixture' WHEN 'web_push' THEN 'push-fixture' END WHERE channel IN ('telegram','inbox','web_push')`)
	f.exec(t, `UPDATE services SET status='suspended' WHERE id=$1`, f.resources.service)
	queue := func() {
		t.Helper()
		lifecycleQueue(t, f.notificationFixture, f.resources, "service.suspended", "ownership-same-notification", nil)
	}
	deliver := func() {
		t.Helper()
		for range 10 {
			job, err := f.db.ClaimNotificationJob(f.ctx)
			if errors.Is(err, pgx.ErrNoRows) {
				return
			}
			if err != nil {
				t.Fatal("claim ownership notification", err)
			}
			if err = f.db.CompleteNotificationJob(f.ctx, *job, notify.Result{Outcome: "accepted", Code: "ownership_fixture_accepted"}); err != nil {
				t.Fatal("accept ownership notification", err)
			}
		}
		t.Fatal("ownership notification delivery did not drain")
	}
	history := func(owner int64, expected int) []NotificationInboxItem {
		t.Helper()
		push, err := f.db.WebPushNotificationHistory(f.ctx, owner, 20, 0)
		if err != nil || len(push) != expected {
			t.Fatal("ownership push history", owner, len(push), expected, err)
		}
		inbox, err := f.db.UserNotificationInbox(f.ctx, owner, 20, 0)
		if err != nil || len(inbox) != expected {
			t.Fatal("ownership inbox history", owner, len(inbox), expected, err)
		}
		return inbox
	}
	queue()
	if got := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND order_id=$2 AND user_id=$3 AND payload->>'ownership_generation'='0'`, f.resources.service, f.resources.order, f.customer); got != 1 {
		t.Fatal("generation zero changed the original purchase binding", got)
	}
	deliver()
	originalInbox := history(f.customer, 1)[0].ID
	f.transfer(t, f.other, "ownership-notifications-to-b")
	history(f.customer, 0)
	queue()
	if got := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND order_id IS NULL AND user_id=$2 AND payload->>'ownership_generation'='1'`, f.resources.service, f.other); got != 1 {
		t.Fatal("new owner service event retained the historical purchase order", got)
	}
	if got := f.count(t, `SELECT count(*) FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.service_id=$1 AND e.user_id=$2 AND d.status='pending' AND d.channel IN ('telegram','inbox','web_push')`, f.resources.service, f.other); got != 3 {
		t.Fatal("new owner did not receive an independent delivery for every channel", got)
	}
	deliver()
	history(f.other, 1)
	for _, refs := range [][2]int64{{0, f.resources.order}, {f.resources.invoice, 0}, {f.invoice(t, f.other, "ownership-unrelated-invoice"), 0}} {
		err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			return f.db.QueueLifecycleNotification(f.ctx, tx, "service.resumed", refs[0], refs[1], f.resources.service, "ownership-invalid-association", nil)
		})
		if code := manualResourceDeliveryErrorCode(t, err); code != "invalid_notification_resource" {
			t.Fatal("invoice or explicit order association was weakened", refs, code)
		}
	}
	// The unrelated invoice is a separate financial fact. It is not associated
	// with the service and cannot obstruct its next ownership transfer.
	f.transfer(t, f.customer, "ownership-notifications-back-a")
	history(f.customer, 0)
	history(f.other, 0)
	err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		return f.db.MarkNotificationInboxRead(f.ctx, tx, f.customer, originalInbox)
	})
	if code := manualResourceDeliveryErrorCode(t, err); code != "notification_message_not_found" {
		t.Fatal("a returning owner could mutate an earlier inbox period", code)
	}
	f.exec(t, `UPDATE notification_deliveries d SET status='pending' FROM notification_events e WHERE e.id=d.event_id AND e.service_id=$1 AND coalesce(e.payload->>'ownership_generation','0')='0'`, f.resources.service)
	if got := f.count(t, `SELECT count(*) FROM notification_deliveries d WHERE d.event_id IN (SELECT id FROM notification_events WHERE service_id=$1 AND payload->>'ownership_generation'='0') AND `+notificationEligible, f.resources.service); got != 0 {
		t.Fatal("returning to the first owner revived an earlier notification", got)
	}
	queue()
	if got := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND user_id=$2 AND payload->>'ownership_generation'='2'`, f.resources.service, f.customer); got != 1 {
		t.Fatal("same logical notification was suppressed in a later ownership period", got)
	}
	deliver()
	history(f.customer, 1)
	history(f.other, 0)
}

func TestServiceOwnershipLegacyRemindersAndLifecyclePeriods(t *testing.T) {
	n := legacyDeadlineFixture(t, "UTC")
	f := ownershipCompatibilityResources(t, n, "ownership-legacy")
	f.exec(t, `UPDATE users SET phone='+8613900000000' WHERE id=$1`, f.other)
	f.exec(t, `UPDATE users SET email_verified=true,phone_verified=true WHERE id=$1`, f.other)
	legacyDeadlineSetService(t, f.notificationFixture, f.resources.service, 7)
	legacyDeadlineScan(t, f.notificationFixture, 2)
	f.transfer(t, f.other, "ownership-legacy-to-b")
	legacyDeadlineScan(t, f.notificationFixture, 2)
	if got := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1 AND ownership_generation=1 AND user_id=$2 AND kind='service_expiry_reminder'`, f.resources.service, f.other); got != 2 {
		t.Fatal("same-stage reminders were suppressed for the new owner", got)
	}
	f.transfer(t, f.customer, "ownership-legacy-back-a")
	legacyDeadlineScan(t, f.notificationFixture, 2)
	// Requeueing an old row models a stale retry without touching its evidence.
	// Both addresses match again, but the old ownership period must stay dead.
	f.exec(t, `UPDATE email_outbox o SET status='pending' FROM notification_delivery_events n WHERE n.email_outbox_id=o.id AND n.service_id=$1 AND n.ownership_generation=0`, f.resources.service)
	f.exec(t, `UPDATE sms_outbox o SET status='pending' FROM notification_delivery_events n WHERE n.sms_outbox_id=o.id AND n.service_id=$1 AND n.ownership_generation=0`, f.resources.service)
	for _, item := range []struct{ table, channel string }{{"email_outbox", "email"}, {"sms_outbox", "sms"}} {
		query := `SELECT count(*) FROM ` + item.table + ` o JOIN notification_delivery_events n ON n.` + item.channel + `_outbox_id=o.id WHERE n.service_id=$1 AND n.ownership_generation=0 AND ` + legacyDeadlineEligibleSQL("o", item.channel)
		if got := f.count(t, query, f.resources.service); got != 0 {
			t.Fatal("old reminder became eligible after ownership returned", item.channel, got)
		}
	}
	// Provisioning and suspension messages are not scheduler reminders. Their
	// separate send fence must still reject historical ownership periods.
	f.exec(t, `UPDATE notification_templates SET active=true WHERE kind IN ('service_provision_success','service_suspended')`)
	if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		if err := f.db.QueueServiceProvisionSuccessNotifications(f.ctx, tx, f.resources.service); err != nil {
			return err
		}
		if _, err := tx.Exec(f.ctx, `UPDATE services SET status='suspended' WHERE id=$1`, f.resources.service); err != nil {
			return err
		}
		return f.db.QueueServiceSuspendedNotifications(f.ctx, tx, f.resources.service)
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.count(t, `SELECT count(*) FROM notification_delivery_events WHERE service_id=$1 AND ownership_generation=2 AND kind IN ('service_provision_success','service_suspended')`, f.resources.service); got != 4 {
		t.Fatal("lifecycle fixture did not materialize both channels and event families", got)
	}
	f.transfer(t, f.other, "ownership-legacy-to-b-again")
	for _, item := range []struct{ table, channel string }{{"email_outbox", "email"}, {"sms_outbox", "sms"}} {
		query := `SELECT count(*) FROM ` + item.table + ` o JOIN notification_delivery_events n ON n.` + item.channel + `_outbox_id=o.id WHERE n.service_id=$1 AND n.kind IN ('service_provision_success','service_suspended') AND ` + legacyDeadlineEligibleSQL("o", item.channel)
		if got := f.count(t, query, f.resources.service); got != 0 {
			t.Fatal("old lifecycle message remained eligible for a former owner", item.channel, got)
		}
	}
}

func TestServiceOwnershipUnifiedDeadlineScans(t *testing.T) {
	for _, kind := range []string{"service.expiry_reminder", "service.expired"} {
		t.Run(kind, func(t *testing.T) {
			f := ownershipCompatibilityResources(t, notificationCenterFixture(t), "ownership-unified-deadline")
			other := lifecycleFixtureResources(t, f.notificationFixture, f.customer, "ownership-unified-following")
			f.exec(t, `UPDATE notification_templates SET active=false`)
			f.exec(t, `UPDATE notification_routes SET enabled=true WHERE channel='inbox' AND event_kind=$1`, kind)
			for _, owner := range []int64{f.customer, f.other} {
				if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
					_, err := f.db.UpdateNotificationPreference(f.ctx, tx, owner, "inbox", true, 0, "business")
					return err
				}); err != nil {
					t.Fatal(err)
				}
			}
			legacyDeadlineSetService(t, f.notificationFixture, f.resources.service, 7)
			if kind == "service.expired" {
				f.exec(t, `UPDATE services SET next_due_at=clock_timestamp()-interval '1 hour' WHERE id=$1`, f.resources.service)
			}
			f.exec(t, `UPDATE services SET next_due_at=(SELECT next_due_at FROM services WHERE id=$1) WHERE id=$2`, f.resources.service, other.service)
			for generation, owner := range []int64{f.customer, f.other, f.customer} {
				if generation > 0 {
					f.transfer(t, owner, fmt.Sprint("ownership-unified-transfer-", generation))
					// A later eligible row exposes repeated selection of an already
					// emitted ownership key: limit=1 must advance past the first row.
					f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 microsecond' WHERE id=$1`, other.service)
				}
				for tick := range 3 {
					if count, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil || count != 0 {
						t.Fatal("unified deadline scan", generation, tick, count, err)
					}
					if got := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind=$2 AND user_id=$3 AND payload->>'ownership_generation'=$4`, f.resources.service, kind, owner, fmt.Sprint(generation)); got != 1 {
						t.Fatal("same deadline was lost or duplicated in an ownership period", generation, tick, got)
					}
					if tick > 0 {
						if got := f.count(t, `SELECT count(*) FROM notification_events WHERE service_id=$1 AND kind=$2`, other.service, kind); got != generation+1 {
							t.Fatal("an emitted ownership key starved the next deadline", generation, tick, got)
						}
					}
				}
			}
		})
	}
}

func TestServiceOwnershipActionKeysAcrossPeriods(t *testing.T) {
	f := ownershipCompatibilityResources(t, notificationCenterFixture(t), "ownership-action-key")
	const key = "ownership-same-action-key"
	run := func() ServiceActionResult {
		t.Helper()
		out, err := f.db.EnqueueServiceAction(f.adminCtx, f.actor, f.resources.service, "sync", key, true)
		if err != nil {
			t.Fatal(err)
		}
		job, err := f.db.ClaimProvisionJob(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		if job.ID != out.JobID {
			t.Fatal("claimed another service action", job.ID, out.JobID)
		}
		if err = f.db.ProcessProvisionJob(f.ctx, *job); err != nil {
			t.Fatal(err)
		}
		return out
	}
	first := run()
	var originalKey string
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT idempotency_key FROM provision_jobs WHERE id=$1`, first.JobID).Scan(&originalKey); err != nil || originalKey != key {
		t.Fatal("generation zero rewrote an existing idempotency namespace", originalKey, err)
	}
	f.transfer(t, f.other, "ownership-action-to-b")
	second := run()
	if second.Replayed || second.JobID == first.JobID {
		t.Fatal("new owner replayed the previous owner's action", first, second)
	}
	replay, err := f.db.EnqueueServiceAction(f.adminCtx, f.actor, f.resources.service, "sync", key, true)
	if err != nil || !replay.Replayed || replay.JobID != second.JobID {
		t.Fatal("same-period action replay was lost", replay, err)
	}
	f.transfer(t, f.customer, "ownership-action-back-a")
	third := run()
	if third.Replayed || third.JobID == first.JobID || third.JobID == second.JobID {
		t.Fatal("returning owner resurrected an older action receipt", first, second, third)
	}
}

func TestServiceOwnershipStatusCorrectionKeepsOriginalPurchaseProof(t *testing.T) {
	f := ownershipCompatibilityResources(t, notificationCenterFixture(t), "ownership-status-source")
	out, err := f.db.EnqueueServiceAction(f.adminCtx, f.actor, f.resources.service, "suspend", "ownership-proven-suspension", true)
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.db.ClaimProvisionJob(f.ctx)
	if err != nil || job.ID != out.JobID {
		t.Fatal("claim suspension", job, err)
	}
	if err = f.db.ProcessProvisionJob(f.ctx, *job); err != nil {
		t.Fatal(err)
	}
	// Only the projection drifts. Preserve the real completed job and audit.
	f.exec(t, `UPDATE services SET status='active',version=version+1 WHERE id=$1`, f.resources.service)
	transferred := f.transfer(t, f.other, "ownership-status-to-b")
	in := AdminServiceStatusCorrectionInput{ExpectedVersion: transferred.Version, SourceJobID: job.ID, TargetStatus: "suspended", Reason: "repair observed projection", Evidence: "original successful simulation suspension"}
	quote, err := f.db.QuoteAdminServiceStatusCorrection(f.adminCtx, f.actor, f.resources.service, in)
	if err != nil {
		t.Fatal("original purchase proof must survive ownership transfer", err)
	}
	in.QuoteHash = quote.QuoteHash
	corrected, err := f.db.CorrectAdminServiceStatus(f.adminCtx, f.actor, f.resources.service, "ownership-status-correction", in)
	if err != nil || corrected.Status != "suspended" || corrected.CustomerID != f.other {
		t.Fatal("correction lost the current owner or original purchase provenance", corrected, err)
	}
}
