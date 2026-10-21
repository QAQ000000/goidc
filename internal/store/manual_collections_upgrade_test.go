package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const manualCollectionPredecessor = "189_service_term_management.sql"

// Start from the actual predecessor, rather than removing the new table from
// the latest schema. Historical evidence is written before schema190 exists.
func manualCollectionSchema189(t *testing.T) *Store {
	t.Helper()
	db := adminServiceManagementSchema187(t)
	for _, name := range []string{"188_admin_service_pricing.sql", manualCollectionPredecessor} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var premature bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('manual_collections') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &premature); err != nil || boundary != manualCollectionPredecessor || premature {
		t.Fatal("manual collection upgrade must originate at genuine schema189", boundary, premature, err)
	}
	return db
}

func manualCollectionHistoricalMoney(t *testing.T, db *Store) string {
	t.Helper()
	var raw string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'customers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
 'administrators',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM administrators x),
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'order_items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
 'order_pricing',(SELECT jsonb_agg(to_jsonb(x) ORDER BY order_item_id) FROM order_item_pricing x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'invoice_items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoice_items x),
 'wallet',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
 'payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_transactions x),
 'webhooks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_webhook_events x),
 'credit_payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM credit_invoice_payments x),
 'credit_receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM credit_repayments x),
 'effects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM service_renewal_effects x),
 'reversals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY effect_id) FROM service_renewal_reversals x),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x),
 'invoice_referrals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM invoice_affiliate_attributions x),
 'fixed_prices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY service_id) FROM service_renewal_pricing x),
 'term_management',(SELECT jsonb_agg(to_jsonb(x) ORDER BY service_id) FROM service_term_management x),
 'managed_effects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY job_id) FROM service_managed_renewal_effects x),
 'notifications',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM notification_events x),
 'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x),
 'old_migrations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY version) FROM schema_migrations x WHERE version<=$1)
 )::text`, manualCollectionPredecessor).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestManualCollectionUpgrade189PreservesFinancialEvidence(t *testing.T) {
	db := manualCollectionSchema189(t)
	ctx := t.Context()
	_, customer := topUpFixture(t, db)
	var product, order, item, service, invoice int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,billing_cycle,provision_mode)
 VALUES('Historical schema189 service','manual-collection-upgrade',2000,'monthly','manual') RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
 VALUES('COLLECTION-HISTORICAL-189',$1,'paid',2000,'collection-historical-order',repeat('9',64)) RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
 VALUES($1,$2,'Historical schema189 service','monthly',1,2000,0,2000) RETURNING id`, order, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot)
 VALUES($1,2000,0,0,2000,'{"historical_schema":189}')`, item); err != nil {
		t.Fatal(err)
	}
	beforeDue := time.Date(2088, time.January, 31, 12, 0, 0, 123000, time.UTC)
	if err = tx.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,next_due_at,
 recurring_price_cents,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discounts_used,renewal_promo_code,auto_renew,version)
 VALUES($1,$2,$3,$4,'Historical schema189 service','collection-history.test','monthly','active','manual',$5,2000,100,3,1,'COLLECTION189',true,9) RETURNING id`, customer, order, item, product, beforeDue).Scan(&service); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents,version)
 VALUES('COLLECTION-RENEWAL-189',$1,1900,4) RETURNING id`, customer).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,service_id,item_type,description,amount_cents,renewal_base_cents,renewal_discount_cents,
 renewal_fixed_discount_cents,renewal_discount_limit,renewal_discount_ordinal,renewal_promo_code)
 VALUES($1,$2,'renew','Historical balance-paid renewal',1900,2000,100,100,3,2,'COLLECTION189')`, invoice, service); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, "UPDATE users SET balance_cents=10000 WHERE id=$1", customer); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Exercise the unchanged real settlement and its deferred financial proof
	// under schema189; do not manufacture an entitlement effect for the upgrade.
	if _, err = db.PayInvoice(ctx, customer, invoice, "collection-historical-balance-payment", 4); err != nil {
		t.Fatal(err)
	}
	var genuine bool
	if err = db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM service_renewal_effects e JOIN services s ON s.id=e.service_id
 JOIN users u ON u.id=s.user_id WHERE e.invoice_id=$1 AND e.service_id=$2 AND e.before_due=$3
 AND e.before_used=1 AND e.after_used=2 AND e.after_due=s.next_due_at AND s.renewal_discounts_used=2 AND u.balance_cents=8100)`, invoice, service, beforeDue).Scan(&genuine); err != nil || !genuine {
		t.Fatal("historical fixture did not produce a genuine paid renewal effect", genuine, err)
	}
	// Preserve original gateway bytes and both successful and unresolved facts.
	// These are synthetic original rows, not proof of an external transaction.
	for _, status := range []string{"paid", "unpaid"} {
		var externalInvoice int64
		if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at)
 VALUES($1,$2,'manual',$3,1234,CASE WHEN $3='paid' THEN now() ELSE NULL END) RETURNING id`, "COLLECTION-OLD-GATEWAY-"+status, customer, status).Scan(&externalInvoice); err != nil {
			t.Fatal(err)
		}
		paymentStatus, externalID := "pending", ""
		if status == "paid" {
			paymentStatus, externalID = "succeeded", "collection-old-external-189"
		}
		if _, err = db.Pool.Exec(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,credential_snapshot,external_transaction_id,completed_at,raw_result)
 VALUES('epay_alipay',$1,$2,$3,1234,$4,$5,$6,CASE WHEN $4='succeeded' THEN now() ELSE NULL END,'{"historical":189}')`, "collection-old-gateway-"+status, externalInvoice, customer, paymentStatus, []byte("historical-opaque-credential-bytes"), externalID); err != nil {
			t.Fatal(err)
		}
	}
	oldMoney := manualCollectionHistoricalMoney(t, db)
	if err = db.CheckSchema(ctx); err == nil {
		t.Fatal("current runtime accepted schema189 before the manual collection migration")
	}
	for pass := 0; pass < 2; pass++ {
		if err = db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		if got := manualCollectionHistoricalMoney(t, db); got != oldMoney {
			t.Fatal("upgrade rewrote historical money, credential bytes, entitlement, notifications, receipts or migration checksums", pass)
		}
		var inferred int
		if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM manual_collections").Scan(&inferred); err != nil || inferred != 0 {
			t.Fatal("upgrade invented historical manual receipts", inferred, err)
		}
	}
	assertNotificationUpgradeChecksums(t, db)
}

type manualCollectionDatabaseFixture struct {
	db                                        *Store
	actor, customer, service, invoice, amount int64
	received                                  time.Time
	transaction                               string
}

func manualCollectionDatabaseSource(t *testing.T, db *Store, actor, customer int64, suffix string) manualCollectionDatabaseFixture {
	t.Helper()
	r := lifecycleFixtureResources(t, notificationFixture{db: db, ctx: t.Context()}, customer, "manual-collection-"+suffix)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET provision_mode='manual',recurring_price_cents=2000,
 upstream_id='verified-local-resource',next_due_at='2088-01-31T12:00:00Z' WHERE id=$1`, r.service); err != nil {
		t.Fatal(err)
	}
	renewal, err := db.CreateRenewalInvoice(t.Context(), customer, r.service, "manual-db-renewal-"+suffix)
	if err != nil {
		t.Fatal(err)
	}
	f := manualCollectionDatabaseFixture{db: db, actor: actor, customer: customer, service: r.service, invoice: renewal.InvoiceID, amount: renewal.AmountCents, transaction: "manual-db-transaction-" + suffix}
	if err = db.Pool.QueryRow(t.Context(), "SELECT clock_timestamp()").Scan(&f.received); err != nil {
		t.Fatal(err)
	}
	return f
}

