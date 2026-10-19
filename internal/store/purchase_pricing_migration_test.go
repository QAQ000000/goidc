package store

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const purchasePricingPredecessor = "179_admin_order_mutation_receipts.sql"

func purchasePricingUpgradeFacts(t *testing.T, db *Store) string {
	t.Helper()
	var raw string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'business',$1::jsonb,
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x),
 'payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_transactions x),
 'refunds',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM external_refunds x),
 'holds',(SELECT jsonb_agg(to_jsonb(x) ORDER BY refund_id) FROM external_refund_order_holds x),
 'refund_services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY refund_id,service_id) FROM external_refund_order_services x)
 )::text`, adminOrderReceiptMigrationFacts(t, db)).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func purchasePricingAdminInput(t *testing.T, db *Store, customer, product int64) AdminOrderInput {
	t.Helper()
	var revision int64
	if err := db.Pool.QueryRow(t.Context(), `SELECT revision FROM products WHERE id=$1`, product).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	return AdminOrderInput{CustomerID: customer, Items: []AdminOrderItemInput{{ProductID: product, ExpectedRevision: revision, BillingCycle: "monthly", Quantity: 1}}}
}

func TestPurchasePricingUpgrade179PreservesRefundEvidenceAndReplay(t *testing.T) {
	// The previous receipt helper writes the real pre-179 receipt SQL. Advance
	// that untouched schema to 179 before creating the actual pending refund.
	db := adminOrderReceiptSchema178(t)
	actor, customer := topUpFixture(t, db)
	product := adminOrderReceiptMigrationProduct(t, db)
	if _, err := db.Pool.Exec(t.Context(), `UPDATE products SET approval_required=true WHERE id=$1`, product); err != nil {
		t.Fatal(err)
	}
	old := legacyAdminOrderReceipt178(t, db, actor, customer, product, "pricing-historical-admin-order", time.Date(2026, 10, 1, 2, 3, 4, 0, time.UTC))
	applyNotificationUpgradeMigration(t, db, purchasePricingPredecessor)
	var boundary string
	var pricing bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('order_item_pricing') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &pricing); err != nil || boundary != purchasePricingPredecessor || pricing {
		t.Fatal("historical checkout was not created before pricing migration", boundary, pricing, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('pricing-upgrade-session',$1,now()+interval '1 hour')`, actor); err != nil {
		t.Fatal(err)
	}
	pay, err := db.CreatePaymentIntent(t.Context(), customer, old.result.InvoiceID, "mock", "pricing-upgrade-payment", "", "pricing-upgrade-payment-key")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ProcessPaymentWebhook(t.Context(), "mock", "pricing-upgrade-paid-event", "pricing-upgrade-payment", old.result.InvoiceID, old.result.TotalCents, "succeeded", []byte(`{"fixture":"verified schema179 merchant payment"}`), "pricing-upgrade-transaction"); err != nil {
		t.Fatal(err)
	}
	ctx := WithExternalRefundSession(t.Context(), "pricing-upgrade-session")
	eligibility, err := db.ExternalRefundOrderContext(ctx, actor, old.result.InvoiceID)
	if err != nil || !eligibility.Eligible {
		t.Fatal("historical manual order must remain refund eligible", eligibility, err)
	}
	in := ExternalRefundInput{PaymentID: pay.PaymentID, AmountCents: old.result.TotalCents, Method: "original_route", ProviderRefundID: "pricing-upgrade-refund-evidence", ReceivedAt: time.Now(), EvidenceRef: "redacted/pricing-upgrade", EvidenceSHA256: strings.Repeat("c", 64), Note: "pre180 immutable merchant evidence", ExpectedVersion: eligibility.InvoiceVersion, ExpectedOrderVersion: eligibility.OrderVersion}
	for _, service := range eligibility.Services {
		in.ExpectedServiceVersions = append(in.ExpectedServiceVersions, ExternalRefundServiceVersion{ID: service.ID, Version: service.Version})
	}
	pending, err := db.CreateExternalRefund(ctx, actor, old.result.InvoiceID, in, "pricing-upgrade-refund-create")
	if err != nil || pending.Refund.OrderReversal == nil || pending.Refund.OrderReversal.Status != "pending" {
		t.Fatal("historical pending hold was not produced by actual refund API", pending, err)
	}
	unreserved := purchasePricingUnreservedHistory(t, db, customer, product)
	before := purchasePricingUpgradeFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, purchasePricingPredecessor)
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted predecessor before pricing upgrade")
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != purchasePricingUpgradeFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, purchasePricingPredecessor) {
		t.Fatal("pricing upgrade changed historical complete rows, refunds, reservations, holds, receipts or checksums")
	}
	var snapshots, births int
	if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM order_item_pricing),(SELECT count(*) FROM purchase_pricing_births)`).Scan(&snapshots, &births); err != nil || snapshots != 0 || births != 0 {
		t.Fatal("pricing upgrade inferred historical snapshots or births", snapshots, births, err)
	}
	result, err := db.CreateAdminOrder(t.Context(), actor, old.key, old.input, PaymentConfiguration{})
	want := old.result
	want.Replayed = true
	if err != nil || !reflect.DeepEqual(result, want) {
		t.Fatal("pricing upgrade invalidated historical checkout receipt", result, err)
	}
	replayed, err := db.CreateExternalRefund(ctx, actor, old.result.InvoiceID, in, "pricing-upgrade-refund-create")
	wantRefund := pending
	wantRefund.Replayed = true
	if err != nil || !reflect.DeepEqual(replayed, wantRefund) {
		t.Fatal("pricing upgrade invalidated historical pending-refund replay", replayed, err)
	}
	if err = db.Migrate(t.Context()); err != nil || before != purchasePricingUpgradeFacts(t, db) {
		t.Fatal("repeat upgrade or replay modified immutable historical evidence", err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
	// A historical item already has a complete immutable stock reservation;
	// retrofitting pricing must fail before changing either its row or evidence.
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot)
SELECT id,total_cents,0,0,total_cents,'{}' FROM order_items WHERE order_id=$1`, old.result.OrderID); err == nil {
		t.Fatal("historical reserved order accepted late pricing")
	}
	if before != purchasePricingUpgradeFacts(t, db) {
		t.Fatal("rejected pricing backfill modified historical evidence")
	}
	// Also reject a predecessor line without any reservation: absence of a
	// refund reservation cannot be interpreted as authority to infer pricing.
	assertPurchasePricingUnreservedHistoryRejected(t, db, unreserved)
	if before != purchasePricingUpgradeFacts(t, db) {
		t.Fatal("rejected unreserved backfill modified historical rows")
	}
	newInput := purchasePricingAdminInput(t, db, customer, product)
	quote, err := db.QuoteAdminOrder(t.Context(), actor, newInput, PaymentConfiguration{})
	if err != nil {
		t.Fatal(err)
	}
	newInput.QuoteHash = quote.QuoteHash
	newOrder, err := db.CreateAdminOrder(t.Context(), actor, "pricing-after-upgrade-order", newInput, PaymentConfiguration{})
	if err != nil {
		t.Fatal(err)
	}
	var itemID int64
	if err = db.Pool.QueryRow(t.Context(), `SELECT p.order_item_id FROM order_item_pricing p JOIN order_items oi ON oi.id=p.order_item_id WHERE oi.order_id=$1`, newOrder.OrderID).Scan(&itemID); err != nil {
		t.Fatal("new order did not freeze pricing", err)
	}
	for _, query := range []string{`UPDATE order_item_pricing SET snapshot='{}' WHERE order_item_id=$1`, `UPDATE order_item_pricing SET net_cents=net_cents WHERE order_item_id=$1`, `DELETE FROM order_item_pricing WHERE order_item_id=$1`} {
		if _, err = db.Pool.Exec(t.Context(), query, itemID); err == nil {
			t.Fatal("immutable new purchase pricing accepted mutation", query)
		}
	}
	for _, query := range []string{`UPDATE purchase_pricing_births SET creation_xid=pg_current_xact_id() WHERE order_id=$1`, `DELETE FROM purchase_pricing_births WHERE order_id=$1`} {
		if _, err = db.Pool.Exec(t.Context(), query, newOrder.OrderID); err == nil {
			t.Fatal("trigger-owned birth marker accepted mutation", query)
		}
	}
}

