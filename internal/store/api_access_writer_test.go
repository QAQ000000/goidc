package store

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/accesslog"
	"github.com/codex/zjmfgo/internal/correlation"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func apiAccessTestRecord(actorID int64, path string, occurredAt time.Time) accesslog.Record {
	return accesslog.Record{
		ActorID: actorID, Method: "GET", Path: path,
		StatusCode: 200, DurationMillis: 15,
		RemoteIP: net.ParseIP("192.0.2.19"), OccurredAt: occurredAt,
	}
}

func apiAccessTestWriter(t *testing.T, db *Store) *APIAccessWriter {
	t.Helper()
	w, err := db.OpenAPIAccessWriter(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(w.Close)
	return w
}

func TestAPIAccessWriterRejectsUnboundedCallsBeforeDatabase(t *testing.T) {
	w := &APIAccessWriter{}
	if err := w.WriteBatch(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteBatch(t.Context(), make([]accesslog.Record, APIAccessBatchLimit+1)); err == nil {
		t.Fatal("accepted unbounded batch")
	}
	if err := w.WriteBatch(t.Context(), []accesslog.Record{{}}); err == nil {
		t.Fatal("accepted event without original timestamp")
	}
	for _, limit := range []int{-1, 0} {
		if n, err := w.Prune(t.Context(), time.Now(), limit); err != nil || n != 0 {
			t.Fatalf("empty prune: n=%d err=%v", n, err)
		}
	}
	if _, err := w.Prune(t.Context(), time.Now(), APIAccessPruneLimit+1); err == nil {
		t.Fatal("accepted unbounded prune")
	}
	if _, err := w.Prune(t.Context(), time.Time{}, 1); err == nil {
		t.Fatal("accepted prune without retention boundary")
	}
	if _, err := (*Store)(nil).OpenAPIAccessWriter(t.Context()); err == nil {
		t.Fatal("accepted missing database")
	}
	w.Close()
}

func TestAPIAccessWriterPreservesEventsAndRedactsQuery(t *testing.T) {
	db := isolatedTestStore(t)
	adminID, customerID := topUpFixture(t, db)
	w := apiAccessTestWriter(t, db)
	when := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Microsecond)
	a := apiAccessTestRecord(adminID, "/api/v1/admin/logs?token=must-not-store#secret-fragment", when)
	a.CorrelationID = correlation.NewID()
	b := apiAccessTestRecord(customerID, "/api/v1/services", when.Add(time.Second))
	b.CorrelationID = correlation.NewID()
	b.RemoteIP = net.ParseIP("2001:db8::2")
	c := apiAccessTestRecord(adminID, "/api/v1/me", when.Add(2*time.Second))
	c.RemoteIP = nil
	writerOrigin := correlation.NewID()
	if err := w.WriteBatch(correlation.WithID(t.Context(), writerOrigin), []accesslog.Record{a, b, c}); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Pool.Query(t.Context(), "SELECT actor_id,method,path,status_code,duration_ms,coalesce(host(remote_ip),''),created_at,coalesce(correlation_id,''),row_to_json(admin_api_logs)::text FROM admin_api_logs ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for i, want := range []accesslog.Record{a, b, c} {
		if !rows.Next() {
			t.Fatalf("missing event %d: %v", i, rows.Err())
		}
		var actorID, duration int64
		var status int
		var method, path, ip, origin, serialized string
		var occurredAt time.Time
		if err := rows.Scan(&actorID, &method, &path, &status, &duration, &ip, &occurredAt, &origin, &serialized); err != nil {
			t.Fatal(err)
		}
		wantPath := strings.Split(want.Path, "?")[0]
		matchingIP := (want.RemoteIP == nil && ip == "") || (want.RemoteIP != nil && net.ParseIP(ip).Equal(want.RemoteIP))
		if actorID != want.ActorID || method != want.Method || path != wantPath || status != want.StatusCode || duration != want.DurationMillis || !matchingIP || !occurredAt.Equal(want.OccurredAt) || origin != want.CorrelationID {
			t.Fatalf("event %d changed: actor=%d method=%s path=%q status=%d duration=%d ip=%q timestamp=%v correlation=%q", i, actorID, method, path, status, duration, ip, occurredAt, origin)
		}
		for _, secret := range []string{"token=", "must-not-store", "secret-fragment", writerOrigin} {
			if strings.Contains(serialized, secret) {
				t.Fatalf("writer leaked unrelated data %q", secret)
			}
		}
	}
	if rows.Next() || rows.Err() != nil {
		t.Fatalf("unexpected remaining event: %v", rows.Err())
	}
}

func TestAPIAccessWriterBatchIsAtomic(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, _ := topUpFixture(t, db)
	w := apiAccessTestWriter(t, db)
	a := apiAccessTestRecord(actorID, "/atomic-first", time.Now())
	b := apiAccessTestRecord(actorID, "/atomic-invalid", time.Now())
	b.StatusCode = 999
	if err := w.WriteBatch(t.Context(), []accesslog.Record{a, b}); err == nil {
		t.Fatal("accepted invalid record")
	}
	var n int
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*) FROM admin_api_logs").Scan(&n); err != nil || n != 0 {
		t.Fatalf("partially committed failed batch: rows=%d err=%v", n, err)
	}
	if err := w.WriteBatch(t.Context(), []accesslog.Record{a}); err != nil {
		t.Fatalf("writer could not recover after failed statement: %v", err)
	}
}

