package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const runtimeFixturePrefix = "release_runtime_"

var runtimeFixtureSchema = regexp.MustCompile(`^release_runtime_[a-f0-9]{24}$`)

type releaseRuntimeFixture struct {
	base                                         *pgxpool.Pool
	db                                           *Store
	schema, dsn, database, applicationName       string
	version                                      int
	serviceID, jobID                             int64
	initialVersion, historyMax                   int64
	before, after                                string
	first                                        releaseRuntimeState
	prepared, verified, restartVerified, cleaned bool
	migrationBaseline                            string
	migrationChecks                              int
}
type releaseRuntimeState struct {
	Status               string `json:"status"`
	ServiceStatus        string `json:"serviceStatus"`
	Mode                 string `json:"mode"`
	Action               string `json:"action"`
	Attempts             int    `json:"attempts"`
	ServiceVersion       int64  `json:"serviceVersion"`
	BusinessAuditCount   int    `json:"businessAuditCount"`
	DeliveryHistoryCount int    `json:"deliveryHistoryCount"`
}

// URL and parsed driver fields must both be constrained: libpq URL query host
// overrides and caller-supplied options/search_path must not bypass loopback.
func runtimeFixtureDSN(raw string) (*url.URL, *pgxpool.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Hostname() != "127.0.0.1" || u.Port() == "" || u.Fragment != "" {
		return nil, nil, errors.New("fixture requires explicit IPv4 loopback PostgreSQL URL")
	}
	if !strings.HasSuffix(strings.TrimPrefix(u.Path, "/"), "_test") {
		return nil, nil, errors.New("fixture requires dedicated _test database")
	}
	for key, values := range u.Query() {
		if key != "sslmode" || len(values) != 1 || (values[0] != "disable" && values[0] != "prefer") {
			return nil, nil, errors.New("fixture DSN query overrides are forbidden")
		}
	}
	cfg, err := pgxpool.ParseConfig(u.String())
	if err != nil || cfg.ConnConfig.Host != "127.0.0.1" || !strings.HasSuffix(cfg.ConnConfig.Database, "_test") || fmt.Sprint(cfg.ConnConfig.Port) != u.Port() {
		return nil, nil, errors.New("fixture parsed database identity invalid")
	}
	cfg.ConnConfig.ConnectTimeout = 5 * time.Second
	cfg.MaxConns = 2
	return u, cfg, nil
}
func newReleaseRuntimeFixture(ctx context.Context, raw string) (*releaseRuntimeFixture, error) {
	u, cfg, err := runtimeFixtureDSN(raw)
	if err != nil {
		return nil, err
	}
	random := make([]byte, 12)
	if _, err = rand.Read(random); err != nil {
		return nil, errors.New("fixture randomness unavailable")
	}
	f := &releaseRuntimeFixture{schema: runtimeFixturePrefix + hex.EncodeToString(random), database: cfg.ConnConfig.Database}
	f.applicationName = runtimeFixturePrefix + f.schema
	cfg.ConnConfig.RuntimeParams = map[string]string{"application_name": "fixture_base_" + f.schema}
	f.base, err = pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, errors.New("fixture database unavailable")
	}
	success := false
	defer func() {
		if !success {
			f.base.Close()
		}
	}()
	var actualDB, serverAddr string
	if err = f.base.QueryRow(ctx, `SELECT current_database(),current_setting('server_version_num')::int,host(inet_server_addr())`).Scan(&actualDB, &f.version, &serverAddr); err != nil {
		return nil, errors.New("fixture cannot establish server identity")
	}
	if actualDB != f.database || f.version < 180000 || f.version >= 190000 || serverAddr != "127.0.0.1" {
		return nil, errors.New("fixture requires matching loopback PG18 dedicated database")
	}
	if _, err = f.base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.schema}.Sanitize()); err != nil {
		return nil, errors.New("fixture schema creation failed")
	}
	defer func() {
		if !success {
			_, _ = f.base.Exec(context.Background(), "DROP SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE")
		}
	}()
	query := url.Values{}
	query.Set("sslmode", "disable")
	query.Set("search_path", f.schema)
	query.Set("application_name", f.applicationName)
	query.Set("connect_timeout", "5")
	u.RawQuery = query.Encode()
	f.dsn = u.String()
	scoped := cfg.Copy()
	scoped.ConnConfig.RuntimeParams = map[string]string{"search_path": f.schema, "application_name": "fixture_control_" + f.schema}
	pool, err := pgxpool.NewWithConfig(ctx, scoped)
	if err != nil {
		return nil, errors.New("fixture isolated connection unavailable")
	}
	f.db = &Store{Pool: pool}
	var actualSchema string
	if err = pool.QueryRow(ctx, "SELECT current_schema()").Scan(&actualSchema); err != nil || actualSchema != f.schema {
		pool.Close()
		return nil, errors.New("fixture search_path isolation failed")
	}
	success = true
	return f, nil
}
func (f *releaseRuntimeFixture) cleanup(ctx context.Context) error {
	if f.cleaned {
		return nil
	}
	var count int
	if err := f.base.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE datname=current_database() AND application_name=$1", f.applicationName).Scan(&count); err != nil {
		return errors.New("fixture cannot check owned runtime connections")
	}
	if count != 0 {
		return errors.New("fixture runtime connections still active; stop owned processes first")
	}
	f.db.Close()
	if !runtimeFixtureSchema.MatchString(f.schema) {
		return errors.New("fixture schema ownership invalid")
	}
	if _, err := f.base.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE"); err != nil {
		return errors.New("fixture owned schema cleanup failed")
	}
	var exists bool
	if err := f.base.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)", f.schema).Scan(&exists); err != nil || exists {
		return errors.New("fixture schema cleanup not proven")
	}
	f.cleaned = true
	f.base.Close()
	return nil
}
func (f *releaseRuntimeFixture) migrationDigest(ctx context.Context) (string, error) {
	h := sha256.New()
	// Bind table columns, constraints, index definitions and trigger/function
	// bodies as well as all seeded rows; a second migrate must change neither.
	queries := []string{
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x::text)::text,'[]') FROM (SELECT table_name,column_name,ordinal_position,data_type,is_nullable,column_default FROM information_schema.columns WHERE table_schema=current_schema()) x`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x::text)::text,'[]') FROM (SELECT c.relname,con.conname,pg_get_constraintdef(con.oid) AS definition FROM pg_constraint con JOIN pg_class c ON c.oid=con.conrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema()) x`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x::text)::text,'[]') FROM (SELECT indexname,indexdef FROM pg_indexes WHERE schemaname=current_schema()) x`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x::text)::text,'[]') FROM (SELECT p.proname,pg_get_functiondef(p.oid) AS definition FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=current_schema() AND p.prokind='f') x`,
		`SELECT COALESCE(jsonb_agg(to_jsonb(x) ORDER BY x::text)::text,'[]') FROM (SELECT t.tgname,pg_get_triggerdef(t.oid) AS definition FROM pg_trigger t JOIN pg_class c ON c.oid=t.tgrelid JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=current_schema() AND NOT t.tgisinternal) x`,
	}
	for _, query := range queries {
		var value string
		if err := f.db.Pool.QueryRow(ctx, query).Scan(&value); err != nil {
			return "", errors.New("fixture schema digest unavailable")
		}
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	rows, err := f.db.Pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() ORDER BY tablename`)
	if err != nil {
		return "", errors.New("fixture migration table inventory unavailable")
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return "", errors.New("fixture migration table inventory invalid")
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", errors.New("fixture migration inventory failed")
	}
	for _, table := range tables {
		var value string
		if err = f.db.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS v FROM `+pgx.Identifier{table}.Sanitize()+` t) x`).Scan(&value); err != nil {
			return "", errors.New("fixture seeded data digest unavailable")
		}
		h.Write([]byte(table))
		h.Write([]byte{0})
		h.Write([]byte(value))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (f *releaseRuntimeFixture) verifyMigrations(ctx context.Context) (string, error) {
	if err := f.db.CheckSchema(ctx); err != nil {
		return "", errors.New("fixture schema/checksum verification failed")
	}
	digest, err := f.migrationDigest(ctx)
	if err != nil {
		return "", err
	}
	if f.prepared {
		return "", errors.New("fixture migrations must be verified before prepare")
	}
	if f.migrationBaseline == "" {
		f.migrationBaseline = digest
		f.migrationChecks++
		return digest, nil
	}
	if digest != f.migrationBaseline {
		return digest, errors.New("fixture migration/schema digest changed between runs")
	}
	f.migrationChecks++
	return digest, nil
}

