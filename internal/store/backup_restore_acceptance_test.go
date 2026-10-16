package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/db/migrations"
	"github.com/codex/zjmfgo/internal/payment"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestCurrentSchemaRepresentativeBackupRestore is deliberately opt-in. Both databases
// must already exist, be dedicated to this run, and have different _test names.
// It owns random schemas only; it never creates or drops a database.
func TestCurrentSchemaRepresentativeBackupRestore(t *testing.T) {
	if os.Getenv("ZJMFGO_BACKUP_RESTORE_ACCEPTANCE") != "1" {
		t.Skip("set ZJMFGO_BACKUP_RESTORE_ACCEPTANCE=1 and two dedicated loopback PostgreSQL 18 _test DSNs")
	}
	sourceCfg := recoveryConfig(t, "ZJMFGO_TEST_DATABASE_URL")
	targetCfg := recoveryConfig(t, "ZJMFGO_RESTORE_TEST_DATABASE_URL")
	if sourceCfg.ConnConfig.Database == targetCfg.ConnConfig.Database {
		t.Fatal("source and restore database names must differ")
	}
	sourceBase := recoveryPreflight(t, sourceCfg)
	targetBase := recoveryPreflight(t, targetCfg)
	recoveryRequireEmpty(t, targetBase)

	db, ctx, actor, uid, first, firstPayment := topupLiabilityFixture(t)
	if _, err := db.Pool.Exec(ctx, `UPDATE finance_settings SET max_balance_cents=0`); err != nil {
		t.Fatal(err)
	}
	var schema string
	if err := db.Pool.QueryRow(ctx, `SELECT current_schema()`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(schema, "store_topup_") {
		t.Fatal("unexpected fixture schema")
	}
	// The failed posting remains an immutable liability fact; a genuine resolution
	// creates the positive ledger which schema153 must recognize after restore.
	resolve := func(out OnlineTopupResult, key string) {
		t.Helper()
		r, err := db.ResolveTopupLiability(ctx, actor, out.InvoiceID, ResolveTopupLiabilityInput{ExpectedVersion: 2, Note: "isolated recovery credit proof"}, key)
		if err != nil || r.Resolution == nil || r.Status != "credited" || r.Settlement.Status != "pending_liability" {
			t.Fatalf("resolution failed: %v", err)
		}
	}
	resolve(first, "recovery-first-resolution")
	second := createTopup(t, db, ctx, uid, 400, "recovery-second-topup")
	secondPayment, err := topupIntent(db, ctx, uid, second, "recovery-second-payment")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `CREATE TRIGGER reject_gateway_ledger BEFORE INSERT ON balance_ledger FOR EACH ROW EXECUTE FUNCTION reject_gateway_ledger()`); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, secondPayment, "recovery-second-success"); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `DROP TRIGGER reject_gateway_ledger ON balance_ledger`); err != nil {
		t.Fatal(err)
	}
	resolve(second, "recovery-second-resolution")
	var reviewer int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role) VALUES('recovery-reviewer','recovery-reviewer@example.test','unused','Recovery reviewer','super_admin') RETURNING id`).Scan(&reviewer); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO sessions(token_hash,user_id,expires_at) VALUES('recovery-review-session',$1,now()+interval '1 hour')`, reviewer); err != nil {
		t.Fatal(err)
	}
	requestCtx := WithExternalRefundSession(ctx, "liability-admin-session")
	reviewCtx := WithExternalRefundSession(ctx, "recovery-review-session")
	createRefund := func(out OnlineTopupResult, p PaymentIntentResult, key string) ExternalRefundResult {
		t.Helper()
		r, e := db.CreateExternalRefund(requestCtx, actor, out.InvoiceID, ExternalRefundInput{PaymentID: p.PaymentID, AmountCents: p.AmountCents, Method: "original_route", ProviderRefundID: key, ReceivedAt: time.Now(), EvidenceRef: "test://recovery-proof", EvidenceSHA256: strings.Repeat("a", 64), Note: "isolated recovery merchant evidence", ExpectedVersion: 3}, key)
		if e != nil || r.Refund.Reversal == nil || r.Refund.Reversal.Status != "pending" {
			t.Fatalf("refund reservation failed: %v", e)
		}
		return r
	}
	pending := createRefund(first, firstPayment, "recovery-pending-refund")
	confirmed := createRefund(second, secondPayment, "recovery-confirmed-refund")
	review := ExternalRefundReviewInput{ExpectedVersion: 1, Note: "independent recovery evidence review"}
	if _, err = db.ReviewExternalRefund(reviewCtx, reviewer, second.InvoiceID, confirmed.Refund.ID, true, review, "recovery-source-confirm"); err != nil {
		t.Fatal(err)
	}
	var spendInvoice int64
	if err = db.Pool.QueryRow(ctx, `INSERT INTO invoices(invoice_no,user_id,invoice_kind,status,amount_cents) VALUES('RECOVERY-RESERVED-SPEND',$1,'manual','unpaid',100) RETURNING id`, uid).Scan(&spendInvoice); err != nil {
		t.Fatal(err)
	}
	if _, err = db.Pool.Exec(ctx, `INSERT INTO invoice_items(invoice_id,item_type,description,amount_cents) VALUES($1,'manual','recovery reservation probe',100)`, spendInvoice); err != nil {
		t.Fatal(err)
	}
	// A separate unknown close, and a trusted close followed by late success,
	// preserve both unresolved operational state and an actual abnormal receipt.
	closeFixture := func(key string) (OnlineTopupResult, PaymentIntentResult, PaymentCloseOperation) {
		t.Helper()
		out := createTopup(t, db, ctx, uid, 100, key+"-topup")
		p, e := topupIntent(db, ctx, uid, out, key+"-intent")
		if e != nil {
			t.Fatal(e)
		}
		op, e := db.BeginPaymentClose(ctx, actor, out.InvoiceID, p.PaymentID, out.Version, key+"-close", "liability-admin-session")
		if e != nil {
			t.Fatal(e)
		}
		return out, p, op
	}
	unknownTopup, unknownPayment, unknown := closeFixture("recovery-unknown")
	if err = db.FailPaymentClose(ctx, unknown, "unknown"); err != nil {
		t.Fatal(err)
	}
	lateTopup, latePayment, late := closeFixture("recovery-late")
	if _, err = db.CompletePaymentClose(ctx, late, payment.CloseResult{ProviderPaymentID: late.ProviderPaymentID, OperationID: late.OperationID, AmountCents: late.AmountCents, Status: "closed"}, "liability-admin-session"); err != nil {
		t.Fatal(err)
	}
	if _, err = topupSuccess(db, ctx, latePayment, "recovery-late-receipt"); err != nil {
		t.Fatal(err)
	}
	recoveryFinanceState(t, db, ctx, uid, 500, 500, 1)
	before := recoverySnapshot(t, db.Pool, schema)
	var migrationCount int
	var lastMigration string
	if err = db.Pool.QueryRow(ctx, `SELECT count(*),max(version) FROM schema_migrations`).Scan(&migrationCount, &lastMigration); err != nil {
		t.Fatal(err)
	}
	embeddedMigrations, err := fs.Glob(migrations.Files, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	if len(embeddedMigrations) == 0 {
		t.Fatal("embedded migrations must not be empty")
	}
	sort.Strings(embeddedMigrations)
	expectedLastMigration := embeddedMigrations[len(embeddedMigrations)-1]
	if migrationCount != len(embeddedMigrations) || lastMigration != expectedLastMigration {
		t.Fatalf("expected %d embedded migrations ending %s, got %d migrations ending %s", len(embeddedMigrations), expectedLastMigration, migrationCount, lastMigration)
	}

	archive := filepath.Join(t.TempDir(), "representative.dump")
	recoveryPGTool(t, sourceCfg, "pg_dump", "--format=custom", "--schema="+schema, "--no-owner", "--no-privileges", "--file="+archive)
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	archiveHash := sha256.Sum256(archiveBytes)
	// The sole target cleanup is registered before restore so partial failures are
	// covered. Never use pg_restore --clean or --create against a shared database.
	t.Cleanup(func() {
		_, e := targetBase.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
		if e != nil {
			t.Errorf("restore schema cleanup failed: %v", e)
			return
		}
		var remains bool
		if e = targetBase.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&remains); e != nil || remains {
			t.Errorf("restore schema cleanup verification failed: %v", e)
		}
	})
	recoveryPGTool(t, targetCfg, "pg_restore", "--exit-on-error", "--single-transaction", "--no-owner", "--no-privileges", archive)
	restoredCfg := targetCfg.Copy()
	restoredCfg.ConnConfig.RuntimeParams["search_path"] = schema
	restoredPool, err := pgxpool.NewWithConfig(ctx, restoredCfg)
	if err != nil {
		t.Fatal("restored pool creation failed")
	}
	t.Cleanup(restoredPool.Close)
	restored := &Store{Pool: restoredPool, ProvisionEncryptionKey: append([]byte(nil), db.ProvisionEncryptionKey...)}
	restoredSnapshot := recoverySnapshot(t, restoredPool, schema)
	recoveryDiagnose(t, before, restoredSnapshot)
	recoveryEqual(t, "restored snapshot", before, restoredSnapshot)
	if err = restored.CheckSchema(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err = restored.Migrate(ctx); err != nil {
			t.Fatal(err)
		}
		if err = restored.CheckSchema(ctx); err != nil {
			t.Fatal(err)
		}
		recoveryEqual(t, "repeat migration unchanged", before, recoverySnapshot(t, restoredPool, schema))
	}
	recoveryFinanceState(t, restored, ctx, uid, 500, 500, 1)
	if _, err = restored.PayInvoice(ctx, uid, spendInvoice, "recovery-restored-spend", 1); conflictCode(t, err) != "balance_reserved" {
		t.Fatal(err)
	}
	if _, err = restored.BeginPaymentClose(ctx, actor, unknownTopup.InvoiceID, unknownPayment.PaymentID, unknownTopup.Version, "recovery-restored-close", "liability-admin-session"); conflictCode(t, err) != "payment_close_in_progress" {
		t.Fatal(err)
	}
	if _, err = restored.CancelOnlineTopup(ctx, uid, unknownTopup.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: unknownTopup.Version}, "recovery-unknown-cancel"); conflictCode(t, err) != "topup_payment_protected" {
		t.Fatal(err)
	}
	if _, err = restored.CancelOnlineTopup(ctx, uid, lateTopup.InvoiceID, OnlineTopupCancelInput{ExpectedVersion: lateTopup.Version + 1}, "recovery-late-cancel"); conflictCode(t, err) != "topup_payment_protected" {
		t.Fatal(err)
	}
	if _, err = restored.ReviewExternalRefund(requestCtx, actor, first.InvoiceID, pending.Refund.ID, true, review, "recovery-self-review"); conflictCode(t, err) != "external_refund_self_review" {
		t.Fatal(err)
	}
	result, err := restored.ReviewExternalRefund(reviewCtx, reviewer, first.InvoiceID, pending.Refund.ID, true, review, "recovery-restored-confirm")
	if err != nil || result.Refund.Status != "confirmed" || result.Replayed {
		t.Fatalf("restored confirmation failed: %v", err)
	}
	afterConfirm := recoverySnapshot(t, restoredPool, schema)
	replay, err := restored.ReviewExternalRefund(reviewCtx, reviewer, first.InvoiceID, pending.Refund.ID, true, review, "recovery-restored-confirm")
	if err != nil || !replay.Replayed || !recoveryReceiptEqual(result.Refund, replay.Refund) {
		t.Fatalf("restored review replay changed receipt: %v", err)
	}
	recoveryEqual(t, "confirmation replay unchanged", afterConfirm, recoverySnapshot(t, restoredPool, schema))
	recoveryFinanceState(t, restored, ctx, uid, 0, 0, 2)
	// Review is allowed to change refund/invoice/reservation/audit facts, but never
	// the original paid receipt, failure, resolution, close, or abnormal receipt.
	for _, table := range []string{"payment_transactions", "payment_webhook_events", "balance_topup_settlements", "balance_topup_liability_resolutions", "payment_close_operations", "payment_close_anomalies"} {
		recoveryEqual(t, "original fact "+table, before.Tables[table], afterConfirm.Tables[table])
	}
	recoveryEqual(t, "source untouched by restore continuation", before, recoverySnapshot(t, db.Pool, schema))
	// Verify source cleanup too, beyond the fixture's best-effort deferred cleanup.
	db.Close()
	if _, err = sourceBase.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
		t.Fatal(err)
	}
	var exists bool
	if err = sourceBase.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists); err != nil || exists {
		t.Fatal("source schema cleanup verification failed")
	}
	snapshotJSON, _ := json.Marshal(before)
	snapshotHash := sha256.Sum256(snapshotJSON)
	var rowCount int64
	for _, v := range before.Tables {
		rowCount += v.Rows
	}
	t.Logf("PASS current-schema representative restore: latest=%s tables=%d rows=%d sequences=%d catalog_records=%d migrations=%d", lastMigration, len(before.Tables), rowCount, len(before.Sequences), len(before.Catalog), migrationCount)
	t.Logf("archive_sha256=%x snapshot_sha256=%x", archiveHash, snapshotHash)
	t.Log("PASS two unchanged Migrate/CheckSchema runs; pending freeze; unknown close and late anomaly freeze; independent refund confirmation; replay without writes; 2 unique negative ledgers; original receipts retained; source schema removed")
}

