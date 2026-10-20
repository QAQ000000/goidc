// This helper parses source only. It never imports or initializes the application.
package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type evidence struct {
	Source string `json:"source"`
	Line   int    `json:"line"`
}
type route struct {
	Method               string  `json:"method"`
	Path                 string  `json:"path"`
	Handler              string  `json:"handler"`
	AuthWrapper          *string `json:"authWrapper"`
	RegistrationFunction string  `json:"registrationFunction"`
	evidence
}
type evaluator struct {
	fset               *token.FileSet
	root               string
	functions          map[string]*ast.FuncDecl
	permissions        map[string]string
	permissionEvidence map[string][]evidence
}

func (e *evaluator) at(pos token.Pos) evidence {
	p := e.fset.Position(pos)
	rel, _ := filepath.Rel(e.root, p.Filename)
	return evidence{filepath.ToSlash(rel), p.Line}
}
func (e *evaluator) fail(n ast.Node, message string) {
	p := e.at(n.Pos())
	panic(fmt.Sprintf("%s:%d: %s", p.Source, p.Line, message))
}
func (e *evaluator) value(n ast.Expr, env map[string]any) any {
	switch n := n.(type) {
	case *ast.BasicLit:
		if n.Kind != token.STRING {
			e.fail(n, "unsupported permission literal")
		}
		v, err := strconv.Unquote(n.Value)
		if err != nil {
			panic(err)
		}
		return v
	case *ast.Ident:
		if n.Name == "true" {
			return true
		}
		if n.Name == "false" {
			return false
		}
		v, ok := env[n.Name]
		if !ok {
			e.fail(n, "unresolved permission identifier "+n.Name)
		}
		return v
	case *ast.ParenExpr:
		return e.value(n.X, env)
	case *ast.CompositeLit:
		_, isMap := n.Type.(*ast.MapType)
		if isMap {
			out := map[string]any{}
			for _, item := range n.Elts {
				kv, ok := item.(*ast.KeyValueExpr)
				if !ok {
					e.fail(item, "invalid permission map")
				}
				key := e.value(kv.Key, env).(string)
				if _, exists := out[key]; exists {
					e.fail(item, "duplicate permission map key "+key)
				}
				out[key] = e.value(kv.Value, env)
			}
			return out
		}
		out := []any{}
		for _, item := range n.Elts {
			out = append(out, e.value(item, env))
		}
		return out
	case *ast.IndexExpr:
		m, ok := e.value(n.X, env).(map[string]any)
		if !ok {
			e.fail(n, "unsupported permission index")
		}
		return m[e.value(n.Index, env).(string)]
	case *ast.BinaryExpr:
		if n.Op == token.ADD {
			return e.value(n.X, env).(string) + e.value(n.Y, env).(string)
		}
	}
	e.fail(n, fmt.Sprintf("unsupported permission expression %T", n))
	return nil
}
func (e *evaluator) statements(body *ast.BlockStmt, env map[string]any) (any, bool) {
	for _, statement := range body.List {
		switch n := statement.(type) {
		case *ast.AssignStmt:
			values := []any{}
			if len(n.Lhs) == 2 && len(n.Rhs) == 1 {
				idx, ok := n.Rhs[0].(*ast.IndexExpr)
				if !ok {
					e.fail(n, "unsupported multi assignment")
				}
				m := e.value(idx.X, env).(map[string]any)
				v, exists := m[e.value(idx.Index, env).(string)]
				values = []any{v, exists}
			} else {
				for _, rhs := range n.Rhs {
					values = append(values, e.value(rhs, env))
				}
			}
			if len(values) != len(n.Lhs) {
				e.fail(n, "permission assignment arity")
			}
			for i, lhs := range n.Lhs {
				switch target := lhs.(type) {
				case *ast.Ident:
					env[target.Name] = values[i]
				case *ast.IndexExpr:
					m := e.value(target.X, env).(map[string]any)
					key := e.value(target.Index, env).(string)
					if _, exists := m[key]; exists {
						e.fail(n, "duplicate permission operation "+key)
					}
					m[key] = values[i]
					if permission, ok := values[i].(string); ok {
						e.permissions[key] = permission
						e.permissionEvidence[key] = append(e.permissionEvidence[key], e.at(n.Pos()))
					}
				default:
					e.fail(n, "unsupported permission assignment target")
				}
			}
		case *ast.RangeStmt:
			bind := func(key, value any) (any, bool) {
				if n.Key != nil {
					env[n.Key.(*ast.Ident).Name] = key
				}
				if n.Value != nil {
					env[n.Value.(*ast.Ident).Name] = value
				}
				return e.statements(n.Body, env)
			}
			switch values := e.value(n.X, env).(type) {
			case []any:
				for i, v := range values {
					if v, done := bind(i, v); done {
						return v, true
					}
				}
			case map[string]any:
				keys := []string{}
				for key := range values {
					keys = append(keys, key)
				}
				sort.Strings(keys)
				for _, key := range keys {
					if v, done := bind(key, values[key]); done {
						return v, true
					}
				}
			default:
				e.fail(n, "unsupported permission range")
			}
		case *ast.IfStmt:
			if n.Init != nil {
				e.statements(&ast.BlockStmt{List: []ast.Stmt{n.Init}}, env)
			}
			condition, ok := e.value(n.Cond, env).(bool)
			if !ok {
				e.fail(n, "unsupported permission condition")
			}
			if condition {
				if v, done := e.statements(n.Body, env); done {
					return v, true
				}
			} else if n.Else != nil {
				block, ok := n.Else.(*ast.BlockStmt)
				if !ok {
					e.fail(n, "unsupported permission else")
				}
				if v, done := e.statements(block, env); done {
					return v, true
				}
			}
		case *ast.ExprStmt:
			call, ok := n.X.(*ast.CallExpr)
			if !ok {
				e.fail(n, "unsupported permission statement")
			}
			name, ok := call.Fun.(*ast.Ident)
			if !ok {
				e.fail(n, "permission helpers must be local static functions")
			}
			if name.Name == "panic" {
				e.fail(n, "permission initializer rejected duplicate operation")
			}
			fn := e.functions[name.Name]
			if fn == nil {
				e.fail(n, "unresolved permission helper "+name.Name)
			}
			local := map[string]any{}
			i := 0
			for _, field := range fn.Type.Params.List {
				for _, param := range field.Names {
					if i >= len(call.Args) {
						e.fail(n, "permission helper arity")
					}
					local[param.Name] = e.value(call.Args[i], env)
					i++
				}
			}
			if i != len(call.Args) {
				e.fail(n, "permission helper arity")
			}
			e.statements(fn.Body, local)
		case *ast.ReturnStmt:
			if len(n.Results) != 1 {
				e.fail(n, "permission initializer must return map")
			}
			return e.value(n.Results[0], env), true
		default:
			e.fail(n, fmt.Sprintf("unsupported permission statement %T", n))
		}
	}
	return nil, false
}
func expression(fset *token.FileSet, n ast.Expr) string {
	var b strings.Builder
	_ = printer.Fprint(&b, fset, n)
	return b.String()
}

