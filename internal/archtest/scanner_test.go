package archtest

import (
	"encoding/json"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"
)

// The fixtures use real go/types package identities without loading the business
// application, reaching a database, or requiring pgx's implementation details.
type fixtureImporter struct {
	packages map[string]*types.Package
	fallback types.Importer
}

func (i *fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := i.packages[path]; ok {
		return pkg, nil
	}
	return i.fallback.Import(path)
}

func newFixtureImporter(t *testing.T) types.Importer {
	t.Helper()
	imp := &fixtureImporter{packages: map[string]*types.Package{}, fallback: importer.Default()}
	packages := []struct{ path, source string }{
		{"github.com/jackc/pgx/v5/pgconn", `package pgconn
import "context"
type CommandTag struct{}
type PgConn struct{}
func (*PgConn) Exec(context.Context, string) *CommandTag { return nil }
func (*PgConn) ExecParams(context.Context, string, [][]byte, []uint32, []int16, []int16) *CommandTag { return nil }
`},
		{"github.com/jackc/pgx/v5", `package pgx
import (
 "context"
 "github.com/jackc/pgx/v5/pgconn"
)
type Rows interface { Close() }
type Row interface { Scan(...any) error }
type TxOptions struct{ IsoLevel string }
type Batch struct{}
func (*Batch) Queue(string, ...any) {}
type BatchResults interface{ Close() error }
type Identifier []string
type CopyFromSource interface{ Next() bool }
type Conn struct{}
func (*Conn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }
func (*Conn) PgConn() *pgconn.PgConn { return nil }
type Tx interface {
 Query(context.Context, string, ...any) (Rows, error)
 QueryRow(context.Context, string, ...any) Row
 Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
 Begin(context.Context) (Tx, error)
 Commit(context.Context) error
 Rollback(context.Context) error
 SendBatch(context.Context, *Batch) BatchResults
 CopyFrom(context.Context, Identifier, []string, CopyFromSource) (int64, error)
 Prepare(context.Context, string, string) (any, error)
 Conn() *Conn
}
func BeginFunc(ctx context.Context, db interface{ Begin(context.Context) (Tx, error) }, fn func(Tx) error) error { return nil }
`},
		{"github.com/jackc/pgx/v5/pgxpool", `package pgxpool
import (
 "context"
 "github.com/jackc/pgx/v5"
 "github.com/jackc/pgx/v5/pgconn"
)
type Pool struct{}
func (*Pool) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (*Pool) QueryRow(context.Context, string, ...any) pgx.Row { return nil }
func (*Pool) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) { return pgconn.CommandTag{}, nil }
func (*Pool) Begin(context.Context) (pgx.Tx, error) { return nil, nil }
func (*Pool) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) { return nil, nil }
func (*Pool) Acquire(context.Context) (*Conn, error) { return nil, nil }
func (*Pool) AcquireFunc(context.Context, func(*Conn) error) error { return nil }
func (*Pool) AcquireAllIdle(context.Context) []*Conn { return nil }
func (*Pool) Ping(context.Context) error { return nil }
type Conn struct{}
func (*Conn) Conn() *pgx.Conn { return nil }
func (*Conn) Hijack() *pgx.Conn { return nil }
`},
	}
	for _, fixture := range packages {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "fixture.go", fixture.source, 0)
		if err != nil {
			t.Fatal(err)
		}
		config := types.Config{Importer: imp}
		pkg, err := config.Check(fixture.path, fset, []*ast.File{file}, nil)
		if err != nil {
			t.Fatalf("build fixture package %s: %v", fixture.path, err)
		}
		imp.packages[fixture.path] = pkg
	}
	return imp
}

func analyzeFixture(t *testing.T, source string) []exception {
	t.Helper()
	return analyzeFixtureFiles(t, map[string][]byte{"internal/httpapi/fixture.go": []byte(source)})
}

func analyzeFixtureFiles(t *testing.T, sources map[string][]byte) []exception {
	t.Helper()
	actual, err := analyzeHTTP(sources, newFixtureImporter(t))
	if err != nil {
		t.Fatalf("analyze valid fixture: %v", err)
	}
	return actual
}