// Write through SQL so the tests establish the database's own pairing and
// immutability guarantees without relying on the public command's validation.
// The same settlement function still produces the genuine renewal effect.
func writeManualCollectionDatabasePair(ctx context.Context, tx pgx.Tx, f manualCollectionDatabaseFixture, scenario string) (collection, payment int64, err error) {
	method, account := "bank_transfer", "verified-test-account"
	if scenario == "account_alias" {
		account = "another-name-for-the-receiving-account"
	}
	reference := manualCollectionReference(method, account, f.transaction)
	if scenario == "wrong_canonical_reference" {
		reference = manualCollectionReference(method, account, "unrelated-original-transaction")
	}
	provider, credential, checkout := ManualCollectionProvider, []byte{}, ""
	if scenario == "nonmanual_payment" {
		provider = "mock"
	}
	if scenario == "gateway_credentials" {
		credential = []byte("a-manual-collection-has-no-gateway-secret")
	}
	if scenario == "checkout_url" {
		checkout = "https://gateway.example.test/unrelated"
	}
	completed, received := f.received, f.received
	if scenario == "wrong_payment_time" {
		completed = completed.Add(-time.Hour)
	}
	if scenario == "before_invoice_creation" {
		completed, received = f.received.Add(-time.Hour), f.received.Add(-time.Hour)
	}
	if scenario == "future_receipt" {
		completed, received = f.received.Add(time.Hour), f.received.Add(time.Hour)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,external_transaction_id,invoice_id,user_id,amount_cents,status,completed_at,credential_snapshot,checkout_url,next_reconcile_at,raw_result)
 VALUES($1,$2,$2,$3,$4,$5,'succeeded',$6,$7,$8,NULL,'{"source":"administrator_offline_collection"}') RETURNING id`, provider, reference, f.invoice, f.customer, f.amount, completed, credential, checkout).Scan(&payment); err != nil {
		return
	}
	if err = settleExternalInvoice(ctx, tx, f.invoice); err != nil {
		return
	}
	if _, err = tx.Exec(ctx, "UPDATE invoices SET paid_at=$2 WHERE id=$1", f.invoice, received); err != nil {
		return
	}
	if scenario == "missing_collection" {
		return
	}
	actor, amount := f.actor, f.amount
	if scenario == "customer_actor" {
		actor = f.customer
	}
	if scenario == "wrong_collection_amount" {
		amount++
	}
	serviceSnapshot := "external_refund_order_snapshot(s)"
	if scenario == "wrong_service_snapshot" {
		serviceSnapshot += "||'{\"unrelated\":true}'::jsonb"
	}
	if err = tx.QueryRow(ctx, `INSERT INTO manual_collections(payment_id,invoice_id,service_id,user_id,actor_id,amount_cents,received_at,method,receiving_account,transaction_no,
 evidence_ref,evidence_sha256,reason,idempotency_key,request_hash,invoice_version,service_version,invoice_snapshot,service_snapshot)
 SELECT $1,i.id,s.id,i.user_id,$4,$5,$6,$7,$8,$9,'isolated/manual-db-evidence',repeat('a',64),'verified isolated receipt',$10,repeat('b',64),
 i.version,s.version,external_refund_order_snapshot(i),`+serviceSnapshot+`
 FROM invoices i CROSS JOIN services s WHERE i.id=$2 AND s.id=$3 RETURNING id`, payment, f.invoice, f.service, actor, amount, received, method, account, f.transaction, "manual-db-key-"+f.transaction).Scan(&collection); err != nil {
		return
	}
	if scenario != "missing_audit" {
		if err = Audit(ctx, tx, actor, "invoice.manual_collection.recorded", "invoice", f.invoice, map[string]any{"collection_id": collection, "payment_id": payment, "service_id": f.service, "customer_id": f.customer, "amount_cents": amount, "received_at": received, "method": method, "reason": "verified isolated receipt", "evidence_ref": "isolated/manual-db-evidence", "evidence_sha256": strings.Repeat("a", 64), "request_hash": strings.Repeat("b", 64)}); err != nil {
			return
		}
	}
	if scenario != "missing_service_audit" {
		if err = Audit(ctx, tx, actor, "service.renewal.manual_collected", "service", f.service, map[string]any{"collection_id": collection, "payment_id": payment, "invoice_id": f.invoice, "customer_id": f.customer, "amount_cents": amount, "received_at": received, "method": method, "reason": "verified isolated receipt", "request_hash": strings.Repeat("b", 64)}); err != nil {
			return
		}
	}
	if scenario != "missing_receipt" {
		out := AdminServiceManualCollectionResult{ServiceID: f.service, CustomerID: f.customer, InvoiceID: f.invoice, PaymentID: payment, CollectionID: collection, AmountCents: amount, ReceivedAt: received, Status: "paid", RenewalStatus: "completed"}
		if err = tx.QueryRow(ctx, "SELECT i.invoice_no,i.version,s.version FROM invoices i CROSS JOIN services s WHERE i.id=$1 AND s.id=$2", f.invoice, f.service).Scan(&out.InvoiceNo, &out.InvoiceVersion, &out.ServiceVersion); err != nil {
			return
		}
		err = SaveOperation(ctx, tx, actor, adminManualCollectionScope, "manual-db-key-"+f.transaction, strings.Repeat("b", 64), out)
	}
	return
}

func manualCollectionDatabaseFacts(t *testing.T, db *Store) string {
	t.Helper()
	var collections string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_array(
 (SELECT coalesce(jsonb_agg(to_jsonb(c) ORDER BY id),'[]'::jsonb) FROM manual_collections c),
 (SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM payment_close_anomalies a))::text`).Scan(&collections); err != nil {
		t.Fatal(err)
	}
	return manualCollectionHistoricalMoney(t, db) + collections
}

