package store

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const adminOrderReceiptPredecessor = "178_worker_runtime.sql"
const adminOrderReceiptScope = "admin:order.create"

// Construct the actual predecessor, rather than dropping the new objects from
// an already upgraded schema. The receipt below is written using the pre-179
// SQL shape with explicit historical INSERTs; it must not invoke current Store
// writers against that schema. This is a synthetic historical writer fixture,
// not evidence of a production snapshot upgrade.
func adminOrderReceiptSchema178(t *testing.T) *Store {
	t.Helper()
	db := supplierExpirySchema171(t)
	for _, name := range []string{supplierExpiryMigration172, "173_notification_deadline_recovery.sql", "174_web_push.sql", "175_supplier_catalog_import.sql", "176_supplier_calendar_cycles.sql", "177_request_correlation.sql", adminOrderReceiptPredecessor} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var oldTable bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('admin_order_creation_receipts') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &oldTable); err != nil || boundary != adminOrderReceiptPredecessor || !oldTable {
		t.Fatal("fixture must originate at untouched schema178", boundary, oldTable, err)
	}
	return db
}

type legacyAdminOrderReceipt struct {
	actor   int64
	key     string
	input   AdminOrderInput
	result  AdminOrderResult
	created time.Time
	rawJSON string
}

func legacyAdminOrderReceipt178(t *testing.T, db *Store, actor, customer, product int64, key string, created time.Time) legacyAdminOrderReceipt {
	t.Helper()
	ctx := t.Context()
	var revision int64
	if err := db.Pool.QueryRow(ctx, "SELECT revision FROM products WHERE id=$1", product).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	in := AdminOrderInput{CustomerID: customer, Notes: "历史代下单：保留请求与回执\n原始凭证", Items: []AdminOrderItemInput{{ProductID: product, ExpectedRevision: revision, BillingCycle: "monthly", Quantity: 1}}}
	normalized, err := normalizeAdminOrder(in)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var authorized bool
	if err = tx.QueryRow(ctx, `SELECT role='super_admin' AND status='active' FROM users WHERE id=$1 FOR SHARE`, actor).Scan(&authorized); err != nil || !authorized {
		t.Fatal("historical actor must be an active administrator", authorized, err)
	}
	if err = tx.QueryRow(ctx, `SELECT role='customer' AND status='active' FROM users WHERE id=$1 FOR SHARE`, customer).Scan(&authorized); err != nil || !authorized {
		t.Fatal("historical owner must be an active customer", authorized, err)
	}
	line := checkoutLine{ProductID: product, Revision: revision, Qty: 1, Cycle: "monthly", Snapshot: []OptionSnapshot{}, PurchaseSnapshot: []PurchaseFieldSnapshot{}}
	if err = tx.QueryRow(ctx, `SELECT p.revision,p.name,pp.price_cents,pp.setup_fee_cents,p.approval_required FROM products p JOIN product_prices pp ON pp.product_id=p.id AND pp.billing_cycle='monthly' WHERE p.id=$1 FOR UPDATE OF p,pp`, product).Scan(&line.CurrentRevision, &line.Name, &line.UnitPrice, &line.UnitSetup, &line.ApprovalRequired); err != nil {
		t.Fatal(err)
	}
	result := CheckoutResult{TotalCents: line.UnitPrice + line.UnitSetup}
	quote := buildAdminOrderQuote(normalized, []checkoutLine{line}, result, line.ApprovalRequired, []RenewalBenefit{{}})
	in.QuoteHash = quote.QuoteHash
	normalized.QuoteHash = quote.QuoteHash
	hash := adminOrderHash(normalized)
	if err = tx.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,review_status)
 VALUES('O-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3,$4,$5) RETURNING id,order_no`, customer, result.TotalCents, "admin:"+fmt.Sprint(actor)+":"+key, hash, quote.ReviewStatus).Scan(&result.OrderID, &result.OrderNo); err != nil {
		t.Fatal(err)
	}
	var item int64
	if err = tx.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents,config_options,option_quantities,config_snapshot,purchase_fields)
 VALUES($1,$2,$3,'monthly',1,$4,$5,$6,'[]','{}','[]','[]') RETURNING id`, result.OrderID, product, line.Name, line.UnitPrice, line.UnitSetup, result.TotalCents).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,unit_number,provision_mode,delivery_snapshot)
 SELECT $1,$2,$3,p.id,p.name,'svc-'||replace(gen_random_uuid()::text,'-',''),'monthly',1,p.provision_mode,p.delivery_config FROM products p WHERE p.id=$4`, customer, result.OrderID, item, product); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO external_refund_order_stock_reservations(order_item_id,order_id,product_id,units,item_snapshot,service_ids) VALUES($1,$2,$3,1,'{}','[]')`, item, result.OrderID, product); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,order_id,amount_cents) VALUES('I-'||replace(gen_random_uuid()::text,'-',''),$1,$2,$3) RETURNING id`, customer, result.OrderID, result.TotalCents).Scan(&result.InvoiceID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'order.created','order',$2,jsonb_build_object('total_cents',$3::bigint,'source','admin'))`, actor, fmt.Sprint(result.OrderID), result.TotalCents); err != nil {
		t.Fatal(err)
	}
	out := AdminOrderResult{CheckoutResult: result, ReviewStatus: quote.ReviewStatus, EmailStatus: "not_requested"}
	if err = tx.QueryRow(ctx, `UPDATE orders SET created_by=$2,payment_provider='',payment_channel_instance_id=NULL,invoice_deferred=false,send_order_email=false,email_status='not_requested',notes=$3 WHERE id=$1 RETURNING version`, result.OrderID, actor, normalized.Notes).Scan(&out.Version); err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(out)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO admin_order_creation_receipts(actor_id,idempotency_key,customer_id,order_id,request_hash,result,created_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, actor, key, customer, out.OrderID, hash, raw, created); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'order.admin_created','order',$2,jsonb_build_object('customer_id',$3::bigint,'invoice_deferred',false,'payment_provider','','channel_instance_id','','confirmed',false,'send_email',false,'email_status','not_requested'))`, actor, fmt.Sprint(out.OrderID), customer); err != nil {
		t.Fatal(err)
	}
	var savedJSON string
	if err = tx.QueryRow(ctx, `SELECT result::text FROM admin_order_creation_receipts WHERE actor_id=$1 AND idempotency_key=$2`, actor, key).Scan(&savedJSON); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return legacyAdminOrderReceipt{actor, key, in, out, created, savedJSON}
}

