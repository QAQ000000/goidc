package httpapi

import "testing"

func TestNativeSecurityMigrationPreservesExistingBusiness(t *testing.T) {
	db, h := testServer(t)
	uid, customer := user(t, db, h, "security-migration-customer", "customer")
	pid := product(t, db, 7)
	downloadService(t, db, h, uid, customer, pid, "security-migration-order")
	if _, err := db.Pool.Exec(t.Context(), "UPDATE site_settings SET site_name='迁移前品牌',version=9,registration_enabled=false WHERE id=1"); err != nil {
		t.Fatal(err)
	}
	const snapshot = `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'products',(SELECT jsonb_agg(to_jsonb(p) ORDER BY id) FROM products p),
 'orders',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM orders o),
 'items',(SELECT jsonb_agg(to_jsonb(o) ORDER BY id) FROM order_items o),
 'services',(SELECT jsonb_agg(to_jsonb(s) ORDER BY id) FROM services s),
 'invoices',(SELECT jsonb_agg(to_jsonb(i) ORDER BY id) FROM invoices i),
 'site',(SELECT to_jsonb(s)-'auth_captcha_enabled' FROM site_settings s WHERE id=1))::text`
	var before, after string
	if err := db.Pool.QueryRow(t.Context(), snapshot).Scan(&before); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `DROP TABLE native_configuration_predicates,native_configuration_rules,auth_captcha_challenges;
ALTER TABLE site_settings DROP COLUMN auth_captcha_enabled;
DROP INDEX audit_events_entity_timeline;
DELETE FROM schema_migrations WHERE version IN ('075_native_configuration_rules.sql','076_auth_captcha.sql','077_service_activity_index.sql');`); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), snapshot).Scan(&after); err != nil || before != after {
		t.Fatalf("migration changed existing business, query error: %v", err)
	}
	var enabled bool
	var rules, challenges int
	if err := db.Pool.QueryRow(t.Context(), `SELECT auth_captcha_enabled,(SELECT count(*) FROM native_configuration_rules),(SELECT count(*) FROM auth_captcha_challenges) FROM site_settings WHERE id=1`).Scan(&enabled, &rules, &challenges); err != nil || enabled || rules != 0 || challenges != 0 {
		t.Fatalf("unsafe migration defaults enabled=%v rules=%d challenges=%d err=%v", enabled, rules, challenges, err)
	}
}