func recoveryConfig(t *testing.T, key string) *pgxpool.Config {
	t.Helper()
	raw := os.Getenv(key)
	if raw == "" {
		t.Fatalf("%s required explicitly", key)
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil {
		t.Fatalf("invalid %s", key)
	}
	c := cfg.ConnConfig
	loopback := func(host string) bool {
		ip := net.ParseIP(host)
		return host == "localhost" || ip != nil && ip.IsLoopback()
	}
	if !strings.HasSuffix(c.Database, "_test") || !loopback(c.Host) {
		t.Fatalf("%s requires a loopback _test database", key)
	}
	for _, f := range c.Fallbacks {
		if !loopback(f.Host) || f.Host != c.Host || f.Port != c.Port {
			t.Fatal("connection fallback changes endpoint")
		}
	}
	// Avoid inherited startup settings redirecting schema or role. The source
	// fixture owns its own random search_path; restored pool gets the same schema.
	for k := range c.RuntimeParams {
		if k != "application_name" {
			t.Fatalf("%s contains unsupported startup setting", key)
		}
	}
	c.RuntimeParams["timezone"] = "UTC"
	c.RuntimeParams["datestyle"] = "ISO, YMD"
	c.ConnectTimeout = 5 * time.Second
	return cfg
}

func recoveryPreflight(t *testing.T, cfg *pgxpool.Config) *pgxpool.Pool {
	t.Helper()
	p, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("isolated test connection configuration failed")
	}
	t.Cleanup(p.Close)
	var version int
	var db, addr string
	if err = p.QueryRow(t.Context(), `SELECT current_setting('server_version_num')::int,current_database(),host(inet_server_addr())`).Scan(&version, &db, &addr); err != nil {
		t.Fatal("isolated test connection failed")
	}
	ip := net.ParseIP(addr)
	if version < 180000 || version >= 190000 || db != cfg.ConnConfig.Database || ip == nil || !ip.IsLoopback() {
		t.Fatalf("requires verified loopback PostgreSQL 18 dedicated _test database (version=%d database_match=%t server_addr=%q)", version, db == cfg.ConnConfig.Database, addr)
	}
	t.Logf("preflight PostgreSQL %d loopback _test database verified", version)
	return p
}