func adminOrderReceiptMigrationProduct(t *testing.T, db *Store) int64 {
	t.Helper()
	var product int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents,stock,provision_mode) VALUES('Historical admin order','historical-admin-order',1234,12,'manual') RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	return product
}

func adminOrderReceiptMigrationFacts(t *testing.T, db *Store) string {
	t.Helper()
	var raw string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'users',`+historicalAccountSnapshotRows(t, db)+`,
 'products',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM products x),
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x),
 'reservations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY order_item_id) FROM external_refund_order_stock_reservations x),
 'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x),
 'mail',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM email_outbox x))::text`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAdminOrderReceiptUpgrade178PreservesHistoricalReplay(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	actor, customer := topUpFixture(t, db)
	var otherActor int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('other-order-root','other-order-root@example.test','unused','Other order root','super_admin') RETURNING id`).Scan(&otherActor); err != nil {
		t.Fatal(err)
	}
	product := adminOrderReceiptMigrationProduct(t, db)
	created := time.Date(2026, 10, 14, 8, 9, 10, 123456000, time.UTC)
	receipts := []legacyAdminOrderReceipt{
		legacyAdminOrderReceipt178(t, db, actor, customer, product, "historical-order-shared-key", created),
		legacyAdminOrderReceipt178(t, db, otherActor, customer, product, "historical-order-shared-key", created.Add(time.Second)),
		legacyAdminOrderReceipt178(t, db, actor, customer, product, "historical-order-other-key", created.Add(2*time.Second)),
	}
	otherScope := "admin:unrelated.business"
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,'{"unrelated":true}')`, actor, otherScope, receipts[0].key, strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
	before := adminOrderReceiptMigrationFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, adminOrderReceiptPredecessor)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema178 before receipt consolidation")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if before != adminOrderReceiptMigrationFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, adminOrderReceiptPredecessor) {
		t.Fatal("receipt migration changed historical business facts or migration checksums")
	}
	var oldTable bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT to_regclass('admin_order_creation_receipts') IS NOT NULL`).Scan(&oldTable); err != nil || oldTable {
		t.Fatal("old receipt writer remained available", oldTable, err)
	}
	for _, old := range receipts {
		normalized, err := normalizeAdminOrder(old.input)
		if err != nil {
			t.Fatal(err)
		}
		var savedHash string
		var savedResult AdminOrderResult
		var savedCreated time.Time
		var savedCustomer, savedOrder int64
		var raw []byte
		if err = db.Pool.QueryRow(t.Context(), `SELECT request_hash,result,created_at,customer_id,order_id FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3`, old.actor, adminOrderReceiptScope, old.key).Scan(&savedHash, &raw, &savedCreated, &savedCustomer, &savedOrder); err != nil {
			t.Fatal(err)
		}
		if err = json.Unmarshal(raw, &savedResult); err != nil || string(raw) != old.rawJSON || savedHash != adminOrderHash(normalized) || !savedCreated.Equal(old.created) || !reflect.DeepEqual(savedResult, old.result) || savedCustomer != old.input.CustomerID || savedOrder != old.result.OrderID {
			t.Fatal("historical hash, result, receipt timestamp or typed references were changed", err)
		}
		replayed, err := db.CreateAdminOrder(t.Context(), old.actor, old.key, old.input, PaymentConfiguration{})
		want := old.result
		want.Replayed = true
		if err != nil || !reflect.DeepEqual(replayed, want) {
			t.Fatal("current order writer did not replay migrated historical result", replayed, err)
		}
		changed := old.input
		changed.Notes += " changed"
		if _, err = db.CreateAdminOrder(t.Context(), old.actor, old.key, changed, PaymentConfiguration{}); conflictCode(t, err) != "idempotency_conflict" {
			t.Fatal("changed historical request bypassed key conflict", err)
		}
	}
	if err := db.WithTx(t.Context(), func(tx pgx.Tx) error {
		var out map[string]bool
		replayed, err := ReplayOperation(t.Context(), tx, actor, otherScope, receipts[0].key, strings.Repeat("b", 64), &out)
		if err != nil {
			return err
		}
		if !replayed || !out["unrelated"] {
			return fmt.Errorf("order receipt collided with another scope")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err != nil || before != adminOrderReceiptMigrationFacts(t, db) {
		t.Fatal("repeat migration or historical replay changed business facts", err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
	assertAdminOrderReceiptReferenceGuards(t, db, receipts[0])
}

func assertAdminOrderReceiptReferenceGuards(t *testing.T, db *Store, old legacyAdminOrderReceipt) {
	t.Helper()
	for _, tc := range []struct {
		name, scope     string
		customer, order any
		result          string
	}{
		{"missing_references", adminOrderReceiptScope, nil, nil, fmt.Sprintf(`{"order_id":%d}`, old.result.OrderID)},
		{"missing_customer", adminOrderReceiptScope, nil, old.result.OrderID, fmt.Sprintf(`{"order_id":%d}`, old.result.OrderID)},
		{"missing_order", adminOrderReceiptScope, old.input.CustomerID, nil, fmt.Sprintf(`{"order_id":%d}`, old.result.OrderID)},
		{"wrong_result_order", adminOrderReceiptScope, old.input.CustomerID, old.result.OrderID, `{"order_id":9223372036854775806}`},
		{"missing_result_order", adminOrderReceiptScope, old.input.CustomerID, old.result.OrderID, `{}`},
		{"string_result_order", adminOrderReceiptScope, old.input.CustomerID, old.result.OrderID, fmt.Sprintf(`{"order_id":"%d"}`, old.result.OrderID)},
		{"nonpositive_order", adminOrderReceiptScope, old.input.CustomerID, int64(0), `{"order_id":0}`},
		{"unknown_customer", adminOrderReceiptScope, int64(9223372036854775806), old.result.OrderID, fmt.Sprintf(`{"order_id":%d}`, old.result.OrderID)},
		{"unknown_order", adminOrderReceiptScope, old.input.CustomerID, int64(9223372036854775806), `{"order_id":9223372036854775806}`},
		{"unrelated_scope_references", "admin:unrelated.business", old.input.CustomerID, old.result.OrderID, fmt.Sprintf(`{"order_id":%d}`, old.result.OrderID)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := db.Pool.Begin(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(t.Context())
			if _, err = tx.Exec(t.Context(), `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result,customer_id,order_id) VALUES($1,$2,$3,$4,$5,$6,$7)`, old.actor, tc.scope, "invalid-reference-"+tc.name, strings.Repeat("d", 64), tc.result, tc.customer, tc.order); err == nil {
				t.Fatal("receipt lost the predecessor customer/order boundary")
			}
		})
	}
	t.Run("bigint_binding_and_order_delete_guard", func(t *testing.T) {
		tx, err := db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		// These IDs deliberately exceed int32; the receipt must preserve bigint
		// resource identities and protect even an order without business children.
		const customer, order int64 = 1 << 32, 1<<32 + 1
		if _, err = tx.Exec(t.Context(), `INSERT INTO users(id,username,email,password_hash,display_name,role) OVERRIDING SYSTEM VALUE VALUES($1,'receipt-bigint-customer','receipt-bigint@example.test','unused','Receipt bigint','customer')`, customer); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(t.Context(), `INSERT INTO orders(id,order_no,user_id,total_cents,idempotency_key,request_hash) OVERRIDING SYSTEM VALUE VALUES($1,'RECEIPT-BIGINT-ORDER',$2,1234,'receipt-bigint-order',$3)`, order, customer, strings.Repeat("e", 64)); err != nil {
			t.Fatal(err)
		}
		const key = "receipt-bigint-binding"
		if _, err = ReplayOperation(t.Context(), tx, old.actor, adminOrderReceiptScope, key, strings.Repeat("e", 64), nil); err != nil {
			t.Fatal(err)
		}
		if err = SaveOperation(t.Context(), tx, old.actor, adminOrderReceiptScope, key, strings.Repeat("e", 64), map[string]any{"order_id": order}, OperationReferences{CustomerID: customer, OrderID: order}); err != nil {
			t.Fatal(err)
		}
		var savedCustomer, savedOrder int64
		if err = tx.QueryRow(t.Context(), `SELECT customer_id,order_id FROM mutation_operations WHERE actor_id=$1 AND scope=$2 AND idempotency_key=$3`, old.actor, adminOrderReceiptScope, key).Scan(&savedCustomer, &savedOrder); err != nil || savedCustomer != customer || savedOrder != order {
			t.Fatal("bigint resource bindings did not round trip", savedCustomer, savedOrder, err)
		}
		if _, err = tx.Exec(t.Context(), "DELETE FROM orders WHERE id=$1", order); err == nil || !strings.Contains(err.Error(), "mutation_operations_order_id_fkey") {
			t.Fatal("receipt no longer protects the referenced order from physical deletion", err)
		}
	})
}

func TestAdminOrderReceiptUpgrade178ConflictRollsBack(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	actor, customer := topUpFixture(t, db)
	old := legacyAdminOrderReceipt178(t, db, actor, customer, adminOrderReceiptMigrationProduct(t, db), "conflicting-order-receipt", time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC))
	raw, err := json.Marshal(old.result)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result) VALUES($1,$2,$3,$4,$5)`, actor, adminOrderReceiptScope, old.key, strings.Repeat("c", 64), raw); err != nil {
		t.Fatal(err)
	}
	before := adminOrderReceiptMigrationFacts(t, db)
	var oldBefore, unifiedBefore string
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,idempotency_key)::text FROM admin_order_creation_receipts x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key)::text FROM mutation_operations x)`).Scan(&oldBefore, &unifiedBefore); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(t.Context()); err == nil {
		t.Fatal("migration silently replaced a conflicting unified receipt")
	}
	var boundary, oldAfter, unifiedAfter string
	if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT max(version) FROM schema_migrations),(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,idempotency_key)::text FROM admin_order_creation_receipts x),(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key)::text FROM mutation_operations x)`).Scan(&boundary, &oldAfter, &unifiedAfter); err != nil || boundary != adminOrderReceiptPredecessor || oldBefore != oldAfter || unifiedBefore != unifiedAfter || before != adminOrderReceiptMigrationFacts(t, db) {
		t.Fatal("failed upgrade lost old receipts, rewrote unified rows or committed partial migration", boundary, err)
	}
}

func TestAdminOrderReceiptUpgrade178RejectsAmbiguousReferences(t *testing.T) {
	for _, tc := range []struct{ name, update string }{
		{"customer_mismatch", `UPDATE admin_order_creation_receipts SET customer_id=actor_id`},
		{"result_order_mismatch", `UPDATE admin_order_creation_receipts SET result=jsonb_set(result,'{order_id}','9223372036854775806'::jsonb)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := adminOrderReceiptSchema178(t)
			actor, customer := topUpFixture(t, db)
			legacyAdminOrderReceipt178(t, db, actor, customer, adminOrderReceiptMigrationProduct(t, db), "ambiguous-historical-order", time.Date(2026, 10, 14, 1, 2, 3, 0, time.UTC))
			if _, err := db.Pool.Exec(t.Context(), tc.update); err != nil {
				t.Fatal(err)
			}
			before := adminOrderReceiptMigrationFacts(t, db)
			var receiptsBefore string
			if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,idempotency_key)::text FROM admin_order_creation_receipts x`).Scan(&receiptsBefore); err != nil {
				t.Fatal(err)
			}
			if err := db.Migrate(t.Context()); err == nil || !strings.Contains(err.Error(), "admin_order_receipt_requires_reconciliation") {
				t.Fatal("ambiguous predecessor references were copied without reconciliation", err)
			}
			var boundary, receiptsAfter string
			var copied int
			if err := db.Pool.QueryRow(t.Context(), `SELECT (SELECT max(version) FROM schema_migrations),(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,idempotency_key)::text FROM admin_order_creation_receipts x),(SELECT count(*) FROM mutation_operations WHERE scope=$1)`, adminOrderReceiptScope).Scan(&boundary, &receiptsAfter, &copied); err != nil || boundary != adminOrderReceiptPredecessor || receiptsBefore != receiptsAfter || copied != 0 || before != adminOrderReceiptMigrationFacts(t, db) {
				t.Fatal("reconciliation refusal did not preserve the original schema and facts", boundary, copied, err)
			}
		})
	}
}
