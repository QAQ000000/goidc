package store

import "testing"

const retiredFeaturesPredecessor = "183_customer_group_membership_versions.sql"

func retiredFeaturesSchema183(t *testing.T) *Store {
	t.Helper()
	db := customerGroupMembershipSchema182(t)
	applyNotificationUpgradeMigration(t, db, retiredFeaturesPredecessor)
	var boundary string
	if err := db.Pool.QueryRow(t.Context(), "SELECT max(version) FROM schema_migrations").Scan(&boundary); err != nil || boundary != retiredFeaturesPredecessor {
		t.Fatal("retirement fixture did not originate at genuine schema183", boundary, err)
	}
	return db
}

// Capture whole historical rows, including immutable documents, receipts,
// account versions and timestamps. Settings are compared separately because
// retiring the two features intentionally changes only their active switches.
func retiredFeaturesHistoricalFacts(t *testing.T, db *Store) string {
	t.Helper()
	var facts string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM users x),
 'administrators',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM administrators x),
 'accounts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY user_id) FROM customer_credit_accounts x),
 'credit_events',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM customer_credit_events x),
 'credit_payments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY invoice_id) FROM credit_invoice_payments x),
 'repayments',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM credit_repayments x),
 'templates',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM contract_templates x),
 'contracts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM contracts x),
 'contract_history',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM contract_history x),
 'documents',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM contract_documents x),
 'contract_receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,operation,idempotency_key) FROM contract_receipts x),
 'operations',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x),
 'audit',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM audit_events x),
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'order_items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'ledger',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM balance_ledger x)
 )::text`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func retiredFeaturesSettingsFacts(t *testing.T, db *Store) string {
	t.Helper()
	var facts string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'credit',(SELECT to_jsonb(x)-'enabled'-'auto_recharge'-'version'-'updated_at' FROM credit_settings x WHERE id=1),
 'contracts',(SELECT to_jsonb(x)-'enabled'-'version'-'updated_at' FROM contract_settings x WHERE id=1),
 'site',(SELECT (to_jsonb(x)-'version'-'updated_at'-'admin_settings')||jsonb_build_object('admin_settings',admin_settings-'frontend_credit_enabled') FROM site_settings x WHERE id=1)
 )::text`).Scan(&facts); err != nil {
		t.Fatal(err)
	}
	return facts
}