func TestScannerRecognizesDatabaseAccessBeyondReceiverNames(t *testing.T) {
	tests := []struct{ name, source string }{
		{"renamed imports and variables", `package httpapi
import ("context"; database "github.com/jackc/pgx/v5/pgxpool")
func handler(pool *database.Pool) { renamed := pool; _, _ = renamed.Exec(context.Background(), "UPDATE widgets SET active=true") }
`},
		{"narrow named interface", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
type reader interface { Query(context.Context, string, ...any) (pgx.Rows, error) }
func handler(db reader) { _, _ = db.Query(context.Background(), "SELECT 1") }
`},
		{"anonymous row interface", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db interface { QueryRow(context.Context, string, ...any) pgx.Row }) { _ = db.QueryRow(context.Background(), "SELECT 1") }
`},
		{"embedded transaction", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
type wrapper struct{ pgx.Tx }
func handler(db wrapper) { _, _ = db.Exec(context.Background(), "DELETE FROM widgets") }
`},
		{"method value", `package httpapi
import "github.com/jackc/pgx/v5"
func handler(db pgx.Tx) { execute := db.Exec; _ = execute }
`},
		{"package initializer method value", `package httpapi
import "github.com/jackc/pgx/v5/pgxpool"
var database *pgxpool.Pool
var execute = database.Exec
`},
		{"method expression", `package httpapi
import "github.com/jackc/pgx/v5/pgxpool"
var execute = (*pgxpool.Pool).Exec
`},
		{"parenthesized call and closure", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { f := func() { _, _ = (db.Exec)(context.Background(), "DELETE FROM widgets") }; _ = f }
`},
		{"batch queue", `package httpapi
import "github.com/jackc/pgx/v5"
func handler(batch *pgx.Batch) { batch.Queue("DELETE FROM widgets") }
`},
		{"batch execution", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _ = db.SendBatch(context.Background(), &pgx.Batch{}) }
`},
		{"copy from", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.CopyFrom(context.Background(), pgx.Identifier{"widgets"}, []string{"id"}, nil) }
`},
		{"prepared statement", `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.Prepare(context.Background(), "delete_widgets", "DELETE FROM widgets") }
`},
		{"raw connection", `package httpapi
import "github.com/jackc/pgx/v5"
func handler(db pgx.Tx) { _ = db.Conn().PgConn() }
`},
		{"acquire", `package httpapi
import ("context"; "github.com/jackc/pgx/v5/pgxpool")
func handler(db *pgxpool.Pool) { _, _ = db.Acquire(context.Background()) }
`},
		{"transaction helper", `package httpapi
import ("context"; "github.com/jackc/pgx/v5"; "github.com/jackc/pgx/v5/pgxpool")
func handler(db *pgxpool.Pool) { _ = pgx.BeginFunc(context.Background(), db, func(pgx.Tx) error { return nil }) }
`},
		{"low level execution", `package httpapi
import ("context"; "github.com/jackc/pgx/v5/pgconn")
func handler(db *pgconn.PgConn) { _ = db.Exec(context.Background(), "DELETE FROM widgets") }
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			actual := analyzeFixture(t, tt.source)
			if len(actual) == 0 {
				t.Fatal("database access escaped the architecture scanner")
			}
			for _, entry := range actual {
				if entry.File == "" || entry.Function == "" || entry.Operation == "" || entry.Fingerprint == "" || entry.Count < 1 {
					t.Fatalf("incomplete exception: %+v", entry)
				}
			}
		})
	}
}

func TestScannerDoesNotMistakeUnrelatedQueryNamesForPersistence(t *testing.T) {
	actual := analyzeFixture(t, `package httpapi
import "net/http"
type unrelated struct{}
func (unrelated) Query(string) string { return "ok" }
func (unrelated) Exec(string) string { return "ok" }
func handler(r *http.Request, local unrelated) { _ = r.URL.Query(); _ = local.Query("SELECT 1"); _ = local.Exec("UPDATE text") }
`)
	if len(actual) != 0 {
		t.Fatalf("non-database operations generated exceptions: %+v", actual)
	}
}

func TestScannerFunctionFingerprintDetectsChangesWithoutCountGrowth(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.Exec(context.Background(), "UPDATE widgets SET active=true") }
`
	allowed := analyzeFixture(t, original)
	for _, tt := range []struct{ name, source string }{
		{"different SQL", strings.Replace(original, "active=true", "active=false", 1)},
		{"SQL literal internal whitespace", strings.Replace(original, "active=true", "name='a  b'", 1)},
		{"different function", strings.Replace(original, "func handler", "func another", 1)},
		{"same old access copied", strings.Replace(original, "func handler", "func another", 1) + "\n" + `func handler(db pgx.Tx) { _, _ = db.Exec(context.Background(), "UPDATE widgets SET active=true") }`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if problems := compareExceptions(allowed, analyzeFixture(t, tt.source)); len(problems) == 0 {
				t.Fatal("changed persistence escaped the frozen baseline")
			}
		})
	}
}

