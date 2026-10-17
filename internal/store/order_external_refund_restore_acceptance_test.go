package store

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This is a same-schema synthetic recovery check, not an old-schema upgrade or
// production cutover proof. Both DSNs must pass the existing loopback PG18 gates.
func TestOrderExternalRefundBackupRestore(t *testing.T) {
	if os.Getenv("ZJMFGO_BACKUP_RESTORE_ACCEPTANCE") != "1" {
		t.Skip("requires explicit backup/restore acceptance and dedicated loopback PG18 _test databases")
	}
	sourceCfg := recoveryConfig(t, "ZJMFGO_TEST_DATABASE_URL")
	targetCfg := recoveryConfig(t, "ZJMFGO_RESTORE_TEST_DATABASE_URL")
	if sourceCfg.ConnConfig.Database == targetCfg.ConnConfig.Database {
		t.Fatal("source and restore database names must differ")
	}
	sourceBase := recoveryPreflight(t, sourceCfg)
	targetBase := recoveryPreflight(t, targetCfg)
	recoveryRequireEmpty(t, targetBase)
	f := newOrderRefundFixture(t, false)
	pending := f.create(t)
	ctx := t.Context()
	var schema string
	if err := f.db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil || !strings.HasPrefix(schema, "store_topup_") {
		t.Fatal("unexpected isolated fixture schema")
	}
	before := recoverySnapshot(t, f.db.Pool, schema)
	archive := filepath.Join(t.TempDir(), "order-refund.dump")
	recoveryPGTool(t, sourceCfg, "pg_dump", "--format=custom", "--schema="+schema, "--no-owner", "--no-privileges", "--file="+archive)
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveHash := sha256.Sum256(archiveBytes)
	t.Cleanup(func() {
		if _, e := targetBase.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			t.Errorf("order refund restored schema cleanup failed: %v", e)
			return
		}
		var exists bool
		if e := targetBase.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); e != nil || exists {
			t.Errorf("order refund restored schema cleanup verification failed: %v", e)
		}
	})
	recoveryPGTool(t, targetCfg, "pg_restore", "--exit-on-error", "--single-transaction", "--no-owner", "--no-privileges", archive)
	restoredCfg := targetCfg.Copy()
	restoredCfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, restoredCfg)
	if err != nil {
		t.Fatal("restored pool creation failed")
	}
	t.Cleanup(pool.Close)
	restored := &Store{Pool: pool}
	recoveryEqual(t, "order refund restored snapshot", before, recoverySnapshot(t, pool, schema))
	for n := 0; n < 2; n++ {
		if err = restored.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = restored.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		recoveryEqual(t, "order refund repeat migration unchanged", before, recoverySnapshot(t, pool, schema))
	}
	if _, err = restored.ReviewOrder(ctx, f.actor, f.order, "approved", "must remain frozen after recovery", "restored-order-approval", f.in.ExpectedOrderVersion); conflictCode(t, err) != "external_refund_order_hold" {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO provision_jobs(service_id,action,idempotency_key) SELECT id,'create','restore-forbidden-'||id FROM services WHERE order_id=$1`, f.order); err == nil {
		t.Fatal("restored hold allowed direct delivery task creation")
	}
	requestCtx := WithExternalRefundSession(ctx, "order-refund-request")
	review := ExternalRefundReviewInput{ExpectedVersion: pending.Refund.Version, Note: "independent recovery verification"}
	if _, err = restored.ReviewExternalRefund(requestCtx, f.actor, f.invoice, pending.Refund.ID, true, review, "restored-order-self-review"); conflictCode(t, err) != "external_refund_self_review" {
		t.Fatal(err)
	}
	// PostgreSQL sequences can advance on a rejected INSERT. Check transactional
	// business state and schema here; the later successful-request replays must
	// preserve even the sequence values because they perform no new inserts.
	afterRejected := recoverySnapshot(t, pool, schema)
	recoveryEqual(t, "failed operations leave restored order rows untouched", before.Tables, afterRejected.Tables)
	recoveryEqual(t, "failed operations leave restored schema untouched", before.Catalog, afterRejected.Catalog)
	confirmCtx := WithExternalRefundSession(ctx, "order-refund-review")
	applied, err := restored.ReviewExternalRefund(confirmCtx, f.reviewer, f.invoice, pending.Refund.ID, true, review, "restored-order-confirm")
	if err != nil || applied.Refund.Status != "confirmed" || applied.Refund.OrderReversal == nil || applied.Refund.OrderReversal.Status != "applied" || applied.Refund.OrderReversal.StockUnitsReleased != 2 {
		t.Fatalf("restored independent confirmation failed: %v", err)
	}
	var stock, cancelled, jobs, releases, outflows int
	var orderStatus, invoiceStatus, reviewStatus string
	err = pool.QueryRow(ctx, `SELECT p.stock,o.status,o.review_status,i.status,
 (SELECT count(*) FROM services WHERE order_id=o.id AND status='cancelled' AND inventory_released),
 (SELECT count(*) FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE s.order_id=o.id),
 (SELECT coalesce(sum(units),0)::int FROM external_refund_order_stock_releases WHERE refund_id=$3),
 (SELECT count(*) FROM external_refunds WHERE invoice_id=i.id AND status='confirmed')
 FROM products p CROSS JOIN orders o JOIN invoices i ON i.order_id=o.id WHERE p.id=$1 AND o.id=$2`, f.product, f.order, pending.Refund.ID).Scan(&stock, &orderStatus, &reviewStatus, &invoiceStatus, &cancelled, &jobs, &releases, &outflows)
	if err != nil || stock != 12 || orderStatus != "refunded" || reviewStatus != "pending" || invoiceStatus != "refunded" || cancelled != 2 || jobs != 0 || releases != 2 || outflows != 1 {
		t.Fatalf("restored order/inventory/outflow invariant failed: %v", err)
	}
	after := recoverySnapshot(t, pool, schema)
	for _, table := range []string{"payment_transactions", "payment_webhook_events", "users", "balance_ledger", "credit_invoice_payments", "order_items", "external_refund_order_stock_reservations", "external_refund_order_births", "external_refund_service_births"} {
		recoveryEqual(t, "preserved original order fact "+table, before.Tables[table], after.Tables[table])
	}
	var originalPaidAt, restoredPaidAt time.Time
	if err = f.db.Pool.QueryRow(ctx, `SELECT paid_at FROM invoices WHERE id=$1`, f.invoice).Scan(&originalPaidAt); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, `SELECT paid_at FROM invoices WHERE id=$1`, f.invoice).Scan(&restoredPaidAt); err != nil || !originalPaidAt.Equal(restoredPaidAt) {
		t.Fatal("original paid_at changed after recovery confirmation")
	}
	confirmedReplay, err := restored.ReviewExternalRefund(confirmCtx, f.reviewer, f.invoice, pending.Refund.ID, true, review, "restored-order-confirm")
	if err != nil || !confirmedReplay.Replayed || !recoveryReceiptEqual(applied.Refund, confirmedReplay.Refund) {
		t.Fatalf("restored confirmation replay failed: %v", err)
	}
	createReplay, err := restored.CreateExternalRefund(requestCtx, f.actor, f.invoice, f.in, "order-refund-create")
	if err != nil || !createReplay.Replayed || !recoveryReceiptEqual(pending.Refund, createReplay.Refund) {
		t.Fatalf("restored original create replay failed: %v", err)
	}
	recoveryEqual(t, "restored original requests do not repeat writes", after, recoverySnapshot(t, pool, schema))
	recoveryEqual(t, "order source untouched by restored continuation", before, recoverySnapshot(t, f.db.Pool, schema))
	f.db.Close()
	if _, err = sourceBase.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = sourceBase.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil || exists {
		t.Fatal("order source cleanup verification failed")
	}
	t.Logf("PASS same-schema order refund recovery: archive_sha256=%x; full snapshot restored; hold persisted; independent confirmation; original receipts/balance preserved; 2 units released once; zero delivery jobs; both original request replays unchanged", archiveHash)
}