func purchasePricingUnreservedHistory(t *testing.T, db *Store, customer, product int64) int64 {
	t.Helper()
	var line int64
	if err := db.Pool.QueryRow(t.Context(), `WITH o AS (
 INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES('unreserved-legacy-pricing',$1,100,'unreserved-legacy-pricing','fixture') RETURNING id
) INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
SELECT id,$2,'Unreserved historical line','monthly',1,100,0,100 FROM o RETURNING id`, customer, product).Scan(&line); err != nil {
		t.Fatal(err)
	}
	return line
}

func assertPurchasePricingUnreservedHistoryRejected(t *testing.T, db *Store, line int64) {
	t.Helper()
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy_backfill_after_rewrite_%t", rewrite), func(t *testing.T) {
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if rewrite {
				// xmin alone cannot establish birth: a historical UPDATE would
				// rewrite xmin for both rows in this transaction.
				if _, err = tx.Exec(t.Context(), `UPDATE orders SET updated_at=clock_timestamp() WHERE id=(SELECT order_id FROM order_items WHERE id=$1)`, line); err != nil {
					t.Fatal(err)
				}
				if _, err = tx.Exec(t.Context(), `UPDATE order_items SET product_name='rewritten historical item' WHERE id=$1`, line); err != nil {
					t.Fatal(err)
				}
			}
			if _, err = tx.Exec(t.Context(), `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot) VALUES($1,100,0,0,100,'{}')`, line); err == nil {
				t.Fatal("a committed unreserved historical line accepted late pricing")
			}
		})
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO purchase_pricing_births(order_id,creation_xid) SELECT order_id,pg_current_xact_id() FROM order_items WHERE id=$1`, line); err == nil {
		t.Fatal("historical order accepted a forged birth marker")
	}
}

func TestPurchasePricingInitialTotalsRollback(t *testing.T) {
	db := isolatedTestStore(t)
	_, customer := topUpFixture(t, db)
	product := adminOrderReceiptMigrationProduct(t, db)
	for _, tc := range []struct {
		name              string
		lines             int
		gross, group, net int64
	}{
		{"wrong_net_sum", 1, 100, 1, 99},
		{"missing_line_snapshot", 2, 100, 0, 100},
		{"gross_does_not_match_line", 1, 99, 0, 99},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			var order, line int64
			key := "invalid-pricing-" + tc.name
			if err = tx.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) VALUES($1,$2,$3,$1,'fixture') RETURNING id`, key, customer, tc.lines*100).Scan(&order); err != nil {
				t.Fatal(err)
			}
			for n := 0; n < tc.lines; n++ {
				if err = tx.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Invalid pricing','monthly',1,100,0,100) RETURNING id`, order, product).Scan(&line); err != nil {
					t.Fatal(err)
				}
			}
			_, err = tx.Exec(t.Context(), `INSERT INTO order_item_pricing(order_item_id,gross_cents,group_discount_cents,promo_discount_cents,net_cents,snapshot) VALUES($1,$2,$3,0,$4,'{}')`, line, tc.gross, tc.group, tc.net)
			if err == nil {
				err = tx.Commit(t.Context())
			}
			if err == nil {
				t.Fatal("invalid initial pricing committed")
			}
			if rollbackErr := tx.Rollback(t.Context()); rollbackErr != nil && rollbackErr != pgx.ErrTxClosed {
				t.Fatal(rollbackErr)
			}
			var orders, pricing int
			if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM orders WHERE order_no=$1),(SELECT count(*) FROM order_item_pricing p JOIN order_items oi ON oi.id=p.order_item_id WHERE oi.order_id=$2)`, key, order).Scan(&orders, &pricing); err != nil || orders != 0 || pricing != 0 {
				t.Fatal("invalid pricing did not roll back completely", orders, pricing, err)
			}
		})
	}
}

type purchasePricingLockFixture struct {
	customer, product, group, rule int64
	input                          AdminOrderInput
}

func purchasePricingLockData(t *testing.T, db *Store, name string, productRule bool) purchasePricingLockFixture {
	t.Helper()
	f := purchasePricingLockFixture{}
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if err = tx.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role) VALUES($1,$1||'@example.test','unused',$1,'customer') RETURNING id`, name).Scan(&f.customer); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents,stock,provision_mode) VALUES($1,$1,1000,12,'manual') RETURNING id`, name).Scan(&f.product); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(t.Context(), `INSERT INTO customer_groups(name,discount_percent) VALUES($1,20) RETURNING id`, name).Scan(&f.group); err != nil {
		t.Fatal(err)
	}
	if productRule {
		if _, err = tx.Exec(t.Context(), `INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)`, f.customer, f.group); err != nil {
			t.Fatal(err)
		}
		var productGroup int64
		if err = tx.QueryRow(t.Context(), `INSERT INTO customer_product_groups(name) VALUES($1) RETURNING id`, name).Scan(&productGroup); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO customer_product_group_products(group_id,product_id) VALUES($1,$2)`, productGroup, f.product); err != nil {
			t.Fatal(err)
		}
		if err = tx.QueryRow(t.Context(), `INSERT INTO customer_product_group_discounts(customer_group_id,product_group_id,discount_type,discount_percent) VALUES($1,$2,'percent',30) RETURNING id`, f.group, productGroup).Scan(&f.rule); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	f.input = purchasePricingAdminInput(t, db, f.customer, f.product)
	return f
}

