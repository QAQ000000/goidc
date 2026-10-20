package store

import (
	"bytes"
	"testing"

	"github.com/codex/zjmfgo/internal/provision"
)

const manualResourcesPredecessor = "184_retire_credit_and_contracts.sql"

func TestManualResourceUpgrade184PreservesFacts(t *testing.T) {
	db := retiredFeaturesSchema183(t)
	applyNotificationUpgradeMigration(t, db, manualResourcesPredecessor)
	var boundary string
	var existed bool
	if err := db.Pool.QueryRow(t.Context(), "SELECT max(version),to_regclass('manual_resources') IS NOT NULL FROM schema_migrations").Scan(&boundary, &existed); err != nil || boundary != manualResourcesPredecessor || existed {
		t.Fatal("inventory fixture must originate at genuine schema184", boundary, existed, err)
	}
	actor, customer := topUpFixture(t, db)
	var product, order, item, service int64
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Pool.QueryRow(t.Context(), "INSERT INTO products(name,slug,price_cents,stock,provision_mode) VALUES('Existing manual product','manual-upgrade-existing',1000,7,'manual') RETURNING id").Scan(&product); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('manual-upgrade-existing',$1,'paid',1000,'manual-upgrade-existing',repeat('a',64)) RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Existing manual product','monthly',1,1000,0,1000) RETURNING id`, order, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at,provision_mode,upstream_id,username,resource_info,version)
 VALUES($1,$2,$3,$4,'Existing manual service','manual-upgrade-existing.test','monthly','active','2027-02-03T04:05:06+08','manual','existing-machine','existing-user','{"dedicated_ip":"192.0.2.12"}',9) RETURNING id`, customer, order, item, product).Scan(&service); err != nil {
		t.Fatal(err)
	}
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := provision.Seal(key, service, provision.Credentials{Password: "historical-encrypted-password"})
	if err != nil {
		t.Fatal(err)
	}
	exec("UPDATE services SET credentials_encrypted=$2 WHERE id=$1", service, sealed)
	exec(`INSERT INTO provision_jobs(service_id,action,status,idempotency_key,result,completed_at) VALUES($1,'create','succeeded','manual-upgrade-historical-job','{"upstream_id":"existing-machine"}','2026-03-01T00:00:00Z')`, service)
	exec(`INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at) VALUES('manual-upgrade-existing',$1,$2,'paid',1000,'2026-03-01T00:00:00Z')`, customer, order)
	exec(`INSERT INTO balance_ledger(user_id,entry_type,delta_cents,balance_after) VALUES($1,'payment',-1000,0)`, customer)
	exec(`INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result) VALUES($1,'manual-upgrade-history','manual-upgrade-history',repeat('b',64),'{"historical":true}')`, actor)
	exec(`INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'service.manual.completed','service',$2::bigint::text,'{"historical":true}')`, actor, service)
	snapshot := func() string {
		t.Helper()
		var value string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'jobs',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM provision_jobs x),
 'product',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM products x),
 'pricing',(SELECT jsonb_agg(to_jsonb(x) ORDER BY order_item_id) FROM order_item_pricing x),
 'site',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM site_settings x),
 'credit_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM credit_settings x),
 'contract_settings',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM contract_settings x)
 )::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value + retiredFeaturesHistoricalFacts(t, db)
	}
	before := snapshot()
	checksums := supplierExpiryUpgradeMigrations(t, db, manualResourcesPredecessor)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted predecessor without inventory schema")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	check := func() {
		t.Helper()
		if snapshot() != before || supplierExpiryUpgradeMigrations(t, db, manualResourcesPredecessor) != checksums {
			t.Fatal("inventory upgrade changed historical services, encrypted credentials, expiry, money, stock, jobs, receipts, settings or old migration checksums")
		}
		var resources, bindings int
		if err := db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM manual_resources),(SELECT count(*) FROM manual_resource_bindings)").Scan(&resources, &bindings); err != nil || resources != 0 || bindings != 0 {
			t.Fatal("upgrade inferred physical inventory or assigned existing services", resources, bindings, err)
		}
	}
	check()
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	check()
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
