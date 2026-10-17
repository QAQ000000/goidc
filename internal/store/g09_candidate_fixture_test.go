package store

// Host-only bootstrap fixture. It never runs migrations or seeds business data.
import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"testing"
	"time"

	"github.com/codex/zjmfgo/internal/g09fixture"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type g09Migration struct {
	Version  string `json:"version"`
	Checksum string `json:"checksum"`
}
type g09Bootstrap struct {
	RunID           string         `json:"runId"`
	BridgeDirectory string         `json:"bridgeDirectory"`
	Migrations      []g09Migration `json:"migrations"`
}
type g09Command struct {
	RunID    string `json:"runId"`
	Sequence uint64 `json:"sequence"`
	Action   string `json:"action"`
}

var g09Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var g09Schema = regexp.MustCompile(`^zjmfgo_test_[a-f0-9]{16}$`)

func g09Decode(raw []byte, out any) error {
	if len(raw) > 262144 {
		return errors.New("g09_json_size")
	}
	// Reject duplicate keys at every object level, as well as unknown/trailing fields.
	d := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		tok, e := d.Token()
		if e != nil {
			return e
		}
		delim, ok := tok.(json.Delim)
		if !ok {
			return nil
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				k, e := d.Token()
				if e != nil {
					return e
				}
				s, ok := k.(string)
				if !ok || seen[s] {
					return errors.New("duplicate")
				}
				seen[s] = true
			}
			if e := walk(); e != nil {
				return e
			}
		}
		_, e = d.Token()
		return e
	}
	if walk() != nil {
		return errors.New("g09_json_invalid")
	}
	if _, e := d.Token(); e != io.EOF {
		return errors.New("g09_json_trailing")
	}
	d = json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errors.New("g09_json_invalid")
	}
	return nil
}
func g09Owned(s os.FileInfo) bool {
	st, ok := s.Sys().(*syscall.Stat_t)
	return ok && st.Uid == uint32(os.Geteuid())
}
func g09Read(path string, out any) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("g09_private_path_invalid")
	}
	for p := filepath.Dir(path); ; p = filepath.Dir(p) {
		st, e := os.Lstat(p)
		if e != nil || !st.IsDir() {
			return errors.New("g09_private_path_invalid")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	st, e := os.Lstat(path)
	if e != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0600 || st.Size() > 262144 || !g09Owned(st) {
		return errors.New("g09_private_file_invalid")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return errors.New("g09_private_file_unavailable")
	}
	return g09Decode(b, out)
}
func g09Write(dir, name string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return errors.New("g09_encode")
	}
	f, e := os.CreateTemp(dir, ".g09-publish-")
	if e != nil {
		return errors.New("g09_publish")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if f.Chmod(0600) != nil {
		return errors.New("g09_publish")
	}
	if _, e = f.Write(append(b, '\n')); e != nil {
		return errors.New("g09_publish")
	}
	if f.Sync() != nil || f.Close() != nil {
		return errors.New("g09_publish")
	}
	// Hard-link publish is atomic and cannot overwrite an existing command/result.
	if os.Link(f.Name(), filepath.Join(dir, name)) != nil {
		return errors.New("g09_publish")
	}
	d, e := os.Open(dir)
	if e != nil {
		return errors.New("g09_publish")
	}
	defer d.Close()
	if d.Sync() != nil {
		return errors.New("g09_publish")
	}
	return nil
}
func g09ValidateBootstrap(c g09Bootstrap) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9_-]{8,96}$`).MatchString(c.RunID) || !filepath.IsAbs(c.BridgeDirectory) || filepath.Clean(c.BridgeDirectory) != c.BridgeDirectory || len(c.Migrations) == 0 || len(c.Migrations) > 1000 {
		return errors.New("g09_bootstrap_invalid")
	}
	for p := c.BridgeDirectory; ; p = filepath.Dir(p) {
		s, e := os.Lstat(p)
		if e != nil || !s.IsDir() {
			return errors.New("g09_bridge_invalid")
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	previous := ""
	for _, m := range c.Migrations {
		if !regexp.MustCompile(`^[0-9]+_[a-z0-9_]+\.sql$`).MatchString(m.Version) || m.Version <= previous || !g09Hex.MatchString(m.Checksum) {
			return errors.New("g09_migrations_invalid")
		}
		previous = m.Version
	}
	return nil
}
func g09ParseCommand(raw []byte, run string, seq uint64) (g09Command, error) {
	var c g09Command
	if len(raw) > 4096 || g09Decode(raw, &c) != nil || c.RunID != run || c.Sequence != seq {
		return c, errors.New("g09_command_invalid")
	}
	switch c.Action {
	case "ready", "verify-migrations", "snapshot", "finish", "abort":
		return c, nil
	}
	return c, errors.New("g09_command_invalid")
}

func TestG09CandidateFixture(t *testing.T) {
	if os.Getenv("ZJMFGO_G09_CANDIDATE_FIXTURE") != "1" {
		t.Skip("explicit G09 candidate bootstrap opt-in required")
	}
	dir := os.Getenv("ZJMFGO_G09_CANDIDATE_FIXTURE_DIR")
	if fixtureDirectory(dir) != nil {
		t.Fatal("g09_directory_invalid")
	}
	if st, e := os.Lstat(dir); e != nil || !g09Owned(st) {
		t.Fatal("g09_directory_owner")
	}
	var boot g09Bootstrap
	if g09Read(os.Getenv("ZJMFGO_G09_CANDIDATE_BOOTSTRAP_CONFIG"), &boot) != nil || g09ValidateBootstrap(boot) != nil {
		t.Fatal("g09_bootstrap_invalid")
	}
	u, cfg, e := runtimeFixtureDSN(os.Getenv("ZJMFGO_TEST_DATABASE_URL"))
	if e != nil || !regexp.MustCompile(`^[a-z][a-z0-9_]{0,57}_test$`).MatchString(cfg.ConnConfig.Database) {
		t.Fatal("g09_database_url_invalid")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	random := make([]byte, 8)
	if _, e = rand.Read(random); e != nil {
		t.Fatal("g09_random")
	}
	schema := "zjmfgo_test_" + hex.EncodeToString(random)
	app := "g09_candidate_" + schema
	cfg.ConnConfig.RuntimeParams = map[string]string{"application_name": "g09_host_" + schema}
	base, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal("g09_connection")
	}
	defer base.Close()
	var db, addr string
	var version int
	if base.QueryRow(ctx, `SELECT current_database(),host(inet_server_addr()),current_setting('server_version_num')::int`).Scan(&db, &addr, &version) != nil || db != cfg.ConnConfig.Database || addr != "127.0.0.1" || version < 180000 || version >= 190000 {
		t.Fatal("g09_identity")
	}
	owned := false
	cleaned := false
	verified := false
	migrationChecks := 0
	ready := false
	var digest string
	var scoped *pgxpool.Pool
	var observer *g09fixture.PGObserver
	cleanup := func() error {
		if cleaned {
			return nil
		}
		if scoped != nil {
			scoped.Close()
		}
		if !owned || !g09Schema.MatchString(schema) {
			return errors.New("g09_cleanup_ownership")
		}
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		conn, e := pgx.ConnectConfig(c, cfg.ConnConfig.Copy())
		if e != nil {
			return errors.New("g09_cleanup_connection")
		}
		defer conn.Close(c)
		if _, e = conn.Exec(c, `SET lock_timeout='3s'`); e != nil {
			return errors.New("g09_cleanup")
		}
		if _, e = conn.Exec(c, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE"); e != nil {
			return errors.New("g09_cleanup")
		}
		conn.Close(c)
		check, e := pgx.ConnectConfig(c, cfg.ConnConfig.Copy())
		if e != nil {
			return errors.New("g09_cleanup_check")
		}
		defer check.Close(c)
		var exists bool
		if check.QueryRow(c, `SELECT EXISTS(SELECT 1 FROM pg_namespace WHERE nspname=$1)`, schema).Scan(&exists) != nil || exists {
			return errors.New("g09_cleanup_check")
		}
		cleaned = true
		return nil
	}
	defer func() {
		if owned && !cleaned {
			if e := cleanup(); e != nil {
				t.Error(e)
			}
		}
		_ = os.Remove(filepath.Join(dir, "config.json"))
		if owned {
			if e := g09Write(dir, "done.json", map[string]any{"runId": boot.RunID, "schemaRemoved": cleaned, "ok": cleaned}); e != nil {
				t.Error(e)
			}
		}
	}()
	if _, e = base.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); e != nil {
		t.Fatal("g09_create_schema")
	}
	owned = true
	host := cfg.Copy()
	host.ConnConfig.RuntimeParams = map[string]string{"search_path": schema, "application_name": "g09_observer_" + schema}
	scoped, e = pgxpool.NewWithConfig(ctx, host)
	if e != nil {
		t.Fatal("g09_observer_pool")
	}
	q := url.Values{"sslmode": {"disable"}, "search_path": {schema}, "application_name": {app}, "connect_timeout": {"5"}}
	u.RawQuery = q.Encode()
	values := map[string]string{"DATABASE_URL": u.String()}
	for _, key := range []string{"MFA_ENCRYPTION_KEY", "PURCHASE_FIELD_ENCRYPTION_KEY", "PROVISION_ENCRYPTION_KEY"} {
		b := make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			t.Fatal("g09_random")
		}
		values[key] = hex.EncodeToString(b)
	}
	if g09Write(dir, "config.json", map[string]any{"version": "zjmfgo-runtime-config-v1", "values": values}) != nil || g09Write(dir, "schema-ready.json", map[string]any{"runId": boot.RunID, "ok": true, "schema": schema, "database": db, "pgVersion": version, "applicationName": app}) != nil {
		t.Fatal("g09_publish")
	}
	for seq := uint64(1); ; seq++ {
		if ctx.Err() != nil {
			t.Fatal("g09_bootstrap_deadline")
		}
		var cmd g09Command
		for {
			path := filepath.Join(dir, fmt.Sprintf("command-%d.json", seq))
			s, e := os.Lstat(path)
			if e == nil {
				if !s.Mode().IsRegular() || s.Mode().Perm() != 0600 || s.Size() > 4096 || !g09Owned(s) {
					t.Fatal("g09_command_file")
				}
				b, e := os.ReadFile(path)
				if e != nil {
					t.Fatal("g09_command_file")
				}
				cmd, e = g09ParseCommand(b, boot.RunID, seq)
				if e != nil {
					t.Fatal(e)
				}
				break
			}
			if !os.IsNotExist(e) {
				t.Fatal("g09_command_file")
			}
			select {
			case <-ctx.Done():
				t.Fatal("g09_bootstrap_deadline")
			case <-time.After(50 * time.Millisecond):
			}
		}
		result := map[string]any{"runId": boot.RunID, "sequence": seq, "action": cmd.Action, "ok": true}
		var opErr error
		switch cmd.Action {
		case "ready":
			if ready {
				opErr = errors.New("g09_already_ready")
			} else {
				ready = true
			}
		case "verify-migrations":
			if !ready {
				opErr = errors.New("g09_not_ready")
				break
			}
			rows, e := scoped.Query(ctx, `SELECT version,checksum FROM schema_migrations ORDER BY version LIMIT 1001`)
			if e != nil {
				opErr = errors.New("g09_migration_history")
				break
			}
			var actual []g09Migration
			for rows.Next() {
				var m g09Migration
				if rows.Scan(&m.Version, &m.Checksum) != nil {
					opErr = errors.New("g09_migration_history")
					break
				}
				actual = append(actual, m)
			}
			if rows.Err() != nil {
				opErr = errors.New("g09_migration_history")
			}
			rows.Close()
			a, _ := json.Marshal(actual)
			b, _ := json.Marshal(boot.Migrations)
			if !bytes.Equal(a, b) {
				opErr = errors.New("g09_migration_mismatch")
			}
			if opErr == nil {
				sum := sha256.Sum256(a)
				next := hex.EncodeToString(sum[:])
				if digest != "" && digest != next {
					opErr = errors.New("g09_migration_changed")
				} else {
					digest = next
					if observer == nil {
						observer, e = g09fixture.NewPGObserver(scoped, g09fixture.PGObserverConfig{Database: db, Schema: schema, MaxSamples: 1000, Timeout: 3 * time.Second})
					}
					if e != nil {
						opErr = errors.New("g09_observer_config")
					} else {
						_, e = observer.Sample(ctx)
						if e != nil {
							opErr = errors.New("g09_observer_identity")
						} else {
							verified = true
							migrationChecks++
							result["migrationSHA256"] = digest
							result["migrationCount"] = len(actual)
						}
					}
				}
			}
		case "snapshot":
			if !verified {
				opErr = errors.New("g09_not_verified")
			} else {
				sample, e := observer.Sample(ctx)
				if e != nil {
					opErr = errors.New("g09_snapshot")
				} else {
					result["snapshot"] = sample
				}
			}
		case "finish":
			if !verified || migrationChecks < 2 {
				opErr = errors.New("g09_not_verified")
			} else {
				opErr = cleanup()
			}
		case "abort":
			opErr = cleanup()
		}
		if opErr != nil {
			result["ok"] = false
			result["errorCode"] = opErr.Error()
		}
		if g09Write(dir, fmt.Sprintf("result-%d.json", seq), result) != nil {
			t.Fatal("g09_result_publish")
		}
		if opErr != nil {
			t.Fatal(opErr)
		}
		if cmd.Action == "finish" || cmd.Action == "abort" {
			return
		}
	}
}

func TestG09CandidateFixtureProtocolGuards(t *testing.T) {
	for _, raw := range []string{`{"runId":"run-test","sequence":1,"action":"abort","sql":"DROP DATABASE x"}`, `{"runId":"run-test","sequence":2,"action":"abort"}`, `{"runId":"run-test","sequence":1,"action":"abort","action":"finish"}`, `{"runId":"other","sequence":1,"action":"ready"}`, `{"runId":"run-test","sequence":1,"action":"prepare"}`} {
		if _, e := g09ParseCommand([]byte(raw), "run-test", 1); e == nil {
			t.Fatal("unsafe command accepted")
		}
	}
	if _, e := g09ParseCommand([]byte(`{"runId":"run-test","sequence":1,"action":"ready"}`), "run-test", 1); e != nil {
		t.Fatal(e)
	}
}

func TestG09CandidateFixtureBootstrapGuards(t *testing.T) {
	dir := t.TempDir()
	good := g09Bootstrap{RunID: "g09-test-run", BridgeDirectory: dir, Migrations: []g09Migration{{Version: "001_init.sql", Checksum: fmt.Sprintf("%064x", 1)}}}
	if e := g09ValidateBootstrap(good); e != nil {
		t.Fatal(e)
	}
	bad := good
	bad.Migrations = append(append([]g09Migration{}, good.Migrations...), good.Migrations[0])
	if g09ValidateBootstrap(bad) == nil {
		t.Fatal("duplicate migration accepted")
	}
	bad = good
	bad.Migrations = []g09Migration{{Version: "../001_init.sql", Checksum: good.Migrations[0].Checksum}}
	if g09ValidateBootstrap(bad) == nil {
		t.Fatal("migration path accepted")
	}
	var c g09Bootstrap
	if g09Decode([]byte(`{"runId":"g09-test-run","bridgeDirectory":"/tmp","migrations":[{"version":"001_init.sql","checksum":"a","checksum":"b"}]}`), &c) == nil {
		t.Fatal("nested duplicate accepted")
	}
	if e := g09Write(dir, "proof.json", map[string]bool{"ok": true}); e != nil {
		t.Fatal(e)
	}
	if e := g09Write(dir, "proof.json", map[string]bool{"ok": false}); e == nil {
		t.Fatal("exclusive publish overwrote evidence")
	}
	st, e := os.Stat(filepath.Join(dir, "proof.json"))
	if e != nil || st.Mode().Perm() != 0600 {
		t.Fatal("private evidence mode")
	}
}