func purchasePricingWaitForBlocked(t *testing.T, ctx context.Context, db *Store, blocker int32) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		var blocked bool
		if err := db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE $1=ANY(pg_blocking_pids(pid)))`, blocker).Scan(&blocked); err != nil {
			t.Fatal(err)
		}
		if blocked {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("expected actual PostgreSQL pricing lock contention")
		case <-tick.C:
		}
	}
}

func purchasePricingMutation(ctx context.Context, tx pgx.Tx, f purchasePricingLockFixture, productRule bool) error {
	if productRule {
		_, err := tx.Exec(ctx, `UPDATE customer_product_group_discounts SET discount_percent=50,version=version+1 WHERE id=$1`, f.rule)
		return err
	}
	_, err := tx.Exec(ctx, `INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)`, f.customer, f.group)
	return err
}

func TestPurchasePricingLocksSerializeMembershipAndRuleChanges(t *testing.T) {
	db := isolatedTestStore(t)
	actor, _ := topUpFixture(t, db)
	for _, productRule := range []bool{false, true} {
		for _, writerFirst := range []bool{false, true} {
			name := fmt.Sprintf("pricing-lock-rule-%t-writer-%t", productRule, writerFirst)
			t.Run(name, func(t *testing.T) {
				f := purchasePricingLockData(t, db, name, productRule)
				ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
				defer cancel()
				first, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				defer first.Rollback(ctx)
				var blocker int32
				if err = first.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&blocker); err != nil {
					t.Fatal(err)
				}
				if writerFirst {
					if err = GroupPricingWriteLock(ctx, first); err != nil {
						t.Fatal(err)
					}
					if err = purchasePricingMutation(ctx, first, f, productRule); err != nil {
						t.Fatal(err)
					}
					type quoteResult struct {
						quote AdminOrderQuote
						err   error
					}
					done := make(chan quoteResult, 1)
					go func() {
						quote, e := db.QuoteAdminOrder(ctx, actor, f.input, PaymentConfiguration{})
						done <- quoteResult{quote, e}
					}()
					purchasePricingWaitForBlocked(t, ctx, db, blocker)
					if err = first.Commit(ctx); err != nil {
						t.Fatal(err)
					}
					select {
					case result := <-done:
						want := int64(800)
						if productRule {
							want = 500
						}
						if result.err != nil || result.quote.TotalCents != want || len(result.quote.Items) != 1 || result.quote.Items[0].Pricing == nil {
							t.Fatal("quote did not see committed pricing rule", result.quote, result.err)
						}
					case <-ctx.Done():
						t.Fatal("quote did not resume after writer committed", ctx.Err())
					}
					return
				}
				if err = GroupPricingReadLock(ctx, first); err != nil {
					t.Fatal(err)
				}
				before, err := loadGroupPricing(ctx, first, f.customer)
				if err != nil {
					t.Fatal(err)
				}
				writer, err := db.Pool.Begin(ctx)
				if err != nil {
					t.Fatal(err)
				}
				done := make(chan error, 1)
				// Direct SQL relies on the statement trigger, including the first
				// membership insertion for which no existing row can be locked.
				go func() {
					defer writer.Rollback(ctx)
					e := purchasePricingMutation(ctx, writer, f, productRule)
					if e == nil {
						e = writer.Commit(ctx)
					}
					done <- e
				}()
				purchasePricingWaitForBlocked(t, ctx, db, blocker)
				frozen, err := loadGroupPricing(ctx, first, f.customer)
				if err != nil || !reflect.DeepEqual(before, frozen) {
					t.Fatal("writer changed rule inside protected read", before, frozen, err)
				}
				line, _, err := frozen.price(checkoutLine{ProductID: f.product, Qty: 1, UnitPrice: 1000, Cycle: "monthly"})
				want := int64(1000)
				if productRule {
					want = 700
				}
				if err != nil || line.NetCents != want {
					t.Fatal("protected reader saw uncommitted pricing", line, err)
				}
				if err = first.Commit(ctx); err != nil {
					t.Fatal(err)
				}
				select {
				case err = <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-ctx.Done():
					t.Fatal("writer did not resume after protected read", ctx.Err())
				}
				quote, err := db.QuoteAdminOrder(ctx, actor, f.input, PaymentConfiguration{})
				want = 800
				if productRule {
					want = 500
				}
				if err != nil || quote.TotalCents != want {
					t.Fatal("next quote did not see new pricing", quote, err)
				}
			})
		}
	}
}