func TestScannerFingerprintIgnoresGoFormatting(t *testing.T) {
	compact := `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.Exec(context.Background(), "SELECT 1") }
`
	formatted := `package httpapi
import (
    "context"
    "github.com/jackc/pgx/v5"
)

func handler(db pgx.Tx) {
    // Formatting and comments do not create new database behavior.
    _, _ = db.Exec(
        context.Background(),
        "SELECT 1",
    )
}
`
	before, after := analyzeFixture(t, compact), analyzeFixture(t, formatted)
	if problems := append(compareExceptions(before, after), compareExceptions(after, before)...); len(problems) != 0 {
		t.Fatalf("format-only edit changed baseline: %v", problems)
	}
}

func TestScannerPreservesWhitespaceInsideSQLStringValues(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.Exec(context.Background(), "SELECT 'a b'") }
`
	modified := strings.Replace(original, "'a b'", "'a  b'", 1)
	if problems := compareExceptions(analyzeFixture(t, original), analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("SQL string content changed but its fingerprint did not")
	}
}

func TestScannerFingerprintPreservesVariadicArgumentExpansion(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx, args []any) { _, _ = db.Exec(context.Background(), "SELECT $1", args...) }
`
	modified := strings.Replace(original, "args...", "args", 1)
	if problems := compareExceptions(analyzeFixture(t, original), analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("changing expanded SQL parameters into one slice parameter preserved the fingerprint")
	}
}

func TestScannerDynamicSQLWrappersCannotAddCallSites(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5/pgxpool")
var database *pgxpool.Pool
func execute(sql string) { _, _ = database.Exec(context.Background(), sql) }
func forward(sql string) { execute(sql) }
func handler() { forward("UPDATE widgets SET active=true") }
`
	allowed := analyzeFixture(t, original)
	modified := original + `
func anotherHandler() { forward("DELETE FROM widgets") }
`
	if problems := compareExceptions(allowed, analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("new call through a local SQL wrapper escaped the baseline")
	}
	modified = strings.Replace(original, "active=true", "active=false", 1)
	if problems := compareExceptions(allowed, analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("changed SQL passed through a local wrapper escaped the baseline")
	}
}

func TestScannerSQLWrappersCannotHideBehindNamedStringTypes(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5/pgxpool")
type SQL string
var database *pgxpool.Pool
func execute(sql SQL) { _, _ = database.Exec(context.Background(), string(sql)) }
func handler() { execute("UPDATE widgets SET active=true") }
`
	modified := original + `
func anotherHandler() { execute("DELETE FROM widgets") }
`
	if problems := compareExceptions(analyzeFixture(t, original), analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("named SQL string type hid a new local wrapper call")
	}
}

func TestScannerDatabaseHelperSignaturesCannotAddCallSites(t *testing.T) {
	original := `package httpapi
import "github.com/jackc/pgx/v5"
func obtainTransaction() pgx.Tx { return nil }
func handler() { _ = obtainTransaction() }
`
	allowed := analyzeFixture(t, original)
	modified := original + `
func anotherHandler() { _ = obtainTransaction() }
`
	if problems := compareExceptions(allowed, analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("new call to a local transaction helper escaped the baseline")
	}
}