func recoveryRequireEmpty(t *testing.T, p *pgxpool.Pool) {
	t.Helper()
	var count int
	err := p.QueryRow(t.Context(), `SELECT (SELECT count(*) FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname NOT IN ('information_schema','public'))+(SELECT count(*) FROM pg_depend WHERE refclassid='pg_namespace'::regclass AND refobjid='public'::regnamespace)+(SELECT count(*) FROM pg_extension WHERE extname<>'plpgsql')`).Scan(&count)
	if err != nil || count != 0 {
		t.Fatal("restore target must have no user schemas, public objects, or nonstandard extensions")
	}
}

func recoveryPGTool(t *testing.T, cfg *pgxpool.Config, name string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	c := cfg.ConnConfig
	// Never put a password or full DSN in argv. Discard inherited PG routing,
	// service and option settings, then provide only the selected connection.
	env := []string{}
	for _, v := range os.Environ() {
		if !strings.HasPrefix(v, "PG") && !strings.HasPrefix(v, "ZJMFGO_") {
			env = append(env, v)
		}
	}
	env = append(env, "PGPASSWORD="+c.Password, "PGSSLMODE=disable", "PGCONNECT_TIMEOUT=5", "PGPASSFILE=/dev/null", "PGSERVICEFILE=/dev/null")
	connArgs := []string{"--host=" + c.Host, fmt.Sprintf("--port=%d", c.Port), "--username=" + c.User, "--dbname=" + c.Database, "--no-password"}
	cmd := exec.CommandContext(ctx, filepath.Join("/www/server/pgsql/bin", name), append(connArgs, args...)...)
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		// Output is intentionally not logged: future libpq errors must not expose
		// credentials, archive rows, or environment-derived connection details.
		t.Fatalf("%s failed (output redacted, %d bytes): %v", name, len(output), err)
	}
}

