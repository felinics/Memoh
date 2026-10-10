package apperror_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// externalFieldNamers are the calls outside a package that name a request
// field on the wire, with the position of the name.
var externalFieldNamers = map[string]map[string]int{
	"apperror": {"FieldRequired": 0, "FieldInvalid": 0},
	"httpx":    {"RequiredParam": 1, "RequiredQuery": 1},
}

// fieldNamingExempt is where a field name is data rather than code: the binder
// names the key a JSON type error was found in, the required helpers pass on
// the name their callers wrote, and an Agent's configuration fields are named
// by the runtime's configuration schema.
var fieldNamingExempt = map[string]bool{
	"internal/httpx/request_fields.go": true,
	"internal/botagents/service.go":    true,
}

// TestRequestFieldNamesAreLiterals keeps every field a response names spelled
// as the request spells it, and keeps error text out of public args. A field
// name is part of the public contract, so it is written as a string literal
// where the field is checked. It may then travel: as the field parameter of a
// helper, or as the Field of an error the handler translates. Every place a
// name is given must be one of those three:
//
//   - the name argument of apperror.FieldRequired / FieldInvalid and of
//     httpx.RequiredParam / RequiredQuery;
//   - the field argument of a function in the same package that takes a
//     string parameter named field;
//   - the Field of a composite literal of a type named ...Error, and the
//     "field" key of the args given to apperror.New / Wrap.
func TestRequestFieldNamesAreLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || !d.IsDir() {
				return err
			}
			checkPackage(t, root, path)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// checkPackage checks the non-test Go files of one directory.
func checkPackage(t *testing.T, root, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	files := map[string]*ast.File{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		rel, _ := filepath.Rel(root, path)
		files[filepath.ToSlash(rel)] = file
	}
	namers := packageFieldNamers(files)
	for rel, file := range files {
		if fieldNamingExempt[rel] {
			continue
		}
		for _, decl := range file.Decls {
			var params map[string]bool
			if fn, ok := decl.(*ast.FuncDecl); ok {
				if i, ok := fieldParam(fn); ok {
					params = map[string]bool{paramName(fn, i): true}
				}
			}
			check := func(e ast.Expr, what string) {
				if !namedLiterally(e, params) {
					t.Errorf("%s: %s names its field with %s; write the field as a string literal", fset.Position(e.Pos()), what, exprString(e))
				}
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				switch n := n.(type) {
				case *ast.CallExpr:
					checkCall(t, fset, n, namers, check)
				case *ast.CompositeLit:
					if strings.HasSuffix(typeName(n.Type), "Error") {
						for _, elt := range n.Elts {
							if kv, ok := elt.(*ast.KeyValueExpr); ok && identName(kv.Key) == "Field" {
								check(kv.Value, typeName(n.Type)+".Field")
							}
						}
					}
				}
				return true
			})
		}
	}
}

func checkCall(t *testing.T, fset *token.FileSet, call *ast.CallExpr, namers map[string]int, check func(ast.Expr, string)) {
	t.Helper()
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		if i, ok := namers[fun.Name]; ok && i < len(call.Args) {
			check(call.Args[i], fun.Name)
		}
	case *ast.SelectorExpr:
		pkg, ok := fun.X.(*ast.Ident)
		if !ok {
			return
		}
		if i, ok := externalFieldNamers[pkg.Name][fun.Sel.Name]; ok && i < len(call.Args) {
			check(call.Args[i], pkg.Name+"."+fun.Sel.Name)
		}
		if pkg.Name == "apperror" && (fun.Sel.Name == "New" || fun.Sel.Name == "Wrap") {
			for _, arg := range call.Args {
				lit, ok := arg.(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.BasicLit); ok && key.Value == `"field"` {
						check(kv.Value, `the "field" arg`)
					}
					if isErrorText(kv.Value) {
						t.Errorf("%s: public arg %s is error text", fset.Position(kv.Pos()), exprString(kv.Key))
					}
				}
			}
		}
	}
}