func TestScannerTracksReferencedConstantOutsideCallingFunction(t *testing.T) {
	sources := map[string][]byte{
		"internal/httpapi/fixture.go": []byte(`package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx) { _, _ = db.Exec(context.Background(), updateSQL) }
`),
		"internal/httpapi/queries.go": []byte(`package httpapi
const updateSQL = "UPDATE widgets SET active=true"
`),
	}
	allowed := analyzeFixtureFiles(t, sources)
	sources["internal/httpapi/queries.go"] = []byte(`package httpapi
const updateSQL = "DELETE FROM widgets"
`)
	if problems := compareExceptions(allowed, analyzeFixtureFiles(t, sources)); len(problems) == 0 {
		t.Fatal("SQL constant replacement outside the function escaped the baseline")
	}
}

func TestScannerConstantShadowingCannotHideChangedSQL(t *testing.T) {
	original := `package httpapi
import ("context"; "github.com/jackc/pgx/v5")
const query = "UPDATE widgets SET active=true"
func handler(db pgx.Tx) {
 _, _ = db.Exec(context.Background(), query)
 { const query = "unrelated local constant"; _ = query }
}
`
	modified := strings.Replace(original, "active=true", "active=false", 1)
	if problems := compareExceptions(analyzeFixture(t, original), analyzeFixture(t, modified)); len(problems) == 0 {
		t.Fatal("shadowed local constant hid the changed external SQL constant")
	}
}

func TestScannerFreezesDynamicSQLAndTransactionOptions(t *testing.T) {
	for _, original := range []string{
		`package httpapi
import ("context"; "github.com/jackc/pgx/v5")
func handler(db pgx.Tx, allowed bool) { query := "UPDATE widgets SET active=true"; if allowed { query = "SELECT 1" }; _, _ = db.Exec(context.Background(), query) }
`,
		`package httpapi
import ("context"; "github.com/jackc/pgx/v5"; "github.com/jackc/pgx/v5/pgxpool")
func handler(db *pgxpool.Pool) { _, _ = db.BeginTx(context.Background(), pgx.TxOptions{IsoLevel: "serializable"}) }
`,
	} {
		allowed := analyzeFixture(t, original)
		modified := strings.Replace(original, "active=true", "active=false", 1)
		if modified == original {
			modified = strings.Replace(original, "serializable", "read committed", 1)
		}
		if problems := compareExceptions(allowed, analyzeFixture(t, modified)); len(problems) == 0 {
			t.Fatal("changed dynamic SQL or transaction options escaped the baseline")
		}
	}
}

func TestCompareExceptionsAllowsOnlyExistingEntriesAndCountsToShrink(t *testing.T) {
	entry := exception{File: "internal/httpapi/fixture.go", Function: "handler", Operation: "pgx.Tx.Exec", Fingerprint: "frozen", Count: 2}
	allowed := []exception{entry}
	for _, count := range []int{1, 2} {
		actual := entry
		actual.Count = count
		if problems := compareExceptions(allowed, []exception{actual}); len(problems) != 0 {
			t.Fatalf("existing exception count %d rejected: %v", count, problems)
		}
	}
	if problems := compareExceptions(allowed, nil); len(problems) != 0 {
		t.Fatalf("removing exception rejected: %v", problems)
	}
	for _, tt := range []struct {
		name   string
		change func(*exception)
	}{
		{"increased count", func(e *exception) { e.Count++ }},
		{"different file", func(e *exception) { e.File = "internal/httpapi/new.go" }},
		{"different function", func(e *exception) { e.Function = "another" }},
		{"different operation", func(e *exception) { e.Operation = "pgx.Tx.Query" }},
		{"different fingerprint", func(e *exception) { e.Fingerprint = "replacement" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			actual := entry
			tt.change(&actual)
			if problems := compareExceptions(allowed, []exception{actual}); len(problems) == 0 {
				t.Fatal("new or expanded exception was accepted")
			}
		})
	}
}

