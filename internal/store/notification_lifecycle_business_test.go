package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5"
)

func lifecycleBusinessFixture(t *testing.T) notificationFixture {
	t.Helper()
	f := notificationCenterFixture(t)
	f.bind(t, f.customer, "1122334455")
	f.preference(t, f.customer, "telegram", true, 0)
	f.preference(t, f.customer, "inbox", true, 0)
	for _, channel := range []string{"telegram", "inbox"} {
		if err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
			_, err := f.db.UpdateNotificationPreference(f.ctx, tx, f.customer, channel, true, 1, "business")
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	f.exec(t, `UPDATE notification_routes SET enabled=true,instance_id=CASE WHEN channel='telegram' THEN 'bot-fixture' END;
 UPDATE notification_templates SET active=false;`)
	return f
}

func lifecycleCheckout(t *testing.T, f notificationFixture, quantity int, deliverySnapshot ...string) (CheckoutResult, int64) {
	t.Helper()
	var product int64
	f.exec(t, `INSERT INTO products(name,slug,price_cents,billing_cycle,provision_mode,approval_required)
 VALUES('Lifecycle product','lifecycle-product',1000,'monthly','simulation',false)`)
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT id FROM products WHERE slug='lifecycle-product'`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if len(deliverySnapshot) > 0 {
		f.exec(t, `UPDATE products SET delivery_config=$2::jsonb WHERE id=$1`, product, deliverySnapshot[0])
	}
	return lifecycleCheckoutProduct(t, f, quantity, product)
}

func lifecycleCheckoutProduct(t *testing.T, f notificationFixture, quantity int, product int64) (CheckoutResult, int64) {
	t.Helper()
	var item int64
	f.exec(t, `INSERT INTO carts(user_id) VALUES($1) ON CONFLICT(user_id) DO NOTHING`, f.customer)
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO cart_items(cart_id,product_id,quantity,product_revision,billing_cycle)
 SELECT c.id,p.id,$3,p.revision,'monthly' FROM carts c CROSS JOIN products p WHERE c.user_id=$1 AND p.id=$2 RETURNING id`, f.customer, product, quantity).Scan(&item); err != nil {
		t.Fatal(err)
	}
	input := CheckoutInput{CartVersion: 1, ItemIDs: []int64{item}}
	out, err := f.db.Checkout(f.ctx, f.customer, "lifecycle-checkout", input)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := f.db.Checkout(f.ctx, f.customer, "lifecycle-checkout", input)
	if err != nil || !replay.Replayed || replay.OrderID != out.OrderID {
		t.Fatal("checkout replay changed fact", replay, err)
	}
	var service int64
	if err = f.db.Pool.QueryRow(f.ctx, `SELECT id FROM services WHERE order_id=$1 ORDER BY id LIMIT 1`, out.OrderID).Scan(&service); err != nil {
		t.Fatal(err)
	}
	return out, service
}