type recoveryDigest struct {
	Rows   int64
	SHA256 string
}
type recoveryState struct {
	Tables    map[string]recoveryDigest
	Sequences map[string]string
	Catalog   []string
}

func recoverySnapshot(t *testing.T, p *pgxpool.Pool, schema string) recoveryState {
	t.Helper()
	ctx := t.Context()
	tx, err := p.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SET LOCAL TIME ZONE 'UTC'; SET LOCAL datestyle='ISO, YMD'; SET LOCAL extra_float_digits=3`); err != nil {
		t.Fatal(err)
	}
	out := recoveryState{Tables: map[string]recoveryDigest{}, Sequences: map[string]string{}}
	rows, err := tx.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relkind IN ('r','p') ORDER BY c.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		r, e := tx.Query(ctx, `SELECT to_jsonb(t)::text FROM `+pgx.Identifier{schema, table}.Sanitize()+` t ORDER BY to_jsonb(t)::text COLLATE "C"`)
		if e != nil {
			t.Fatal(e)
		}
		h := sha256.New()
		var count int64
		for r.Next() {
			var s string
			if e = r.Scan(&s); e != nil {
				t.Fatal(e)
			}
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(s)))
			h.Write(size[:])
			h.Write([]byte(s))
			count++
		}
		r.Close()
		if e = r.Err(); e != nil {
			t.Fatal(e)
		}
		out.Tables[table] = recoveryDigest{count, hex.EncodeToString(h.Sum(nil))}
	}
	rows, err = tx.Query(ctx, `SELECT c.relname,to_jsonb(s)::text FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 ORDER BY c.relname`, schema)
	if err != nil {
		t.Fatal(err)
	}
	var sequences []string
	for rows.Next() {
		var name, params string
		if err = rows.Scan(&name, &params); err != nil {
			t.Fatal(err)
		} // Remove the installation-specific sequence OID.
		var v map[string]any
		if err = json.Unmarshal([]byte(params), &v); err != nil {
			t.Fatal(err)
		}
		delete(v, "seqrelid")
		normalized, _ := json.Marshal(v)
		out.Sequences[name] = string(normalized)
		sequences = append(sequences, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range sequences {
		var state string
		if err = tx.QueryRow(ctx, `SELECT jsonb_build_array(last_value,is_called)::text FROM `+pgx.Identifier{schema, name}.Sanitize()).Scan(&state); err != nil {
			t.Fatal(err)
		}
		out.Sequences[name] += state
	}
	// Include full constraints plus indexes, triggers, functions, column defaults
	// and nullability: restore must preserve the money guards, not just rows.
	rows, err = tx.Query(ctx, `SELECT fact FROM (
 SELECT 'constraint:'||c.relname||':'||x.conname||':'||pg_get_constraintdef(x.oid,true)||':'||x.convalidated::text||':'||x.condeferrable::text||':'||x.condeferred::text AS fact FROM pg_constraint x JOIN pg_class c ON c.oid=x.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1
 UNION ALL SELECT 'index:'||c.relname||':'||pg_get_indexdef(i.indexrelid) FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1
 UNION ALL SELECT 'trigger:'||c.relname||':'||t.tgenabled::text||':'||pg_get_triggerdef(t.oid,false) FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND NOT t.tgisinternal
 UNION ALL SELECT 'function:'||p.proname||':'||pg_get_functiondef(p.oid) FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=$1 AND p.prokind IN ('f','p')
 UNION ALL SELECT 'column:'||c.relname||':'||a.attname||':'||format_type(a.atttypid,a.atttypmod)||':'||a.attnotnull::text||':'||a.attidentity::text||':'||a.attgenerated::text||':'||coalesce(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a JOIN pg_class c ON c.oid=a.attrelid JOIN pg_namespace n ON n.oid=c.relnamespace LEFT JOIN pg_attrdef d ON d.adrelid=c.oid AND d.adnum=a.attnum WHERE n.nspname=$1 AND c.relkind IN ('r','p') AND a.attnum>0 AND NOT a.attisdropped
 ) facts ORDER BY fact COLLATE "C"`, schema)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		out.Catalog = append(out.Catalog, s)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return out
}

func recoveryEqual(t *testing.T, label string, want, got any) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("%s differs (values redacted)", label)
	}
}

func recoveryFinanceState(t *testing.T, db *Store, ctx context.Context, uid, balance, reserved, negative int64) {
	t.Helper()
	var b, r, n, proofs, liabilities, anomalies, unknown int64
	err := db.Pool.QueryRow(ctx, `SELECT balance_cents,
 (SELECT coalesce(sum(amount_cents),0)::bigint FROM external_refund_reversal_reservations WHERE user_id=$1 AND status='pending'),
 (SELECT count(*) FROM balance_ledger WHERE user_id=$1 AND entry_type='refund' AND delta_cents<0),
 (SELECT count(*) FROM balance_topup_liability_resolutions x JOIN balance_ledger l ON l.id=x.ledger_id WHERE l.delta_cents=x.amount_cents),
 (SELECT count(*) FROM balance_topup_settlements WHERE status='pending_liability' AND ledger_id IS NULL),
 (SELECT count(*) FROM payment_close_anomalies WHERE payment_close_anomaly_unresolved(id)),
 (SELECT count(*) FROM payment_close_operations WHERE status='unknown' AND payment_close_operation_unresolved(id))
 FROM users WHERE id=$1`, uid).Scan(&b, &r, &n, &proofs, &liabilities, &anomalies, &unknown)
	if err != nil {
		t.Fatal(err)
	}
	if b != balance || r != reserved || n != negative || proofs != 2 || liabilities != 2 || anomalies != 1 || unknown != 1 {
		t.Fatalf("finance invariant mismatch balance/reserved/negative=%d/%d/%d proofs/liabilities/anomalies/unknown=%d/%d/%d/%d", b, r, n, proofs, liabilities, anomalies, unknown)
	}
	var invalid int64
	if err = db.Pool.QueryRow(ctx, `SELECT count(*) FROM external_refund_reversal_reservations r WHERE r.status='applied' AND (SELECT count(*) FROM balance_ledger l WHERE l.id=r.ledger_id AND l.reference_type='invoice' AND l.reference_id=r.invoice_id AND l.delta_cents=-r.amount_cents AND l.idempotency_key='external-refund-balance-'||r.refund_id::text)<>1`).Scan(&invalid); err != nil || invalid != 0 {
		t.Fatalf("refund negative ledger proof invalid: %v", err)
	}
}

// Diagnose only object names/counts, never row contents or connection settings.
func recoveryDiagnose(t *testing.T, want, got recoveryState) {
	t.Helper()
	for name, w := range want.Tables {
		if g, ok := got.Tables[name]; !ok || w != g {
			t.Logf("table digest mismatch %s source_rows=%d target_rows=%d", name, w.Rows, g.Rows)
		}
	}
	for name, w := range want.Sequences {
		if g, ok := got.Sequences[name]; !ok || w != g {
			t.Logf("sequence mismatch %s", name)
		}
	}
	if !reflect.DeepEqual(want.Catalog, got.Catalog) {
		t.Logf("catalog count source=%d target=%d", len(want.Catalog), len(got.Catalog))
		for i, w := range want.Catalog {
			if i < len(got.Catalog) && w != got.Catalog[i] {
				t.Logf("first catalog mismatch object=%q", strings.Join(strings.SplitN(w, ":", 4)[:3], ":"))
				break
			}
		}
	}
}

// A replay decodes the persisted JSON receipt; compare its wire representation
// rather than time.Time's private location pointer/monotonic representation.
func recoveryReceiptEqual(a, b ExternalRefund) bool {
	first, err := json.Marshal(a)
	if err != nil {
		return false
	}
	second, err := json.Marshal(b)
	return err == nil && string(first) == string(second)
}
