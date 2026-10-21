package store

import (
	"errors"
	"io/fs"
	"sort"
	"testing"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestServiceOwnershipUpgrade190PreservesHistoryAndRowShape(t *testing.T) {
	db := supplierLifecycleSchema169(t)
	history := supplierLifecycleUpgradeHistory(t, db)
	names, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(names)
	const predecessor = "190_manual_collections.sql"
	for _, name := range names {
		if name > cancellationSchema169 && name <= predecessor {
			applyNotificationUpgradeMigration(t, db, name)
		}
	}
	var boundary string
	var exists bool
	if err = db.Pool.QueryRow(t.Context(), `SELECT max(version),to_regclass('service_ownership_transfers') IS NOT NULL FROM schema_migrations`).Scan(&boundary, &exists); err != nil || boundary != predecessor || exists {
		t.Fatal("not a genuine schema190 predecessor", boundary, exists, err)
	}
	if _, err = db.Pool.Exec(t.Context(), `INSERT INTO notification_delivery_events(service_id,user_id,channel,kind,stage,due_date) SELECT id,user_id,'email','service_provision_success',0,CURRENT_DATE FROM services WHERE id=$1`, history.services[0]); err != nil {
		t.Fatal(err)
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	checksums := supplierExpiryUpgradeMigrations(t, db, predecessor)
	var legacy string
	if err = db.Pool.QueryRow(t.Context(), `SELECT jsonb_agg(to_jsonb(n) ORDER BY id)::text FROM notification_delivery_events n`).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSchema(t.Context()); err == nil {
		t.Fatal("new runtime accepted schema190")
	}
	for range 2 {
		if err = db.Migrate(t.Context()); err != nil {
			t.Fatal(err)
		}
		if err = db.CheckSchema(t.Context()); err != nil {
			t.Fatal(err)
		}
		if before != supplierLifecycleUpgradeFacts(t, db) || checksums != supplierExpiryUpgradeMigrations(t, db, predecessor) {
			t.Fatal("ownership migration changed existing service shape, finance or predecessor checksums")
		}
		var count int
		var after string
		if err = db.Pool.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM service_ownership_transfers),(SELECT jsonb_agg(to_jsonb(n)-'ownership_generation' ORDER BY id)::text FROM notification_delivery_events n)`).Scan(&count, &after); err != nil || count != 0 || legacy != after {
			t.Fatal("upgrade inferred ownership or changed delivery history", count, err)
		}
		if err = db.Pool.QueryRow(t.Context(), `SELECT count(*) FROM notification_delivery_events WHERE ownership_generation<>0`).Scan(&count); err != nil || count != 0 {
			t.Fatal("old notifications acquired a new ownership period", count, err)
		}
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestServiceOwnershipDatabaseFactsAndDirectOwnerGuard(t *testing.T) {
	f := newOwnershipReadFixture(t)
	reject := func(query string, args ...any) {
		t.Helper()
		_, err := f.db.Pool.Exec(f.ctx, query, args...)
		var pg *pgconn.PgError
		if !errors.As(err, &pg) || pg.Code != "23514" {
			t.Fatal("database accepted unpaired owner mutation or mutable ownership fact", err)
		}
	}
	reject(`UPDATE services SET user_id=$2,version=version+1 WHERE id=$1`, f.service, f.other)
	f.transfer(t, f.other, "ownership-database-guard")
	reject(`UPDATE service_ownership_transfers SET reason='changed' WHERE service_id=$1`, f.service)
	reject(`DELETE FROM service_ownership_transfers WHERE service_id=$1`, f.service)
	reject(`UPDATE services SET user_id=$2,version=version+1 WHERE id=$1`, f.service, f.customer)
	var owner, generation int64
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT user_id,service_ownership_generation(id) FROM services WHERE id=$1`, f.service).Scan(&owner, &generation); err != nil || owner != f.other || generation != 1 {
		t.Fatal("rejected mutations changed committed ownership", owner, generation, err)
	}
}

func TestServiceOwnershipRenewalRefundCannotCrossRoundTrip(t *testing.T) {
	f := newOwnershipReadFixture(t)
	f.exec(t, `UPDATE users SET balance_cents=100000 WHERE id=$1`, f.customer)
	payRenewal := func(key string) int64 {
		t.Helper()
		r, err := f.db.CreateRenewalInvoice(f.ctx, f.customer, f.service, key+"-create")
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.PayInvoice(f.ctx, f.customer, r.InvoiceID, key+"-pay"); err != nil {
			t.Fatal(err)
		}
		return r.InvoiceID
	}
	reason := func(invoice int64) string {
		t.Helper()
		var code string
		if err := f.db.Pool.QueryRow(f.ctx, `SELECT local_renewal_refund_reason($1,NULL,false)`, invoice).Scan(&code); err != nil {
			t.Fatal(err)
		}
		return code
	}
	old := payRenewal("ownership-refund-original")
	if code := reason(old); code != "" {
		t.Fatal("generation-zero renewal should remain refundable", code)
	}
	f.transfer(t, f.other, "ownership-refund-outward")
	if code := reason(old); code != "renewal_service_state" {
		t.Fatal("another customer's entitlement became reversible", code)
	}
	f.transfer(t, f.customer, "ownership-refund-return")
	if code := reason(old); code != "renewal_effect_changed" {
		t.Fatal("round trip revived an earlier renewal", code)
	}
	if _, err := f.db.RefundInvoice(f.ctx, f.actor, old, "ownership-old-refund"); conflictCode(t, err) != "renewal_effect_changed" {
		t.Fatal("old renewal refund crossed ownership periods", err)
	}
	fresh := payRenewal("ownership-refund-current")
	if code := reason(fresh); code != "" {
		t.Fatal("new ownership renewal lost its own refund path", code)
	}
	var generation int64
	if err := f.db.Pool.QueryRow(f.ctx, `SELECT (configuration->>'_ownership_generation')::bigint FROM service_renewal_effects WHERE invoice_id=$1`, fresh).Scan(&generation); err != nil || generation != 2 {
		t.Fatal("new financial effect did not freeze its ownership generation", generation, err)
	}
	if _, err := f.db.RefundInvoice(f.ctx, f.actor, fresh, "ownership-new-refund"); err != nil {
		t.Fatal("new ownership renewal could not refund normally", err)
	}
	if code := reason(old); code != "renewal_effect_changed" {
		t.Fatal("refunding the successor revived a previous ownership period", code)
	}
}