func TestScannerRejectsUnsupportedHTTPSourceVariants(t *testing.T) {
	for _, tt := range []struct{ name, source string }{
		{"internal/httpapi/subpackage/fixture.go", "package httpapi\n"},
		{"internal/httpapi/fixture_linux.go", "package httpapi\n"},
		{"internal/httpapi/fixture_amd64.go", "package httpapi\n"},
		{"internal/httpapi/fixture.go", "//go:build enterprise\n\npackage httpapi\n"},
		{"internal/httpapi/legacy.go", "// +build enterprise\n\npackage httpapi\n"},
	} {
		t.Run(tt.name+tt.source, func(t *testing.T) {
			_, err := analyzeHTTP(map[string][]byte{tt.name: []byte(tt.source)}, newFixtureImporter(t))
			if err == nil {
				t.Fatal("unsupported source variant escaped explicit scanner coverage")
			}
		})
	}
}

func TestBaselineDecoderRejectsMalformedOrExpandedSchema(t *testing.T) {
	entry := exception{File: "internal/httpapi/fixture.go", Function: "handler", Operation: "driver-method:Exec", Fingerprint: strings.Repeat("a", 64), Count: 1}
	encode := func(entries []exception) []byte {
		data, err := json.Marshal(baseline{Version: 1, Entries: entries})
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	valid := encode([]exception{entry})
	if _, err := decodeBaseline(valid); err != nil {
		t.Fatalf("valid baseline rejected: %v", err)
	}
	if _, err := decodeBaseline([]byte(`{"version":1,"entries":[]}`)); err != nil {
		t.Fatalf("fully removed baseline rejected: %v", err)
	}
	badHash, badCount := entry, entry
	badHash.Fingerprint, badCount.Count = "not-a-sha256", 0
	negativeCount := entry
	negativeCount.Count = -1
	for _, tt := range []struct {
		name string
		data []byte
	}{
		{"unknown version", []byte(`{"version":2,"entries":[]}`)},
		{"unknown top-level field", []byte(`{"version":1,"entries":[],"allow_new":true}`)},
		{"unknown entry field", []byte(strings.Replace(string(valid), `"count":1`, `"count":1,"allow_new":true`, 1))},
		{"duplicate exception", encode([]exception{entry, entry})},
		{"invalid hash", encode([]exception{badHash})},
		{"zero count", encode([]exception{badCount})},
		{"negative count", encode([]exception{negativeCount})},
		{"missing entries", []byte(`{"version":1}`)},
		{"null entries", []byte(`{"version":1,"entries":null}`)},
		{"trailing JSON", append(append([]byte{}, valid...), []byte(` {}`)...)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodeBaseline(tt.data); err == nil {
				t.Fatal("invalid baseline accepted")
			}
		})
	}
}

func TestImportBoundaryIncludesTaggedGeneratedAndNestedProductionSources(t *testing.T) {
	violating := []string{
		"internal/store/direct.go",
		"internal/store/tagged_linux.go",
		"internal/provision/adapter/generated.go",
		"internal/domain/nested/command.go",
	}
	importHTTP := `package fixture
import _ "github.com/codex/zjmfgo/internal/httpapi"
`
	files := map[string][]byte{
		violating[0]:                          []byte(importHTTP),
		violating[1]:                          []byte("//go:build enterprise\n\n" + importHTTP),
		violating[2]:                          []byte("// Code generated by a fixture. DO NOT EDIT.\n" + importHTTP),
		violating[3]:                          []byte(strings.Replace(importHTTP, "/internal/httpapi\"", "/internal/httpapi/nested\"", 1)),
		"internal/store/fixture_test.go":      []byte(importHTTP),
		"internal/store/testdata/fixture.go":  []byte(importHTTP),
		"internal/httpapi/fixture.go":         []byte(importHTTP),
		"internal/httpapi/nested/fixture.go":  []byte(importHTTP),
		"internal/store/generated-fixture.ts": []byte(importHTTP),
		"internal/store/allowed.go":           []byte("package fixture\nimport _ \"net/http\"\n"),
	}
	failures, err := importViolations(files)
	if err != nil {
		t.Fatal(err)
	}
	if len(failures) != len(violating) {
		t.Fatalf("got %d import violations, want %d: %v", len(failures), len(violating), failures)
	}
	for _, name := range violating {
		if !strings.Contains(strings.Join(failures, "\n"), name+":") {
			t.Errorf("missed reverse HTTP dependency in %s", name)
		}
	}
}
