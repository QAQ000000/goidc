package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"
)

// This fixture only owns a fresh schema in an explicit loopback PG18 _test DB.
// It never migrates, starts services, issues HTTP requests, or creates sessions.
// The controller must perform those operations using authenticated candidates.
type dualWebCredential struct {
	Username string `json:"username"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type dualWebCredentials struct {
	Customer dualWebCredential `json:"customer"`
	Admin    dualWebCredential `json:"admin"`
}

type releaseDualWebFixture struct {
	*releaseRuntimeFixture
	credentials                       dualWebCredentials
	siteMarker                        string
	customerID, adminID               int64
	businessBaseline, sessionBaseline string
	continuityChecks                  int
	finalVerified, logoutVerified     bool
}

func dualWebFixtureEnabled(contract bool) bool {
	key := "ZJMFGO_RELEASE_DUAL_WEB_FIXTURE"
	if contract {
		key = "ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_CONTRACT"
	}
	return os.Getenv(key) == "1" && os.Getenv("ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_ALLOW_SCHEMA") == "1"
}

func dualWebRandom() (string, error) {
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return "", errors.New("dual fixture randomness unavailable")
	}
	return hex.EncodeToString(raw), nil
}

func dualWebPrivateInputs(dir, dsn string) (dualWebCredentials, error) {
	var credentials dualWebCredentials
	values := map[string]string{"DATABASE_URL": dsn}
	for _, key := range []string{"MFA_ENCRYPTION_KEY", "PURCHASE_FIELD_ENCRYPTION_KEY", "PROVISION_ENCRYPTION_KEY"} {
		raw := make([]byte, 32)
		if _, err := rand.Read(raw); err != nil {
			return credentials, errors.New("dual fixture key generation failed")
		}
		values[key] = hex.EncodeToString(raw)
	}
	for _, account := range []*dualWebCredential{&credentials.Customer, &credentials.Admin} {
		name, err := dualWebRandom()
		if err != nil {
			return credentials, err
		}
		account.Username = "dual-" + name[:20]
		account.Email = account.Username + "@example.test"
		account.Password, err = dualWebRandom()
		if err != nil {
			return credentials, err
		}
	}
	if err := fixtureWrite(dir, "config.json", map[string]any{"version": "zjmfgo-runtime-config-v1", "values": values}, 0600); err != nil {
		return credentials, err
	}
	if err := fixtureWrite(dir, "credentials.json", credentials, 0600); err != nil {
		return credentials, err
	}
	return credentials, nil
}

func (f *releaseDualWebFixture) verifyDualMigrations(ctx context.Context) (string, error) {
	if f.migrationChecks >= 4 {
		return "", errors.New("dual fixture requires exactly four migration verifications")
	}
	return f.verifyMigrations(ctx)
}

func (f *releaseDualWebFixture) prepareDual(ctx context.Context) error {
	if f.prepared || f.migrationChecks != 4 {
		return errors.New("dual fixture prepare requires four same-schema migration verifications")
	}
	if err := f.db.CheckSchema(ctx); err != nil {
		return errors.New("dual fixture migrated schema invalid")
	}
	var empty bool
	if err := f.db.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM account_principals) AND NOT EXISTS(SELECT 1 FROM sessions) AND NOT EXISTS(SELECT 1 FROM audit_events) AND NOT EXISTS(SELECT 1 FROM products) AND NOT EXISTS(SELECT 1 FROM services) AND NOT EXISTS(SELECT 1 FROM orders) AND NOT EXISTS(SELECT 1 FROM provision_jobs)`).Scan(&empty); err != nil || !empty {
		return errors.New("dual fixture requires empty newly migrated business tables")
	}
	tx, err := f.db.Pool.Begin(ctx)
	if err != nil {
		return errors.New("dual fixture preparation unavailable")
	}
	defer tx.Rollback(ctx)
	for _, sql := range []string{
		`UPDATE scheduler_task_settings SET enabled=false`,
		`UPDATE payment_settings SET configured=true,enabled=false,mock_enabled=false,api_url='',pid='',types='{}',key_ciphertext=''::bytea`,
		`UPDATE smtp_settings SET configured=true,enabled=false,host='',username='',password_encrypted=''::bytea,from_address=''`,
		`UPDATE sms_settings SET configured=true,enabled=false,endpoint='',token_encrypted=''::bytea`,
		`UPDATE channel_instances SET enabled=false`,
		`UPDATE notification_templates SET active=false`,
		`UPDATE affiliate_programs SET status='disabled'`,
		`UPDATE auth_settings SET login_enabled=true,login_email_enabled=true,captcha_auto_enabled=false`,
		`UPDATE site_settings SET auth_captcha_enabled=false`,
	} {
		if _, err = tx.Exec(ctx, sql); err != nil {
			return errors.New("dual fixture safe settings preparation failed")
		}
	}
	// This schema-derived ASCII marker is public test data, distinct from the
	// Web fallback brand. RootLayout consumes API data.site_name for its title.
	marker := "DualWebSSR_" + f.schema
	if _, err = tx.Exec(ctx, "UPDATE site_settings SET site_name=$1 WHERE id=1", marker); err != nil {
		return errors.New("dual fixture SSR marker preparation failed")
	}
	for _, account := range []struct {
		credential dualWebCredential
		role       string
		table      string
		id         *int64
	}{{f.credentials.Customer, "customer", "users", &f.customerID}, {f.credentials.Admin, "super_admin", "administrators", &f.adminID}} {
		hash, err := bcrypt.GenerateFromPassword([]byte(account.credential.Password), 12)
		if err != nil {
			return errors.New("dual fixture credential hashing failed")
		}
		if err = tx.QueryRow(ctx, `INSERT INTO `+account.table+`(username,email,password_hash,display_name,role,email_verified,phone_verified,password_reset_required,mfa_enabled) VALUES($1,$2,$3,$4,$5,true,false,false,false) RETURNING id`, account.credential.Username, account.credential.Email, string(hash), "Dual Web "+account.role, account.role).Scan(account.id); err != nil {
			return errors.New("dual fixture account preparation failed")
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("dual fixture preparation commit failed")
	}
	if err = f.safeInputs(ctx); err != nil {
		return err
	}
	f.businessBaseline, err = f.dualBusinessDigest(ctx)
	if err != nil {
		return err
	}
	f.prepared = true
	f.siteMarker = marker
	return nil
}

