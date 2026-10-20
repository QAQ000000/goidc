package store

import (
	"io/fs"
	"sort"
	"strings"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
)

const membershipVersionPredecessor = "182_separate_administrator_accounts.sql"

// Build the genuine predecessor from its embedded SQL, never by removing the
// new metadata from a current database. Order SQL below has the unchanged 182
// shape and writes a synthetic pre-upgrade purchase, rather than an old binary.
func customerGroupMembershipSchema182(t *testing.T) *Store {
	t.Helper()
	db := adminOrderReceiptSchema178(t)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name > adminOrderReceiptPredecessor && name <= membershipVersionPredecessor {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var newTable bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('customer_group_membership_versions') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &newTable); err != nil || boundary != membershipVersionPredecessor || newTable {
		t.Fatal("fixture did not originate at untouched schema182", boundary, newTable, err)
	}
	return db
}

func membershipUpgradeFrozenPurchase(t *testing.T, db *Store) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
 'orders',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM orders x),
 'items',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM order_items x),
 'prices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY order_item_id) FROM order_item_pricing x),
 'services',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM services x),
 'invoices',(SELECT jsonb_agg(to_jsonb(x) ORDER BY id) FROM invoices x),
 'receipts',(SELECT jsonb_agg(to_jsonb(x) ORDER BY actor_id,scope,idempotency_key) FROM mutation_operations x WHERE scope='admin:order.create')
 )::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestCustomerGroupMembershipUpgrade182PreservesHistoryAndInitializesIndependentVersions(t *testing.T) {
	db := customerGroupMembershipSchema182(t)
	actor, ungrouped := topUpFixture(t, db)
	session := strings.Repeat("d", 64)
	if _, err := db.Pool.Exec(t.Context(), "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '1 day')", session, actor); err != nil {
		t.Fatal(err)
	}
	ctx := WithStorageCredential(t.Context(), session, false)
	f := purchasePricingLockData(t, db, "membership-upgrade-historical", true)
	quote, err := db.QuoteAdminOrder(t.Context(), actor, f.input, PaymentConfiguration{})
	if err != nil {
		t.Fatal(err)
	}
	f.input.QuoteHash = quote.QuoteHash
	order, err := db.CreateAdminOrder(t.Context(), actor, "membership-pre183-order", f.input, PaymentConfiguration{})
	if err != nil || order.OrderID < 1 {
		t.Fatal("pre183 purchase fixture failed", order, err)
	}
	oldFacts := adminOrderReceiptMigrationFacts(t, db)
	frozen := membershipUpgradeFrozenPurchase(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, membershipVersionPredecessor)
	var oldMemberships string
	if err = db.Pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY customer_id),'[]'::jsonb)::text FROM customer_group_members x`).Scan(&oldMemberships); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema182")
	}
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if oldFacts != adminOrderReceiptMigrationFacts(t, db) || frozen != membershipUpgradeFrozenPurchase(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, membershipVersionPredecessor) {
		t.Fatal("membership migration changed account rows, purchase snapshots, receipts or old checksums")
	}
	var afterMemberships string
	var metadata, customers, nonInitial int
	if err = db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY customer_id),'[]'::jsonb)::text FROM customer_group_members x),
 (SELECT count(*) FROM customer_group_membership_versions),
 (SELECT count(*) FROM users),
 (SELECT count(*) FROM customer_group_membership_versions WHERE version<>1)`).Scan(&afterMemberships, &metadata, &customers, &nonInitial); err != nil || oldMemberships != afterMemberships || metadata != customers || nonInitial != 0 {
		t.Fatal("upgrade inferred membership history or omitted customers", metadata, customers, nonInitial, err)
	}
	if err = db.Migrate(t.Context()); err != nil || oldFacts != adminOrderReceiptMigrationFacts(t, db) {
		t.Fatal("repeat migration changed historical rows", err)
	}
	if err = db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertNotificationUpgradeChecksums(t, db)
	current, err := db.AdminCustomerGroupMembership(ctx, actor, f.customer)
	if err != nil || current["group_membership_version"] != int64(1) {
		t.Fatal("existing membership did not receive version one", current, err)
	}
	clear := AdminCustomerGroupAssignmentInput{GroupIDPresent: true, ExpectedVersion: 1}
	cleared, err := db.AssignAdminCustomerGroup(ctx, actor, f.customer, "membership-post183-clear", clear)
	if err != nil || cleared["group_membership_version"] != int64(2) {
		t.Fatal("post-upgrade clear did not retain its version", cleared, err)
	}
	if frozen != membershipUpgradeFrozenPurchase(t, db) {
		t.Fatal("post-upgrade assignment recalculated historical orders or contract prices")
	}
	replay, err := db.CreateAdminOrder(t.Context(), actor, "membership-pre183-order", f.input, PaymentConfiguration{})
	if err != nil || !replay.Replayed || replay.OrderID != order.OrderID || replay.TotalCents != order.TotalCents || frozen != membershipUpgradeFrozenPurchase(t, db) {
		t.Fatal("pre183 order receipt stopped replaying after membership change", replay, err)
	}
	// Raw/import writers use the same version trigger. Clearing keeps a
	// tombstone version, and assigning the same group is not a new fact.
	assertVersion := func(customer, want int64) {
		t.Helper()
		var version, sales int64
		if e := db.Pool.QueryRow(t.Context(), `SELECT v.version,u.sales_version FROM customer_group_membership_versions v JOIN users u ON u.id=v.customer_id WHERE v.customer_id=$1`, customer).Scan(&version, &sales); e != nil || version != want || sales != 1 {
			t.Fatal("independent membership version mismatch", version, sales, want, e)
		}
	}
	assertVersion(ungrouped, 1)
	for _, step := range []struct {
		query string
		want  int64
	}{
		{"INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)", 2},
		{"UPDATE customer_group_members SET group_id=$2 WHERE customer_id=$1", 2},
		{"DELETE FROM customer_group_members WHERE customer_id=$1 AND group_id=$2", 3},
		{"INSERT INTO customer_group_members(customer_id,group_id) VALUES($1,$2)", 4},
	} {
		if _, err = db.Pool.Exec(t.Context(), step.query, ungrouped, f.group); err != nil {
			t.Fatal(err)
		}
		assertVersion(ungrouped, step.want)
	}
	var born int64
	if err = db.Pool.QueryRow(t.Context(), `INSERT INTO users(username,email,password_hash,display_name) VALUES('membership-born','membership-born@example.test','unused','Born') RETURNING id`).Scan(&born); err != nil {
		t.Fatal(err)
	}
	assertVersion(born, 1)
	if err = db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	assertVersion(ungrouped, 4)
	assertVersion(f.customer, 2)
	if frozen != membershipUpgradeFrozenPurchase(t, db) {
		t.Fatal("repeat migration changed frozen purchase evidence")
	}
}
