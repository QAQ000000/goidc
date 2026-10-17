package g09fixture

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// URL parsing never connects. This narrow opt-in fixture deliberately rejects
// libpq keyword DSNs, environment options and connection-target overrides.
func observerFixtureConfig(raw string) (*pgxpool.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Fragment != "" || u.User == nil || u.User.Username() == "" || !observerDatabase.MatchString(strings.TrimPrefix(u.Path, "/")) {
		return nil, errors.New("observer_test_dsn_invalid")
	}
	for key, values := range u.Query() {
		if key != "sslmode" || len(values) != 1 || values[0] != "disable" {
			return nil, errors.New("observer_test_dsn_invalid")
		}
	}
	cfg, err := pgxpool.ParseConfig(raw)
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || cfg.ConnConfig.Database != strings.TrimPrefix(u.Path, "/") {
		return nil, errors.New("observer_test_dsn_invalid")
	}
	cfg.MaxConns = 2
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.ConnConfig.RuntimeParams = map[string]string{"application_name": "g09_observer_test"}
	return cfg, nil
}

func TestPGObserverPureConfiguration(t *testing.T) {
	for _, raw := range []string{"", "host=127.0.0.1 dbname=unsafe_test", "postgres://u@localhost:5432/unsafe_test", "postgres://u@127.0.0.1:5432/live", "postgres://u@127.0.0.1:5432/unsafe_test?host=elsewhere", "postgres://u@127.0.0.1:5432/unsafe_test?options=-csearch_path=public", "postgres://u@127.0.0.1:5432/unsafe_test?sslmode=disable&sslmode=disable"} {
		if _, err := observerFixtureConfig(raw); err == nil {
			t.Fatal("unsafe configuration accepted")
		}
	}
	cfg, err := observerFixtureConfig("postgres://synthetic:private@127.0.0.1:1/g09_observer_test?sslmode=disable")
	if err != nil {
		t.Fatal("valid synthetic configuration refused")
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "zjmfgo_test_0123456789abcdef"
	var dials atomic.Int64
	cfg.ConnConfig.DialFunc = func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("pure_test_network_forbidden")
	}
	defer func() {
		if dials.Load() != 0 {
			t.Error("pure configuration attempted to dial")
		}
	}()
	// MinConns=0: NewWithConfig does not dial. No query/ping is called in this test.
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal("pool config refused")
	}
	defer pool.Close()
	good := PGObserverConfig{Database: "g09_observer_test", Schema: "zjmfgo_test_0123456789abcdef", MaxSamples: 2, Timeout: time.Second, SupplierAccountIDs: []int64{1, 2}}
	observer, err := NewPGObserver(pool, good)
	if err != nil {
		t.Fatal("observer config refused")
	}
	good.SupplierAccountIDs[0] = 99
	if observer.config.SupplierAccountIDs[0] != 1 {
		t.Fatal("authorization slice not frozen")
	}
	for _, change := range []func(*PGObserverConfig){func(c *PGObserverConfig) { c.Database = "live" }, func(c *PGObserverConfig) { c.Database = "different_test" }, func(c *PGObserverConfig) { c.Schema = "public" }, func(c *PGObserverConfig) { c.MaxSamples = 0 }, func(c *PGObserverConfig) { c.Timeout = 6 * time.Second }, func(c *PGObserverConfig) { c.SupplierAccountIDs = []int64{1, 1} }} {
		bad := good
		change(&bad)
		if _, err := NewPGObserver(pool, bad); err == nil {
			t.Fatal("invalid authorization accepted")
		}
	}
}

