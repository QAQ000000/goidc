package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Build schema 153 from the actual embedded migrations. This deliberately does
// not call isolatedTestStore/Migrate first: no migration-154 object or trigger
// may ever have existed when the legacy checkout fixture was written.
func isolatedOrderRefundSchema153(t *testing.T) *Store {
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
	ctx := t.Context()
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		base.Close()
		t.Fatal(err)
	}
	schema := "order_refund_upgrade_" + hex.EncodeToString(raw)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
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
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	expectedNames := []string{}
	for _, name := range names {
		if name > "153_resolved_topup_external_refunds.sql" {
			continue
		}
		content, e := migrations.Files.ReadFile(name)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = tx.Exec(ctx, string(content)); e != nil {
			t.Fatalf("schema-153 setup migration %s: %v", name, e)
		}
		sum := sha256.Sum256(content)
		if _, e = tx.Exec(ctx, `INSERT INTO schema_migrations(version,checksum) VALUES($1,$2)`, name, hex.EncodeToString(sum[:])); e != nil {
			t.Fatal(e)
		}
		expectedNames = append(expectedNames, name)
	}
	if len(expectedNames) == 0 || expectedNames[len(expectedNames)-1] != "153_resolved_topup_external_refunds.sql" {
		t.Fatal("embedded schema-153 migration boundary missing")
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var applied int
	var appliedNames []string
	var last string
	var has154 bool
	if err = pool.QueryRow(ctx, `SELECT count(*),max(version),array_agg(version ORDER BY version),to_regclass('external_refund_order_births') IS NOT NULL OR to_regclass('external_refund_order_stock_reservations') IS NOT NULL FROM schema_migrations`).Scan(&applied, &last, &appliedNames, &has154); err != nil || applied != len(expectedNames) || !slices.Equal(appliedNames, expectedNames) || last != "153_resolved_topup_external_refunds.sql" || has154 {
		t.Fatal("fixture was not born strictly under schema 153", applied, last, has154, err)
	}
	return db
}

// Synthetic upgrade fixture, equivalent to the plain finite-stock, two-unit
// path of persistCheckoutLines at base 9038e0d. It is not a production snapshot
// or proof of a historical HTTP checkout. Current persistCheckoutLines cannot
// run on schema 153 because its new reservation table intentionally needs 154.
// Keep the original structure: pending review, order_items as purchase truth,
// one manual pending-payment service per unit, direct stock subtraction, and
// an automatic unpaid invoice. Payment below uses the unchanged Store entrypoints.
func createSyntheticSchema153Checkout(t *testing.T, db *Store, actor, user, product int64) CheckoutResult {
	t.Helper()
	ctx := t.Context()
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM products WHERE id=$1 FOR UPDATE`, product); err != nil {
		t.Fatal(err)
	}
	result := CheckoutResult{TotalCents: 2400}
	var item int64
	if err = tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,review_status) VALUES('SYNTHETIC-SCHEMA153-ORDER',$1,2400,'synthetic-schema153-checkout','synthetic-schema153-hash','pending') RETURNING id,order_no`, user).Scan(&result.OrderID, &result.OrderNo); err != nil {
		t.Fatal(err)
	}
	if err = InitializeServiceNavigation(ctx, tx, user); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,purchase_fields) VALUES($1,$2,'Schema153 upgrade product','monthly',2,1000,200,2400,'[]','{}','[]','[]') RETURNING id`, result.OrderID, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number,provision_mode,delivery_snapshot) SELECT $1,$2,$3,p.id,p.name,'synthetic-schema153-unit-'||n,'monthly',n,p.provision_mode,p.delivery_config FROM products p CROSS JOIN generate_series(1,2) n WHERE p.id=$4`, user, result.OrderID, item, product); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE order_items SET first_price_override_cents=NULL,renewal_price_override_cents=NULL WHERE id=$1`, item); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `UPDATE products SET stock=stock-2,updated_at=now() WHERE id=$1`, product); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('SYNTHETIC-SCHEMA153-INVOICE',$1,$2,2400) RETURNING id`, user, result.OrderID).Scan(&result.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if err = Audit(ctx, tx, actor, "order.created", "order", result.OrderID, map[string]any{"total_cents": 2400, "source": "synthetic-schema153-upgrade-fixture"}); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return result
}