func run() error {
	root, err := filepath.Abs(os.Args[1])
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	e := evaluator{fset: fset, root: root, functions: map[string]*ast.FuncDecl{}, permissions: map[string]string{}, permissionEvidence: map[string][]evidence{}}
	files := []*ast.File{}
	sources := []string{}
	var initializer ast.Expr
	err = filepath.WalkDir(filepath.Join(root, "internal/httpapi"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		files = append(files, file)
		rel, _ := filepath.Rel(root, p)
		sources = append(sources, filepath.ToSlash(rel))
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				e.functions[fn.Name.Name] = fn
			}
			if gen, ok := decl.(*ast.GenDecl); ok {
				for _, spec := range gen.Specs {
					if value, ok := spec.(*ast.ValueSpec); ok {
						for i, name := range value.Names {
							if name.Name == "adminRoutePermissions" && i < len(value.Values) {
								if initializer != nil {
									return fmt.Errorf("duplicate adminRoutePermissions declaration")
								}
								initializer = value.Values[i]
							}
						}
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	call, ok := initializer.(*ast.CallExpr)
	if !ok {
		return fmt.Errorf("adminRoutePermissions initializer must be a static function")
	}
	fn, ok := call.Fun.(*ast.FuncLit)
	if !ok || len(call.Args) != 0 {
		return fmt.Errorf("unsupported adminRoutePermissions initializer")
	}
	value, returned := e.statements(fn.Body, map[string]any{})
	if !returned {
		return fmt.Errorf("adminRoutePermissions did not return")
	}
	permissions, ok := value.(map[string]any)
	if !ok {
		return fmt.Errorf("adminRoutePermissions did not return map")
	}
	for operation, value := range permissions {
		permission, ok := value.(string)
		if !ok || permission == "" {
			return fmt.Errorf("invalid permission for %s", operation)
		}
		e.permissions[operation] = permission
	}
	routes := []route{}
	for _, file := range files {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || (sel.Sel.Name != "HandleFunc" && sel.Sel.Name != "Handle") {
					return true
				}
				if len(call.Args) != 2 {
					e.fail(call, "unresolved route registration")
				}
				literal, ok := call.Args[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					e.fail(call, "nonliteral route pattern requires explicit inventory support")
				}
				pattern, err := strconv.Unquote(literal.Value)
				if err != nil {
					panic(err)
				}
				parts := strings.SplitN(pattern, " ", 2)
				if len(parts) != 2 || !strings.HasPrefix(parts[1], "/") {
					e.fail(call, "route must have explicit method and path")
				}
				handler := expression(fset, call.Args[1])
				var wrapper *string
				if outer, ok := call.Args[1].(*ast.CallExpr); ok {
					value := expression(fset, outer.Fun)
					wrapper = &value
					if len(outer.Args) == 0 {
						e.fail(outer, "route wrapper has no handler")
					}
					handler = expression(fset, outer.Args[0])
				}
				if _, ok := call.Args[1].(*ast.FuncLit); ok {
					handler = "<anonymous>"
				}
				routes = append(routes, route{parts[0], parts[1], handler, wrapper, fn.Name.Name, e.at(call.Pos())})
				return true
			})
		}
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"routes": routes, "permissions": e.permissions, "permissionEvidence": e.permissionEvidence, "sources": sources})
}
func main() {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintln(os.Stderr, p)
			os.Exit(1)
		}
	}()
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "expected repository root")
		os.Exit(1)
	}
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
