package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
 'webhooks',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM payment_webhook_events x),
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

// Seed the actual schema179 SQL shape. Current payment and refund entrypoints
// require the physical account split in 182 and must never run on this fixture.
// This is synthetic historical evidence, not execution of an old HTTP binary.
// Keep the real database guards enabled while freezing the payment, refund hold,
// complete resource snapshots and the canonical request/result receipt.
func purchasePricingHistoricalRefund179(t *testing.T, db *Store, actor, customer int64, order CheckoutResult) (in ExternalRefundInput, pending ExternalRefundResult) {
	t.Helper()
	ctx := t.Context()
	if err := db.WithTx(ctx, func(tx pgx.Tx) error {
		var boundary string
		var split bool
		if err := tx.QueryRow(ctx, `SELECT max(version),to_regclass('account_principals') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &split); err != nil {
			return err
		}
		if boundary != purchasePricingPredecessor || split {
			return fmt.Errorf("historical payment/refund requires untouched schema179: boundary=%s split=%v", boundary, split)
		}
		var authorized bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE id=$1 AND role='super_admin' AND status='active') AND EXISTS(SELECT 1 FROM users WHERE id=$2 AND role='customer' AND status='active')`, actor, customer).Scan(&authorized); err != nil {
			return err
		}
		if !authorized {
			return fmt.Errorf("historical refund requires the original active staff and customer")
		}
		var paymentID int64
		if err := tx.QueryRow(ctx, `INSERT INTO payment_transactions(provider,provider_payment_id,channel_instance_id,invoice_id,user_id,amount_cents,idempotency_key)
 SELECT 'mock','pricing-upgrade-payment',(SELECT id FROM channel_instances WHERE id='payment-mock-default'),i.id,i.user_id,i.amount_cents,'pricing-upgrade-payment-key'
 FROM invoices i WHERE i.id=$1 AND i.user_id=$2 AND i.status='unpaid' RETURNING id`, order.InvoiceID, customer).Scan(&paymentID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'payment.intent.created','invoice',$2,jsonb_build_object('provider','mock','payment_id','pricing-upgrade-payment','amount_cents',$3::bigint))`, customer, fmt.Sprint(order.InvoiceID), order.TotalCents); err != nil {
			return err
		}
		raw := []byte(`{"fixture":"verified schema179 merchant payment"}`)
		payloadHash := sha256.Sum256(raw)
		if _, err := tx.Exec(ctx, `INSERT INTO payment_webhook_events(provider,event_id,payload_hash,status)
 VALUES('mock','pricing-upgrade-paid-event',$1,'processed')`, hex.EncodeToString(payloadHash[:])); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE invoices SET status='paid',paid_at=now() WHERE id=$1 AND status='unpaid'`, order.InvoiceID); err != nil {
			return err
		}
		// Approval remains pending, so settlement does not queue or provision the
		// service; the existing immutable stock reservation remains authoritative.
		if _, err := tx.Exec(ctx, `UPDATE orders SET status='paid',updated_at=now() WHERE id=$1 AND review_status='pending'`, order.OrderID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE payment_transactions SET status='succeeded',completed_at=now(),raw_result=$2,external_transaction_id='pricing-upgrade-transaction',next_reconcile_at=NULL,updated_at=now() WHERE id=$1`, paymentID, json.RawMessage(raw)); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(action,entity_type,entity_id,payload)
 VALUES('payment.webhook.succeeded','invoice',$1,jsonb_build_object('provider','mock','event_id','pricing-upgrade-paid-event','payment_id','pricing-upgrade-payment','amount_cents',$2::bigint))`, fmt.Sprint(order.InvoiceID), order.TotalCents); err != nil {
			return err
		}
		in = ExternalRefundInput{PaymentID: paymentID, AmountCents: order.TotalCents, Method: "original_route", ProviderRefundID: "pricing-upgrade-refund-evidence", ReceivedAt: time.Now(), EvidenceRef: "redacted/pricing-upgrade", EvidenceSHA256: strings.Repeat("c", 64), Note: "pre180 immutable merchant evidence"}
		var reason string
		if err := tx.QueryRow(ctx, `SELECT i.version,o.version,external_refund_order_reason(i.id) FROM invoices i JOIN orders o ON o.id=i.order_id WHERE i.id=$1`, order.InvoiceID).Scan(&in.ExpectedVersion, &in.ExpectedOrderVersion, &reason); err != nil {
			return err
		}
		if reason != "" {
			return fmt.Errorf("historical pending refund is ineligible: %s", reason)
		}
		rows, err := tx.Query(ctx, `SELECT id,version FROM services WHERE order_id=$1 ORDER BY id`, order.OrderID)
		if err != nil {
			return err
		}
		if in.ExpectedServiceVersions, err = pgx.CollectRows(rows, pgx.RowToStructByPos[ExternalRefundServiceVersion]); err != nil {
			return err
		}
		if len(in.ExpectedServiceVersions) != 1 {
			return fmt.Errorf("historical refund lost its original service")
		}
		var refundID int64
		if err := tx.QueryRow(ctx, `INSERT INTO external_refunds(invoice_id,payment_id,user_id,amount_cents,method,provider,channel_instance_id,provider_payment_id,provider_refund_id,received_at,evidence_ref,evidence_sha256,note,requested_by,original_transaction_id,merchant_scope)
 SELECT i.id,p.id,i.user_id,i.amount_cents,$3,p.provider,coalesce(p.channel_instance_id,''),p.provider_payment_id,$4,$5,$6,$7,$8,$9,p.external_transaction_id,'mock:test-only'
 FROM invoices i JOIN payment_transactions p ON p.invoice_id=i.id WHERE i.id=$1 AND p.id=$2 RETURNING id`, order.InvoiceID, paymentID, in.Method, in.ProviderRefundID, in.ReceivedAt, in.EvidenceRef, in.EvidenceSHA256, in.Note, actor).Scan(&refundID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO external_refund_order_holds(refund_id,invoice_id,payment_id,order_id,user_id,amount_cents,invoice_version,order_version,invoice_snapshot,order_snapshot,items_snapshot)
 SELECT r.id,i.id,r.payment_id,o.id,r.user_id,r.amount_cents,i.version,o.version,external_refund_order_snapshot(i),external_refund_order_snapshot(o),(SELECT jsonb_agg(external_refund_order_snapshot(oi) ORDER BY oi.id) FROM order_items oi WHERE oi.order_id=o.id)
 FROM external_refunds r JOIN invoices i ON i.id=r.invoice_id JOIN orders o ON o.id=i.order_id WHERE r.id=$1`, refundID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO external_refund_order_services(refund_id,service_id,order_item_id,product_id,unit_number,version,snapshot,stock_at_request)
 SELECT $1,s.id,s.order_item_id,s.product_id,s.unit_number,s.version,external_refund_order_snapshot(s),p.stock FROM services s JOIN products p ON p.id=s.product_id WHERE s.order_id=$2 ORDER BY s.id`, refundID, order.OrderID); err != nil {
			return err
		}
		var refundJSON []byte
		if err := tx.QueryRow(ctx, `SELECT to_jsonb(r)||jsonb_build_object('order_reversal',external_refund_order_reversal_json(r.id)) FROM external_refunds r WHERE id=$1`, refundID).Scan(&refundJSON); err != nil {
			return err
		}
		if err := json.Unmarshal(refundJSON, &pending.Refund); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'external_refund.created','external_refund',$2,jsonb_build_object('invoice_id',$3::bigint,'payment_id',$4::bigint,'amount_cents',$5::bigint,'status','pending_review'))`, actor, fmt.Sprint(refundID), order.InvoiceID, paymentID, in.AmountCents); err != nil {
			return err
		}
		canonical, err := json.Marshal([]any{order.InvoiceID, int64(0), false, in, ExternalRefundReviewInput{}})
		if err != nil {
			return err
		}
		requestHash := sha256.Sum256(canonical)
		resultJSON, err := json.Marshal(pending)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result)
 VALUES($1,'external-refund','pricing-upgrade-refund-create',$2,$3)`, actor, hex.EncodeToString(requestHash[:]), resultJSON)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return
}

