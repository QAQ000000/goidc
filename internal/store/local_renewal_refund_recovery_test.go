package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// The paid renewal below is a synthetic schema-154 fixture, not a historical
// HTTP payment or a production snapshot. No schema-155 object has existed when
// it is written; constructing today's schema then dropping a column would not
// prove the no-backfill boundary.
func TestLocalRenewalUpgradeFrom154DoesNotInventEffects(t *testing.T) {
	db := isolatedOrderRefundSchema153(t)
	ctx := t.Context()
	actor, customer := topUpFixture(t, db)
	var product int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,setup_fee_cents,stock,billing_cycle,approval_required) VALUES('Historical renewal','historical-renewal',1000,200,12,'monthly',true) RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	checkout := createSyntheticSchema153Checkout(t, db, actor, customer, product)
	name := "154_order_external_refunds.sql"
	content, err := migrations.Files.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, string(content)); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(content)
	if _, err = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, name, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var boundary string
	var invented bool
	if err = db.Pool.QueryRow(ctx, `SELECT max(version),to_regclass('service_renewal_effects') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &invented); err != nil || boundary != name || invented {
		t.Fatal("fixture was not born under schema 154", boundary, invented, err)
	}
	var service, invoice int64
	if err = db.Pool.QueryRow(ctx, `SELECT id FROM services WHERE order_id=$1 ORDER BY id LIMIT 1`, checkout.OrderID).Scan(&service); err != nil {
		t.Fatal(err)
	}
	// Preserve the old arithmetic's actual final facts; never reconstruct a
	// before_due from this value during upgrade.
	if _, err = db.Pool.Exec(ctx, `UPDATE services SET status='active',next_due_at='2028-02-29T12:00:00Z',recurring_price_cents=1000,renewal_discounts_used=1 WHERE id=$1`, service); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,amount_cents,status,paid_at) VALUES('SYNTHETIC-154-RENEWAL',$1,1000,'paid',now()) RETURNING id`, customer).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,service_id,item_type,description,amount_cents) VALUES($1,$2,'renew','synthetic schema154 paid renewal',1000)`, invoice, service); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('renewal-upgrade-request',$1,now()+interval '1 hour')`, actor); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var v string
		if e := db.Pool.QueryRow(ctx, `SELECT jsonb_build_object('service',(SELECT to_jsonb(s)-'renewal_generation' FROM services s WHERE id=$1),'invoice',(SELECT to_jsonb(i) FROM invoices i WHERE id=$2),'items',(SELECT jsonb_agg(to_jsonb(ii) ORDER BY id) FROM invoice_items ii WHERE invoice_id=$2),'customer',(SELECT to_jsonb(u) FROM users u WHERE id=$3))::text`, service, invoice, customer).Scan(&v); e != nil {
			t.Fatal(e)
		}
		return v
	}
	before := snapshot()
	for pass := 0; pass < 2; pass++ {
		if err = db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = db.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		if snapshot() != before {
			t.Fatal("upgrade rewrote historical business facts")
		}
	}
	var count int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM service_renewal_effects`).Scan(&count); err != nil || count != 0 {
		t.Fatal("upgrade invented historical renewal effects", count, err)
	}
	c, err := db.ExternalRefundRenewalContext(WithExternalRefundSession(ctx, "renewal-upgrade-request"), actor, invoice)
	if err != nil || c.Eligible || c.Effect != nil || c.ReasonCode != "renewal_effect_missing" {
		t.Fatal("historical paid renewal must remain unproven", c, err)
	}
	if _, err = db.RefundInvoice(ctx, actor, invoice, "historical-renewal-refund"); conflictCode(t, err) != "renewal_effect_missing" {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE invoices SET status='refunded' WHERE id=$1`, invoice); err == nil {
		t.Fatal("direct SQL guessed a historical reversal")
	}
	if snapshot() != before {
		t.Fatal("refused historical reversal changed business facts")
	}
	// New, genuinely settled renewals receive provenance after migration, while
	// the old paid invoice continues to have no adopted effect.
	r, err := db.CreateRenewalInvoice(ctx, customer, service, "post155-renewal-create")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `UPDATE users SET balance_cents=10000 WHERE id=$1`, customer); err != nil {
		t.Fatal(err)
	}
	if _, err = db.PayInvoice(ctx, customer, r.InvoiceID, "post155-renewal-pay"); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM service_renewal_effects WHERE invoice_id=$1 AND service_id=$2 AND before_due='2028-02-29T12:00:00Z' AND after_due='2028-03-29T12:00:00Z' AND before_used=1 AND after_used=1`, r.InvoiceID, service).Scan(&count); err != nil || count != 1 {
		t.Fatal("new settlement did not capture its actual entitlement", count, err)
	}
}

func TestLocalRenewalRefundBackupRestore(t *testing.T) {
	if os.Getenv("ZJMFGO_BACKUP_RESTORE_ACCEPTANCE") != "1" {
		t.Skip("requires explicit backup/restore acceptance and two dedicated loopback PG18 _test databases")
	}
	sourceCfg := recoveryConfig(t, "ZJMFGO_TEST_DATABASE_URL")
	targetCfg := recoveryConfig(t, "ZJMFGO_RESTORE_TEST_DATABASE_URL")
	if sourceCfg.ConnConfig.Database == targetCfg.ConnConfig.Database {
		t.Fatal("source and restore databases must differ")
	}
	recoveryPreflight(t, sourceCfg)
	target := recoveryPreflight(t, targetCfg)
	recoveryRequireEmpty(t, target)
	f := newLocalRenewalFixture(t, true, 734)
	pending := f.renewalCreate(t)
	ctx := t.Context()
	var schema string
	if err := f.db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || !strings.HasPrefix(schema, "store_topup_") {
		t.Fatal("unexpected isolated schema", err)
	}
	before := recoverySnapshot(t, f.db.Pool, schema)
	archive := filepath.Join(t.TempDir(), "local-renewal-refund.dump")
	recoveryPGTool(t, sourceCfg, "pg_dump", "--format=custom", "--schema="+schema, "--no-owner", "--no-privileges", "--file="+archive)
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveHash := sha256.Sum256(archiveBytes)
	t.Cleanup(func() {
		if _, e := target.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Errorf("restored renewal schema cleanup: %v", e)
			return
		}
		var exists bool
		if e := target.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); e != nil || exists {
			t.Errorf("restored renewal schema cleanup verification: %v", e)
		}
	})
	recoveryPGTool(t, targetCfg, "pg_restore", "--exit-on-error", "--single-transaction", "--no-owner", "--no-privileges", archive)
	cfg := targetCfg.Copy()
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	f.db = &Store{Pool: pool}
	recoveryEqual(t, "renewal restored snapshot", before, recoverySnapshot(t, pool, schema))
	for n := 0; n < 2; n++ {
		if err = f.db.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = f.db.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		recoveryEqual(t, "renewal repeated migration unchanged", before, recoverySnapshot(t, pool, schema))
	}
	c := f.renewalContext(t)
	if c.Eligible || c.ReasonCode != "renewal_refund_hold" || c.RenewalReversal == nil || c.RenewalReversal.Status != "pending" {
		t.Fatal("restored pending reservation lost", c)
	}
	for _, q := range []string{
		`UPDATE services SET next_due_at=next_due_at+interval '1 month' WHERE id=$1`,
		`INSERT INTO provision_jobs(service_id,action,idempotency_key) VALUES($1,'suspend','restored-renewal-bypass')`,
	} {
		if _, err = pool.Exec(ctx, q, f.service); err == nil {
			t.Fatal("restored freeze bypass", q)
		}
	}
	if _, err = f.db.ReviewExternalRefund(WithExternalRefundSession(ctx, "order-refund-request"), f.actor, f.renewInvoice, pending.Refund.ID, true, ExternalRefundReviewInput{ExpectedVersion: pending.Refund.Version, Note: "recovery self review refused"}, "recovery-renewal-self"); conflictCode(t, err) != "external_refund_self_review" {
		t.Fatal(err)
	}
	afterRejected := recoverySnapshot(t, pool, schema)
	recoveryEqual(t, "restored refusal leaves rows untouched", before.Tables, afterRejected.Tables)
	recoveryEqual(t, "restored refusal leaves schema untouched", before.Catalog, afterRejected.Catalog)
	done, err := f.renewalReview(t, pending, true)
	if err != nil || done.Refund.Status != "confirmed" || done.Refund.RenewalReversal == nil || done.Refund.RenewalReversal.Status != "applied" {
		t.Fatal("restored independent review failed", done, err)
	}
	f.exact(t, f.before, 0, 1)
	after := recoverySnapshot(t, pool, schema)
	replay, err := f.renewalReview(t, pending, true)
	if err != nil || !replay.Replayed {
		t.Fatal("restored original review replay failed", replay, err)
	}
	recoveryEqual(t, "restored original review replay writes nothing", after, recoverySnapshot(t, pool, schema))
	if _, err = f.db.ProcessPaymentWebhook(ctx, "mock", "local-renew-event", "local-renew-original", f.renewInvoice, 734, "succeeded", []byte(`{"verified":"fixture"}`), "local-renew-tx"); err != nil {
		t.Fatal(err)
	}
	f.exact(t, f.before, 0, 1)
	t.Logf("loopback PG18 synthetic local renewal restore: archive_sha256=%x archive_bytes=%d source_db=%s restore_db=%s; pending hold, exact reversal, independent review and original replay PASS; not production recovery/RTO evidence", archiveHash, len(archiveBytes), sourceCfg.ConnConfig.Database, targetCfg.ConnConfig.Database)
}