func TestRetiredFeaturesUpgrade183PreservesHistory(t *testing.T) {
	db := retiredFeaturesSchema183(t)
	actor, customer := topUpFixture(t, db)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), query, args...); err != nil {
			t.Fatal(err)
		}
	}
	// These are synthetic pre-upgrade SQL facts, not writes through a retired
	// runtime, a production dump or evidence of an old binary deployment.
	exec("UPDATE credit_settings SET enabled=true,auto_recharge=true,limit_cents=9000,version=11 WHERE id=1")
	exec("UPDATE contract_settings SET enabled=true,version=21 WHERE id=1")
	exec("UPDATE site_settings SET admin_settings=jsonb_set(admin_settings,'{frontend_credit_enabled}','true'::jsonb),version=31 WHERE id=1")
	exec("UPDATE customer_credit_accounts SET limit_cents=5000,used_cents=600,version=7,frozen=true WHERE user_id=$1", customer)
	var product, order, item, invoice, service, template, contract int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO products(name,slug,price_cents,stock) VALUES('历史商品','retired-historical-product',1000,3) RETURNING id`).Scan(&product); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash)
 VALUES('retired-historical-order',$1,'paid',1000,'retired-historical-order',repeat('1',64)) RETURNING id`, customer).Scan(&order); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents)
 VALUES($1,$2,'历史成交商品','monthly',1,1000,0,1000) RETURNING id`, order, product).Scan(&item); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,order_id,status,amount_cents,paid_at,version)
 VALUES('retired-historical-invoice',$1,$2,'paid',1000,'2024-02-01T00:00:00+08',4) RETURNING id`, customer, order).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,next_due_at)
 VALUES($1,$2,$3,$4,'历史服务','retired-historical.test','monthly','active','2024-03-01T00:00:00+08') RETURNING id`, customer, order, item, product).Scan(&service); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO credit_invoice_payments(invoice_id,user_id,amount_cents,remaining_cents,expected_version,version,idempotency_key)
 VALUES($1,$2,1000,600,3,2,'retired-historical-credit-payment')`, invoice, customer)
	exec(`INSERT INTO credit_repayments(invoice_id,user_id,actor_id,amount_cents,receipt,received_at,idempotency_key,expected_version,version_after,remaining_after)
 VALUES($1,$2,$3,400,'retired-bank-receipt','2024-02-02T00:00:00+08','retired-historical-repayment',1,2,600)`, invoice, customer, actor)
	exec(`INSERT INTO customer_credit_events(user_id,actor_id,event_type,delta_limit_cents,limit_after_cents,used_after_cents,expected_version,version_after,idempotency_key,note)
 VALUES($1,$2,'limit_adjustment',5000,5000,0,1,2,'retired-historical-limit','历史授信')`, customer, actor)
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO contract_templates(name,content,version) VALUES('历史模板','合同原始模板 {{customer_name}}',3) RETURNING id`).Scan(&template); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO contracts(customer_id,service_id,template_id,template_name,customer_name,customer_email,product_name,service_name,domain,paid_at,content,party_snapshot,service_snapshot,status,signer_name,version)
 VALUES($1,$2,$3,'已冻结模板名','历史客户','historical@example.test','已冻结商品','已冻结服务','retired-historical.test','2024-02-01T00:00:00+08','已签署不可变正文','{"company":"frozen"}','{"service":"frozen"}','approved','历史签署人',2) RETURNING id`, customer, service, template).Scan(&contract); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO contract_history(contract_id,actor_id,action,before_status,after_status,note,version)
 VALUES($1,$2,'reviewed','pending','approved','历史审批',2)`, contract, actor)
	exec(`INSERT INTO contract_documents(contract_id,version,content,signer_name,status)
 VALUES($1,2,'已签署不可变正文','历史签署人','approved')`, contract)
	exec(`INSERT INTO contract_receipts(actor_id,operation,idempotency_key,request_hash,result)
 VALUES($1,'POST /api/v1/contracts','retired-historical-contract',repeat('2',64),jsonb_build_object('id',$2::bigint))`, customer, contract)
	exec(`INSERT INTO mutation_operations(actor_id,scope,idempotency_key,request_hash,result)
 VALUES($1,'credit-freeze','retired-historical-freeze',repeat('3',64),'{"version":7}'),
 ($1,'admin:contract.settings','retired-historical-settings',repeat('4',64),'{"version":21}')`, actor)
	exec(`INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload)
 VALUES($1,'credit.repayment_confirmed','invoice',$2::bigint::text,'{"amount_cents":400}'),
 ($1,'contract.reviewed','contract',$3::bigint::text,'{"version":2}')`, actor, invoice, contract)
	facts := retiredFeaturesHistoricalFacts(t, db)
	settingsFacts := retiredFeaturesSettingsFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, retiredFeaturesPredecessor)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("retired runtime accepted schema183")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRetirement := func() {
		t.Helper()
		var creditEnabled, autoRecharge, contractEnabled, displayEnabled bool
		var creditVersion, contractVersion, siteVersion, creditLimit int64
		if err := db.Pool.QueryRow(t.Context(), `SELECT c.enabled,c.auto_recharge,c.version,c.limit_cents,k.enabled,k.version,
 (s.admin_settings->>'frontend_credit_enabled')::boolean,s.version
 FROM credit_settings c CROSS JOIN contract_settings k CROSS JOIN site_settings s WHERE c.id=1 AND k.id=1 AND s.id=1`).Scan(&creditEnabled, &autoRecharge, &creditVersion, &creditLimit, &contractEnabled, &contractVersion, &displayEnabled, &siteVersion); err != nil {
			t.Fatal(err)
		}
		if creditEnabled || autoRecharge || contractEnabled || displayEnabled || creditVersion != 12 || contractVersion != 22 || siteVersion != 32 || creditLimit != 9000 {
			t.Fatalf("retirement changed wrong settings or repeated versions: credit=%t/%t v%d limit=%d contract=%t v%d display=%t siteVersion=%d", creditEnabled, autoRecharge, creditVersion, creditLimit, contractEnabled, contractVersion, displayEnabled, siteVersion)
		}
		if facts != retiredFeaturesHistoricalFacts(t, db) || settingsFacts != retiredFeaturesSettingsFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, retiredFeaturesPredecessor) {
			t.Fatal("retirement changed historical debt, payment, contract snapshot, receipt, audit, other settings or old migration checksums")
		}
	}
	assertRetirement()
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertRetirement()
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
