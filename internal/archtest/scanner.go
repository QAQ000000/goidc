// Package archtest checks persistence boundaries without executing application code.
package archtest

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"path"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

const modulePath = "github.com/codex/zjmfgo"

type exception struct {
	File        string `json:"file"`
	Function    string `json:"function"`
	Operation   string `json:"operation"`
	Fingerprint string `json:"fingerprint"`
	Count       int    `json:"count"`
}

type baseline struct {
	Version int         `json:"version"`
	Entries []exception `json:"entries"`
}

func (e exception) key() string {
	return strings.Join([]string{e.File, e.Function, e.Operation, e.Fingerprint}, "|")
}

func driverPackage(p string) bool {
	return p == "database/sql" || p == "database/sql/driver" || p == "github.com/jackc/pgx/v5" || strings.HasPrefix(p, "github.com/jackc/pgx/v5/")
}

// Signatures expose narrow local interfaces too: Query's pgx.Rows return type,
// for example, cannot disappear by renaming the receiver or its interface.
func driverType(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	t = types.Unalias(t)
	switch v := t.(type) {
	case *types.Named:
		if p := v.Obj().Pkg(); p != nil && driverPackage(p.Path()) {
			return true
		}
		return driverType(v.Underlying(), seen)
	case *types.Pointer:
		return driverType(v.Elem(), seen)
	case *types.Signature:
		return driverType(v.Params(), seen) || driverType(v.Results(), seen)
	case *types.Tuple:
		for i := 0; i < v.Len(); i++ {
			if driverType(v.At(i).Type(), seen) {
				return true
			}
		}
	case *types.Interface:
		v.Complete()
		for i := 0; i < v.NumMethods(); i++ {
			if driverType(v.Method(i).Type(), seen) {
				return true
			}
		}
	case *types.Slice:
		return driverType(v.Elem(), seen)
	case *types.Array:
		return driverType(v.Elem(), seen)
	case *types.Map:
		return driverType(v.Key(), seen) || driverType(v.Elem(), seen)
	case *types.Chan:
		return driverType(v.Elem(), seen)
	case *types.TypeParam:
		return driverType(v.Constraint(), seen)
	}
	return false
}

func databaseHandle(t types.Type) bool {
	t = types.Unalias(t)
	if p, ok := t.(*types.Pointer); ok {
		t = types.Unalias(p.Elem())
	}
	n, ok := t.(*types.Named)
	if !ok || n.Obj().Pkg() == nil {
		return false
	}
	p, name := n.Obj().Pkg().Path(), n.Obj().Name()
	switch p {
	case "database/sql":
		return name == "DB" || name == "Tx" || name == "Conn" || name == "Stmt"
	case "github.com/jackc/pgx/v5":
		return name == "Tx" || name == "Conn" || name == "Batch" || name == "BatchResults" || name == "LargeObjects" || name == "LargeObject"
	case "github.com/jackc/pgx/v5/pgxpool":
		return name == "Pool" || name == "Conn"
	case "github.com/jackc/pgx/v5/pgconn":
		return name == "PgConn" || name == "Pipeline" || name == "Batch"
	}
	return false
}

func signatureHasHandle(t types.Type, seen map[types.Type]bool) bool {
	if t == nil || seen[t] {
		return false
	}
	seen[t] = true
	if databaseHandle(t) {
		return true
	}
	switch v := types.Unalias(t).Underlying().(type) {
	case *types.Signature:
		return signatureHasHandle(v.Params(), seen) || signatureHasHandle(v.Results(), seen)
	case *types.Tuple:
		for i := 0; i < v.Len(); i++ {
			if signatureHasHandle(v.At(i).Type(), seen) {
				return true
			}
		}
	case *types.Interface:
		v.Complete()
		for i := 0; i < v.NumMethods(); i++ {
			if signatureHasHandle(v.Method(i).Type(), seen) {
				return true
			}
		}
	}
	return false
}

func driverFunction(fn *types.Func) bool {
	sig := fn.Type().(*types.Signature)
	return fn.Pkg() != nil && driverPackage(fn.Pkg().Path()) && sig.Recv() == nil && signatureHasHandle(sig, make(map[types.Type]bool))
}

type sourceDecl struct {
	file, name string
	node       ast.Node
	object     *types.Func
	stringArg  bool
}

func selectorOperation(s *ast.SelectorExpr, info *types.Info, bridges map[*types.Func]bool) string {
	fn, ok := info.Uses[s.Sel].(*types.Func)
	if !ok {
		return ""
	}
	if bridges[fn] {
		return "http-sql-helper:" + fn.FullName()
	}
	if selection := info.Selections[s]; selection != nil {
		// Obj's declared receiver also catches promoted/embedded driver methods.
		sig := fn.Type().(*types.Signature)
		if databaseHandle(selection.Recv()) || sig.Recv() != nil && databaseHandle(sig.Recv().Type()) {
			return "driver-method:" + fn.FullName()
		}
		if driverType(sig, make(map[types.Type]bool)) {
			return "driver-signature:" + fn.FullName()
		}
	} else if driverFunction(fn) {
		// Includes connection factories and pgx.BeginFunc; types and constants
		// are not functions and are intentionally excluded.
		return "driver-function:" + fn.FullName()
	}
	return ""
}