func TestAPIAccessWriterCopiesSchemaHooksAndDoesNotUseBusinessLeases(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, _ := topUpFixture(t, db)
	cfg := db.Pool.Config().Copy()
	cfg.MaxConns, cfg.MinConns = 2, 0
	previousAfterConnect := cfg.AfterConnect
	cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
		if previousAfterConnect != nil {
			if err := previousAfterConnect(ctx, conn); err != nil {
				return err
			}
		}
		_, err := conn.Exec(ctx, "SELECT set_config('goidc.accesslog_test_hook','preserved',false)")
		return err
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	business := &Store{Pool: pool}
	var leases []*pgxpool.Conn
	defer func() {
		for _, conn := range leases {
			conn.Release()
		}
	}()
	for range cfg.MaxConns {
		conn, err := pool.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, conn)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	w, err := business.OpenAPIAccessWriter(ctx)
	if err != nil {
		t.Fatalf("opening log pool waited for occupied business leases: %v", err)
	}
	defer w.Close()
	if got := w.pool.Config(); got.MaxConns != 1 || got.MinConns != 0 {
		t.Fatalf("unbounded log connection pool: max=%d min=%d", got.MaxConns, got.MinConns)
	}
	if err := w.WriteBatch(ctx, []accesslog.Record{apiAccessTestRecord(actorID, "/separate-lease", time.Now())}); err != nil {
		t.Fatalf("log write waited for occupied business leases: %v", err)
	}
	var originalSchema, writerSchema, hook string
	if err := db.Pool.QueryRow(t.Context(), "SELECT current_schema()").Scan(&originalSchema); err != nil {
		t.Fatal(err)
	}
	if err := w.pool.QueryRow(ctx, "SELECT current_schema(),current_setting('goidc.accesslog_test_hook')").Scan(&writerSchema, &hook); err != nil || originalSchema != writerSchema || hook != "preserved" {
		t.Fatalf("log pool lost schema/hooks: source=%q copied=%q hook=%q err=%v", originalSchema, writerSchema, hook, err)
	}
	if pool.Stat().AcquiredConns() != cfg.MaxConns {
		t.Fatal("test did not keep every business connection occupied")
	}
}

func TestAPIAccessWriterPrunesBoundedOldEventsOnly(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, _ := topUpFixture(t, db)
	w := apiAccessTestWriter(t, db)
	boundary := time.Now().UTC().Add(-30 * 24 * time.Hour).Truncate(time.Microsecond)
	var records []accesslog.Record
	for _, offset := range []time.Duration{-3 * time.Second, -2 * time.Second, -time.Second, 0, time.Second} {
		records = append(records, apiAccessTestRecord(actorID, "/retention", boundary.Add(offset)))
	}
	if err := w.WriteBatch(t.Context(), records); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{2, 1, 0} {
		if n, err := w.Prune(t.Context(), boundary, 2); err != nil || n != want {
			t.Fatalf("bounded prune: rows=%d want=%d err=%v", n, want, err)
		}
	}
	var n int
	var oldest time.Time
	if err := db.Pool.QueryRow(t.Context(), "SELECT count(*),min(created_at) FROM admin_api_logs").Scan(&n, &oldest); err != nil || n != 2 || !oldest.Equal(boundary) {
		t.Fatalf("retention deleted boundary/new events: rows=%d oldest=%v err=%v", n, oldest, err)
	}
}

func TestAPIAccessWriterPruneSkipsLockedEvents(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, _ := topUpFixture(t, db)
	w := apiAccessTestWriter(t, db)
	boundary := time.Now().UTC().Truncate(time.Microsecond)
	if err := w.WriteBatch(t.Context(), []accesslog.Record{
		apiAccessTestRecord(actorID, "/locked-oldest", boundary.Add(-2*time.Second)),
		apiAccessTestRecord(actorID, "/unlocked-old", boundary.Add(-time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	tx, err := db.Pool.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	if _, err := tx.Exec(t.Context(), "SELECT id FROM admin_api_logs WHERE path='/locked-oldest' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if n, err := w.Prune(ctx, boundary, 1); err != nil || n != 1 {
		t.Fatalf("prune blocked on concurrent row: rows=%d err=%v", n, err)
	}
	var path string
	if err := db.Pool.QueryRow(t.Context(), "SELECT path FROM admin_api_logs").Scan(&path); err != nil || path != "/locked-oldest" {
		t.Fatalf("prune did not skip locked row: path=%q err=%v", path, err)
	}
}

func TestAPIAccessWriterCloseAndSynchronousCompatibility(t *testing.T) {
	db := isolatedTestStore(t)
	actorID, _ := topUpFixture(t, db)
	before := time.Now().UTC().Truncate(time.Microsecond)
	origin := correlation.NewID()
	if err := db.RecordAPIAccess(correlation.WithID(t.Context(), origin), APIAccessRecord{ActorID: actorID, Method: "GET", Path: "/explicit-sync?secret=hidden", StatusCode: 200}); err != nil {
		t.Fatal(err)
	}
	var occurredAt time.Time
	var gotOrigin, path string
	if err := db.Pool.QueryRow(t.Context(), "SELECT created_at,correlation_id,path FROM admin_api_logs").Scan(&occurredAt, &gotOrigin, &path); err != nil || occurredAt.Before(before) || occurredAt.After(time.Now()) || gotOrigin != origin || path != "/explicit-sync" {
		t.Fatalf("synchronous compatibility lost fallback: time=%v correlation=%q path=%q err=%v", occurredAt, gotOrigin, path, err)
	}
	w := apiAccessTestWriter(t, db)
	w.Close()
	w.Close()
	if err := w.WriteBatch(t.Context(), []accesslog.Record{apiAccessTestRecord(actorID, "/closed", time.Now())}); err == nil {
		t.Fatal("closed writer accepted data")
	}
	if _, err := w.Prune(t.Context(), time.Now(), 1); err == nil {
		t.Fatal("closed writer accepted prune")
	}
	if err := db.Pool.Ping(t.Context()); err != nil {
		t.Fatalf("closing writer also closed business pool: %v", err)
	}
}