func manualCollectionDatabaseRejected(t *testing.T, db *Store, before string, write func(pgx.Tx) error) {
	t.Helper()
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = write(tx); err == nil {
		err = tx.Commit(t.Context())
	}
	if err == nil {
		t.Fatal("database accepted an invalid or mutable manual collection fact")
	}
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || (pg.Code != "23514" && pg.Code != "23505") {
		t.Fatalf("expected a business integrity rejection, got %v", err)
	}
	_ = tx.Rollback(t.Context())
	if manualCollectionDatabaseFacts(t, db) != before {
		t.Fatal("rejected manual collection transaction changed historical money or evidence")
	}
}

func TestManualCollectionDatabasePairingAndImmutability(t *testing.T) {
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	for _, scenario := range []string{"missing_collection", "nonmanual_payment", "wrong_collection_amount", "missing_audit", "missing_service_audit", "missing_receipt", "wrong_service_snapshot", "wrong_canonical_reference", "wrong_payment_time", "before_invoice_creation", "future_receipt", "customer_actor", "gateway_credentials", "checkout_url"} {
		t.Run(scenario, func(t *testing.T) {
			f := manualCollectionDatabaseSource(t, db, actor, customer, scenario)
			before := manualCollectionDatabaseFacts(t, db)
			manualCollectionDatabaseRejected(t, db, before, func(tx pgx.Tx) error {
				_, _, err := writeManualCollectionDatabasePair(t.Context(), tx, f, scenario)
				return err
			})
		})
	}
	f := manualCollectionDatabaseSource(t, db, actor, customer, "positive")
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	collection, payment, err := writeManualCollectionDatabasePair(t.Context(), tx, f, "")
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal("valid SQL collection pair was rejected", err)
	}
	var paired bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM manual_collections c JOIN payment_transactions p ON p.id=c.payment_id
 JOIN invoices i ON i.id=c.invoice_id JOIN service_renewal_effects e ON e.invoice_id=i.id
 WHERE c.id=$1 AND p.id=$2 AND c.service_id=$3 AND i.status='paid' AND p.status='succeeded' AND p.provider='manual_offline')`, collection, payment, f.service).Scan(&paired); err != nil || !paired {
		t.Fatal("valid collection lacked its atomically linked payment and entitlement", paired, err)
	}
	for _, mutation := range []struct {
		name, sql string
		id        int64
	}{
		{"collection_update", "UPDATE manual_collections SET reason='silently rewrite original evidence' WHERE id=$1", collection},
		{"collection_delete", "DELETE FROM manual_collections WHERE id=$1", collection},
		{"payment_update", "UPDATE payment_transactions SET amount_cents=amount_cents+1 WHERE id=$1", payment},
		{"payment_source_rewrite", "UPDATE payment_transactions SET provider='mock' WHERE id=$1", payment},
		{"payment_delete", "DELETE FROM payment_transactions WHERE id=$1", payment},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			before := manualCollectionDatabaseFacts(t, db)
			manualCollectionDatabaseRejected(t, db, before, func(tx pgx.Tx) error {
				_, err := tx.Exec(t.Context(), mutation.sql, mutation.id)
				return err
			})
		})
	}
}

func insertManualCollectionCollisionGateway(ctx context.Context, tx pgx.Tx, customer int64, transaction, suffix string) error {
	var invoice int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at)
 VALUES($1,$2,'manual','paid',2000,clock_timestamp()) RETURNING id`, "manual-collision-gateway-"+suffix, customer).Scan(&invoice); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,external_transaction_id,completed_at)
 VALUES('epay_alipay',$1,$2,$3,2000,'succeeded',$4,clock_timestamp())`, "manual-collision-gateway-"+suffix, invoice, customer, transaction)
	return err
}

func insertManualCollectionCollisionCredit(ctx context.Context, tx pgx.Tx, actor, customer int64, transaction, suffix string) error {
	var invoice int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents,paid_at)
 VALUES($1,$2,'manual','paid',2000,clock_timestamp()) RETURNING id`, "manual-collision-credit-"+suffix, customer).Scan(&invoice); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,status,expected_version,idempotency_key)
 VALUES($1,$2,2000,0,'repaid',1,$3)`, invoice, customer, "manual-collision-credit-"+suffix); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,note,idempotency_key,expected_version,version_after,remaining_after)
 VALUES($1,$2,$3,2000,$4,clock_timestamp(),'historical credit receipt fixture',$5,1,2,0)`, invoice, customer, actor, transaction, "manual-collision-credit-"+suffix)
	return err
}