func identifierOperation(id *ast.Ident, info *types.Info, bridges map[*types.Func]bool) string {
	fn, ok := info.Uses[id].(*types.Func)
	if ok {
		if bridges[fn] {
			return "http-sql-helper:" + fn.FullName()
		}
		if driverFunction(fn) {
			return "driver-function:" + fn.FullName()
		}
	}
	if variable, ok := info.Uses[id].(*types.Var); ok {
		if sig, ok := variable.Type().Underlying().(*types.Signature); ok && driverType(sig, make(map[types.Type]bool)) {
			return "driver-function-value:" + types.TypeString(sig, func(p *types.Package) string { return p.Path() })
		}
	}
	return ""
}

func operations(n ast.Node, info *types.Info, bridges map[*types.Func]bool) []string {
	var result []string
	ast.Inspect(n, func(n ast.Node) bool {
		var op string
		switch v := n.(type) {
		case *ast.SelectorExpr:
			op = selectorOperation(v, info, bridges)
			if op != "" {
				result = append(result, op)
			}
			// Never reinterpret Sel as a standalone/dot-imported function.
			// X may itself be a call such as pool.Acquire(...).Conn().
			result = append(result, operations(v.X, info, bridges)...)
			return false
		case *ast.Ident:
			op = identifierOperation(v, info, bridges)
		}
		if op != "" {
			result = append(result, op)
		}
		return true
	})
	return result
}

func analyzeHTTP(sources map[string][]byte, imp types.Importer) ([]exception, error) {
	fset := token.NewFileSet()
	info := &types.Info{Types: map[ast.Expr]types.TypeAndValue{}, Defs: map[*ast.Ident]types.Object{}, Uses: map[*ast.Ident]types.Object{}, Selections: map[*ast.SelectorExpr]*types.Selection{}}
	var files []*ast.File
	var names []string
	for name := range sources {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := supportedHTTPFile(name, sources[name]); err != nil {
			return nil, err
		}
		f, err := parser.ParseFile(fset, name, sources[name], 0)
		if err != nil {
			return nil, err
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("no HTTP production files found")
	}
	cfg := types.Config{Importer: imp}
	if _, err := cfg.Check(modulePath+"/internal/httpapi", fset, files, info); err != nil {
		return nil, fmt.Errorf("type-check HTTP persistence boundary: %w", err)
	}
	var decls []sourceDecl
	bridges := make(map[*types.Func]bool)
	for i, file := range files {
		for index, node := range file.Decls {
			d := sourceDecl{file: names[i], node: node, name: fmt.Sprintf("declaration:%d", index)}
			if fn, ok := node.(*ast.FuncDecl); ok {
				d.object, _ = info.Defs[fn.Name].(*types.Func)
				d.name = d.object.FullName()
				sig := d.object.Type().(*types.Signature)
				for p := 0; p < sig.Params().Len(); p++ {
					if types.Identical(sig.Params().At(p).Type().Underlying(), types.Typ[types.String]) {
						d.stringArg = true
					}
				}
				if driverType(sig, make(map[types.Type]bool)) {
					bridges[d.object] = true
				}
			} else if g, ok := node.(*ast.GenDecl); ok {
				if g.Tok == token.IMPORT || g.Tok == token.TYPE {
					continue
				}
				// Global initializers also cannot obtain or retain driver methods.
				var declared []string
				for _, spec := range g.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for _, name := range value.Names {
							declared = append(declared, name.Name)
						}
					}
				}
				d.name = g.Tok.String() + ":" + strings.Join(declared, ",")
			}
			decls = append(decls, d)
		}
	}
	// A SQL-taking HTTP utility remains persistence access at every caller,
	// including utilities calling another utility instead of the driver itself.
	for changed := true; changed; {
		changed = false
		for _, d := range decls {
			if d.object != nil && d.stringArg && !bridges[d.object] && len(operations(d.node, info, bridges)) > 0 {
				bridges[d.object], changed = true, true
			}
		}
	}
	counts := map[string]exception{}
	for _, d := range decls {
		ops := operations(d.node, info, bridges)
		if len(ops) == 0 {
			continue
		}
		fingerprint, err := declarationFingerprint(fset, d.node, info)
		if err != nil {
			return nil, err
		}
		for _, op := range ops {
			e := exception{File: d.file, Function: d.name, Operation: op, Fingerprint: fingerprint}
			key := e.key()
			if previous, ok := counts[key]; ok {
				e.Count = previous.Count
			}
			e.Count++
			counts[key] = e
		}
	}
	result := make([]exception, 0, len(counts))
	for _, e := range counts {
		result = append(result, e)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].key() < result[j].key() })
	return result, nil
}