// packageFieldNamers are the functions of a package that take a field name:
// a string parameter named field, by position.
func packageFieldNamers(files map[string]*ast.File) map[string]int {
	namers := map[string]int{}
	for _, file := range files {
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil {
				if i, ok := fieldParam(fn); ok {
					namers[fn.Name.Name] = i
				}
			}
		}
	}
	return namers
}

// fieldParam is the position of fn's string parameter named field.
func fieldParam(fn *ast.FuncDecl) (int, bool) {
	i := 0
	for _, p := range fn.Type.Params.List {
		for _, name := range p.Names {
			if name.Name == "field" && identName(p.Type) == "string" {
				return i, true
			}
			i++
		}
		if len(p.Names) == 0 {
			i++
		}
	}
	return 0, false
}

func paramName(fn *ast.FuncDecl, i int) string {
	j := 0
	for _, p := range fn.Type.Params.List {
		for _, name := range p.Names {
			if j == i {
				return name.Name
			}
			j++
		}
	}
	return ""
}

// namedLiterally reports whether e gives a field name the guard accepts: a
// string literal, the field parameter of the enclosing helper, or the Field of
// an error being translated (x.Field or x.Field()).
func namedLiterally(e ast.Expr, params map[string]bool) bool {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Kind == token.STRING
	case *ast.Ident:
		return params[v.Name]
	case *ast.SelectorExpr:
		return v.Sel.Name == "Field"
	case *ast.CallExpr:
		sel, ok := v.Fun.(*ast.SelectorExpr)
		return ok && sel.Sel.Name == "Field" && len(v.Args) == 0
	}
	return false
}

// isErrorText reports a call of an Error method.
func isErrorText(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Error" && len(call.Args) == 0
}

func typeName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.StarExpr:
		return typeName(v.X)
	}
	return ""
}

func identName(e ast.Expr) string {
	if id, ok := e.(*ast.Ident); ok {
		return id.Name
	}
	return ""
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.CallExpr:
		return exprString(v.Fun) + "()"
	}
	return "an expression"
}

// badRequestStatuses are the statuses whose echo.HTTPError message a user
// would read as the reason a request was refused.
var badRequestStatuses = map[string]bool{
	"StatusBadRequest": true, "StatusUnprocessableEntity": true, "400": true, "422": true,
}

// TestBadRequestsCarryNoUserText keeps a refusal's explanation out of
// echo.NewHTTPError. The boundary answers an *echo.HTTPError with the generic
// code for its status and never sends its message, so a sentence written there
// for the user is never read. A field problem uses apperror.FieldRequired or
// FieldInvalid, a rule has a catalog code, and text kept for the access record
// goes in WithInternal.
func TestBadRequestsCarryNoUserText(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NewHTTPError" || identName(sel.X) != "echo" {
					return true
				}
				if badRequestStatuses[statusName(call.Args[0])] {
					t.Errorf("%s: echo.NewHTTPError(%s, ...) carries text the user never sees; use a field or catalog error, or WithInternal", fset.Position(call.Pos()), statusName(call.Args[0]))
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func statusName(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name
	case *ast.BasicLit:
		return v.Value
	}
	return ""
}

// TestHTTPErrorsCarryNoErrorText keeps an error's text out of the message of
// an echo.HTTPError in the handlers and channels. The message is not sent, so
// the text reaches neither the user nor the result record, and the cause is
// lost with it. A handler returns the error wrapped with errs.Wrap, maps it to
// a catalog error, or attaches it with WithInternal.
func TestHTTPErrorsCarryNoErrorText(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	for _, dir := range []string{filepath.Join("internal", "handlers"), filepath.Join("internal", "channel")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok || len(call.Args) < 2 {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "NewHTTPError" || identName(sel.X) != "echo" {
					return true
				}
				for _, arg := range call.Args[1:] {
					if isErrorTextCall(arg) {
						t.Errorf("%s: echo.NewHTTPError(%s, %s) puts error text in a message that is never sent; return the error with errs.Wrap, map it to a catalog error, or use WithInternal", fset.Position(call.Pos()), statusName(call.Args[0]), exprString(arg))
					}
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// isErrorTextCall reports whether e is a call of the form x.Error().
func isErrorTextCall(e ast.Expr) bool {
	call, ok := e.(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Error"
}