func (f *releaseRuntimeFixture) prepare(ctx context.Context) error {
	if f.prepared {
		return errors.New("fixture already prepared")
	}
	if f.migrationChecks < 2 {
		return errors.New("fixture requires two verified same-package migration runs")
	}
	if err := f.db.CheckSchema(ctx); err != nil {
		return errors.New("fixture requires same-package migrations before prepare")
	}
	var tableCount int
	if err := f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM users)+(SELECT count(*) FROM products)+(SELECT count(*) FROM orders)+(SELECT count(*) FROM services)+(SELECT count(*) FROM provision_jobs)`).Scan(&tableCount); err != nil || tableCount != 0 {
		return errors.New("fixture requires an empty newly migrated schema")
	}
	// Explicitly close every scheduler/external channel input in our own schema.
	for _, sql := range []string{
		`UPDATE scheduler_task_settings SET enabled=false`,
		`UPDATE payment_settings SET configured=true,enabled=false,mock_enabled=false,api_url='',pid='',types='{}',key_ciphertext=''::bytea`,
		`UPDATE smtp_settings SET configured=true,enabled=false,host='',username='',password_encrypted=''::bytea,from_address=''`,
		`UPDATE sms_settings SET configured=true,enabled=false,endpoint='',token_encrypted=''::bytea`,
		`UPDATE channel_instances SET enabled=false`,
		`UPDATE notification_templates SET active=false`,
		`UPDATE affiliate_programs SET status='disabled'`,
	} {
		if _, err := f.db.Pool.Exec(ctx, sql); err != nil {
			return errors.New("fixture safe settings preparation failed")
		}
	}
	var uid, pid, order, item int64
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO users(username,email,password_hash,display_name,role,balance_cents,email_verified,phone_verified) VALUES('runtime-fixture','runtime-fixture@example.test','invalid-login-hash','Runtime fixture','customer',12345,false,false) RETURNING id`).Scan(&uid); err != nil {
		return errors.New("fixture user preparation failed")
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO products(name,slug,price_cents,provision_mode,approval_required,active) VALUES('Runtime simulation','runtime-simulation',100,'simulation',false,false) RETURNING id`).Scan(&pid); err != nil {
		return errors.New("fixture product preparation failed")
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO orders(order_no,user_id,total_cents,idempotency_key,request_hash,status,review_status) VALUES('RUNTIME-SIMULATION',$1,100,'runtime-order','runtime-fixture','active','approved') RETURNING id`, uid).Scan(&order); err != nil {
		return errors.New("fixture reviewed order preparation failed")
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO order_items(order_id,product_id,product_name,billing_cycle,quantity,price_cents,setup_fee_cents,total_cents) VALUES($1,$2,'Runtime simulation','monthly',1,100,0,100) RETURNING id`, order, pid).Scan(&item); err != nil {
		return errors.New("fixture item preparation failed")
	}
	if err := f.db.Pool.QueryRow(ctx, `INSERT INTO services(user_id,order_id,order_item_id,product_id,name,domain,billing_cycle,status,provision_mode,auto_renew,next_due_at) VALUES($1,$2,$3,$4,'Runtime simulation','runtime-simulation.test','monthly','suspended','simulation',false,now()+interval '1 year') RETURNING id,version`, uid, order, item, pid).Scan(&f.serviceID, &f.initialVersion); err != nil {
		return errors.New("fixture suspended simulation preparation failed")
	}
	queued, err := f.db.EnqueueServiceAction(ctx, uid, f.serviceID, "resume", "runtime-simulation-resume", false)
	if err != nil {
		return errors.New("fixture legitimate resume enqueue failed")
	}
	f.jobID = queued.JobID
	if err = f.db.Pool.QueryRow(ctx, `SELECT coalesce(max(id),0) FROM external_refund_delivery_history`).Scan(&f.historyMax); err != nil {
		return errors.New("fixture delivery history baseline unavailable")
	}
	if err = f.safeInputs(ctx); err != nil {
		return err
	}
	state, err := f.state(ctx)
	if err != nil || state.Status != "pending" || state.Action != "resume" || state.ServiceStatus != "suspended" || state.Attempts != 0 || state.ServiceVersion != f.initialVersion || state.BusinessAuditCount != 0 || state.DeliveryHistoryCount != 0 {
		return errors.New("fixture pending resume validity failed")
	}
	f.before, err = f.stableDigest(ctx)
	if err != nil {
		return err
	}
	f.prepared = true
	return nil
}
func (f *releaseRuntimeFixture) safeInputs(ctx context.Context) error {
	var safe bool
	err := f.db.Pool.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM channel_instances WHERE enabled OR octet_length(secret_ciphertext)>0) AND
 NOT EXISTS(SELECT 1 FROM payment_settings WHERE enabled OR mock_enabled OR api_url<>'' OR pid<>'' OR octet_length(key_ciphertext)>0) AND
 NOT EXISTS(SELECT 1 FROM smtp_settings WHERE enabled OR host<>'' OR octet_length(password_encrypted)>0) AND
 NOT EXISTS(SELECT 1 FROM sms_settings WHERE enabled OR endpoint<>'' OR octet_length(token_encrypted)>0) AND
 NOT EXISTS(SELECT 1 FROM supplier_accounts) AND
 NOT EXISTS(SELECT 1 FROM services WHERE provision_mode<>'simulation' OR auto_renew OR next_due_at<now()+interval '30 days') AND
 NOT EXISTS(SELECT 1 FROM services WHERE id<>$2) AND
 NOT EXISTS(SELECT 1 FROM notification_templates WHERE active) AND
 NOT EXISTS(SELECT 1 FROM scheduler_task_settings WHERE enabled) AND
 NOT EXISTS(SELECT 1 FROM affiliate_programs WHERE status='active') AND
 NOT EXISTS(SELECT 1 FROM provision_jobs WHERE id<>$1) AND
 NOT EXISTS(SELECT 1 FROM email_outbox) AND NOT EXISTS(SELECT 1 FROM sms_outbox) AND
 NOT EXISTS(SELECT 1 FROM notification_delivery_events) AND
 NOT EXISTS(SELECT 1 FROM payment_transactions) AND NOT EXISTS(SELECT 1 FROM invoices) AND
 NOT EXISTS(SELECT 1 FROM scheduler_runs WHERE affected_count<>0) AND
 NOT EXISTS(SELECT 1 FROM affiliate_commission_entries) AND NOT EXISTS(SELECT 1 FROM affiliate_turnover_entries)`, f.jobID, f.serviceID).Scan(&safe)
	if err != nil || !safe {
		return errors.New("fixture external channels or unrelated due/notification/financial work detected")
	}
	return nil
}
func (f *releaseRuntimeFixture) state(ctx context.Context) (releaseRuntimeState, error) {
	var s releaseRuntimeState
	err := f.db.Pool.QueryRow(ctx, `SELECT j.status,s.status,coalesce(j.result->>'mode',''),j.action,j.attempts,s.version,
 (SELECT count(*) FROM audit_events WHERE action='service.simulation.completed' AND entity_type='service' AND entity_id=s.id::text AND payload->>'job_id'=j.id::text AND payload->>'action'='resume'),
 (SELECT count(*) FROM external_refund_delivery_history WHERE id>$3)
 FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1 AND s.id=$2`, f.jobID, f.serviceID, f.historyMax).Scan(&s.Status, &s.ServiceStatus, &s.Mode, &s.Action, &s.Attempts, &s.ServiceVersion, &s.BusinessAuditCount, &s.DeliveryHistoryCount)
	if err != nil {
		return s, errors.New("fixture expected job/service missing")
	}
	return s, nil
}