func declarationFingerprint(fset *token.FileSet, n ast.Node, info *types.Info) (string, error) {
	var printed bytes.Buffer
	if err := format.Node(&printed, fset, n); err != nil {
		return "", err
	}
	hash := sha256.New()
	// Reparse without object resolution to avoid object graph links; ignore
	// positions and comments, including optional trailing commas/semicolons.
	canonical, err := parser.ParseFile(token.NewFileSet(), "declaration.go", "package httpapi\n"+printed.String(), parser.SkipObjectResolution)
	if err != nil {
		return "", err
	}
	ast.Inspect(canonical, func(n ast.Node) bool {
		if literal, ok := n.(*ast.BasicLit); ok && (literal.Kind == token.STRING || literal.Kind == token.CHAR) {
			value, _ := strconv.Unquote(literal.Value)
			literal.Value = strconv.Quote(value)
		}
		// These position fields encode syntax, not just layout. Preserve the
		// presence of variadic expansion and type aliases before dropping positions.
		if call, ok := n.(*ast.CallExpr); ok {
			fmt.Fprintf(hash, "variadic:%t;", call.Ellipsis.IsValid())
		}
		if spec, ok := n.(*ast.TypeSpec); ok {
			fmt.Fprintf(hash, "type-alias:%t;", spec.Assign.IsValid())
		}
		return true
	})
	if err := ast.Fprint(hash, nil, canonical.Decls[0], func(name string, value reflect.Value) bool {
		return value.Type() != reflect.TypeFor[token.Pos]() && name != "Doc" && name != "Comment" && name != "Obj"
	}); err != nil {
		return "", err
	}
	// A constant's declaration may be in another file or package. Hash its
	// evaluated value too, preserving SQL string contents, comments and spaces.
	constants := make(map[string]bool)
	ast.Inspect(n, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok {
			if c, ok := info.Uses[id].(*types.Const); ok {
				identity := c.Id()
				if c.Pkg() != nil {
					identity = c.Pkg().Path() + "." + c.Name()
				}
				// Shadowed constants may share a name but have different values.
				constants[identity+"="+c.Val().ExactString()] = true
			}
		}
		return true
	})
	var keys []string
	for key := range constants {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(hash, "const:%s;", key)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func supportedHTTPFile(name string, src []byte) error {
	if path.Dir(name) != "internal/httpapi" || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
		return fmt.Errorf("HTTP source %s needs explicit architecture scanner support", name)
	}
	// No variants exist today. Reject rather than silently miss future build
	// tags, platform files or subpackages in the host's type-check configuration.
	parts := strings.Split(strings.TrimSuffix(path.Base(name), ".go"), "_")
	platforms := " aix android darwin dragonfly freebsd hurd illumos ios js linux nacl netbsd openbsd plan9 solaris wasip1 windows zos 386 amd64 amd64p32 arm arm64 loong64 mips mipsle mips64 mips64le mips64p32 mips64p32le ppc ppc64 ppc64le riscv riscv64 s390 s390x sparc sparc64 wasm "
	for _, suffix := range parts[1:] {
		if strings.Contains(platforms, " "+suffix+" ") {
			return fmt.Errorf("platform-specific HTTP source %s needs architecture scanner support", name)
		}
	}
	for _, line := range strings.Split(string(src), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "//go:build") || strings.HasPrefix(line, "// +build") {
			return fmt.Errorf("build-constrained HTTP source %s needs architecture scanner support", name)
		}
	}
	return nil
}

func decodeBaseline(data []byte) (baseline, error) {
	var b baseline
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&b); err != nil {
		return b, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return b, fmt.Errorf("baseline has trailing JSON or invalid data")
	}
	if b.Version != 1 || b.Entries == nil {
		return b, fmt.Errorf("unsupported or missing architecture baseline version/entries")
	}
	seen := map[string]bool{}
	for _, e := range b.Entries {
		if path.Dir(e.File) != "internal/httpapi" || !strings.HasSuffix(e.File, ".go") || e.Function == "" || e.Operation == "" || e.Count < 1 {
			return b, fmt.Errorf("invalid architecture exception: %+v", e)
		}
		if raw, err := hex.DecodeString(e.Fingerprint); err != nil || len(raw) != sha256.Size {
			return b, fmt.Errorf("invalid architecture fingerprint: %s", e.File)
		}
		if seen[e.key()] {
			return b, fmt.Errorf("duplicate architecture exception: %s", e.key())
		}
		seen[e.key()] = true
	}
	return b, nil
}

// Current may remove exceptions or reduce counts; it cannot spend a deleted
// exception on a different file, function, operation or function body.
func compareExceptions(allowed, actual []exception) []string {
	limits := make(map[string]int)
	for _, e := range allowed {
		limits[e.key()] += e.Count
	}
	used := make(map[string]int)
	var failures []string
	for _, e := range actual {
		used[e.key()] += e.Count
		if used[e.key()] > limits[e.key()] {
			failures = append(failures, fmt.Sprintf("%s %s %s: %d access(es), allowed %d; move persistence into Store", e.File, e.Function, e.Operation, used[e.key()], limits[e.key()]))
		}
	}
	sort.Strings(failures)
	return failures
}