const schema153OrderSnapshot = `SELECT jsonb_build_object(
 'order',(SELECT to_jsonb(o) FROM orders o WHERE id=$1),
 'items',(SELECT jsonb_agg(to_jsonb(oi) ORDER BY id) FROM order_items oi WHERE order_id=$1),
 'services',(SELECT jsonb_agg(to_jsonb(s)-'renewal_generation' ORDER BY id) FROM services s WHERE order_id=$1),
 'invoice',(SELECT to_jsonb(i) FROM invoices i WHERE order_id=$1),
 'payments',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM payment_transactions p WHERE invoice_id=(SELECT id FROM invoices WHERE order_id=$1)),
 'callback',(SELECT to_jsonb(e) FROM payment_webhook_events e WHERE provider='mock' AND event_id='synthetic-schema153-paid-event'),
 'audit',(SELECT jsonb_agg(to_jsonb(a) ORDER BY id) FROM audit_events a WHERE entity_type='order' AND entity_id=$1::text),
 'user',(SELECT to_jsonb(u) FROM users u WHERE id=(SELECT user_id FROM orders WHERE id=$1)),
 'ledger',(SELECT jsonb_agg(to_jsonb(b) ORDER BY id) FROM balance_ledger b WHERE user_id=(SELECT user_id FROM orders WHERE id=$1))
)::text`