func lifecycleRunJob(t *testing.T, f notificationFixture) ProvisionJob {
	t.Helper()
	job, err := f.db.ClaimProvisionJob(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.ProcessProvisionJob(f.ctx, *job); err != nil {
		t.Fatal(err)
	}
	return *job
}

func lifecycleEventCount(t *testing.T, f notificationFixture, kind string, expected int) {
	t.Helper()
	if count := f.count(t, `SELECT count(*) FROM notification_events WHERE kind=$1`, kind); count != expected {
		t.Fatalf("%s events=%d want=%d", kind, count, expected)
	}
	if count := f.count(t, `SELECT count(*) FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.kind=$1 AND d.channel IN ('telegram','inbox')`, kind); count != expected*2 {
		t.Fatalf("%s deliveries=%d want=%d", kind, count, expected*2)
	}
}

func TestLifecycleBusinessCheckoutAndConfirmedServiceTransitions(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	order, service := lifecycleCheckout(t, f, 1)
	lifecycleEventCount(t, f, "order.created", 1)
	lifecycleEventCount(t, f, "service.provisioned", 0)
	f.exec(t, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
	if _, err := f.db.PayInvoice(f.ctx, f.customer, order.InvoiceID, "lifecycle-first-pay"); err != nil {
		t.Fatal(err)
	}
	lifecycleRunJob(t, f)
	lifecycleEventCount(t, f, "service.provisioned", 1)
	for index, action := range []string{"suspend", "resume", "suspend", "resume"} {
		key := fmt.Sprintf("lifecycle-action-%d", index)
		out, err := f.db.EnqueueServiceAction(f.ctx, f.actor, service, action, key, true)
		if err != nil {
			t.Fatal(err)
		}
		// Accepted jobs are not completed lifecycle facts.
		lifecycleEventCount(t, f, "service.suspended", (index+1)/2)
		job := lifecycleRunJob(t, f)
		if job.ID != out.JobID {
			t.Fatal("wrong job")
		}
		if err = f.db.ProcessProvisionJob(f.ctx, job); err == nil {
			t.Fatal("completed job unexpectedly executed again")
		}
	}
	lifecycleEventCount(t, f, "service.suspended", 2)
	lifecycleEventCount(t, f, "service.resumed", 2)
	renew, err := f.db.CreateRenewalInvoice(f.ctx, f.customer, service, "lifecycle-renew-create")
	if err != nil {
		t.Fatal(err)
	}
	lifecycleEventCount(t, f, "service.renewed", 0)
	if _, err = f.db.PayInvoice(f.ctx, f.customer, renew.InvoiceID, "lifecycle-renew-pay"); err != nil {
		t.Fatal(err)
	}
	if err = f.db.WithTx(f.ctx, func(tx pgx.Tx) error { return f.db.QueuePaymentSuccessNotifications(f.ctx, tx, renew.InvoiceID) }); err != nil {
		t.Fatal(err)
	}
	lifecycleEventCount(t, f, "service.renewed", 1)
	if _, err = f.db.EnqueueServiceAction(f.ctx, f.actor, service, "terminate", "lifecycle-terminate", true); err != nil {
		t.Fatal(err)
	}
	lifecycleEventCount(t, f, "service.terminated", 0)
	lifecycleRunJob(t, f)
	lifecycleEventCount(t, f, "service.terminated", 1)
	if count := f.count(t, `SELECT (SELECT count(*) FROM email_outbox)+(SELECT count(*) FROM sms_outbox)`); count != 0 {
		t.Fatal("Telegram-only lifecycle unexpectedly queued legacy messages", count)
	}
}

func TestLifecycleBusinessDeadlineAndBillingScansWithoutLegacyChannels(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	order, _ := lifecycleCheckout(t, f, 4)
	rows, err := f.db.Pool.Query(f.ctx, `SELECT id FROM services WHERE order_id=$1 ORDER BY id`, order.OrderID)
	if err != nil {
		t.Fatal(err)
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil || len(ids) != 4 {
		t.Fatal(ids, err)
	}
	// A UTC connection sees this upcoming deadline on the previous date.
	f.exec(t, `UPDATE services SET status='active',next_due_at=
 (((now() AT TIME ZONE 'Asia/Shanghai')::date+7)+time '00:30') AT TIME ZONE 'Asia/Shanghai' WHERE id=$1`, ids[0])
	f.exec(t, `UPDATE services SET status='active',next_due_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, ids[1])
	f.exec(t, `UPDATE services SET status='active',next_due_at=
 (((now() AT TIME ZONE 'Asia/Shanghai')::date-1)+time '12:00') AT TIME ZONE 'Asia/Shanghai' WHERE id=$1`, ids[2])
	f.exec(t, `UPDATE services SET status='terminated',next_due_at=clock_timestamp()-interval '1 microsecond' WHERE id=$1`, ids[3])
	for range 2 {
		count, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 100)
		if err != nil || count != 0 {
			t.Fatal("legacy count changed or scan failed", count, err)
		}
	}
	lifecycleEventCount(t, f, "service.expiry_reminder", 1)
	// The previous-day deadline is now sampled by bounded catchup, but its
	// routes and consent were enabled after expiry and cannot receive it.
	lifecycleEventCount(t, f, "service.expired", 2)
	for range 2 {
		if count, err := f.db.EnqueueUnpaidOrderReminders(f.ctx, 100); err != nil || count != 0 {
			t.Fatal(count, err)
		}
	}
	lifecycleEventCount(t, f, "order.payment_reminder", 1)
	f.exec(t, `UPDATE invoices SET due_at=now()-interval '2 days' WHERE id=$1`, order.InvoiceID)
	for range 2 {
		if count, err := f.db.EnqueueOverdueInvoiceReminders(f.ctx, 100); err != nil || count != 0 {
			t.Fatal(count, err)
		}
	}
	lifecycleEventCount(t, f, "invoice.overdue", 1)
	f.exec(t, `UPDATE invoices SET due_at=due_at-interval '1 hour' WHERE id=$1`, order.InvoiceID)
	if _, err := f.db.EnqueueOverdueInvoiceReminders(f.ctx, 100); err != nil {
		t.Fatal(err)
	}
	lifecycleEventCount(t, f, "invoice.overdue", 2)
	f.exec(t, `UPDATE scheduler_task_settings SET settings=jsonb_set(settings,'{expiry_days}','[]'::jsonb)`)
	if _, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 100); err != nil {
		t.Fatal("empty reminder stages rejected", err)
	}
	f.exec(t, `UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=ANY($1)`, ids[:2])
	f.exec(t, `UPDATE invoices SET status='cancelled' WHERE id=$1`, order.InvoiceID)
	if job, err := f.db.ClaimNotificationJob(f.ctx); err != nil {
		t.Fatal(err)
	} else if job == nil {
		t.Fatal("unrelated order event should remain eligible")
	}
	// The reminder rows remain historical facts, but cannot reach the adapter
	// after renewal/cancellation. Claim consumes both inbox and stale rows.
	for range 20 {
		job, err := f.db.ClaimNotificationJob(f.ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if job != nil {
			t.Fatal("obsolete reminder reached external adapter", job)
		}
	}
	if count := f.count(t, `SELECT count(*) FROM notification_deliveries d JOIN notification_events e ON e.id=d.event_id WHERE e.kind IN ('service.expiry_reminder','service.expired','order.payment_reminder','invoice.overdue') AND d.status<>'skipped'`); count != 0 {
		t.Fatal("obsolete reminder remained eligible", count)
	}
}

func TestLifecycleBusinessNotificationFailureRollsBackCheckout(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	f.exec(t, `CREATE FUNCTION reject_lifecycle_event() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'fixture lifecycle event failure'; END $$;
 CREATE TRIGGER reject_lifecycle_event BEFORE INSERT ON notification_events FOR EACH ROW EXECUTE FUNCTION reject_lifecycle_event();`)
	var product int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO products(name,slug,price_cents,provision_mode,approval_required) VALUES('Rollback','rollback-lifecycle',100,'simulation',false) RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	err := f.db.WithTx(f.ctx, func(tx pgx.Tx) error {
		_, err := f.db.persistCheckoutLines(f.ctx, tx, f.customer, "rollback-lifecycle", "hash", []checkoutLine{{ProductID: product, Name: "Rollback", Cycle: "monthly", Qty: 1, UnitPrice: 100, OptionIDs: []int64{}, OptionQuantities: map[int64]int64{}, Snapshot: []OptionSnapshot{}, PurchaseSnapshot: []PurchaseFieldSnapshot{}}}, CheckoutResult{TotalCents: 100}, []RenewalBenefit{{}}, false, 0, checkoutOptions{GenerateInvoice: true, ActorID: f.customer, Source: "customer"})
		return err
	})
	if err == nil {
		t.Fatal("event failure committed checkout")
	}
	for _, table := range []string{"orders", "invoices", "services", "order_items", "notification_events", "notification_deliveries"} {
		if count := f.count(t, `SELECT count(*) FROM `+table); count != 0 {
			t.Fatal(table, count)
		}
	}
}

func TestLifecycleBusinessManualDeliveryAndNOKVMResults(t *testing.T) {
	t.Run("manual confirmation", func(t *testing.T) {
		f := lifecycleBusinessFixture(t)
		order, service := lifecycleCheckout(t, f, 1)
		f.exec(t, `UPDATE services SET provision_mode='manual' WHERE id=$1`, service)
		f.exec(t, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
		if _, err := f.db.PayInvoice(f.ctx, f.customer, order.InvoiceID, "manual-lifecycle-pay"); err != nil {
			t.Fatal(err)
		}
		job := lifecycleRunJob(t, f)
		lifecycleEventCount(t, f, "service.provisioned", 0)
		input := ManualCompletion{JobID: job.ID, UpstreamID: "manual-resource", Username: "redacted-account", Note: "fixture resource confirmed"}
		if _, err := f.db.CompleteManualServiceJob(f.ctx, f.actor, service, input); err != nil {
			t.Fatal(err)
		}
		if replay, err := f.db.CompleteManualServiceJob(f.ctx, f.actor, service, input); err != nil || !replay.Replayed {
			t.Fatal("manual completion replay changed lifecycle", replay, err)
		}
		lifecycleEventCount(t, f, "service.provisioned", 1)
	})
	t.Run("NOKVM manual reconciliation", func(t *testing.T) {
		f := lifecycleBusinessFixture(t)
		_, service := lifecycleCheckout(t, f, 1, `{"resources":{"login":"fixture"}}`)
		f.exec(t, `UPDATE services SET status='provisioning',provision_mode='nokvm' WHERE id=$1`, service)
		for _, action := range []string{"create", "suspend", "resume", "terminate"} {
			var job int64
			if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts) VALUES($1,$2,$3,'waiting_verification',1) RETURNING id`, service, action, "nokvm-lifecycle-"+action).Scan(&job); err != nil {
				t.Fatal(err)
			}
			input := DeliveryVerification{JobID: job, Resolution: "applied", UpstreamID: "42", Note: "fixture operator verified resource"}
			if _, err := f.db.VerifyNOKVMJob(f.ctx, f.actor, service, input); err != nil {
				t.Fatal(action, err)
			}
			if replay, err := f.db.VerifyNOKVMJob(f.ctx, f.actor, service, input); err != nil || !replay.Replayed {
				t.Fatal("reconciliation replay changed fact", replay, err)
			}
		}
		for _, kind := range []string{"service.provisioned", "service.suspended", "service.resumed", "service.terminated"} {
			lifecycleEventCount(t, f, kind, 1)
		}
	})
}