func TestPurchasePricingUpgrade179PreservesRefundEvidenceAndReplay(t *testing.T) {
	// The previous receipt helper writes the real pre-179 receipt SQL. Advance
	// that untouched schema to 179 before seeding its historical pending refund.
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
	in, pending := purchasePricingHistoricalRefund179(t, db, actor, customer, old.result.CheckoutResult)
	ctx := WithExternalRefundSession(t.Context(), "pricing-upgrade-session")
	if pending.Refund.Status != "pending_review" || pending.Refund.OrderReversal == nil || pending.Refund.OrderReversal.Status != "pending" {
		t.Fatal("historical SQL did not retain a complete pending refund hold", pending)
	}
	unreserved := purchasePricingUnreservedHistory(t, db, customer, product)
	before := purchasePricingUpgradeFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, purchasePricingPredecessor)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted predecessor before pricing upgrade")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != purchasePricingUpgradeFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, purchasePricingPredecessor) {
		t.Fatal("pricing upgrade changed historical complete rows, refunds, reservations, holds, receipts or checksums")
	}
	var snapshots, births int
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM order_item_pricing),(SELECT count(*) FROM purchase_pricing_births)`).Scan(&snapshots, &births); err != nil || snapshots != 0 || births != 0 {
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
	changed := in
	changed.Note = "changed historical refund evidence"
	if _, err = db.CreateExternalRefund(ctx, actor, old.result.InvoiceID, changed, "pricing-upgrade-refund-create"); conflictCode(t, err) != "idempotency_conflict" {
		t.Fatal("historical refund receipt accepted a changed request", err)
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