// Synthetic source observation for testing the database boundary directly.
// HTTP tests separately exercise trusted close and late callback processing.
func insertManualCollectionCollisionObservation(ctx context.Context, tx pgx.Tx, customer int64, transaction, suffix string) error {
	var invoice, payment int64
	if err := tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents)
 VALUES($1,$2,'manual','unpaid',2000) RETURNING id`, "manual-observation-"+suffix, customer).Scan(&invoice); err != nil {
		return err
	}
	if err := tx.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status)
 VALUES('epay_alipay',$1,$2,$3,2000,'pending') RETURNING id`, "manual-observation-"+suffix, invoice, customer).Scan(&payment); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO payment_close_anomalies(payment_id,invoice_id,user_id,provider,event_id,amount_cents,external_transaction_id,payload_sha256)
 VALUES($1,$2,$3,'epay_alipay',$4,2000,$5,repeat('a',64))`, payment, invoice, customer, "manual-observation-"+suffix, transaction)
	return err
}

func TestManualCollectionDatabaseCrossSourceTransactionIdentity(t *testing.T) {
	db := isolatedTestStore(t)
	actor, customer := topUpFixture(t, db)
	for _, source := range []string{"gateway", "credit", "late_observation"} {
		t.Run("existing_"+source+"_blocks_manual", func(t *testing.T) {
			f := manualCollectionDatabaseSource(t, db, actor, customer, "existing-"+source)
			if err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
				if source == "gateway" {
					return insertManualCollectionCollisionGateway(t.Context(), tx, customer, f.transaction, "existing-"+source)
				}
				if source == "late_observation" {
					return insertManualCollectionCollisionObservation(t.Context(), tx, customer, f.transaction, "existing-"+source)
				}
				return insertManualCollectionCollisionCredit(t.Context(), tx, actor, customer, f.transaction, "existing-"+source)
			}); err != nil {
				t.Fatal(err)
			}
			before := manualCollectionDatabaseFacts(t, db)
			manualCollectionDatabaseRejected(t, db, before, func(tx pgx.Tx) error {
				_, _, err := writeManualCollectionDatabasePair(t.Context(), tx, f, "")
				return err
			})
		})
	}
	f := manualCollectionDatabaseSource(t, db, actor, customer, "cross-source-original")
	if err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
		_, _, err := writeManualCollectionDatabasePair(t.Context(), tx, f, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	t.Run("manual_claim_does_not_discard_later_observation", func(t *testing.T) {
		if err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
			return insertManualCollectionCollisionObservation(t.Context(), tx, customer, f.transaction, "after-manual")
		}); err != nil {
			t.Fatal("manual claim discarded later external evidence", err)
		}
		var observations int
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM payment_close_anomalies WHERE external_transaction_id=$1", f.transaction).Scan(&observations); err != nil || observations != 1 {
			t.Fatal("late observation missing", observations, err)
		}
	})
	for index, source := range []string{"gateway", "credit", "manual_account_alias"} {
		t.Run("existing_manual_blocks_"+source, func(t *testing.T) {
			var alias manualCollectionDatabaseFixture
			if source == "manual_account_alias" {
				alias = manualCollectionDatabaseSource(t, db, actor, customer, fmt.Sprintf("alias-%d", index))
				alias.transaction = f.transaction
			}
			before := manualCollectionDatabaseFacts(t, db)
			manualCollectionDatabaseRejected(t, db, before, func(tx pgx.Tx) error {
				switch source {
				case "gateway":
					return insertManualCollectionCollisionGateway(t.Context(), tx, customer, f.transaction, "after-manual")
				case "credit":
					return insertManualCollectionCollisionCredit(t.Context(), tx, actor, customer, f.transaction, "after-manual")
				default:
					_, _, err := writeManualCollectionDatabasePair(t.Context(), tx, alias, "account_alias")
					return err
				}
			})
		})
	}
	for isolationIndex, isolation := range []pgx.TxIsoLevel{pgx.RepeatableRead, pgx.Serializable} {
		for _, source := range []string{"manual_payment", "gateway", "credit", "manual_collection", "late_observation"} {
			t.Run(string(isolation)+"_rejects_"+source, func(t *testing.T) {
				// All supporting invoices originate outside the snapshot-isolated
				// transaction. Schema187's invoice-birth restriction must not be
				// mistaken for this source-identity isolation guarantee.
				suffix := fmt.Sprintf("isolation-%d-%s", isolationIndex, source)
				candidate := manualCollectionDatabaseSource(t, db, actor, customer, suffix)
				if source == "credit" {
					if _, err := db.Pool.Exec(t.Context(), `INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,status,expected_version,idempotency_key)
 VALUES($1,$2,2000,2000,'outstanding',1,$3)`, candidate.invoice, customer, "manual-credit-"+suffix); err != nil {
						t.Fatal(err)
					}
				}
				var observationPayment int64
				if source == "late_observation" {
					if err := db.Pool.QueryRow(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status)
 VALUES('epay_alipay',$1,$2,$3,2000,'pending') RETURNING id`, "manual-observation-"+suffix, candidate.invoice, customer).Scan(&observationPayment); err != nil {
						t.Fatal(err)
					}
				}
				before := manualCollectionDatabaseFacts(t, db)
				tx, err := db.Pool.BeginTx(t.Context(), pgx.TxOptions{IsoLevel: isolation})
				if err != nil {
					t.Fatal(err)
				}
				defer tx.Rollback(t.Context())
				var fixedSnapshot int64
				if err = tx.QueryRow(t.Context(), "SELECT count(*) FROM manual_collections").Scan(&fixedSnapshot); err != nil {
					t.Fatal(err)
				}
				switch source {
				case "late_observation":
					_, err = tx.Exec(t.Context(), `INSERT INTO payment_close_anomalies(payment_id,invoice_id,user_id,provider,event_id,amount_cents,external_transaction_id,payload_sha256)
 VALUES($1,$2,$3,'epay_alipay',$4,2000,$5,repeat('a',64))`, observationPayment, candidate.invoice, customer, "manual-observation-"+suffix, candidate.transaction)
				case "manual_payment":
					_, _, err = writeManualCollectionDatabasePair(t.Context(), tx, candidate, "")
				case "gateway":
					_, err = tx.Exec(t.Context(), `INSERT INTO payment_transactions(provider,provider_payment_id,invoice_id,user_id,amount_cents,status,external_transaction_id)
 VALUES('epay_alipay',$1,$2,$3,2000,'pending',$4)`, "manual-gateway-"+suffix, candidate.invoice, customer, candidate.transaction)
				case "credit":
					_, err = tx.Exec(t.Context(), `INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,note,idempotency_key,expected_version,version_after,remaining_after)
 VALUES($1,$2,$3,2000,$4,clock_timestamp(),'isolation rejection fixture',$5,1,2,0)`, candidate.invoice, customer, actor, candidate.transaction, "manual-credit-"+suffix)
				case "manual_collection":
					// The BEFORE source trigger must explicitly reject the stale
					// isolation before any duplicate-pair unique constraint runs.
					_, err = tx.Exec(t.Context(), `INSERT INTO manual_collections(payment_id,invoice_id,service_id,user_id,actor_id,amount_cents,received_at,method,receiving_account,transaction_no,
 evidence_ref,evidence_sha256,reason,idempotency_key,request_hash,invoice_version,service_version,invoice_snapshot,service_snapshot)
 SELECT payment_id,invoice_id,service_id,user_id,actor_id,amount_cents,received_at,method,receiving_account,transaction_no,
 evidence_ref,evidence_sha256,reason,idempotency_key,request_hash,invoice_version,service_version,invoice_snapshot,service_snapshot
 FROM manual_collections WHERE invoice_id=$1`, f.invoice)
				}
				var pg *pgconn.PgError
				if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "manual receipt sources require read committed isolation" {
					t.Fatalf("source writer did not explicitly reject snapshot isolation: %v", err)
				}
				if err = tx.Rollback(t.Context()); err != nil {
					t.Fatal(err)
				}
				if manualCollectionDatabaseFacts(t, db) != before {
					t.Fatal("snapshot-isolated source writer changed money or evidence")
				}
			})
		}
	}
}
