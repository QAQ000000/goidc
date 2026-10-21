package store

import (
	"errors"
	"io/fs"
	"sort"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/supplier"
	"github.com/jackc/pgx/v5/pgconn"
)

const serviceTermsPredecessor = "187_customer_referrer_invoice_snapshots.sql"

func TestServiceTermUpgrade187PreservesSnapshotsAndDoesNotInferUpstreamTerms(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	supplierLifecycleUpgradeHistory(t, db)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		if name > cancellationSchema169 && name <= serviceTermsPredecessor {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var existed bool
	if err = db.Pool.QueryRow(t.Context(), "SELECT max(version),to_regclass('service_term_management') IS NOT NULL FROM schema_migrations").Scan(&boundary, &existed); err != nil || boundary != serviceTermsPredecessor || existed {
		t.Fatal("term fixture must originate at genuine schema187", boundary, existed, err)
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, serviceTermsPredecessor)
	configuration := func() string {
		t.Helper()
		var value string
		if err := db.Pool.QueryRow(t.Context(), "SELECT jsonb_agg(jsonb_build_object('id',s.id,'row',external_refund_order_snapshot(s),'configuration',local_renewal_configuration(s)) ORDER BY s.id)::text FROM services s").Scan(&value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	frozen := configuration()
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted schema187")
	}
	for range 2 {
		if err = db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		if before != supplierLifecycleUpgradeFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, serviceTermsPredecessor) || frozen != configuration() {
			t.Fatal("term migration changed business history, full-row financial snapshots or predecessor checksums")
		}
		var observed, effects int
		if err = db.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM service_term_management),(SELECT count(*) FROM service_managed_renewal_effects)").Scan(&observed, &effects); err != nil || observed != 0 || effects != 0 {
			t.Fatal("upgrade invented upstream observations or managed customer entitlements", observed, effects, err)
		}
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestServiceTermDatabaseRejectsUnconfirmedRenewalEffects(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	f := supplierLifecycleUpgradeHistory(t, db)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Run("omitted deadline retains last real observation", func(t *testing.T) {
		tx, err := db.Pool.Begin(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback(t.Context())
		if _, err = tx.Exec(t.Context(), "SELECT id FROM services WHERE id=$1 FOR UPDATE", f.services[4]); err != nil {
			t.Fatal(err)
		}
		mode, err := observeSupplierTerm(t.Context(), tx, f.services[4], nil)
		if err != nil || mode != "follow_supplier" {
			t.Fatal("missing first deadline invented a managed term", mode, err)
		}
		var count int
		if err = tx.QueryRow(t.Context(), "SELECT count(*) FROM service_term_management WHERE service_id=$1", f.services[4]).Scan(&count); err != nil || count != 0 {
			t.Fatal("missing deadline invented an observation", count, err)
		}
		due := time.Date(2027, 3, 31, 1, 2, 3, 0, time.UTC)
		if _, err = observeSupplierTerm(t.Context(), tx, f.services[4], &due); err != nil {
			t.Fatal(err)
		}
		var observedBefore, observedAfter time.Time
		if err = tx.QueryRow(t.Context(), "SELECT supplier_observed_at FROM service_term_management WHERE service_id=$1", f.services[4]).Scan(&observedBefore); err != nil {
			t.Fatal(err)
		}
		if _, err = observeSupplierTerm(t.Context(), tx, f.services[4], nil); err != nil {
			t.Fatal(err)
		}
		var stored time.Time
		if err = tx.QueryRow(t.Context(), "SELECT supplier_next_due_at,supplier_observed_at FROM service_term_management WHERE service_id=$1", f.services[4]).Scan(&stored, &observedAfter); err != nil || !stored.Equal(due) || !observedAfter.Equal(observedBefore) {
			t.Fatal("omitted deadline erased or refreshed a real upstream observation", stored, observedBefore, observedAfter, err)
		}
	})
	for _, cycle := range []struct {
		name   string
		months int
	}{{"monthly", 1}, {"quarterly", 3}, {"semiannually", 6}, {"annually", 12}, {"biennially", 24}, {"triennially", 36}, {"fourly", 48}, {"fively", 60}, {"sixly", 72}, {"sevenly", 84}, {"eightly", 96}, {"ninely", 108}, {"tenly", 120}, {"once", 0}} {
		var actual int
		if err := db.Pool.QueryRow(t.Context(), "SELECT service_term_billing_months($1)", cycle.name).Scan(&actual); err != nil || actual != cycle.months {
			t.Fatal("managed calendar differs from supplier contract", cycle, actual, err)
		}
	}
	for _, item := range []struct{ before, cycle string }{
		{"2027-01-31T09:10:11+08:00", "monthly"},
		{"2028-01-31T09:10:11+08:00", "monthly"},
		{"2028-02-29T09:10:11+08:00", "annually"},
		{"2027-01-30T16:10:11.123456Z", "monthly"},
	} {
		before, err := time.Parse(time.RFC3339Nano, item.before)
		if err != nil {
			t.Fatal(err)
		}
		expected, ok := supplier.BillingPeriodEnd(before, item.cycle)
		if !ok {
			t.Fatal("valid calendar period rejected")
		}
		var actual time.Time
		if err = db.Pool.QueryRow(t.Context(), `SELECT (($1::timestamptz AT TIME ZONE 'Asia/Shanghai')+make_interval(months=>service_term_billing_months($2))) AT TIME ZONE 'Asia/Shanghai'`, before, item.cycle).Scan(&actual); err != nil || !actual.Equal(expected) {
			t.Fatal("managed SQL and upstream calendar disagree at a month boundary", item, expected, actual, err)
		}
	}
	_, err := db.Pool.Exec(t.Context(), `INSERT INTO service_managed_renewal_effects(job_id,service_id,invoice_id,billing_cycle,before_due,period_start,after_due,supplier_after_due)
 VALUES($1,$2,999999,'monthly','2027-01-01T00:00:00Z','2027-01-01T00:00:00Z','2027-02-01T00:00:00Z','2027-02-01T00:00:00Z')`, f.jobs[4], f.services[4])
	var pg *pgconn.PgError
	if !errors.As(err, &pg) || pg.Code != "23514" || pg.Message != "managed renewal effect source invalid" {
		t.Fatal("unconfirmed unrelated lifecycle job manufactured a customer renewal", err)
	}
	var effects int
	if err = db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM service_managed_renewal_effects").Scan(&effects); err != nil || effects != 0 {
		t.Fatal("rejected source left an entitlement fact", effects, err)
	}
}
