package store

import "testing"

// Start from the actual predecessor schema: a key that resembles an automatic
// request must never gain authority to spend a customer's balance on upgrade.
func TestAutoRenewalUpgrade170PreservesHistoryAndDoesNotInferSource(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	applyNotificationUpgradeMigration(t, db, supplierLifecycleMigration170)
	f := supplierLifecycleUpgradeHistory(t, db)
	var boundary string
	var present bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),
to_regclass('auto_renewal_invoice_sources') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &present); err != nil || boundary != supplierLifecycleMigration170 || present {
		t.Fatal("fixture did not start at actual schema170", boundary, present, err)
	}
	var invoice int64
	if err := db.Pool.QueryRow(t.Context(), `INSERT INTO invoices(invoice_no,user_id,amount_cents,idempotency_key)
SELECT 'AUTO-UPGRADE-HISTORY',user_id,1000,'auto-renew-historical-customer-chosen-key'
FROM services WHERE id=$1 RETURNING id`, f.services[0]).Scan(&invoice); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO invoice_items(invoice_id,description,amount_cents,item_type,service_id)
VALUES($1,'Historical unpaid renewal',1000,'renew',$2)`, invoice, f.services[0]); err != nil {
		t.Fatal(err)
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted predecessor schema before upgrade")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	var sources int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM auto_renewal_invoice_sources").Scan(&sources); err != nil || sources != 0 {
		t.Fatal("historical invoice key was inferred to be an automatic spending source", sources, err)
	}
	if actual := supplierLifecycleUpgradeFacts(t, db); actual != before {
		t.Fatal("upgrade changed original financial, service, job, lease or account facts")
	}
	if err := db.Migrate(t.Context()); err != nil || supplierLifecycleUpgradeFacts(t, db) != before {
		t.Fatal("repeat migration changed original facts", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}