func dualWebAbortEvidence(dir string, schemaRemoved bool) error {
	if !schemaRemoved {
		return nil
	}
	if _, err := os.Lstat(filepath.Join(dir, "done.json")); err == nil {
		return nil // Never overwrite a normal completion or earlier evidence.
	} else if !os.IsNotExist(err) {
		return errors.New("dual fixture cleanup evidence unavailable")
	}
	return fixtureWrite(dir, "done.json", map[string]any{"ok": false, "phase": "aborted", "schemaRemoved": true}, 0644)
}

// Only sessions, login rate counters and constrained authentication/API logs may change. Hash every
// other table, including users, funds, services, jobs and notification settings.
// Hashes of credentials/session rows remain private in memory, never in reports.
func (f *releaseDualWebFixture) dualBusinessDigest(ctx context.Context) (string, error) {
	rows, err := f.db.Pool.Query(ctx, `SELECT tablename FROM pg_tables WHERE schemaname=current_schema() AND tablename NOT IN ('sessions','audit_events','auth_rate_limits','admin_api_logs') ORDER BY tablename`)
	if err != nil {
		return "", errors.New("dual fixture table inventory unavailable")
	}
	var tables []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			rows.Close()
			return "", errors.New("dual fixture table inventory invalid")
		}
		tables = append(tables, name)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return "", errors.New("dual fixture table inventory failed")
	}
	h := sha256.New()
	for _, table := range tables {
		var data string
		if err = f.db.Pool.QueryRow(ctx, `SELECT COALESCE(jsonb_agg(v ORDER BY v::text),'[]'::jsonb)::text FROM (SELECT to_jsonb(t) AS v FROM `+pgx.Identifier{table}.Sanitize()+` t) x`).Scan(&data); err != nil {
			return "", errors.New("dual fixture business snapshot unavailable")
		}
		h.Write([]byte(table))
		h.Write([]byte{0})
		h.Write([]byte(data))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (f *releaseDualWebFixture) verifyDual(ctx context.Context, logout bool) error {
	if !f.prepared || logout && !f.finalVerified {
		return errors.New("dual fixture verification phase invalid")
	}
	if err := f.safeInputs(ctx); err != nil {
		return err
	}
	digest, err := f.dualBusinessDigest(ctx)
	if err != nil || digest != f.businessBaseline {
		return errors.New("dual fixture unrelated business state changed")
	}
	var validAudits bool
	var logins, logouts, sessions, customerSessions, adminSessions int
	err = f.db.Pool.QueryRow(ctx, `SELECT
 NOT EXISTS(SELECT 1 FROM audit_events WHERE actor_id IS NULL OR actor_id NOT IN ($1,$2) OR entity_type<>'user' OR entity_id<>actor_id::text OR action NOT IN ('auth.login_succeeded','auth.logout')),
 (SELECT count(*) FROM audit_events WHERE action='auth.login_succeeded'),
 (SELECT count(*) FROM audit_events WHERE action='auth.logout'),
 (SELECT count(*) FROM sessions),
 (SELECT count(*) FROM sessions WHERE user_id=$1 AND expires_at>now()),
 (SELECT count(*) FROM sessions WHERE user_id=$2 AND expires_at>now())`, f.customerID, f.adminID).Scan(&validAudits, &logins, &logouts, &sessions, &customerSessions, &adminSessions)
	if err != nil || !validAudits || logins != 2 {
		return errors.New("dual fixture authentication audit invariant failed")
	}
	// One successful login per actor, and exactly one logout per actor at end.
	var perActor bool
	err = f.db.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='auth.login_succeeded')=1 AND (SELECT count(*) FROM audit_events WHERE actor_id=$2 AND action='auth.login_succeeded')=1 AND (NOT $3::boolean OR ((SELECT count(*) FROM audit_events WHERE actor_id=$1 AND action='auth.logout')=1 AND (SELECT count(*) FROM audit_events WHERE actor_id=$2 AND action='auth.logout')=1))`, f.customerID, f.adminID, logout).Scan(&perActor)
	if err != nil || !perActor {
		return errors.New("dual fixture per-account authentication invariant failed")
	}
	// authWith logs even customer /me reads (including SSR's internal /me).
	// Permit only this batch's authenticated read/logout routes, never arbitrary
	// logs or unrelated actors. Permission failures return before this insert.
	var validAPILogs bool
	if err = f.db.Pool.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM admin_api_logs WHERE
 actor_id IS NULL OR actor_id NOT IN ($1,$2) OR status_code<>200 OR
 NOT ((method='GET' AND path='/api/v1/me') OR
      (method='GET' AND path='/api/v1/admin/site-settings' AND actor_id=$2) OR
      (method='POST' AND path='/api/v1/auth/logout')))`, f.customerID, f.adminID).Scan(&validAPILogs); err != nil || !validAPILogs {
		return errors.New("dual fixture authenticated API log invariant failed")
	}
	// Two genuine HTTP logins from the private IPv4 loopback proxy increment
	// exactly this single bucket. CSRF/origin rejections happen before authLimit.
	rateKey := sha256.Sum256([]byte("login:127.0.0.1"))
	var validRate bool
	if err = f.db.Pool.QueryRow(ctx, `SELECT count(*)=1 AND coalesce(bool_and(key=$1 AND attempts=2),false) FROM auth_rate_limits`, hex.EncodeToString(rateKey[:])).Scan(&validRate); err != nil || !validRate {
		return errors.New("dual fixture login rate counter invariant failed")
	}
	if logout {
		if sessions != 0 || logouts != 2 {
			return errors.New("dual fixture logout did not revoke both sessions")
		}
		f.logoutVerified = true
		return nil
	}
	if logouts != 0 || sessions != 2 || customerSessions != 1 || adminSessions != 1 {
		return errors.New("dual fixture active session identity invariant failed")
	}
	var snapshot string
	if err = f.db.Pool.QueryRow(ctx, `SELECT jsonb_agg(jsonb_build_array(token_hash,user_id,expires_at,created_at) ORDER BY user_id)::text FROM sessions`).Scan(&snapshot); err != nil {
		return errors.New("dual fixture session snapshot unavailable")
	}
	if f.sessionBaseline == "" {
		f.sessionBaseline = snapshot
	} else if snapshot != f.sessionBaseline {
		return errors.New("dual fixture session changed across proxy generations")
	}
	return nil
}