// Entire ordinary business rows are hashed, not just counts: offsetting balance
// changes, payment rewrites or unrelated audits cannot hide behind equal totals.
func (f *releaseRuntimeFixture) stableDigest(ctx context.Context) (string, error) {
	// The worker records zero-effect scheduler polls even when task settings are
	// disabled. Everything else outside the intended execution is immutable.
	rows, err := f.db.Pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename NOT IN ('services','provision_jobs','audit_events','scheduler_runs','external_refund_delivery_history') ORDER BY tablename`)
	if err != nil {
		return "", errors.New("fixture business table inventory unavailable")
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			rows.Close()
			return "", errors.New("fixture business table inventory invalid")
		}
		tables = append(tables, table)
	}
	rows.Close()
	if rows.Err() != nil {
		return "", errors.New("fixture business table inventory failed")
	}
	hash := sha256.New()
	for _, table := range tables {
		var rows string
		if err := f.db.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS v FROM `+pgx.Identifier{table}.Sanitize()+` t) x`).Scan(&rows); err != nil {
			return "", errors.New("fixture business snapshot unavailable")
		}
		hash.Write([]byte(table))
		hash.Write([]byte{0})
		hash.Write([]byte(rows))
		hash.Write([]byte{0})
	}
	var audits string
	if err := f.db.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(a) ORDER BY a.id),'[]'::jsonb)::text FROM audit_events a WHERE NOT(action='service.simulation.completed' AND entity_type='service' AND entity_id=$1 AND payload->>'job_id'=$2 AND payload->>'action'='resume')`, fmt.Sprint(f.serviceID), fmt.Sprint(f.jobID)).Scan(&audits); err != nil {
		return "", errors.New("fixture audit snapshot unavailable")
	}
	hash.Write([]byte(audits))
	var history string
	if err := f.db.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(to_jsonb(h) ORDER BY h.id),'[]'::jsonb)::text FROM external_refund_delivery_history h WHERE id<=$1`, f.historyMax).Scan(&history); err != nil {
		return "", errors.New("fixture delivery history snapshot unavailable")
	}
	hash.Write([]byte(history))
	var unrelatedHistory bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM external_refund_delivery_history h WHERE h.id>$1 AND (h.service_id IS DISTINCT FROM $2 OR h.order_id IS DISTINCT FROM (SELECT order_id FROM services WHERE id=$2) OR h.event<>'service_changed'))`, f.historyMax, f.serviceID).Scan(&unrelatedHistory); err != nil || unrelatedHistory {
		return "", errors.New("fixture unrelated delivery history changed")
	}
	var service string
	if err := f.db.Pool.QueryRow(ctx, `SELECT (to_jsonb(s)-'status'-'version'-'updated_at')::text FROM services s WHERE id=$1`, f.serviceID).Scan(&service); err != nil {
		return "", errors.New("fixture service baseline unavailable")
	}
	hash.Write([]byte(service))
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func (f *releaseRuntimeFixture) verify(ctx context.Context, restart bool) (releaseRuntimeState, bool, error) {
	var zero releaseRuntimeState
	if !f.prepared {
		return zero, false, errors.New("fixture not prepared")
	}
	if err := f.safeInputs(ctx); err != nil {
		return zero, false, err
	}
	state, err := f.state(ctx)
	if err != nil {
		return zero, false, err
	}
	digest, err := f.stableDigest(ctx)
	if err != nil {
		return zero, false, err
	}
	if digest != f.before {
		return state, false, errors.New("fixture financial/notification/unrelated business state changed")
	}
	if !restart && (state.Status == "pending" || state.Status == "running") {
		return state, true, nil
	}
	if state.Status != "succeeded" || state.ServiceStatus != "active" || state.Mode != "simulation" || state.Action != "resume" || state.Attempts != 1 || state.ServiceVersion != f.initialVersion+1 || state.BusinessAuditCount != 1 || state.DeliveryHistoryCount != 1 {
		return state, false, errors.New("fixture simulation completion invariant failed")
	}
	// Include full service/job/audit bytes after the first success, so restart
	// cannot silently mutate even fields omitted from the public state summary.
	var execution string
	if err = f.db.Pool.QueryRow(ctx, `SELECT jsonb_build_array(to_jsonb(j),to_jsonb(s),(SELECT jsonb_agg(to_jsonb(a) ORDER BY a.id) FROM audit_events a WHERE a.action='service.simulation.completed'),(SELECT jsonb_agg(to_jsonb(h) ORDER BY h.id) FROM external_refund_delivery_history h))::text FROM provision_jobs j JOIN services s ON s.id=j.service_id WHERE j.id=$1`, f.jobID).Scan(&execution); err != nil {
		return state, false, errors.New("fixture execution snapshot unavailable")
	}
	if f.verified || restart {
		if !f.verified || state != f.first || execution != f.after {
			return state, false, errors.New("fixture restarted worker duplicated or changed execution")
		}
	} else {
		f.first = state
		f.after = execution
		f.verified = true
	}
	if restart {
		f.restartVerified = true
	}
	return state, false, nil
}

func fixtureDirectory(dir string) error {
	if !filepath.IsAbs(dir) {
		return errors.New("fixture directory must be absolute")
	}
	current := string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(filepath.Clean(dir), current), string(filepath.Separator)) {
		current = filepath.Join(current, part)
		s, e := os.Lstat(current)
		if e != nil || s.Mode()&os.ModeSymlink != 0 {
			return errors.New("fixture directory link/missing component forbidden")
		}
	}
	stat, err := os.Stat(dir)
	if err != nil || !stat.IsDir() || stat.Mode().Perm() != 0700 {
		return errors.New("fixture directory requires mode 0700")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		return errors.New("fixture directory must be exclusive and empty")
	}
	return nil
}
func fixtureWrite(dir, name string, value any, mode os.FileMode) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return errors.New("fixture response encoding failed")
	}
	raw = append(raw, '\n')
	file, err := os.OpenFile(filepath.Join(dir, name+".tmp"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return errors.New("fixture response file unavailable")
	}
	defer os.Remove(filepath.Join(dir, name+".tmp"))
	if err = file.Chmod(mode); err != nil {
		file.Close()
		return errors.New("fixture response permissions failed")
	}
	if _, err = file.Write(raw); err != nil {
		file.Close()
		return errors.New("fixture response write failed")
	}
	if err = file.Close(); err != nil {
		return errors.New("fixture response close failed")
	}
	if _, err = os.Lstat(filepath.Join(dir, name)); !os.IsNotExist(err) {
		return errors.New("fixture response already exists")
	}
	if err = os.Rename(filepath.Join(dir, name+".tmp"), filepath.Join(dir, name)); err != nil {
		return errors.New("fixture response publish failed")
	}
	return nil
}
func fixtureReadCommand(dir string, sequence int) (string, bool, error) {
	filename := filepath.Join(dir, fmt.Sprintf("command-%d.json", sequence))
	stat, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil || !stat.Mode().IsRegular() || stat.Mode().Perm() != 0600 || stat.Size() > 4096 {
		return "", false, errors.New("fixture command type/permissions/size invalid")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", false, errors.New("fixture command unavailable")
	}
	var command struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(raw, &command) != nil {
		return "", false, errors.New("fixture command JSON invalid")
	}
	switch command.Operation {
	case "prepare", "verify-migrations", "verify-first", "verify-restart", "finish", "abort":
		return command.Operation, true, nil
	}
	return "", false, errors.New("fixture command operation invalid")
}

// TestReleaseRuntimeFixture owns only its random schema. It deliberately does
// not migrate, build, launch a worker, or process a job: the external controller
// must do those with the independently verified sealed package.
func TestReleaseRuntimeFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_RELEASE_RUNTIME_FIXTURE") != "1" {
		t.Skip("opt-in runtime controller fixture")
	}
	dir := os.Getenv("ZJMFGO_RELEASE_RUNTIME_FIXTURE_DIR")
	if err := fixtureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	f, err := newReleaseRuntimeFixture(ctx, os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if !f.cleaned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if e := f.cleanup(cleanupCtx); e != nil {
				t.Error(e)
			}
		}
		_ = os.Remove(filepath.Join(dir, "config.json"))
	}()
	values := map[string]string{"DATABASE_URL": f.dsn}
	for _, key := range []string{"MFA_ENCRYPTION_KEY", "PURCHASE_FIELD_ENCRYPTION_KEY", "PROVISION_ENCRYPTION_KEY"} {
		raw := make([]byte, 32)
		if _, err = rand.Read(raw); err != nil {
			t.Fatal("fixture key generation failed")
		}
		values[key] = hex.EncodeToString(raw)
	}
	if err = fixtureWrite(dir, "config.json", map[string]any{"version": "zjmfgo-runtime-config-v1", "values": values}, 0600); err != nil {
		t.Fatal(err)
	}
	if err = fixtureWrite(dir, "schema-ready.json", map[string]any{"ok": true, "phase": "schema-ready", "schema": f.schema, "database": f.database, "pgVersion": f.version, "applicationName": f.applicationName}, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log("runtime fixture schema-ready; private config written")
	for sequence := 1; ; sequence++ {
		var operation string
		for {
			var present bool
			operation, present, err = fixtureReadCommand(dir, sequence)
			if err != nil {
				t.Fatal(err)
			}
			if present {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("runtime fixture controller deadline exceeded")
			case <-time.After(100 * time.Millisecond):
			}
		}
		result := map[string]any{"ok": true, "phase": operation}
		switch operation {
		case "verify-migrations":
			var digest string
			digest, err = f.verifyMigrations(ctx)
			if err == nil {
				result["migrationSchemaSHA256"] = digest
			}
		case "prepare":
			err = f.prepare(ctx)
			if err == nil {
				result["serviceID"] = f.serviceID
				result["jobID"] = f.jobID
				result["baselineSHA256"] = f.before
				result["initialServiceVersion"] = f.initialVersion
				result["safeInputs"] = true
			}
		case "verify-first", "verify-restart":
			var state releaseRuntimeState
			var pending bool
			state, pending, err = f.verify(ctx, operation == "verify-restart")
			result["state"] = state
			result["pending"] = pending
			result["serviceID"] = f.serviceID
			result["jobID"] = f.jobID
			result["unchangedSHA256"] = f.before
		case "finish":
			if !f.restartVerified {
				err = errors.New("fixture restart has not been verified")
			} else {
				_, _, err = f.verify(ctx, true)
			}
			if err == nil {
				err = f.cleanup(ctx)
			}
		case "abort":
			err = f.cleanup(ctx)
		}
		if err != nil {
			result["ok"] = false
			result["errorCode"] = err.Error()
		}
		if e := fixtureWrite(dir, fmt.Sprintf("result-%d.json", sequence), result, 0644); e != nil {
			t.Fatal(e)
		}
		if (operation == "finish" || operation == "abort") && err == nil {
			if e := os.Remove(filepath.Join(dir, "config.json")); e != nil && !os.IsNotExist(e) {
				t.Fatal("fixture private config cleanup failed")
			}
			if e := fixtureWrite(dir, "done.json", map[string]any{"ok": true, "phase": "cleaned", "schemaRemoved": true}, 0644); e != nil {
				t.Fatal(e)
			}
			return
		}
	}
}
