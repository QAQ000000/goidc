package store

import (
	"strings"
	"testing"
)

func authenticationMigrationFacts(t *testing.T, db *Store) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'users',(SELECT jsonb_agg(to_jsonb(u) ORDER BY id) FROM users u),
 'sessions',(SELECT jsonb_agg(to_jsonb(s) ORDER BY token_hash) FROM sessions s),
 'auth_settings',(SELECT to_jsonb(a) FROM auth_settings a WHERE id=1)
 )::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestAuthenticationPortalUpgrade180InvalidatesOnlyUnboundChallenges(t *testing.T) {
	db := adminOrderReceiptSchema178(t)
	applyNotificationUpgradeMigration(t, db, "179_admin_order_mutation_receipts.sql")
	applyNotificationUpgradeMigration(t, db, "180_customer_group_pricing.sql")
	actor, customer := topUpFixture(t, db)
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 hour')`, strings.Repeat("a", 64), actor); err != nil {
		t.Fatal(err)
	}
	for i, userID := range []int64{actor, customer} {
		hash := strings.Repeat(string(rune('b'+i)), 64)
		if _, err := db.Pool.Exec(t.Context(), `INSERT INTO mfa_challenges(token_hash,user_id,login_method,expires_at) VALUES($1,$2,'email',now()+interval '5 minutes')`, hash, userID); err != nil {
			t.Fatal(err)
		}
	}
	before := authenticationMigrationFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, "180_customer_group_pricing.sql")
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted schema180 without portal migration")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if authenticationMigrationFacts(t, db) != before || supplierExpiryUpgradeMigrations(t, db, "180_customer_group_pricing.sql") != checksums {
		t.Fatal("portal migration changed accounts, sessions, settings or historical migration checksums")
	}
	var challenges int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mfa_challenges").Scan(&challenges); err != nil || challenges != 0 {
		t.Fatal("unbound old MFA challenges survived", challenges, err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO mfa_challenges(token_hash,user_id,login_method,expires_at) VALUES($1,$2,'email',now()+interval '5 minutes')`, strings.Repeat("d", 64), customer); err == nil {
		t.Fatal("old writer created an unbound challenge")
	}
	if err := db.CreateAuthenticationMFAChallenge(t.Context(), customer, "email", "customer", strings.Repeat("e", 64)); err != nil {
		t.Fatal(err)
	}
	var portal string
	if err := db.Pool.QueryRow(t.Context(), "SELECT login_portal FROM mfa_challenges WHERE user_id=$1", customer).Scan(&portal); err != nil || portal != "customer" {
		t.Fatal("new writer did not bind login audience", portal, err)
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM mfa_challenges").Scan(&challenges); err != nil || challenges != 1 || authenticationMigrationFacts(t, db) != before {
		t.Fatal("repeat migration removed new challenges or changed sessions", challenges, err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
}
