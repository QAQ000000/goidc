package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Derive CatalogTx's transitive Store callers from production code so a new
// facade cannot silently bypass the HTTP credential binding by using an
// actor-only context. Internal Store callers retain their explicit authority.
func TestCatalogHTTPAuthorityBindings(t *testing.T) {
	parse := func(dir string) map[string]*ast.File {
		t.Helper()
		files, err := filepath.Glob(filepath.Join(dir, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]*ast.File{}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			out[path] = file
		}
		return out
	}
	storeFiles := parse("../store")
	edges := map[string][]string{}
	for _, file := range storeFiles {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List[0].Names) != 1 {
				continue
			}
			receiver, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			typ, ok := receiver.X.(*ast.Ident)
			if !ok || typ.Name != "Store" {
				continue
			}
			name := fn.Recv.List[0].Names[0].Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				id, ok := sel.X.(*ast.Ident)
				if ok && id.Name == name {
					edges[fn.Name.Name] = append(edges[fn.Name.Name], sel.Sel.Name)
				}
				return true
			})
		}
	}
	catalog := map[string]bool{"CatalogTx": true}
	for changed := true; changed; {
		changed = false
		for name, calls := range edges {
			if catalog[name] {
				continue
			}
			for _, call := range calls {
				if catalog[call] {
					catalog[name] = true
					changed = true
					break
				}
			}
		}
	}
	httpFiles := parse(".")
	handlers := map[string]bool{}
	handlerEdges := map[string][]string{}
	checked := map[string]bool{}
	for path, file := range httpFiles {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if receiver, ok := sel.X.(*ast.Ident); ok && receiver.Name == "s" {
					handlerEdges[fn.Name.Name] = append(handlerEdges[fn.Name.Name], sel.Sel.Name)
				}
				receiver, ok := sel.X.(*ast.SelectorExpr)
				if !ok || receiver.Sel.Name != "db" || !catalog[sel.Sel.Name] {
					return true
				}
				handlers[fn.Name.Name] = true
				checked[sel.Sel.Name] = true
				if len(call.Args) == 0 {
					t.Errorf("%s %s has no credential context", path, sel.Sel.Name)
					return true
				}
				bound, ok := call.Args[0].(*ast.CallExpr)
				if !ok {
					t.Errorf("%s %s does not bind its original HTTP credential", path, sel.Sel.Name)
					return true
				}
				binding, ok := bound.Fun.(*ast.Ident)
				if !ok || binding.Name != "requestFileContext" {
					t.Errorf("%s %s must bind the original HTTP credential", path, sel.Sel.Name)
				}
				return true
			})
		}
	}
	for changed := true; changed; {
		changed = false
		for name, calls := range handlerEdges {
			if handlers[name] {
				continue
			}
			for _, call := range calls {
				if handlers[call] {
					handlers[name] = true
					changed = true
					break
				}
			}
		}
	}
	// Every registered route reaching CatalogTx is explicitly classified and
	// protected by administrative authentication, including shared GET/PUT funcs.
	registered := map[string]bool{}
	for _, file := range httpFiles {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 2 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "HandleFunc" {
				return true
			}
			route, ok := call.Args[0].(*ast.BasicLit)
			if !ok {
				return true
			}
			pattern, err := strconv.Unquote(route.Value)
			if err != nil {
				t.Fatal(err)
			}
			var targets []string
			ast.Inspect(call.Args[1], func(n ast.Node) bool {
				target, ok := n.(*ast.SelectorExpr)
				if ok && handlers[target.Sel.Name] {
					targets = append(targets, target.Sel.Name)
				}
				return true
			})
			if len(targets) == 0 {
				return true
			}
			for _, target := range targets {
				registered[target] = true
			}
			wrapper, ok := call.Args[1].(*ast.CallExpr)
			if !ok || len(wrapper.Args) < 2 {
				t.Errorf("catalog route %s lacks administrative authentication", pattern)
				return true
			}
			guard, ok := wrapper.Fun.(*ast.SelectorExpr)
			if !ok || (guard.Sel.Name != "auth" && guard.Sel.Name != "sessionAuth") {
				t.Errorf("catalog route %s lacks administrative authentication", pattern)
			}
			admin, ok := wrapper.Args[1].(*ast.Ident)
			if !ok || admin.Name != "true" {
				t.Errorf("catalog route %s is not admin-only", pattern)
			}
			expected := "products.manage"
			if strings.HasPrefix(pattern, "GET ") {
				expected = "products.view"
			}
			if adminRoutePermissions[pattern] != expected {
				t.Errorf("catalog route %s has unexpected permission %q", pattern, adminRoutePermissions[pattern])
			}
			return true
		})
	}
	for method := range handlers {
		if method == "deleteConfiguration" {
			continue
		}
		if !registered[method] {
			t.Errorf("catalog handler %s has no reviewed route registration", method)
		}
	}
	if len(checked) < 20 {
		t.Fatalf("catalog call graph scan covered only %d Store boundaries", len(checked))
	}
	t.Logf("verified %d Store boundaries and %d route handlers", len(checked), len(registered))
}
