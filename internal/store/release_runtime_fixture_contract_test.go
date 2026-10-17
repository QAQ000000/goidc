package store

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReleaseRuntimeFixtureContractRejectsNonOwnedDatabaseAndSchemaInputs(t *testing.T) {
	good, cfg, err := runtimeFixtureDSN("postgres://fixture:secret@127.0.0.1:55439/runtime_contract_test?sslmode=disable")
	if err != nil || good.Hostname() != "127.0.0.1" || cfg.ConnConfig.Database != "runtime_contract_test" || cfg.ConnConfig.Port != 55439 {
		t.Fatalf("valid isolated DSN rejected: %v", err)
	}
	for _, dsn := range []string{
		"postgres://fixture@localhost:55439/runtime_contract_test?sslmode=disable",
		"postgres://fixture@127.0.0.1:55439/runtime_contract?sslmode=disable",
		"postgres://fixture@127.0.0.1:55439/runtime_contract_test?sslmode=require&search_path=public",
		"postgres://fixture@127.0.0.1:55439/runtime_contract_test?sslmode=disable&host=203.0.113.1",
		"postgres://fixture@127.0.0.1:55439/runtime_contract_test?sslmode=disable&options=-csearch_path%3Dpublic",
		"postgres://fixture@127.0.0.1:55439/runtime_contract_test?sslmode=disable&port=5432",
		"postgres://fixture@127.0.0.1:55439/runtime_contract_test?sslmode=disable&search_path=public",
		"postgres://fixture@127.0.0.1/runtime_contract_test?sslmode=disable",
	} {
		if _, _, err := runtimeFixtureDSN(dsn); err == nil {
			t.Fatalf("unsafe DSN accepted: %s", dsn)
		}
	}
	if !runtimeFixtureSchema.MatchString("release_runtime_0123456789abcdef01234567") {
		t.Fatal("owned schema pattern rejected valid random schema")
	}
	for _, schema := range []string{"zjmfgo_test_0123456789abcdef", "release_runtime_short", "release_runtime_0123456789abcdef0123456/"} {
		if runtimeFixtureSchema.MatchString(schema) {
			t.Fatalf("foreign/invalid schema accepted: %s", schema)
		}
	}
}

