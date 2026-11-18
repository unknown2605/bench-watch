package api

import (
	"cmp"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Generated constructors whose defaults answer with http.Error and the error's text.
var textDefaults = map[string]bool{"Handler": true, "HandlerFromMux": true, "HandlerFromMuxWithBaseURL": true,
	"HandlerWithOptions": true, "NewStrictHandler": true, "NewStrictHandlerWithOptions": true,
	"ServerInterfaceWrapper": true}

// The two NewHandler may call, and the option fields each call must set to a handler of its own.
var requiredOptions = map[string][]string{
	"HandlerWithOptions":          {"ErrorHandlerFunc"},
	"NewStrictHandlerWithOptions": {"RequestErrorHandlerFunc", "ResponseErrorHandlerFunc"},
}

func errorTextUses(filename string, src []byte) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filename, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	var problems []string
	report := func(n ast.Node, what string) {
		problems = append(problems, fmt.Sprintf("%s: %s", fset.Position(n.Pos()), what))
	}
	httpName, apiName := "", ""
	for _, imp := range f.Imports {
		p, _ := strconv.Unquote(imp.Path.Value)
		name := ""
		if imp.Name != nil {
			name = imp.Name.Name
		}
		switch {
		case p == "net/http":
			httpName = cmp.Or(name, "http")
		case strings.HasSuffix(p, "/internal/api"):
			apiName = cmp.Or(name, "api")
		}
		if name == "." && (p == "net/http" || strings.HasSuffix(p, "/internal/api")) {
			report(imp, "dot import of "+p)
		}
	}
	inAPI := f.Name.Name == "api"
	mayBuild := inAPI && filepath.Base(filename) == "handler.go"
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		var parent ast.Node
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		stack = append(stack, n)
		switch n := n.(type) {
		case *ast.SelectorExpr:
			x, ok := n.X.(*ast.Ident)
			if !ok {
				return true
			}
			if httpName != "" && x.Name == httpName && n.Sel.Name == "Error" {
				report(n, "http.Error sends error text")
			}
			if apiName != "" && x.Name == apiName && textDefaults[n.Sel.Name] {
				report(n, apiName+"."+n.Sel.Name+" can answer with error text; use NewHandler")
			}
		case *ast.Ident:
			if !inAPI || !textDefaults[n.Name] || namesSomethingElse(parent, n) {
				return true
			}
			if call, ok := parent.(*ast.CallExpr); ok && mayBuild && call.Fun == n && requiredOptions[n.Name] != nil &&
				setsOptions(call, requiredOptions[n.Name]) {
				return true
			}
			report(n, n.Name+" can answer with error text; only NewHandler may call it, with its own error handlers")
		}
		return true
	})
	return problems, nil
}

// namesSomethingElse is true where the identifier is a field, key or declared name rather than the generated function.
func namesSomethingElse(parent ast.Node, id *ast.Ident) bool {
	switch p := parent.(type) {
	case *ast.SelectorExpr:
		return p.Sel == id
	case *ast.KeyValueExpr:
		return p.Key == id
	case *ast.Field:
		return slices.Contains(p.Names, id)
	case *ast.FuncDecl:
		return p.Name == id
	case *ast.TypeSpec:
		return p.Name == id
	}
	return false
}

func setsOptions(call *ast.CallExpr, keys []string) bool {
	if len(call.Args) == 0 {
		return false
	}
	lit, ok := call.Args[len(call.Args)-1].(*ast.CompositeLit)
	if !ok {
		return false
	}
	set := map[string]bool{}
	for _, el := range lit.Elts {
		kv, ok := el.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok {
			continue
		}
		if v, isIdent := kv.Value.(*ast.Ident); isIdent && v.Name == "nil" {
			continue
		}
		set[key.Name] = true
	}
	for _, k := range keys {
		if !set[k] {
			return false
		}
	}
	return true
}

func TestErrorTextGuardCatchesEveryForm(t *testing.T) {
	const imports = "import (\n\t\"net/http\"\n\tapi \"example.com/server/internal/api\"\n)\n"
	cases := []struct {
		name, file, src string
		flagged         bool
	}{
		{"Handler", "main.go", "package main\n" + imports + "func f(si api.ServerInterface) { _ = api.Handler(si) }", true},
		{"HandlerWithOptions", "main.go", "package main\n" + imports +
			"func f(si api.ServerInterface) { _ = api.HandlerWithOptions(si, api.StdHTTPServerOptions{}) }", true},
		{"strict", "main.go", "package main\n" + imports + "func f() { _ = api.NewStrictHandler(nil, nil) }", true},
		{"function value", "main.go", "package main\n" + imports + "var h = api.HandlerFromMux", true},
		{"wrapper", "main.go", "package main\n" + imports + "var w api.ServerInterfaceWrapper", true},
		{"aliased api", "main.go", "package main\nimport x \"example.com/server/internal/api\"\nvar h = x.Handler", true},
		{"http.Error", "main.go", "package main\n" + imports +
			"func f(w http.ResponseWriter) { http.Error(w, \"x\", 500) }", true},
		{"aliased http", "main.go", "package main\nimport nh \"net/http\"\nvar e = nh.Error", true},
		{"dot import", "main.go", "package main\nimport . \"example.com/server/internal/api\"\nvar _ = 1", true},
		{"no error handler", "handler.go", "package api\nfunc f(s ServerInterface) { HandlerWithOptions(s, StdHTTPServerOptions{}) }", true},
		{"nil error handler", "handler.go",
			"package api\nfunc f(s ServerInterface) { HandlerWithOptions(s, StdHTTPServerOptions{ErrorHandlerFunc: nil}) }", true},
		{"one strict handler", "handler.go", "package api\nfunc f(s StrictServerInterface) {\n" +
			"NewStrictHandlerWithOptions(s, nil, StrictHTTPServerOptions{RequestErrorHandlerFunc: g}) }", true},
		{"other file in api", "server.go",
			"package api\nfunc f(s ServerInterface) { HandlerWithOptions(s, StdHTTPServerOptions{ErrorHandlerFunc: g}) }", true},
		{"bare Handler in api", "handler.go", "package api\nfunc f(s ServerInterface) { Handler(s) }", true},
		{"own handlers", "handler.go", "package api\nfunc f(s ServerInterface) {\n" +
			"HandlerWithOptions(s, StdHTTPServerOptions{ErrorHandlerFunc: g}) }", false},
		{"NewHandler", "main.go", "package main\n" + imports +
			"func f(s api.StrictServerInterface) { h, _ := api.NewHandler(s); _ = http.Server{Handler: h} }", false},
		{"other Handler methods", "main.go", "package main\n" + imports +
			"func f(m *http.ServeMux, r *http.Request, w api.ServerInterfaceWrapper2) { m.Handler(r); _ = w.Handler }", false},
	}
	for _, c := range cases {
		problems, err := errorTextUses(c.file, []byte(c.src))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if (len(problems) > 0) != c.flagged {
			t.Errorf("%s: problems %v, want flagged %v", c.name, problems, c.flagged)
		}
	}
}

func TestNoServerCodeSendsErrorText(t *testing.T) {
	root := filepath.Join("..", "..")
	files := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "bin" || d.Name() == "testdata") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".gen.go") {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files++
		problems, err := errorTextUses(path, b)
		if err != nil {
			return err
		}
		for _, p := range problems {
			t.Error(p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if files < 5 {
		t.Fatalf("only %d Go files checked", files)
	}
}