func observerPGFixture(t *testing.T) (*pgxpool.Pool, PGObserverConfig) {
	t.Helper()
	if os.Getenv("ZJMFGO_G09_PG_OBSERVER_SMOKE") != "1" {
		t.Skip("explicit isolated PG18 observer opt-in required")
	}
	cfg, err := observerFixtureConfig(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal("explicit observer smoke requires loopback _test URL")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	t.Cleanup(cancel)
	base, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal("observer base unavailable")
	}
	t.Cleanup(base.Close)
	var database, addr string
	var version int
	if err = base.QueryRow(ctx, `SELECT current_database(),host(inet_server_addr()),current_setting('server_version_num')::int`).Scan(&database, &addr, &version); err != nil || database != cfg.ConnConfig.Database || addr != "127.0.0.1" || version < 180000 || version >= 190000 {
		t.Fatal("observer requires authorized loopback PG18 database")
	}
	random := make([]byte, 8)
	if _, err = rand.Read(random); err != nil {
		t.Fatal("random schema unavailable")
	}
	schema := "zjmfgo_test_" + hex.EncodeToString(random)
	if _, err = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal("observer schema creation failed")
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := base.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); err != nil {
			t.Error("observer owned schema cleanup failed")
		}
		var remains bool
		if err := base.QueryRow(cleanup, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)`, schema).Scan(&remains); err != nil || remains {
			t.Error("observer schema cleanup not verified")
		}
	})
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams = map[string]string{"search_path": schema, "application_name": "g09_observer_test"}
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
	if err != nil {
		t.Fatal("observer scoped pool failed")
	}
	t.Cleanup(pool.Close)
	if err = (&store.Store{Pool: pool}).Migrate(ctx); err != nil {
		t.Fatal("observer production migrations failed")
	}
	return pool, PGObserverConfig{Database: database, Schema: schema, MaxSamples: 2, Timeout: 2 * time.Second}
}

func TestPGObserverIsolatedPG18Snapshot(t *testing.T) {
	pool, cfg := observerPGFixture(t)
	// Fixture-only queue seed is not business throughput. Do not complete jobs,
	// advance leases or rewrite available_at during observation.
	_, err := pool.Exec(t.Context(), `INSERT INTO email_outbox(recipient,subject,body,kind,available_at,created_at) VALUES('fixture@example.test','fixture','fixture','system',clock_timestamp()-interval '1 second',clock_timestamp()-interval '20 seconds'),('fixture@example.test','fixture','fixture','system',clock_timestamp()+interval '1 hour',clock_timestamp());
 INSERT INTO sms_outbox(recipient,body_encrypted,kind,expires_at,available_at) VALUES('+12025550101',decode('01','hex'),'system',clock_timestamp()+interval '1 day',clock_timestamp()+interval '1 hour');
 WITH u AS (INSERT INTO users(username,email,password_hash,display_name) VALUES('observer','observer@example.test','synthetic','fixture') RETURNING id),
 p AS (INSERT INTO products(name,slug,price_cents,provision_mode) VALUES('fixture','observer-product',100,'simulation') RETURNING id),
 o AS (INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash) SELECT 'observer-order',u.id,100,'fixture','synthetic' FROM u RETURNING id,user_id),
 i AS (INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) SELECT o.id,p.id,'fixture','monthly',1,100,0,100 FROM o,p RETURNING id,order_id,product_id),
 s AS (INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode) SELECT o.user_id,i.order_id,i.id,i.product_id,'fixture','observer.test','monthly','active','simulation' FROM i JOIN o ON o.id=i.order_id RETURNING id),
 j AS (INSERT INTO provision_jobs(service_id,action,status,idempotency_key,locked_at) SELECT id,'sync','running','observer-job',clock_timestamp() FROM s RETURNING id)
 INSERT INTO supplier_accounts(name,provider,base_url,username,credentials_encrypted,running_job_id) SELECT 'fixture','zjmf','http://127.0.0.1:1','fixture',decode('01','hex'),id FROM j`)
	if err != nil {
		t.Fatal("observer queue seed failed")
	}
	var account int64
	if err = pool.QueryRow(t.Context(), `SELECT id FROM supplier_accounts`).Scan(&account); err != nil {
		t.Fatal("observer account seed failed")
	}
	cfg.SupplierAccountIDs = []int64{account}
	observer, err := NewPGObserver(pool, cfg)
	if err != nil {
		t.Fatal("observer config failed")
	}
	out, err := observer.Sample(t.Context())
	if err != nil {
		t.Fatal("observer snapshot failed")
	}
	if out.CapacityVerified || out.Sequence != 1 || len(out.SupplierLeases) != 1 || (out.SupplierLeases[0].RunningJobID == nil || out.SupplierLeases[0].JobStatus == nil || *out.SupplierLeases[0].JobStatus != "running" || out.SupplierLeases[0].LockedAt == nil) {
		t.Fatal("observer account facts invalid")
	}
	emailFound, smsFound := false, false
	for _, q := range out.Queues {
		switch q.Queue {
		case "email_outbox":
			emailFound = true
			if q.Count != 2 || q.Ready != 1 || q.Future != 1 || q.OldestReadySeconds == nil || *q.OldestReadySeconds < 19 {
				t.Fatal("email queue snapshot invalid")
			}
		case "sms_outbox":
			smsFound = true
			if q.Count != 1 || q.Ready != 0 || q.Future != 1 || q.OldestReadySeconds != nil {
				t.Fatal("SMS unavailable age coerced")
			}
		}
	}
	if !emailFound || !smsFound || !out.Database.Available || out.Database.Deadlocks == nil || out.WAL.Scope != "whole_cluster_not_schema_or_database_attributable" {
		t.Fatal("observer scope/statistics invalid")
	}
	raw, _ := json.Marshal(out)
	for _, secret := range []string{"fixture@example.test", "+12025550101", os.Getenv("ZJMFGO_TEST_DATABASE_URL")} {
		if secret != "" && strings.Contains(string(raw), secret) {
			t.Fatal("observer leaked private payload")
		}
	}
	if _, err = observer.Sample(t.Context()); err != nil {
		t.Fatal("second observer snapshot failed")
	}
	if _, err = observer.Sample(t.Context()); err == nil {
		t.Fatal("sample budget exceeded")
	}
	t.Run("relation_lock_timeout_latches", func(t *testing.T) {
		tx, err := pool.Begin(t.Context())
		if err != nil {
			t.Fatal("lock fixture begin failed")
		}
		defer tx.Rollback(context.Background())
		if _, err = tx.Exec(t.Context(), `LOCK TABLE email_outbox IN ACCESS EXCLUSIVE MODE`); err != nil {
			t.Fatal("lock fixture failed")
		}
		bounded := cfg
		bounded.Timeout = 50 * time.Millisecond
		probe, err := NewPGObserver(pool, bounded)
		if err != nil {
			t.Fatal("bounded observer configuration failed")
		}
		started := time.Now()
		if _, err = probe.Sample(t.Context()); err == nil {
			t.Fatal("locked relation did not time out")
		}
		if time.Since(started) > 2*time.Second {
			t.Fatal("query timeout not bounded")
		}
		if _, err = probe.Sample(t.Context()); err == nil {
			t.Fatal("timeout observer resumed")
		}
	})
	t.Run("physical_session_identity_latches", func(t *testing.T) {
		changed := pool.Config()
		changed.MaxConns = 1
		changed.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			_, err := conn.Exec(ctx, `SET search_path=public`)
			return err
		}
		altered, err := pgxpool.NewWithConfig(t.Context(), changed)
		if err != nil {
			t.Fatal("identity test pool failed")
		}
		defer altered.Close()
		probe, err := NewPGObserver(altered, cfg)
		if err != nil {
			t.Fatal("static authorization changed unexpectedly")
		}
		if _, err = probe.Sample(t.Context()); err == nil {
			t.Fatal("physical session escaped search_path")
		}
		if _, err = probe.Sample(t.Context()); err == nil {
			t.Fatal("identity observer resumed")
		}
	})
	// Identity change and cancelled query both permanently latch, without writes.
	wrong := pool.Config()
	wrong.ConnConfig.RuntimeParams["search_path"] = "public"
	p, err := pgxpool.NewWithConfig(t.Context(), wrong)
	if err != nil {
		t.Fatal("test pool configuration failed")
	}
	defer p.Close()
	if _, err = NewPGObserver(p, cfg); err == nil {
		t.Fatal("wrong pool scope accepted")
	}
	cancelled, err := NewPGObserver(pool, cfg)
	if err != nil {
		t.Fatal("observer config failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = cancelled.Sample(ctx); err == nil {
		t.Fatal("cancelled observer succeeded")
	}
	if _, err = cancelled.Sample(t.Context()); err == nil {
		t.Fatal("failed observer resumed")
	}
}
