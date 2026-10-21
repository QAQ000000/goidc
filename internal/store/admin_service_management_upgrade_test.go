package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

const adminServiceManagementPredecessor = "187_customer_referrer_invoice_snapshots.sql"

// Build the actual schema187 from embedded migrations, without constructing the
// current schema and deleting its new objects. No schema188/189 object has ever
// existed when the historical payment effect and snapshots below are created.
func adminServiceManagementSchema187(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("ZJMFGO_TEST_DATABASE_URL required")
		}
		t.Skip("requires isolated PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("refusing database without _test suffix")
	}
	base, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		base.Close()
		t.Fatal(err)
	}
	schema := "admin_service_upgrade_" + hex.EncodeToString(random)
	if _, err = base.Exec(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	scoped.ConnConfig.RuntimeParams["timezone"] = "UTC"
	ConfigurePoolCorrelation(scoped)
	pool, err := pgxpool.NewWithConfig(t.Context(), scoped)
	if err != nil {
		_, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		base.Close()
		t.Fatal(err)
	}
	db := &Store{Pool: pool}
	t.Cleanup(func() {
		db.Close()
		_, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		base.Close()
	})
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	tx, err := pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	var last string
	for _, name := range names {
		if name > adminServiceManagementPredecessor {
			break
		}
		content, readErr := migrations.Files.ReadFile(name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if _, err = tx.Exec(t.Context(), string(content)); err != nil {
			t.Fatalf("historical schema187 migration %s: %v", name, err)
		}
		sum := sha256.Sum256(content)
		if _, err = tx.Exec(t.Context(), "INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)", name, hex.EncodeToString(sum[:])); err != nil {
			t.Fatal(err)
		}
		last = name
	}
	if last != adminServiceManagementPredecessor {
		t.Fatal("schema187 boundary missing", last)
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	var boundary string
	var premature bool
	if err = pool.QueryRow(t.Context(), `SELECT max(version),
 to_regclass('service_renewal_pricing') IS NOT NULL OR to_regclass('service_term_management') IS NOT NULL
 OR to_regclass('service_managed_renewal_effects') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &premature); err != nil || premature || boundary != adminServiceManagementPredecessor {
		t.Fatal("fixture must originate under genuine untouched schema187", boundary, premature, err)
	}
	return db
}

func adminServiceManagementUpgradeMoney(t *testing.T, db *Store) string {
	t.Helper()
	var raw string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'customers',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'order_items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'invoice_items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoice_items x),
 'wallet',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
 'payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_transactions x),
 'effects',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM service_renewal_effects x),
 'reversals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY effect_id) FROM service_renewal_reversals x),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x),
 'invoice_referrals',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM invoice_affiliate_attributions x),
 'old_migrations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY version) FROM schema_migrations x WHERE version<=$1)
 )::text`, adminServiceManagementPredecessor).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

// Reproduce schema187's persisted balance-payment writes without calling the
// current notification writer, which requires later ownership schema objects.
// The paid transition still runs the genuine 155 settlement and its deferred
// payment-proof checks; neither the service nor its renewal effect is fabricated.
func adminServiceManagementHistoricalPayment187(t *testing.T, db *Store, customer, invoice int64) {
	t.Helper()
	ctx := t.Context()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var balance, amount, version int64
	var status string
	var automaticRenewal bool
	if err = tx.QueryRow(ctx, `SELECT balance_cents FROM users WHERE id=$1 FOR NO KEY UPDATE`, customer).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `SELECT amount_cents,status,version,invoice_kind='automatic' AND order_id IS NULL
 FROM invoices WHERE id=$1 AND user_id=$2 FOR UPDATE`, invoice, customer).Scan(&amount, &status, &version, &automaticRenewal); err != nil {
		t.Fatal(err)
	}
	if amount != 1900 || version != 4 || status != "unpaid" || !automaticRenewal || balance < amount {
		t.Fatal("unexpected historical balance-payment input", amount, version, status, automaticRenewal, balance)
	}
	balance -= amount
	if _, err = tx.Exec(ctx, `UPDATE users SET balance_cents=$2,updated_at=now() WHERE id=$1`, customer, balance); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1`, invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after,reference_type,reference_id,idempotency_key,note)
 VALUES($1,'payment',$2,$3,'invoice',$4,'historical-renewal-payment-187','账单支付')`, customer, -amount, balance, invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'invoice.paid','invoice',$2::bigint::text,jsonb_build_object('amount_cents',$3::bigint,'balance_cents',$4::bigint))`, customer, invoice, amount, balance); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestAdminServiceManagementUpgrade187PreservesFinancialEvidence(t *testing.T) {
	db := adminServiceManagementSchema187(t)
	ctx := t.Context()
	_, customer := topUpFixture(t, db)
	var product, order, item, service, invoice int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,billing_cycle,provision_mode)
 VALUES('Historical manual service','admin-service-upgrade',2000,'monthly','manual') RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	// These are synthetic original schema187 rows. The genuine 155 settlement
	// trigger, rather than a hand-written effect, must create the renewal fact.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if err = tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
 VALUES('HISTORICAL-SERVICE-187',$1,'paid',2000,'historical-order-187',repeat('7',64)) RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
 VALUES($1,$2,'Historical manual service','monthly',1,2000,0,2000) RETURNING id`, order, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot)
 VALUES($1,2000,0,0,2000,'{"historical_schema":187}')`, item); err != nil {
		t.Fatal(err)
	}
	beforeDue := time.Date(2088, time.January, 31, 12, 0, 0, 123000, time.UTC)
	if err = tx.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,next_due_at,
 recurring_price_cents,renewal_fixed_discount_cents,renewal_discount_limit,renewal_discounts_used,renewal_promo_code,auto_renew,version)
 VALUES($1,$2,$3,$4,'Historical manual service','historical-service-187.test','monthly','active','manual',$5,2000,100,3,1,'HISTORICAL187',true,9) RETURNING id`, customer, order, item, product, beforeDue).Scan(&service); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents,version)
 VALUES('HISTORICAL-RENEWAL-187',$1,1900,4) RETURNING id`, customer).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO invoice_items(invoice_id,service_id,item_type,description,amount_cents,renewal_base_cents,renewal_discount_cents,
 renewal_fixed_discount_cents,renewal_discount_limit,renewal_discount_ordinal,renewal_promo_code)
 VALUES($1,$2,'renew','Historical renewal paid under schema187',1900,2000,100,100,3,2,'HISTORICAL187')`, invoice, service); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE users SET balance_cents=10000 WHERE id=$1`, customer); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Keep the historical writer bounded to schema187. Today's renewal and
	// notification methods read later pricing/ownership objects and must not be
	// invoked while constructing the predecessor's genuine financial history.
	adminServiceManagementHistoricalPayment187(t, db, customer, invoice)
	var oldSnapshot, oldConfig, oldReason string
	var oldDue time.Time
	var oldUsed, oldVersion, oldBalance int64
	var genuineEffect bool
	if err = db.Pool.QueryRow(ctx, `SELECT external_refund_order_snapshot(s)::text,local_renewal_configuration(s)::text,
 s.next_due_at,s.renewal_discounts_used,s.version,u.balance_cents,local_renewal_refund_reason($2),
 EXISTS(SELECT 1 FROM service_renewal_effects e WHERE e.invoice_id=$2 AND e.service_id=s.id AND e.before_used=1 AND e.after_used=2
  AND e.configuration=local_renewal_configuration(s) AND e.before_due=$3 AND e.after_due=s.next_due_at)
 FROM services s JOIN users u ON u.id=s.user_id WHERE s.id=$1`, service, invoice, beforeDue).
		Scan(&oldSnapshot, &oldConfig, &oldDue, &oldUsed, &oldVersion, &oldBalance, &oldReason, &genuineEffect); err != nil {
		t.Fatal(err)
	}
	if !genuineEffect || oldUsed != 2 || oldVersion != 10 || oldBalance != 8100 || oldReason != "" {
		t.Fatal("historical trigger did not produce the exact paid renewal fact", genuineEffect, oldUsed, oldVersion, oldBalance, oldReason, err)
	}
	oldMoney := adminServiceManagementUpgradeMoney(t, db)
	for pass := 0; pass < 2; pass++ {
		if err = db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		if got := adminServiceManagementUpgradeMoney(t, db); got != oldMoney {
			t.Fatal("upgrade changed historical customer, wallet, invoice, effect, receipt, referral or migration checksum")
		}
		var snapshot, config, reason string
		var due time.Time
		var used, version, newRows int64
		if err = db.Pool.QueryRow(ctx, `SELECT external_refund_order_snapshot(s)::text,local_renewal_configuration(s)::text,
 next_due_at,renewal_discounts_used,version,local_renewal_refund_reason($2),
 (SELECT count(*) FROM service_renewal_pricing)+(SELECT count(*) FROM service_term_management)+(SELECT count(*) FROM service_managed_renewal_effects)
 FROM services s WHERE id=$1`, service, invoice).Scan(&snapshot, &config, &due, &used, &version, &reason, &newRows); err != nil {
			t.Fatal(err)
		}
		if snapshot != oldSnapshot || config != oldConfig || !due.Equal(oldDue) || used != oldUsed || version != oldVersion || reason != oldReason || newRows != 0 {
			t.Fatal("upgrade rewrote old service/refund evidence or invented pricing, management intent or supplier observations", pass, due, used, version, reason, newRows)
		}
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO service_renewal_pricing(service_id,fixed_amount_cents) VALUES($1,1234)`, service); err != nil {
		t.Fatal(err)
	}
	var fixedCompatible bool
	if err = db.Pool.QueryRow(ctx, `SELECT local_renewal_configuration(s)-'_admin_renewal_fixed_cents'=$2::jsonb
 AND local_renewal_configuration(s)->>'_admin_renewal_fixed_cents'='1234'
 AND recurring_price_cents=2000 AND renewal_discounts_used=$3 AND next_due_at=$4
 FROM services s WHERE id=$1`, service, oldConfig, oldUsed, oldDue).Scan(&fixedCompatible); err != nil || !fixedCompatible {
		t.Fatal("fixed mode changed preserved contract configuration beyond its explicit internal key", fixedCompatible, err)
	}
	if _, err = db.Pool.Exec(ctx, `DELETE FROM service_renewal_pricing WHERE service_id=$1`, service); err != nil {
		t.Fatal(err)
	}
	var restored bool
	if err = db.Pool.QueryRow(ctx, `SELECT local_renewal_configuration(s)=$2::jsonb AND recurring_price_cents=2000
 AND renewal_discounts_used=$3 AND next_due_at=$4 AND local_renewal_refund_reason($5)=$6
 FROM services s WHERE id=$1`, service, oldConfig, oldUsed, oldDue, invoice, oldReason).Scan(&restored); err != nil || !restored {
		t.Fatal("restoring contract failed to recover the original configuration and refund eligibility", restored, err)
	}
	if got := adminServiceManagementUpgradeMoney(t, db); got != oldMoney {
		t.Fatal("fixed/contract mode edits rewrote historical finance or immutable renewal effects")
	}
}