func TestReleaseRuntimeFixtureContractFilesArePrivateOrReadOnlyAndExclusive(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := fixtureDirectory(dir); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWrite(dir, "private.json", map[string]any{"secret": "must not log"}, 0o600); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(filepath.Join(dir, "private.json"))
	if err != nil {
		t.Fatalf("private response unavailable: %v", err)
	}
	if stat.Mode().Perm() != 0o600 {
		t.Fatalf("private response mode: %v", stat.Mode().Perm())
	}
	if err := fixtureWrite(dir, "private.json", map[string]any{}, 0o600); err == nil {
		t.Fatal("fixture overwrote an existing response")
	}
	if err := os.Remove(filepath.Join(dir, "private.json")); err != nil {
		t.Fatal(err)
	}
	if err := fixtureWrite(dir, "public.json", map[string]any{"phase": "schema-ready"}, 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err = os.Stat(filepath.Join(dir, "public.json"))
	if err != nil {
		t.Fatalf("public response unavailable: %v", err)
	}
	if stat.Mode().Perm() != 0o644 {
		t.Fatalf("public response mode: %v", stat.Mode().Perm())
	}
	if err := fixtureWrite(dir, "command-1.json", map[string]any{"operation": "prepare"}, 0o600); err != nil {
		t.Fatal(err)
	}
	operation, present, err := fixtureReadCommand(dir, 1)
	if err != nil || !present || operation != "prepare" {
		t.Fatalf("valid command not read: %q %v %v", operation, present, err)
	}
	if err := os.Chmod(filepath.Join(dir, "command-1.json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := fixtureReadCommand(dir, 1); err == nil {
		t.Fatal("world-readable command accepted")
	}
	if err := os.Remove(filepath.Join(dir, "command-1.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "foreign"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := fixtureDirectory(dir); err == nil {
		t.Fatal("non-empty fixture directory accepted")
	}
}

func TestReleaseRuntimeFixtureDatabaseContracts(t *testing.T) {
	if os.Getenv("ZJMFGO_RELEASE_RUNTIME_FIXTURE_CONTRACT") != "1" {
		t.Skip("opt-in isolated PG18 fixture contract")
	}
	ctx := t.Context()
	f, err := newReleaseRuntimeFixture(ctx, os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if !f.cleaned {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := f.cleanup(cleanupCtx); err != nil {
				t.Error(err)
			}
		}
	})
	if err = f.prepare(ctx); err == nil {
		t.Fatal("prepare before same-package migrations accepted")
	}
	if err = f.db.Migrate(ctx); err != nil {
		t.Fatal("first test-only migration failed")
	}
	first, err := f.verifyMigrations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.prepare(ctx); err == nil {
		t.Fatal("prepare after only one migration verification accepted")
	}
	if err = f.db.Migrate(ctx); err != nil {
		t.Fatal("repeat test-only migration failed")
	}
	second, err := f.verifyMigrations(ctx)
	if err != nil || first != second {
		t.Fatal("repeat migration changed schema/checksums", err)
	}
	if err = f.prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if _, pending, err := f.verify(ctx, false); err != nil || !pending {
		t.Fatal("pending fixture not proven", pending, err)
	}
	// This is only the fixture's local contract test. The opt-in controller path
	// never calls Claim/Process; real acceptance must use its same-package worker.
	job, err := f.db.ClaimProvisionJob(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job.ID != f.jobID || job.ServiceID != f.serviceID || job.Action != "resume" {
		t.Fatal("wrong legitimate job claimed")
	}
	if err = f.db.ProcessProvisionJob(ctx, *job); err != nil {
		t.Fatal(err)
	}
	state, pending, err := f.verify(ctx, false)
	if err != nil || pending || state.Attempts != 1 || state.BusinessAuditCount != 1 {
		t.Fatal("simulation completion proof failed", state, pending, err)
	}
	if f.restartVerified {
		t.Fatal("first completion counted as explicit restart verification")
	}
	var jobUpdatedAt time.Time
	if err = f.db.Pool.QueryRow(ctx, `SELECT updated_at FROM provision_jobs WHERE id=$1`, f.jobID).Scan(&jobUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET updated_at=updated_at+interval '1 second' WHERE id=$1`, f.jobID); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.verify(ctx, false); err == nil {
		t.Fatal("repeated first verification silently replaced successful snapshot")
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE provision_jobs SET updated_at=$2 WHERE id=$1`, f.jobID, jobUpdatedAt); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.verify(ctx, true); err != nil {
		t.Fatal(err)
	}
	if !f.restartVerified {
		t.Fatal("explicit restart verification not recorded")
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE users SET balance_cents=balance_cents+1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.verify(ctx, true); err == nil {
		t.Fatal("offset business state tamper accepted")
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE users SET balance_cents=balance_cents-1,updated_at=(SELECT updated_at FROM users LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.db.Pool.Exec(ctx, `UPDATE channel_instances SET enabled=true WHERE id=(SELECT id FROM channel_instances LIMIT 1)`); err != nil {
		t.Fatal(err)
	}
	if err = f.safeInputs(ctx); err == nil {
		t.Fatal("enabled external channel accepted")
	}
	if err = f.cleanup(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestReleaseRuntimeFixtureProtocolContract(t *testing.T) {
	if os.Getenv("ZJMFGO_RELEASE_RUNTIME_FIXTURE_CONTRACT") != "1" {
		t.Skip("opt-in isolated PG18 fixture protocol")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReleaseRuntimeFixture$", "-test.v")
	cmd.Env = append(os.Environ(), "ZJMFGO_RELEASE_RUNTIME_FIXTURE=1", "ZJMFGO_RELEASE_RUNTIME_FIXTURE_DIR="+dir)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	wait := func(name string) []byte {
		t.Helper()
		for {
			raw, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				return raw
			}
			select {
			case <-ctx.Done():
				t.Fatal("fixture protocol deadline")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	ready := wait("schema-ready.json")
	var metadata struct {
		Schema, Database string
		PGVersion        int `json:"pgVersion"`
	}
	if json.Unmarshal(ready, &metadata) != nil || !runtimeFixtureSchema.MatchString(metadata.Schema) || metadata.PGVersion < 180000 {
		t.Fatal("invalid redacted ready metadata")
	}
	config := wait("config.json")
	if bytes.Contains(ready, []byte("DATABASE_URL")) || bytes.Contains(ready, []byte("postgres://")) {
		t.Fatal("ready leaked private config")
	}
	if err := fixtureWrite(dir, "command-1.json", map[string]any{"operation": "abort"}, 0600); err != nil {
		t.Fatal(err)
	}
	result := wait("result-1.json")
	if !bytes.Contains(result, []byte(`"ok":true`)) {
		t.Fatalf("abort failed: %s", result)
	}
	wait("done.json")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("fixture exit failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("fixture exit deadline")
	}
	if _, err := os.Stat(filepath.Join(dir, "config.json")); !os.IsNotExist(err) {
		t.Fatal("private config remained after cleanup")
	}
	var private struct {
		Values map[string]string `json:"values"`
	}
	if json.Unmarshal(config, &private) != nil {
		t.Fatal("private config malformed")
	}
	if strings.Contains(output.String(), private.Values["DATABASE_URL"]) || strings.Contains(output.String(), "postgres://") {
		t.Fatal("fixture logs leaked DSN")
	}
}
