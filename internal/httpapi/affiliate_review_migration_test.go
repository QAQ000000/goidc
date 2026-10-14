package httpapi

import (
	"strings"
	"testing"
)

// TestAffiliateReviewMigrationsAreIdempotentAndRepairable simulates a database
// installed before this fix (no optional rule_set_id, no derived-status
// constraint, no hot foreign-key indexes), then proves the new migrations
// upgrade it and repeat safely.
func TestAffiliateReviewMigrationsAreIdempotentAndRepairable(t *testing.T) {
	db, _ := testServer(t)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	nullable := func() bool {
		t.Helper()
		var nullable string
		if err := db.Pool.QueryRow(t.Context(), `SELECT is_nullable FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='affiliate_turnover_entries' AND column_name='rule_set_id'`).Scan(&nullable); err != nil {
			t.Fatal(err)
		}
		return nullable == "YES"
	}
	constrained := func() bool {
		t.Helper()
		var exists bool
		if err := db.Pool.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM pg_constraint WHERE conname='affiliate_commission_status_derived' AND conrelid='affiliate_commission_entries'::regclass)`).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		return exists
	}
	const indexExists = `SELECT EXISTS(SELECT 1 FROM pg_indexes WHERE schemaname=current_schema() AND tablename=$1 AND indexname=$2)`
	assertIndexes := func(present bool) {
		t.Helper()
		for _, tc := range []struct{ table, index string }{
			{"invoice_items", "invoice_items_invoice"},
			{"order_items", "order_items_order"},
			{"services", "services_product"},
		} {
			var exists bool
			if err := db.Pool.QueryRow(t.Context(), indexExists, tc.table, tc.index).Scan(&exists); err != nil {
				t.Fatal(err)
			}
			if exists != present {
				t.Fatalf("%s on %s exists=%v want %v", tc.index, tc.table, exists, present)
			}
		}
	}
	if !nullable() || !constrained() {
		t.Fatal("fresh schema lacks the review-fix invariants")
	}
	assertIndexes(true)
	// Revert to the pre-fix schema shape and forget the three migrations.
	exec(`ALTER TABLE affiliate_turnover_entries ALTER COLUMN rule_set_id SET NOT NULL`)
	exec(`ALTER TABLE affiliate_commission_entries DROP CONSTRAINT affiliate_commission_status_derived`)
	exec(`DROP INDEX invoice_items_invoice; DROP INDEX order_items_order; DROP INDEX services_product`)
	exec(`DELETE FROM schema_migrations WHERE version IN ('138_affiliate_turnover_optional_rule_set.sql','139_affiliate_commission_status_derived.sql','140_hot_foreign_key_indexes.sql')`)
	if nullable() || constrained() {
		t.Fatal("revert did not take effect")
	}
	assertIndexes(false)
	for attempt := 0; attempt < 2; attempt++ {
		if err := db.Migrate(t.Context()); err != nil {
			t.Fatalf("upgrade/repeat %d: %v", attempt, err)
		}
		if err := db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if !nullable() || !constrained() {
		t.Fatal("re-run did not restore the review-fix invariants")
	}
	assertIndexes(true)
	// The indexes must be usable for the access paths they exist for. Disable
	// sequential scans so the assertion does not depend on table statistics.
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	if _, err = tx.Exec(t.Context(), `SET LOCAL enable_seqscan=off`); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ table, index, query string }{
		{"invoice_items", "invoice_items_invoice", `SELECT id FROM invoice_items WHERE invoice_id=1`},
		{"order_items", "order_items_order", `SELECT id FROM order_items WHERE order_id=1`},
		{"services", "services_product", `SELECT id FROM services WHERE product_id=1`},
	} {
		var plan string
		if err = tx.QueryRow(t.Context(), `EXPLAIN (COSTS OFF) `+tc.query).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, tc.index) {
			t.Fatalf("%s lookup plan: %s", tc.table, plan)
		}
	}
}

// TestAffiliateCommissionStatusConstraintRejectsDrift proves the database, not
// only the trigger, refuses a status that contradicts the bucket columns.
func TestAffiliateCommissionStatusConstraintRejectsDrift(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 1)
	var id int64
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT id FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries DISABLE TRIGGER affiliate_derive_commission_status`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries ENABLE TRIGGER affiliate_derive_commission_status`); err != nil {
			t.Fatal(err)
		}
	}()
	// pending_cents>0 must derive 'pending'; a stale 'reserved' copy is rejected.
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE affiliate_commission_entries SET status='reserved' WHERE id=$1`, id); err == nil {
		t.Fatal("constraint accepted a status contradicting the buckets")
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE affiliate_commission_entries SET status='pending' WHERE id=$1`, id); err != nil {
		t.Fatalf("consistent status rejected: %v", err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries ENABLE TRIGGER affiliate_derive_commission_status`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE affiliate_commission_entries SET status='paid' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if got := assertCommissionStatusDerived(t, f, invoice); got != "pending" {
		t.Fatalf("trigger no longer derives status: %s", got)
	}
}