func dualWebReadCommand(dir string, sequence int) (string, bool, error) {
	filename := filepath.Join(dir, fmt.Sprintf("command-%d.json", sequence))
	s, err := os.Lstat(filename)
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || s.Size() > 4096 {
		return "", false, errors.New("dual fixture command type/permissions/size invalid")
	}
	raw, err := os.ReadFile(filename)
	if err != nil {
		return "", false, errors.New("dual fixture command unavailable")
	}
	var c struct {
		Operation string `json:"operation"`
	}
	if json.Unmarshal(raw, &c) != nil {
		return "", false, errors.New("dual fixture command JSON invalid")
	}
	switch c.Operation {
	case "verify-migrations", "prepare", "verify-session-continuity", "verify-final", "verify-logout", "cleanup", "abort":
		return c.Operation, true, nil
	}
	return "", false, errors.New("dual fixture command operation invalid")
}

func TestReleaseDualWebFixture(t *testing.T) {
	if !dualWebFixtureEnabled(false) {
		t.Skip("dual opt-in isolated dual Web controller fixture")
	}
	dir := os.Getenv("ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_DIR")
	if err := fixtureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 12*time.Minute)
	defer cancel()
	base, err := newReleaseRuntimeFixture(ctx, os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	f := &releaseDualWebFixture{releaseRuntimeFixture: base}
	defer func() {
		if !f.cleaned {
			cleanupCtx, stop := context.WithTimeout(context.Background(), 10*time.Second)
			defer stop()
			if err := f.cleanup(cleanupCtx); err != nil {
				t.Error(err)
			}
		}
		for _, name := range []string{"config.json", "credentials.json"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
				t.Error("dual fixture deferred private input cleanup failed")
			}
		}
		if err := dualWebAbortEvidence(dir, f.cleaned); err != nil {
			t.Error(err)
		}
	}()
	f.credentials, err = dualWebPrivateInputs(dir, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = fixtureWrite(dir, "schema-ready.json", map[string]any{"ok": true, "phase": "schema-ready", "schema": f.schema, "database": f.database, "pgVersion": f.version, "applicationName": f.applicationName}, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log("dual Web fixture schema-ready; private inputs written")
	for sequence := 1; ; sequence++ {
		var operation string
		for {
			var present bool
			operation, present, err = dualWebReadCommand(dir, sequence)
			if err != nil {
				t.Fatal(err)
			}
			if present {
				break
			}
			select {
			case <-ctx.Done():
				t.Fatal("dual fixture controller deadline exceeded")
			case <-time.After(50 * time.Millisecond):
			}
		}
		result := map[string]any{"ok": true, "phase": operation}
		switch operation {
		case "verify-migrations":
			var digest string
			digest, err = f.verifyDualMigrations(ctx)
			if err == nil {
				result["migrationSchemaSHA256"] = digest
				result["migrationChecks"] = f.migrationChecks
			}
		case "prepare":
			err = f.prepareDual(ctx)
			if err == nil {
				result["customerID"] = f.customerID
				result["adminID"] = f.adminID
				result["safeInputs"] = true
				result["externalChannelsDisabled"] = true
				result["siteMarker"] = f.siteMarker
			}
		case "verify-session-continuity":
			if f.finalVerified {
				err = errors.New("dual fixture continuity requested after final verification")
			} else {
				err = f.verifyDual(ctx, false)
			}
			if err == nil {
				f.continuityChecks++
				result["continuityChecks"] = f.continuityChecks
				result["activeSessions"] = 2
				result["successfulLogins"] = 2
				result["sessionIdentityStable"] = true
				result["businessUnchanged"] = true
			}
		case "verify-final":
			if f.continuityChecks < 3 || f.finalVerified {
				err = errors.New("dual fixture final requires three continuity checkpoints")
			} else {
				err = f.verifyDual(ctx, false)
			}
			if err == nil {
				f.finalVerified = true
				result["activeSessions"] = 2
				result["sessionIdentityStable"] = true
				result["businessUnchanged"] = true
			}
		case "verify-logout":
			if f.logoutVerified {
				err = errors.New("dual fixture logout already verified")
			} else {
				err = f.verifyDual(ctx, true)
			}
			if err == nil {
				result["activeSessions"] = 0
				result["successfulLogins"] = 2
				result["successfulLogouts"] = 2
				result["businessUnchanged"] = true
			}
		case "cleanup":
			if !f.logoutVerified {
				err = errors.New("dual fixture successful cleanup requires verified logout")
			} else if err = f.verifyDual(ctx, true); err == nil {
				err = f.cleanup(ctx)
			}
		case "abort":
			err = f.cleanup(ctx)
		}
		if err != nil {
			result["ok"] = false
			result["errorCode"] = err.Error()
		}
		if err = fixtureWrite(dir, fmt.Sprintf("result-%d.json", sequence), result, 0644); err != nil {
			t.Fatal(err)
		}
		if f.cleaned {
			for _, name := range []string{"config.json", "credentials.json"} {
				if err = os.Remove(filepath.Join(dir, name)); err != nil && !os.IsNotExist(err) {
					t.Fatal("dual fixture private input cleanup failed")
				}
			}
			if err = fixtureWrite(dir, "done.json", map[string]any{"ok": true, "phase": "cleaned", "schemaRemoved": true}, 0644); err != nil {
				t.Fatal(err)
			}
			return
		}
	}
}

func TestReleaseDualWebFixtureOptInAndCommandContracts(t *testing.T) {
	for _, key := range []string{"ZJMFGO_RELEASE_DUAL_WEB_FIXTURE", "ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_CONTRACT", "ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_ALLOW_SCHEMA"} {
		t.Setenv(key, "")
	}
	t.Setenv("ZJMFGO_RELEASE_DUAL_WEB_FIXTURE", "1")
	if dualWebFixtureEnabled(false) {
		t.Fatal("single opt-in enabled schema writes")
	}
	t.Setenv("ZJMFGO_RELEASE_DUAL_WEB_FIXTURE_ALLOW_SCHEMA", "1")
	if !dualWebFixtureEnabled(false) || dualWebFixtureEnabled(true) {
		t.Fatal("fixture and contract opt-ins were not independent")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	for i, operation := range []string{"verify-migrations", "prepare", "verify-session-continuity", "verify-final", "verify-logout", "cleanup", "abort"} {
		if err := fixtureWrite(dir, fmt.Sprintf("command-%d.json", i+1), map[string]string{"operation": operation}, 0600); err != nil {
			t.Fatal(err)
		}
		actual, present, err := dualWebReadCommand(dir, i+1)
		if err != nil || !present || actual != operation {
			t.Fatal("valid dual fixture command rejected")
		}
	}
	if err := fixtureWrite(dir, "command-8.json", map[string]string{"operation": "arbitrary-sql"}, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dualWebReadCommand(dir, 8); err == nil {
		t.Fatal("foreign command accepted")
	}
	if err := os.Chmod(filepath.Join(dir, "command-1.json"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := dualWebReadCommand(dir, 1); err == nil {
		t.Fatal("world-readable command accepted")
	}
	if _, present, err := dualWebReadCommand(dir, 9); err != nil || present {
		t.Fatal("absent command invalid")
	}
}

func TestReleaseDualWebFixturePrivateCredentialsContract(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	credentials, err := dualWebPrivateInputs(dir, "private-database-input")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"config.json", "credentials.json"} {
		s, err := os.Lstat(filepath.Join(dir, name))
		if err != nil || !s.Mode().IsRegular() || s.Mode().Perm() != 0600 {
			t.Fatal("private input permissions invalid")
		}
	}
	if credentials.Customer.Password == credentials.Admin.Password || credentials.Customer.Username == credentials.Admin.Username {
		t.Fatal("accounts share random credentials")
	}
	for _, account := range []dualWebCredential{credentials.Customer, credentials.Admin} {
		if raw, err := hex.DecodeString(account.Password); err != nil || len(raw) != 24 {
			t.Fatal("credential entropy invalid")
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "credentials.json"))
	if err != nil {
		t.Fatal("private credentials unavailable")
	}
	var saved dualWebCredentials
	if json.Unmarshal(raw, &saved) != nil || saved != credentials {
		t.Fatal("private credential roundtrip invalid")
	}
	if _, err := dualWebPrivateInputs(dir, "another-input"); err == nil {
		t.Fatal("existing private config overwritten")
	}
}

func TestReleaseDualWebFixtureAbortEvidenceContract(t *testing.T) {
	dir := t.TempDir()
	if err := dualWebAbortEvidence(dir, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "done.json")); !os.IsNotExist(err) {
		t.Fatal("unproven cleanup wrote evidence")
	}
	if err := dualWebAbortEvidence(dir, true); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "done.json"))
	if err != nil {
		t.Fatal("deferred cleanup evidence missing")
	}
	var done struct {
		OK            bool   `json:"ok"`
		Phase         string `json:"phase"`
		SchemaRemoved bool   `json:"schemaRemoved"`
	}
	if json.Unmarshal(raw, &done) != nil || done.OK || done.Phase != "aborted" || !done.SchemaRemoved {
		t.Fatal("deferred cleanup was relabeled as success")
	}
	if err := dualWebAbortEvidence(dir, true); err != nil {
		t.Fatal(err)
	}
	normal := t.TempDir()
	if err := fixtureWrite(normal, "done.json", map[string]any{"ok": true, "phase": "cleaned", "schemaRemoved": true}, 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(normal, "done.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dualWebAbortEvidence(normal, true); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(normal, "done.json"))
	if err != nil || string(after) != string(before) {
		t.Fatal("normal completion evidence overwritten")
	}
}

// This test checks the fixture's DB invariants with synthetic authentication
// rows. It is not evidence of HTTP login, API routing, SSR or proxy continuity.
func TestReleaseDualWebFixtureDatabaseContract(t *testing.T) {
	if !dualWebFixtureEnabled(true) {
		t.Skip("dual opt-in isolated PG18 fixture database contract")
	}
	ctx := t.Context()
	base, err := newReleaseRuntimeFixture(ctx, os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	f := &releaseDualWebFixture{releaseRuntimeFixture: base}
	t.Cleanup(func() {
		if !f.cleaned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := f.cleanup(cleanupCtx); err != nil {
				t.Error(err)
			}
		}
	})
	dir := t.TempDir()
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	f.credentials, err = dualWebPrivateInputs(dir, f.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.prepareDual(ctx); err == nil {
		t.Fatal("prepare without four external migrations accepted")
	}
	var migrationDigest string
	for i := 0; i < 4; i++ {
		if err = f.db.Migrate(ctx); err != nil {
			t.Fatal("test-only migration failed")
		}
		digest, err := f.verifyDualMigrations(ctx)
		if err != nil || i > 0 && digest != migrationDigest {
			t.Fatal("repeat schema/data migration digest changed")
		}
		migrationDigest = digest
		if i == 0 && f.prepareDual(ctx) == nil {
			t.Fatal("prepare after one migration accepted")
		}
	}
	if _, err = f.verifyDualMigrations(ctx); err == nil {
		t.Fatal("fifth migration verification accepted")
	}
	if err = f.prepareDual(ctx); err != nil {
		t.Fatal(err)
	}
	site, err := f.db.SiteSettings(ctx)
	if err != nil || site.Name != f.siteMarker || f.siteMarker != "DualWebSSR_"+f.schema {
		t.Fatal("public site SSR marker invalid")
	}
	if err = f.prepareDual(ctx); err == nil {
		t.Fatal("second prepare accepted")
	}
	if err = f.verifyDual(ctx, false); err == nil {
		t.Fatal("prepare created an authenticated session")
	}
	for _, account := range []struct {
		id         int64
		credential dualWebCredential
		role       string
		table      string
	}{{f.customerID, f.credentials.Customer, "customer", "users"}, {f.adminID, f.credentials.Admin, "super_admin", "administrators"}} {
		var hash, role string
		if err = f.db.Pool.QueryRow(ctx, "SELECT password_hash,role FROM "+account.table+" WHERE id=$1", account.id).Scan(&hash, &role); err != nil || role != account.role || bcrypt.CompareHashAndPassword([]byte(hash), []byte(account.credential.Password)) != nil {
			t.Fatal("real login credential invalid")
		}
		token, err := dualWebRandom()
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.db.Pool.Exec(ctx, "INSERT INTO sessions(token_hash,user_id,expires_at) VALUES($1,$2,now()+interval '24 hours')", token, account.id); err != nil {
			t.Fatal("contract session preparation failed")
		}
		if _, err = f.db.Pool.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'auth.login_succeeded','user',$2,'{}')", account.id, fmt.Sprint(account.id)); err != nil {
			t.Fatal("contract audit preparation failed")
		}
	}
	rateKey := sha256.Sum256([]byte("login:127.0.0.1"))
	if _, err = f.db.Pool.Exec(ctx, "INSERT INTO auth_rate_limits(key,attempts) VALUES($1,2)", hex.EncodeToString(rateKey[:])); err != nil {
		t.Fatal("contract login rate counter preparation failed")
	}
	if err = f.verifyDual(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, "INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms) VALUES($1,'GET','/api/v1/me',200,0)", f.customerID); err != nil {
		t.Fatal("contract authentication log preparation failed")
	}
	if err = f.verifyDual(ctx, false); err != nil {
		t.Fatal("unchanged sessions rejected")
	}
	if _, err = f.db.Pool.Exec(ctx, "INSERT INTO admin_api_logs(actor_id,method,path,status_code,duration_ms) VALUES($1,'POST','/api/v1/orders',200,0)", f.customerID); err != nil {
		t.Fatal("contract unexpected API log preparation failed")
	}
	if err = f.verifyDual(ctx, false); err == nil {
		t.Fatal("unrelated API activity accepted")
	}
	if _, err = f.db.Pool.Exec(ctx, "DELETE FROM admin_api_logs WHERE path='/api/v1/orders'"); err != nil {
		t.Fatal("contract unexpected API log cleanup failed")
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE users SET balance_cents=1 WHERE id=$1", f.customerID); err != nil {
		t.Fatal("contract financial tamper failed")
	}
	if err = f.verifyDual(ctx, false); err == nil {
		t.Fatal("financial tamper accepted")
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE users SET balance_cents=0 WHERE id=$1", f.customerID); err != nil {
		t.Fatal("contract financial restore failed")
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE sessions SET expires_at=expires_at+interval '1 second' WHERE user_id=$1", f.customerID); err != nil {
		t.Fatal("contract session tamper failed")
	}
	if err = f.verifyDual(ctx, false); err == nil {
		t.Fatal("session identity tamper accepted")
	}
	if _, err = f.db.Pool.Exec(ctx, "UPDATE sessions SET expires_at=expires_at-interval '1 second' WHERE user_id=$1", f.customerID); err != nil {
		t.Fatal("contract session restore failed")
	}
	if err = f.verifyDual(ctx, true); err == nil {
		t.Fatal("logout accepted before final verification")
	}
	f.finalVerified = true
	if err = f.verifyDual(ctx, true); err == nil {
		t.Fatal("logout accepted while sessions active")
	}
	if _, err = f.db.Pool.Exec(ctx, "DELETE FROM sessions"); err != nil {
		t.Fatal("contract session removal failed")
	}
	for _, id := range []int64{f.customerID, f.adminID} {
		if _, err = f.db.Pool.Exec(ctx, "INSERT INTO audit_events(actor_id,action,entity_type,entity_id,payload) VALUES($1,'auth.logout','user',$2,'{}')", id, fmt.Sprint(id)); err != nil {
			t.Fatal("contract logout audit preparation failed")
		}
	}
	if err = f.verifyDual(ctx, true); err != nil {
		t.Fatal(err)
	}
	connection, err := pgx.Connect(ctx, f.dsn)
	if err != nil {
		t.Fatal("owned runtime connection unavailable")
	}
	if err = f.cleanup(ctx); err == nil {
		connection.Close(ctx)
		t.Fatal("schema removed with live owned runtime connection")
	}
	if err = connection.Close(ctx); err != nil {
		t.Fatal("owned runtime connection cleanup failed")
	}
	if err = f.cleanup(ctx); err != nil {
		t.Fatal(err)
	}
	if err = f.cleanup(ctx); err != nil {
		t.Fatal("repeated cleanup invalid")
	}
}