func TestOrderExternalRefundUpgradeFrom153DoesNotAdoptHistoricalOrders(t *testing.T) {
	db := isolatedOrderRefundSchema153(t)
	ctx := t.Context()
	actor, user := topUpFixture(t, db)
	var product int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,setup_fee_cents,stock,billing_cycle,approval_required) VALUES('Schema153 upgrade product','schema153-upgrade-product',1000,200,12,'monthly',true) RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('schema153-upgrade-session',$1,now()+interval '1 hour')`, actor); err != nil {
		t.Fatal(err)
	}
	old := createSyntheticSchema153Checkout(t, db, actor, user, product)
	originalPayment, err := db.CreatePaymentIntent(ctx, user, old.InvoiceID, "mock", "synthetic-schema153-payment", "", "synthetic-schema153-payment-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProcessPaymentWebhook(ctx, "mock", "synthetic-schema153-paid-event", "synthetic-schema153-payment", old.InvoiceID, 2400, "succeeded", []byte(`{"fixture":"synthetic153-original-receipt"}`), "synthetic-schema153-original-transaction"); err != nil {
		t.Fatal(err)
	}
	var stock, units, jobs, items int
	var invoiceStatus, orderStatus, reviewStatus string
	if err = db.Pool.QueryRow(ctx, `SELECT p.stock,o.status,o.review_status,i.status,(SELECT count(*) FROM services WHERE order_id=o.id AND status='pending_payment' AND provision_mode='manual' AND NOT inventory_released),(SELECT count(*) FROM provision_jobs),(SELECT count(*) FROM invoice_items) FROM products p CROSS JOIN orders o JOIN invoices i ON i.order_id=o.id WHERE p.id=$1 AND o.id=$2`, product, old.OrderID).Scan(&stock, &orderStatus, &reviewStatus, &invoiceStatus, &units, &jobs, &items); err != nil || stock != 10 || orderStatus != "paid" || reviewStatus != "pending" || invoiceStatus != "paid" || units != 2 || jobs != 0 || items != 0 {
		t.Fatal("schema153 original checkout/payment facts invalid", stock, orderStatus, reviewStatus, invoiceStatus, units, jobs, items, err)
	}
	var beforeOrder, beforeProduct string
	if err = db.Pool.QueryRow(ctx, schema153OrderSnapshot, old.OrderID).Scan(&beforeOrder); err != nil {
		t.Fatal(err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM products p WHERE id=$1`, product).Scan(&beforeProduct); err != nil {
		t.Fatal(err)
	}
	// First call actually adds 154; the second verifies exact idempotent replay.
	for attempt := 1; attempt <= 2; attempt++ {
		if err = db.Migrate(ctx); err != nil {
			t.Fatalf("upgrade Migrate pass %d: %v", attempt, err)
		}
		if err = db.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		var afterOrder, afterProduct string
		if err = db.Pool.QueryRow(ctx, schema153OrderSnapshot, old.OrderID).Scan(&afterOrder); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(ctx, `SELECT to_jsonb(p)::text FROM products p WHERE id=$1`, product).Scan(&afterProduct); err != nil {
			t.Fatal(err)
		}
		if afterOrder != beforeOrder || afterProduct != beforeProduct {
			t.Fatalf("Migrate pass %d rewrote original business facts", attempt)
		}
	}
	// The additive generation column must not rewrite imported business state.
	var legacyGenerations int
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM services WHERE order_id=$1 AND renewal_generation<>0`, old.OrderID).Scan(&legacyGenerations); err != nil || legacyGenerations != 0 {
		t.Fatal("upgrade changed legacy renewal generation", legacyGenerations, err)
	}
	var births, reservations int
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM external_refund_order_births WHERE order_id=$1)+(SELECT count(*) FROM external_refund_service_births WHERE order_id=$1),(SELECT count(*) FROM external_refund_order_stock_reservations WHERE order_id=$1)`, old.OrderID).Scan(&births, &reservations); err != nil || births != 0 || reservations != 0 {
		t.Fatal("upgrade adopted historical provenance", births, reservations, err)
	}
	refundCtx := WithExternalRefundSession(ctx, "schema153-upgrade-session")
	c, err := db.ExternalRefundOrderContext(refundCtx, actor, old.InvoiceID)
	if err != nil || c.Eligible || c.ReasonCode != "external_refund_order_not_eligible" {
		t.Fatal("historical order must stay ineligible", c, err)
	}
	in := ExternalRefundInput{PaymentID: originalPayment.PaymentID, AmountCents: 2400, Method: "original_route", ProviderRefundID: "synthetic153-not-allowed-refund", ReceivedAt: time.Now(), EvidenceRef: "redacted/synthetic153", EvidenceSHA256: strings.Repeat("a", 64), Note: "synthetic upgrade refusal proof", ExpectedVersion: c.InvoiceVersion, ExpectedOrderVersion: c.OrderVersion}
	for _, s := range c.Services {
		in.ExpectedServiceVersions = append(in.ExpectedServiceVersions, ExternalRefundServiceVersion{ID: s.ID, Version: s.Version})
	}
	if _, err = db.CreateExternalRefund(refundCtx, actor, old.InvoiceID, in, "synthetic153-refund-refused"); conflictCode(t, err) != "external_refund_order_not_eligible" {
		t.Fatal(err)
	}
	var holds, refunds int
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM external_refund_order_holds),(SELECT count(*) FROM external_refunds)`).Scan(&holds, &refunds); err != nil || holds != 0 || refunds != 0 {
		t.Fatal("old order refusal wrote refund facts", holds, refunds, err)
	}
	// A genuinely new checkout after 154 receives the new provenance and reserves
	// stock itself. Reuse the same product to prove no backfill/release happened.
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	fresh, err := db.persistCheckoutLines(ctx, tx, user, "post154-real-checkout", "post154-real-checkout-hash", []checkoutLine{{ProductID: product, Name: "Schema153 upgrade product", Cycle: "monthly", Qty: 2, UnitPrice: 1000, UnitSetup: 200, OptionIDs: []int64{}, OptionQuantities: map[int64]int64{}, Snapshot: []OptionSnapshot{}, PurchaseSnapshot: []PurchaseFieldSnapshot{}}}, CheckoutResult{TotalCents: 2400}, []RenewalBenefit{{}}, true, 0, checkoutOptions{GenerateInvoice: true, ActorID: actor, Source: "post154-upgrade-test"})
	if err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = db.CreatePaymentIntent(ctx, user, fresh.InvoiceID, "mock", "post154-real-payment", "", "post154-real-payment-key"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProcessPaymentWebhook(ctx, "mock", "post154-real-paid-event", "post154-real-payment", fresh.InvoiceID, 2400, "succeeded", []byte(`{"fixture":"post154-real-payment"}`), "post154-real-transaction"); err != nil {
		t.Fatal(err)
	}
	freshContext, err := db.ExternalRefundOrderContext(refundCtx, actor, fresh.InvoiceID)
	if err != nil || !freshContext.Eligible || len(freshContext.Services) != 2 {
		t.Fatal("new checkout after upgrade not eligible", freshContext, err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT p.stock,(SELECT count(*) FROM external_refund_order_births WHERE order_id=$2)+(SELECT count(*) FROM external_refund_service_births WHERE order_id=$2),(SELECT count(*) FROM external_refund_order_stock_reservations WHERE order_id=$2 AND units=2 AND stock_before=10 AND stock_after=8) FROM products p WHERE id=$1`, product, fresh.OrderID).Scan(&stock, &births, &reservations); err != nil || stock != 8 || births != 3 || reservations != 1 {
		t.Fatal("new checkout stock/provenance invalid", stock, births, reservations, err)
	}
	var afterOrder string
	if err = db.Pool.QueryRow(ctx, schema153OrderSnapshot, old.OrderID).Scan(&afterOrder); err != nil || afterOrder != beforeOrder {
		t.Fatal("new checkout or old refund refusal changed original business facts", err)
	}
	if err = db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM external_refund_order_births WHERE order_id=$1)+(SELECT count(*) FROM external_refund_service_births WHERE order_id=$1),(SELECT count(*) FROM external_refund_order_stock_reservations WHERE order_id=$1)`, old.OrderID).Scan(&births, &reservations); err != nil || births != 0 || reservations != 0 {
		t.Fatal("new checkout retroactively adopted old provenance", births, reservations, err)
	}
	t.Log("actual embedded 001..153 -> 154 upgrade and second Migrate preserved original paid/pending order, payment/callback/paid_at and finite stock; historical order refused, genuinely new two-unit checkout eligible with actual stock 10 -> 8")
}