func TestLifecycleBusinessSupplierCallbackConfirmsActualStateOnce(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	var supplierID, productID, bindingID int64
	if err := f.db.Pool.QueryRow(f.ctx, `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,config) VALUES('Lifecycle fixture',$1,'https://supplier.example.test','fixture',''::bytea,'{"endpoint":"https://supplier.example.test","username":"fixture"}') RETURNING id`, supplier.ProviderZJMF).Scan(&supplierID); err != nil {
		t.Fatal(err)
	}
	sealed, err := supplierSeal(f.db.ProvisionEncryptionKey, "account", supplierID, "lifecycle-fixture-private")
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE supplier_accounts SET credentials_encrypted=$2 WHERE id=$1`, supplierID, sealed)
	if err = f.db.Pool.QueryRow(f.ctx, `INSERT INTO products(name,slug,price_cents,billing_cycle,provision_mode,approval_required) VALUES('Lifecycle supplier product','lifecycle-supplier-product',1000,'monthly','manual',false) RETURNING id`).Scan(&productID); err != nil {
		t.Fatal(err)
	}
	fields, err := supplierSeal(f.db.ProvisionEncryptionKey, "binding-fields", productID, map[int64]string{})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.db.Pool.QueryRow(f.ctx, `INSERT INTO supplier_product_bindings(product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,custom_fields_encrypted) VALUES($1,$2,$3,7,'fixture','monthly','CNY',1000,$4) RETURNING id`, productID, supplierID, supplier.ProviderZJMF, fields).Scan(&bindingID); err != nil {
		t.Fatal(err)
	}
	// The callback protocol is owned by ZJMF and resolves the immutable checkout
	// snapshot, including its exact binding, rather than the current product.
	snapshot, err := json.Marshal(supplierSnapshot{SupplierID: supplierID, BindingID: bindingID, Provider: supplier.ProviderZJMF})
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, `UPDATE products SET provision_mode='supplier',delivery_config=$2::jsonb WHERE id=$1`, productID, snapshot)
	_, service := lifecycleCheckoutProduct(t, f, 1, productID)
	f.exec(t, `UPDATE services SET status='provisioning' WHERE id=$1`, service)
	for _, status := range []string{"Active", "Active", "Suspended", "Suspended", "Active", "Terminated", "Terminated"} {
		input := SupplierHostSyncInput{ID: fmt.Sprint(service), HostID: "42", DomainStatus: status, RandStr: "fixture-random"}
		input.Signature = supplierCallbackSignature(input.ID, supplierDownstreamToken(f.db.ProvisionEncryptionKey, service), input.RandStr)
		if err := f.db.SyncSupplierHost(f.ctx, input); err != nil {
			t.Fatal(status, err)
		}
	}
	lifecycleEventCount(t, f, "service.provisioned", 1)
	lifecycleEventCount(t, f, "service.suspended", 1)
	// Existing lifecycle merge forbids a callback reviving suspended resources.
	// Notification code reports only the state that the business layer applied.
	lifecycleEventCount(t, f, "service.resumed", 0)
	lifecycleEventCount(t, f, "service.terminated", 1)
}

func TestLifecycleBusinessReminderPolicyAndBatchDrain(t *testing.T) {
	f := lifecycleBusinessFixture(t)
	order, _ := lifecycleCheckout(t, f, 6)
	f.exec(t, `UPDATE services SET status='active',next_due_at=
 (((now() AT TIME ZONE 'Asia/Shanghai')::date+7)+time '00:30') AT TIME ZONE 'Asia/Shanghai' WHERE order_id=$1`, order.OrderID)
	f.exec(t, `UPDATE invoices SET due_at=now()-interval '2 days' WHERE id=$1`, order.InvoiceID)
	f.exec(t, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,amount_cents,due_at)
 SELECT 'LIFECYCLE-BATCH-'||n::text,$1,'manual',100,now()-interval '2 days' FROM generate_series(1,5) n`, f.customer)
	f.exec(t, `UPDATE notification_routes SET enabled=false`)
	for range 2 {
		if _, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.EnqueueBillingNotifications(f.ctx, 1); err != nil {
			t.Fatal(err)
		}
	}
	if count := f.count(t, `SELECT count(*) FROM notification_events WHERE kind IN ('service.expiry_reminder','invoice.overdue')`); count != 0 {
		t.Fatal("disabled reminder routes generated events", count)
	}
	f.exec(t, `UPDATE notification_routes SET enabled=true`)
	var wg sync.WaitGroup
	errorsCh := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			for range 6 {
				if _, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil {
					errorsCh <- err
					return
				}
				if _, err := f.db.EnqueueBillingNotifications(f.ctx, 1); err != nil {
					errorsCh <- err
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
	lifecycleEventCount(t, f, "service.expiry_reminder", 6)
	lifecycleEventCount(t, f, "invoice.overdue", 6)
	// Reopening routes does not recreate facts already sampled by the scan.
	f.exec(t, `UPDATE notification_routes SET enabled=false`)
	f.exec(t, `UPDATE notification_routes SET enabled=true,version=version+1`)
	if _, err := f.db.EnqueueServiceExpiryReminders(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.EnqueueBillingNotifications(f.ctx, 1); err != nil {
		t.Fatal(err)
	}
	lifecycleEventCount(t, f, "service.expiry_reminder", 6)
	lifecycleEventCount(t, f, "invoice.overdue", 6)
}