// TestAffiliateCommissionStatusRepairRealignsStaleRows reverts the constraint,
// plants the stale status copies a pre-fix database could hold, and proves the
// migration repairs them without touching any bucket amount.
func TestAffiliateCommissionStatusRepairRealignsStaleRows(t *testing.T) {
	f := settlementFixture(t)
	invoice, _ := f.purchase(t, 1)
	if _, err := f.db.SettleAffiliateCommissions(t.Context(), 100); err != nil {
		t.Fatal(err)
	}
	var id, buckets string
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT id,pending_cents||'/'||available_cents||'/'||reserved_cents||'/'||paid_cents||'/'||reversed_cents FROM affiliate_commission_entries WHERE invoice_id=$1`, invoice).Scan(&id, &buckets); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries DROP CONSTRAINT affiliate_commission_status_derived`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `DELETE FROM schema_migrations WHERE version='139_affiliate_commission_status_derived.sql'`); err != nil {
		t.Fatal(err)
	}
	// A pre-fix release only moved the buckets, leaving a stale 'pending' copy.
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries DISABLE TRIGGER affiliate_derive_commission_status`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `UPDATE affiliate_commission_entries SET status='pending' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.Pool.Exec(t.Context(), `ALTER TABLE affiliate_commission_entries ENABLE TRIGGER affiliate_derive_commission_status`); err != nil {
		t.Fatal(err)
	}
	if err := f.db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	var status, after string
	var drift int
	if err := f.db.Pool.QueryRow(t.Context(), `SELECT status,pending_cents||'/'||available_cents||'/'||reserved_cents||'/'||paid_cents||'/'||reversed_cents,(status IS DISTINCT FROM `+derivedCommissionStatusSQL+`)::int FROM affiliate_commission_entries WHERE id=$1`, id).Scan(&status, &after, &drift); err != nil {
		t.Fatal(err)
	}
	if status != "available" || drift != 0 {
		t.Fatalf("repair status=%s drift=%d want available/0", status, drift)
	}
	if after != buckets {
		t.Fatalf("repair changed buckets %s -> %s", buckets, after)
	}
}

// TestServiceAndResourceListsFilterImportReviewRequired covers the corrected
// filter: import_review_required is a services flag, so the status filter must
// select the flagged legacy services instead of always returning nothing.
func TestServiceAndResourceListsFilterImportReviewRequired(t *testing.T) {
	db, h := testServer(t)
	_, root := user(t, db, h, "review-filter-root", "super_admin")
	uid, customer := user(t, db, h, "review-filter-customer", "customer")
	pid := product(t, db, 10)
	flagged := downloadService(t, db, h, uid, customer, pid, "review-filter-flagged")
	clean := downloadService(t, db, h, uid, customer, product(t, db, 10), "review-filter-clean")
	if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET import_review_required=true WHERE id=$1`, flagged); err != nil {
		t.Fatal(err)
	}
	query := func(path string) map[string]any {
		t.Helper()
		response := request(h, "GET", path, nil, root, "")
		expect(t, response, 200)
		return data(t, response)
	}
	ids := func(result map[string]any) map[int64]bool {
		t.Helper()
		out := map[int64]bool{}
		for _, item := range result["items"].([]any) {
			out[int64(item.(map[string]any)["id"].(float64))] = true
		}
		return out
	}
	services := query("/api/v1/admin/services/page?status=import_review_required&limit=20")
	serviceIDs := ids(services)
	if services["total"] != float64(1) || !serviceIDs[flagged] || serviceIDs[clean] {
		t.Fatalf("services import_review_required filter=%v", services)
	}
	active := query("/api/v1/admin/services/page?status=active&limit=20")
	if ids(active)[flagged] || ids(active)[clean] {
		t.Fatalf("services active filter leaked flagged rows: %v", active)
	}
	resources := query("/api/v1/admin/resources?status=import_review_required&limit=20")
	resourceIDs := ids(resources)
	if resources["total"] != float64(1) || !resourceIDs[flagged] || resourceIDs[clean] {
		t.Fatalf("resources import_review_required filter=%v", resources)
	}
	// A status CHECK value still filters by the real column.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE services SET status='active' WHERE id=ANY($1)`, []int64{flagged, clean}); err != nil {
		t.Fatal(err)
	}
	if got := ids(query("/api/v1/admin/services/page?status=active&limit=20")); !got[flagged] || !got[clean] {
		t.Fatalf("services active filter=%v", got)
	}
	if got := query("/api/v1/admin/services/page?status=import_review_required&limit=20"); got["total"] != float64(1) {
		t.Fatalf("flag filter must ignore status: %v", got)
	}
}
