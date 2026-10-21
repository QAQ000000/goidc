package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"go/ast"
	"go/format"
	"go/parser"
	"go/scanner"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// This ordered baseline was captured before extracting Handler's registrations.
// Each row records the method/path, the entire handler expression (including
// wrapper order and arguments), and its effective administrative permission.
// File locations and whitespace are deliberately excluded so moving a route is
// harmless, while dropping, duplicating, reordering or weakening one is visible.
func TestRouteRegistrationContract(t *testing.T) {
	actual := routeRegistrationManifest(t)
	f, err := os.Open("testdata/route-registration-baseline.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var expected [][3]string
	lines := bufio.NewScanner(f)
	for lines.Scan() {
		var entry [3]string
		if err := json.Unmarshal(lines.Bytes(), &entry); err != nil {
			t.Fatalf("invalid route baseline row %d: %v", len(expected)+1, err)
		}
		expected = append(expected, entry)
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	if len(actual) != len(expected) {
		t.Fatalf("registered routes = %d, baseline = %d; review method/path, wrappers and permission for intended API changes", len(actual), len(expected))
	}
	for i := range actual {
		if actual[i] != expected[i] {
			t.Errorf("route %d changed:\n baseline: %q\n current:  %q", i+1, expected[i], actual[i])
		}
	}
	t.Logf("preserved %d ordered route registrations, handler/wrapper expressions and permissions", len(actual))
}

func routeRegistrationManifest(t *testing.T) [][3]string {
	t.Helper()
	fset := token.NewFileSet()
	methods := map[string]*ast.FuncDecl{}
	registered := 0
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			if call, ok := n.(*ast.CallExpr); ok {
				if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "HandleFunc" {
					registered++
				}
			}
			return true
		})
		for _, declaration := range file.Decls {
			fn, ok := declaration.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			pointer, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			receiver, ok := pointer.X.(*ast.Ident)
			if ok && receiver.Name == "Server" {
				methods[fn.Name.Name] = fn
			}
		}
	}
	var result [][3]string
	visiting := map[string]bool{}
	var expand func(string)
	expand = func(name string) {
		fn, ok := methods[name]
		if !ok || fn.Body == nil || visiting[name] {
			t.Fatalf("missing or recursive route registrar %q", name)
		}
		visiting[name] = true
		defer delete(visiting, name)
		for _, statement := range fn.Body.List {
			expression, ok := statement.(*ast.ExprStmt)
			if !ok {
				if name == "Handler" {
					continue // ServeMux construction and the outer middleware return.
				}
				t.Fatalf("route registrar %s contains nonliteral control flow at %s", name, fset.Position(statement.Pos()))
			}
			call, ok := expression.X.(*ast.CallExpr)
			if !ok {
				t.Fatalf("unexpected route statement in %s", name)
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				t.Fatalf("unexpected route call in %s", name)
			}
			owner, ok := sel.X.(*ast.Ident)
			if !ok {
				t.Fatalf("nonliteral route receiver in %s", name)
			}
			if owner.Name == "s" && len(call.Args) == 1 {
				if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "mux" {
					expand(sel.Sel.Name)
					continue
				}
			}
			if owner.Name != "mux" || sel.Sel.Name != "HandleFunc" || len(call.Args) != 2 {
				t.Fatalf("unsupported registration call in %s at %s", name, fset.Position(call.Pos()))
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				t.Fatalf("nonliteral route pattern in %s", name)
			}
			pattern, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatal(err)
			}
			result = append(result, [3]string{pattern, routeExpressionTokens(t, call.Args[1]), adminRoutePermissions[pattern]})
		}
	}
	expand("Handler")
	if len(result) != registered {
		t.Fatalf("Handler reaches %d registrations but production source contains %d; orphaned or repeated registrar", len(result), registered)
	}
	return result
}

func routeExpressionTokens(t *testing.T, expression ast.Expr) string {
	t.Helper()
	var formatted bytes.Buffer
	if err := format.Node(&formatted, token.NewFileSet(), expression); err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	file := fset.AddFile("handler", -1, formatted.Len())
	var lexer scanner.Scanner
	lexer.Init(file, formatted.Bytes(), func(_ token.Position, message string) { t.Error(message) }, 0)
	var tokens []string
	for {
		_, kind, literal := lexer.Scan()
		if kind == token.EOF {
			break
		}
		if literal == "" {
			literal = kind.String()
		}
		// Inserted semicolons depend only on formatting, not the handler AST.
		if kind != token.SEMICOLON || literal != "\n" {
			tokens = append(tokens, literal)
		}
	}
	return strings.Join(tokens, " ")
}
