package store

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

const supplierExpiryMigration171 = "171_automatic_renewal_sources.sql"
const supplierExpiryMigration172 = "172_supplier_expiry_lifecycle.sql"

// This predecessor is built from the original embedded SQL. The expiry
// envelope must not exist until migration 172 actually runs.
func supplierExpirySchema171(t *testing.T) *Store {
	t.Helper()
	db := supplierLifecycleSchema169(t)
	applyNotificationUpgradeMigration(t, db, supplierLifecycleMigration170)
	applyNotificationUpgradeMigration(t, db, supplierExpiryMigration171)
	var boundary string
	var expiryCheck bool
	if err := db.Pool.QueryRow(t.Context(), `SELECT max(version),
EXISTS(SELECT 1 FROM pg_constraint WHERE conrelid='provision_jobs'::regclass
 AND conname='provision_jobs_password_request_check'
 AND pg_get_expr(conbin,conrelid) LIKE '%system_expiry_suspension%')
FROM schema_migrations`).Scan(&boundary, &expiryCheck); err != nil || boundary != supplierExpiryMigration171 || expiryCheck {
		t.Fatal("fixture did not originate at untouched schema171", boundary, expiryCheck, err)
	}
	return db
}

func supplierExpiryUpgradeMigrations(t *testing.T, db *Store, boundary string) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT coalesce(jsonb_agg(to_jsonb(m) ORDER BY version),'[]'::jsonb)::text
FROM schema_migrations m WHERE version<=$1`, boundary).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func supplierExpiryUpgradeSources(t *testing.T, db *Store) string {
	t.Helper()
	var value string
	if err := db.Pool.QueryRow(t.Context(), `SELECT jsonb_build_object(
'automatic_sources',(SELECT coalesce(jsonb_agg(to_jsonb(x) ORDER BY invoice_id),'[]'::jsonb)
 FROM auto_renewal_invoice_sources x),
'expiry_jobs',(SELECT count(*) FROM provision_jobs WHERE request_payload ? 'system_expiry_suspension'
 OR request_payload ? 'expiry_due_unix_micro'),
'expiry_markers',(SELECT count(*) FROM services WHERE resource_info ? 'system_expiry_suspension_job_id'))::text`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSupplierExpiryUpgrade171PreservesHistoryAndDoesNotInferSource(t *testing.T) {
	db := supplierExpirySchema171(t)
	f := supplierLifecycleUpgradeHistory(t, db)
	// A historical suspended resource and even a canonical-looking job key
	// establish neither scheduler authority nor permission to resume it.
	if _, err := db.Pool.Exec(t.Context(), `UPDATE services
SET status='suspended',resource_info=resource_info||'{"suspend_reason":"due-历史到期暂停"}'::jsonb,
next_due_at=clock_timestamp()-interval '2 days'
WHERE id=$1`, f.services[4]); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,attempts,completed_at)
SELECT id,'suspend','expiry-suspend-service-'||id::text||'-due-'||
 (extract(epoch FROM next_due_at)*1000000)::bigint::text,'succeeded',1,clock_timestamp()
FROM services WHERE id=$1`, f.services[4]); err != nil {
		t.Fatal(err)
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	oldMigrations := supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration171)
	oldSources := supplierExpiryUpgradeSources(t, db)
	if err := db.CheckSchema(t.Context()); err == nil {
		t.Fatal("current runtime accepted schema171 before the expiry migration")
	}
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := db.CheckSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if actual := supplierLifecycleUpgradeFacts(t, db); actual != before {
		t.Fatal("schema171->172 changed historical resources, jobs, account lease or financial facts")
	}
	if actual := supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration171); actual != oldMigrations {
		t.Fatal("expiry migration changed predecessor checksums or application records")
	}
	if actual := supplierExpiryUpgradeSources(t, db); actual != oldSources {
		t.Fatal("expiry migration inferred a historical suspension marker or source")
	}
	var sources, markers int
	if err := db.Pool.QueryRow(t.Context(), `SELECT
 (SELECT count(*) FROM provision_jobs WHERE request_payload ? 'system_expiry_suspension'
  OR request_payload ? 'expiry_due_unix_micro'),
 (SELECT count(*) FROM services WHERE resource_info ? 'system_expiry_suspension_job_id')`).Scan(&sources, &markers); err != nil || sources != 0 || markers != 0 {
		t.Fatal("historical suspensions acquired expiry authority", sources, markers, err)
	}
	allMigrations := supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration172)
	if err := db.Migrate(t.Context()); err != nil || supplierLifecycleUpgradeFacts(t, db) != before ||
		supplierExpiryUpgradeSources(t, db) != oldSources ||
		supplierExpiryUpgradeMigrations(t, db, supplierExpiryMigration172) != allMigrations {
		t.Fatal("repeat migration changed history, source authority or migration identity", err)
	}
	assertNotificationUpgradeChecksums(t, db)
}

