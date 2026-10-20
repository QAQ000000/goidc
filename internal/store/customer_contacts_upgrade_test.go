package store

import "testing"

func TestCustomerContactsUpgrade185PreservesRecipients(t *testing.T) {
	db := retiredFeaturesSchema183(t)
	applyNotificationUpgradeMigration(t, db, "184_retire_credit_and_contracts.sql")
	applyNotificationUpgradeMigration(t, db, "185_manual_server_resources.sql")
	var boundary string
	if err := db.Pool.QueryRow(t.Context(), "SELECT max(version) FROM schema_migrations").Scan(&boundary); err != nil || boundary != "185_manual_server_resources.sql" {
		t.Fatal("contact migration must originate at genuine schema185", boundary, err)
	}
	_, customer := topUpFixture(t, db)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO customer_contacts(customer_id,username,email,status,invoice_emails,support_emails,permissions,created_at,updated_at)
 VALUES($1,'Historical recipient','historical-contact@example.test','active',true,true,ARRAY['invoices.read'],'2026-02-03T04:05:06Z','2026-02-04T04:05:06Z')`, customer); err != nil {
		t.Fatal(err)
	}
	snapshot := func() string {
		t.Helper()
		var value string
		if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
   'contacts',(SELECT jsonb_agg(to_jsonb(c)-'version' ORDER BY id) FROM customer_contacts c),
   'settings',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM site_settings s),
   'audits',(SELECT coalesce(jsonb_agg(to_jsonb(a) ORDER BY id),'[]'::jsonb) FROM audit_events a),
   'receipts',(SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY actor_id,scope,idempotency_key),'[]'::jsonb) FROM mutation_operations m))::text`).Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value + retiredFeaturesHistoricalFacts(t, db)
	}
	before := snapshot()
	checksums := supplierExpiryUpgradeMigrations(t, db, "185_manual_server_resources.sql")
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("version-aware runtime accepted predecessor schema")
	}
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if after := snapshot(); after != before {
			t.Fatal("version upgrade changed existing recipient settings, timestamps or other business facts")
		}
		if supplierExpiryUpgradeMigrations(t, db, "185_manual_server_resources.sql") != checksums {
			t.Fatal("upgrade rewrote historical migration checksums")
		}
		var valid bool
		if err := db.Pool.QueryRow(t.Context(), "SELECT count(*)=1 AND bool_and(version=1) FROM customer_contacts").Scan(&valid); err != nil || !valid {
			t.Fatal("historical contacts not initialized at version 1", valid, err)
		}
		if err := db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	assertNotificationUpgradeChecksums(t, db)
}
