package store

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/codex/zjmfgo/internal/supplier"
)

// Seed the genuine 165 schema before any supplier configuration column exists.
// Neither migration nor runtime resolution may release an uncertain purchase.
func TestSupplierConfigurationUpgrade165PreservesDeliveryIdentity(t *testing.T) {
	db := notificationSchema160(t)
	for _, name := range []string{notificationUpgrade161, notificationUpgrade162, "163_notification_lifecycle.sql", "164_certification_provider_runtime.sql", "165_payment_runtime_contract.sql"} {
		applyNotificationUpgradeMigration(t, db, name)
	}
	var boundary string
	var newColumns bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),EXISTS(SELECT 1 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='supplier_accounts' AND column_name IN ('config','schema_version','credential_format','secure_fields','identity_key')) FROM schema_migrations`).Scan(&boundary, &newColumns); err != nil || boundary != "165_payment_runtime_contract.sql" || newColumns {
		t.Fatal("fixture must originate at actual schema 165", boundary, newColumns, err)
	}
	db.ProvisionEncryptionKey = bytes.Repeat([]byte{7}, 32)
	actor, customer := topUpFixture(t, db)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('supplier-upgrade-session',$1,now()+interval '1 hour')`, actor); err != nil {
		t.Fatal(err)
	}
	accounts := []int64{}
	for n, state := range []string{"running", "waiting_verification"} {
		var accountID, productID, bindingID, orderID, itemID, serviceID, jobID int64
		endpoint, username := fmt.Sprintf("https://supplier-upgrade-%d.example.test/api", n), fmt.Sprintf("legacy-account-%d", n)
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,version) VALUES($1,$2,$3,$4,''::bytea,$5) RETURNING id`, "Legacy "+state, supplier.ProviderZJMF, endpoint, username, 7+n).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		ciphertext, err := supplierSeal(db.ProvisionEncryptionKey, "account", accountID, "supplier-upgrade-private-key")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET credentials_encrypted=$2 WHERE id=$1`, accountID, ciphertext); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents,provision_mode) VALUES($1,$2,1000,'supplier') RETURNING id`, "Upgrade "+state, "supplier-upgrade-"+state).Scan(&productID); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_product_bindings(product_id,supplier_id,provider,upstream_product_id,upstream_revision,billing_cycle,currency,maximum_cost_cents,custom_fields_encrypted) VALUES($1,$2,$3,17,'legacy-revision','monthly','CNY',1000,''::bytea) RETURNING id`, productID, accountID, supplier.ProviderZJMF).Scan(&bindingID); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,status,review_status) VALUES($1,$2,1000,$1,'fixture','paid','approved') RETURNING id`, "SUPPLIER-UPGRADE-"+state, customer).Scan(&orderID); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Legacy supplier service','monthly',1,1000,0,1000) RETURNING id`, orderID, productID).Scan(&itemID); err != nil {
			t.Fatal(err)
		}
		snapshot, err := json.Marshal(supplierSnapshot{SupplierID: accountID, BindingID: bindingID, Provider: supplier.ProviderZJMF})
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,delivery_snapshot,version) VALUES($1,$2,$3,$4,'Legacy delivery',$5,'monthly','provisioning','supplier',$6,11) RETURNING id`, customer, orderID, itemID, productID, "supplier-upgrade-"+state+".example.test", snapshot).Scan(&serviceID); err != nil {
			t.Fatal(err)
		}
		if err = db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,locked_at,external_started_at,result) VALUES($1,'create',$2,$3,3,now(),now(),'{"upstream_id":42,"invoice_id":90,"unknown":true}'::jsonb) RETURNING id`, serviceID, "supplier-upgrade-"+state, state).Scan(&jobID); err != nil {
			t.Fatal(err)
		}
		if _, err = db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=$2 WHERE id=$1`, accountID, jobID); err != nil {
			t.Fatal(err)
		}
		accounts = append(accounts, accountID)
	}
	// The old raw tuple uniqueness allowed equivalent URLs to coexist. Retain
	// even a disabled alias if it still owns an uncertain account reservation.
	var historicalAlias int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,active,running_job_id) SELECT 'Historical URL alias',provider,'https://SUPPLIER-UPGRADE-0.EXAMPLE.TEST:443/api/',username,''::bytea,false,running_job_id FROM supplier_accounts WHERE id=$1 RETURNING id`, accounts[0]).Scan(&historicalAlias); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		var raw string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'accounts',(SELECT jsonb_agg(to_jsonb(a)-'config'-'schema_version'-'credential_format'-'secure_fields'-'identity_key' ORDER BY id) FROM supplier_accounts a),
 'bindings',(SELECT jsonb_agg(to_jsonb(b) ORDER BY id) FROM supplier_product_bindings b),
 'products',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM products p),
 'services',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM services s),
 'jobs',(SELECT jsonb_agg(to_jsonb(j) ORDER BY id) FROM provision_jobs j),
 'orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM orders o),
 'items',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM order_items i),
 'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
 'ledger',(SELECT jsonb_agg(to_jsonb(l) ORDER BY id) FROM balance_ledger l)
 )::text`).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		return raw
	}
	before := snapshot()
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema 165 before migration")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if snapshot() != before {
		t.Fatal("supplier migration changed historical ciphertext, identities or in-flight delivery facts")
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	for n, id := range accounts {
		rec, err := scanSupplierAccount(db.Pool.QueryRow(t.Context(), "SELECT "+supplierAccountColumns+" FROM supplier_accounts a WHERE a.id=$1", id))
		if err != nil || rec.credentialFormat != 0 || len(rec.fields) != 0 || rec.identity != "" || rec.item.SchemaVersion != 1 || rec.item.Version != int64(7+n) || rec.item.RunningJobID == nil {
			t.Fatal("legacy account migration metadata invalid", err)
		}
		account, err := db.supplierRuntimeAccount(rec)
		if err != nil || account.Password != "supplier-upgrade-private-key" || account.BaseURL != fmt.Sprintf("https://supplier-upgrade-%d.example.test/api", n) || account.Username != fmt.Sprintf("legacy-account-%d", n) {
			t.Fatal("legacy account no longer resolves through the shared runtime", err)
		}
		adapter, version, err := db.supplierAdapter(t.Context(), id)
		if n == 0 {
			if conflictCode(t, err) != "supplier_identity_exists" || adapter != nil {
				t.Fatal("historical URL alias bypassed shared upstream reservation")
			}
		} else if err != nil || adapter == nil || version != int64(7+n) {
			t.Fatal("legacy factory resolution failed", err)
		}
	}
	if err := db.Migrate(t.Context()); err != nil || snapshot() != before {
		t.Fatal("repeated supplier migration changed historical facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
	active := true
	in := SupplierInput{Name: "Parallel account", Provider: supplier.ProviderZJMF, BaseURL: "https://supplier-upgrade-0.example.test/api/", Username: "legacy-account-0", Password: "different-private-key", Active: &active, SessionHash: "supplier-upgrade-session"}
	if _, err := db.SaveSupplier(t.Context(), actor, 0, in); conflictCode(t, err) != "supplier_identity_exists" || snapshot() != before {
		t.Fatal("legacy empty identity key allowed a parallel account to bypass its running job")
	}
	if _, err := db.Pool.Exec(t.Context(), `UPDATE supplier_accounts SET running_job_id=NULL WHERE id=$1`, historicalAlias); err != nil {
		t.Fatal(err)
	}
	if adapter, _, err := db.supplierAdapter(t.Context(), accounts[0]); err != nil || adapter == nil {
		t.Fatal("a disabled alias without an outstanding reservation blocked the surviving account", err)
	}
	// An unresolvable historical schema must block identity comparison rather
	// than be skipped and leave a route around the same shared upstream lease.
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,config,schema_version) VALUES('Unresolvable legacy',$1,'https://unresolvable.example.test','legacy',''::bytea,'{"endpoint":"https://unresolvable.example.test","username":"legacy"}',99)`, supplier.ProviderZJMF); err != nil {
		t.Fatal(err)
	}
	in.BaseURL, in.Username = "https://another-supplier.example.test", "different-account"
	if _, err := db.SaveSupplier(t.Context(), actor, 0, in); conflictCode(t, err) != "supplier_identity_unavailable" {
		t.Fatal("unresolvable historical identity was silently ignored")
	}
}