func TestSupplierExpiryRequestDatabaseGuards(t *testing.T) {
	db := supplierExpirySchema171(t)
	f := supplierLifecycleUpgradeHistory(t, db)
	if err := db.Migrate(t.Context()); err != nil {
		t.Fatal(err)
	}
	valid := []string{
		`{"system_expiry_suspension":true,"expiry_due_unix_micro":1}`,
		`{"system_expiry_suspension":true,"expiry_due_unix_micro":9223372036854775807}`,
		`{"manual_lifecycle":true,"system_expiry_suspension":true,"expiry_due_unix_micro":1800000000000000}`,
	}
	var immutableJob int64
	for n, payload := range valid {
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,request_payload)
VALUES($1,'suspend',$2,'succeeded',$3::jsonb) RETURNING id`, f.services[4], fmt.Sprintf("expiry-valid-%d", n), payload).Scan(&id); err != nil {
			t.Fatal("valid credential-free expiry envelope rejected", payload, err)
		}
		if n == 0 {
			immutableJob = id
		}
	}
	invalid := []struct {
		name, action, payload string
		credentials           []byte
		fingerprint           string
	}{
		{name: "missing_due", action: "suspend", payload: `{"system_expiry_suspension":true}`},
		{name: "missing_system", action: "suspend", payload: `{"expiry_due_unix_micro":1}`},
		{name: "system_false", action: "suspend", payload: `{"system_expiry_suspension":false,"expiry_due_unix_micro":1}`},
		{name: "system_string", action: "suspend", payload: `{"system_expiry_suspension":"true","expiry_due_unix_micro":1}`},
		{name: "due_null", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":null}`},
		{name: "due_zero", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":0}`},
		{name: "due_negative", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":-1}`},
		{name: "due_fraction", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1.5}`},
		{name: "due_decimal", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1.0}`},
		{name: "due_string", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":"1"}`},
		{name: "due_boolean", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":true}`},
		{name: "due_overflow", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":9223372036854775808}`},
		{name: "due_very_large", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1000000000000000000000000}`},
		{name: "manual_false", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1,"manual_lifecycle":false}`},
		{name: "manual_string", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1,"manual_lifecycle":"true"}`},
		{name: "manual_null", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1,"manual_lifecycle":null}`},
		{name: "injected_field", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1,"token":"injected"}`},
		{name: "injected_invoice", action: "suspend", payload: `{"system_expiry_suspension":true,"expiry_due_unix_micro":1,"billing_invoice_id":2}`},
		{name: "resume", action: "resume", payload: valid[0]},
		{name: "terminate", action: "terminate", payload: valid[0]},
		{name: "create", action: "create", payload: valid[0]},
		{name: "sync", action: "sync", payload: valid[0]},
		{name: "encrypted_credentials", action: "suspend", payload: valid[0], credentials: []byte{1, 2, 3}},
		{name: "credential_fingerprint", action: "suspend", payload: valid[0], fingerprint: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{name: "array", action: "suspend", payload: `[]`},
		{name: "boolean", action: "suspend", payload: `true`},
	}
	before := supplierLifecycleUpgradeFacts(t, db)
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			_, err := db.Pool.Exec(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,
request_payload,request_credentials_encrypted,request_fingerprint)
VALUES($1,$2,$3,'succeeded',$4::jsonb,$5,$6)`, f.services[4], item.action, "expiry-invalid-"+item.name, item.payload, item.credentials, item.fingerprint)
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "provision_jobs_password_request_check" {
				t.Fatalf("invalid expiry envelope was not rejected by the request CHECK: err=%v", err)
			}
			if supplierLifecycleUpgradeFacts(t, db) != before {
				t.Fatal("rejected expiry envelope changed a resource, request or financial fact")
			}
		})
	}
	for _, query := range []string{
		`UPDATE provision_jobs SET request_payload='{"system_expiry_suspension":true,"expiry_due_unix_micro":2}' WHERE id=$1`,
		`UPDATE provision_jobs SET request_payload='{}' WHERE id=$1`,
		`UPDATE provision_jobs SET request_payload='{"manual_lifecycle":true,"system_expiry_suspension":true,"expiry_due_unix_micro":1}' WHERE id=$1`,
		`UPDATE provision_jobs SET action='resume' WHERE id=$1`,
		`UPDATE provision_jobs SET service_id=` + fmt.Sprint(f.services[5]) + ` WHERE id=$1`,
		`UPDATE provision_jobs SET idempotency_key='replacement-expiry-source' WHERE id=$1`,
	} {
		if _, err := db.Pool.Exec(t.Context(), query, immutableJob); err == nil || supplierLifecycleUpgradeFacts(t, db) != before {
			t.Fatal("immutable expiry source was changed", query, err)
		}
	}
	// The only payload transition retained from migration 170 adds the manual
	// marker to the same, still-undispatched running request. It may not swap
	// the original due/source, or relabel a dispatched write as unsent work.
	for _, dispatched := range []bool{false, true} {
		serviceID := f.services[5]
		if dispatched {
			// The prior case intentionally retains its waiting_manual duty.
			// Use another resource so the one-inflight constraint remains real.
			serviceID = f.services[6]
		}
		var id int64
		if err := db.Pool.QueryRow(t.Context(), `INSERT INTO provision_jobs(service_id,action,idempotency_key,status,
attempts,locked_at,external_started_at,request_payload)
VALUES($1,'suspend',$2,'running',1,clock_timestamp(),CASE WHEN $3 THEN clock_timestamp() END,$4::jsonb)
RETURNING id`, serviceID, fmt.Sprintf("expiry-running-dispatched-%v", dispatched), dispatched, valid[0]).Scan(&id); err != nil {
			t.Fatal(err)
		}
		unchanged := supplierLifecycleUpgradeFacts(t, db)
		if _, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET status='waiting_manual',locked_at=NULL,
request_payload='{"manual_lifecycle":true,"system_expiry_suspension":true,"expiry_due_unix_micro":2}' WHERE id=$1`, id); err == nil || supplierLifecycleUpgradeFacts(t, db) != unchanged {
			t.Fatal("running-to-manual transition changed the original expiry source", dispatched, err)
		}
		_, err := db.Pool.Exec(t.Context(), `UPDATE provision_jobs SET status='waiting_manual',locked_at=NULL,
request_payload=request_payload||'{"manual_lifecycle":true}'::jsonb WHERE id=$1`, id)
		if dispatched {
			if err == nil || supplierLifecycleUpgradeFacts(t, db) != unchanged {
				t.Fatal("dispatched expiry action was relabeled as unsent manual work", err)
			}
			continue
		}
		if err != nil {
			t.Fatal("undispatched expiry action could not retain its source when becoming manual", err)
		}
		var state string
		var intact bool
		if err := db.Pool.QueryRow(t.Context(), `SELECT status,
request_payload='{"manual_lifecycle":true,"system_expiry_suspension":true,"expiry_due_unix_micro":1}'::jsonb
AND external_started_at IS NULL AND locked_at IS NULL FROM provision_jobs WHERE id=$1`, id).Scan(&state, &intact); err != nil || state != "waiting_manual" || !intact {
			t.Fatal("manual transition lost expiry identity or acquired a dispatch marker", state, intact, err)
		}
	}
}
