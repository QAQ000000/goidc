package store

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// isolatedTestStore mirrors the httpapi isolation contract: an isolated database
// whose name ends in _test, plus a random schema created for the single test.
func isolatedTestStore(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("ZJMFGO_TEST_DATABASE_URL")
	if dsn == "" {
		if os.Getenv("REQUIRE_INTEGRATION") == "1" {
			t.Fatal("ZJMFGO_TEST_DATABASE_URL required")
		}
		t.Skip("requires isolated PostgreSQL test database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal("invalid test DSN")
	}
	if !strings.HasSuffix(cfg.ConnConfig.Database, "_test") {
		t.Fatal("refusing database without _test suffix")
	}
	ctx := context.Background()
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 8)
	if _, err = rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	schema := "store_topup_" + hex.EncodeToString(raw)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		base.Close()
		t.Fatal(err)
	}
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
	if err != nil {
		base.Close()
		t.Fatal(err)
	}
	db := &Store{Pool: pool}
	t.Cleanup(func() {
		db.Close()
		_, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		base.Close()
	})
	if err = db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err = db.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

func topUpFixture(t *testing.T, db *Store) (actorID, customerID int64) {
	t.Helper()
	ctx := t.Context()
	if err := db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('topup-root','topup-root@example.test','unused','Topup root','super_admin') RETURNING id").Scan(&actorID); err != nil {
		t.Fatal(err)
	}
	if err := db.Pool.QueryRow(ctx, "INSERT INTO users(username,email,password_hash,display_name,role) VALUES('topup-customer','topup-customer@example.test','unused','Topup customer','customer') RETURNING id").Scan(&customerID); err != nil {
		t.Fatal(err)
	}
	return actorID, customerID
}

func conflictCode(t *testing.T, err error) string {
	t.Helper()
	var c *Conflict
	if !errors.As(err, &c) {
		t.Fatalf("expected conflict, got %v", err)
	}
	return c.Code
}

func TestTopUpEnforcesFinanceSettingsTransactionally(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, customerID := topUpFixture(t, db)
	ctx := t.Context()

	// Migration 110 defaults must keep the legacy behaviour: top-ups allowed.
	balance, replayed, err := db.TopUp(ctx, actorID, customerID, 2000, "topup-default-1", "")
	if err != nil || replayed || balance != 2000 {
		t.Fatalf("defaults rejected topup: balance=%d replayed=%v err=%v", balance, replayed, err)
	}

	// topup_enabled=false refuses with a dedicated, actionable code.
	if _, err = db.Pool.Exec(ctx, "UPDATE finance_settings SET topup_enabled=false"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.TopUp(ctx, actorID, customerID, 2000, "topup-disabled-1", ""); conflictCode(t, err) != "topup_disabled" {
		t.Fatalf("disabled topup err=%v", err)
	}

	// Range and ceiling are enforced independently and reported distinctly.
	if _, err = db.Pool.Exec(ctx, "UPDATE finance_settings SET topup_enabled=true,min_topup_cents=500,max_topup_cents=5000,max_balance_cents=6000"); err != nil {
		t.Fatal(err)
	}
	if _, _, err = db.TopUp(ctx, actorID, customerID, 499, "topup-min-1", ""); conflictCode(t, err) != "topup_below_minimum" {
		t.Fatalf("min topup err=%v", err)
	}
	if _, _, err = db.TopUp(ctx, actorID, customerID, 5001, "topup-max-1", ""); conflictCode(t, err) != "topup_above_maximum" {
		t.Fatalf("max topup err=%v", err)
	}
	// 2000 + 4001 > 6000 while staying inside the per-transaction range.
	if _, _, err = db.TopUp(ctx, actorID, customerID, 4001, "topup-balance-1", ""); conflictCode(t, err) != "topup_balance_limit" {
		t.Fatalf("balance limit err=%v", err)
	}
	if balance, _, err = db.TopUp(ctx, actorID, customerID, 4000, "topup-limit-ok", ""); err != nil || balance != 6000 {
		t.Fatalf("boundary topup rejected: balance=%d err=%v", balance, err)
	}
	var ledger int
	if err = db.Pool.QueryRow(ctx, "SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND reference_type='admin_topup'", customerID).Scan(&ledger); err != nil || ledger != 2 {
		t.Fatalf("rejected topups must not write ledger rows: count=%d err=%v", ledger, err)
	}

	// max_balance_cents=0 means "no ceiling" (the migration default).
	if _, err = db.Pool.Exec(ctx, "UPDATE finance_settings SET max_balance_cents=0,max_topup_cents=100000000"); err != nil {
		t.Fatal(err)
	}
	if balance, _, err = db.TopUp(ctx, actorID, customerID, 500, "topup-nocap-1", ""); err != nil || balance != 6500 {
		t.Fatalf("zero ceiling blocked topup: balance=%d err=%v", balance, err)
	}

	// Idempotent replay of an already-committed top-up keeps working even
	// though the moment it was written the limits were stricter.
	if _, err = db.Pool.Exec(ctx, "UPDATE finance_settings SET max_balance_cents=6000"); err != nil {
		t.Fatal(err)
	}
	if replayBalance, replayed, err := db.TopUp(ctx, actorID, customerID, 4000, "topup-limit-ok", ""); err != nil || !replayed || replayBalance != 6000 {
		t.Fatalf("idempotent replay broke after settings change: balance=%d replayed=%v err=%v", replayBalance, replayed, err)
	}
}

func TestTopUpRequireActiveOrderAndHardcodedDefences(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, customerID := topUpFixture(t, db)
	ctx := t.Context()

	if _, err := db.Pool.Exec(ctx, "UPDATE finance_settings SET require_active_order=true"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.TopUp(ctx, actorID, customerID, 1000, "topup-order-missing", ""); conflictCode(t, err) != "topup_active_order_required" {
		t.Fatalf("missing active order err=%v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('topup-paid',$1,'paid',1000,'topup-paid','fixture')`, customerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.TopUp(ctx, actorID, customerID, 1000, "topup-order-paid", ""); conflictCode(t, err) != "topup_active_order_required" {
		t.Fatalf("paid order satisfied active-order rule: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `INSERT INTO orders(order_no,user_id,status,total_cents,idempotency_key,request_hash) VALUES('topup-active',$1,'active',1000,'topup-active','fixture')`, customerID); err != nil {
		t.Fatal(err)
	}
	if balance, _, err := db.TopUp(ctx, actorID, customerID, 1000, "topup-order-active", ""); err != nil || balance != 1000 {
		t.Fatalf("active order did not permit topup: balance=%d err=%v", balance, err)
	}

	// Hardcoded last-defence bounds survive the settings rollout.
	if _, _, err := db.TopUp(ctx, actorID, customerID, 0, "topup-zero", ""); conflictCode(t, err) != "invalid_topup" {
		t.Fatalf("zero amount err=%v", err)
	}
	if _, _, err := db.TopUp(ctx, actorID, customerID, 10000000001, "topup-too-big", ""); conflictCode(t, err) != "invalid_topup" {
		t.Fatalf("oversized amount err=%v", err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE finance_settings SET max_topup_cents=10000000000,max_balance_cents=0,require_active_order=false"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(ctx, "UPDATE users SET balance_cents=9223372036854775807-5 WHERE id=$1", customerID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.TopUp(ctx, actorID, customerID, 10000000000, "topup-overflow", ""); conflictCode(t, err) != "balance_overflow" {
		t.Fatalf("overflow protection lost: %v", err)
	}
}
