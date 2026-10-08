package apperror_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

// fieldNamers are the calls that name a request field on the wire. The name
// is part of the public contract, so it is written where the field is read:
// a literal, never a value computed from Go identifiers or error text.
var fieldNamers = map[string]map[string]bool{
	"apperror": {"FieldRequired": true, "FieldInvalid": true},
	"httpx":    {"RequiredParam": true, "RequiredQuery": true},
}

// fieldNameArg is the position of the field name in each call.
var fieldNameArg = map[string]int{
	"FieldRequired": 0, "FieldInvalid": 0, "RequiredParam": 1, "RequiredQuery": 1,
}

// fieldNamingExempt is where the field name comes from the request itself:
// the binder names the key a JSON type error was found in, and the required
// helpers pass on the name their callers wrote.
var fieldNamingExempt = map[string]bool{
	"internal/httpx/request_fields.go": true,
}

// TestRequestFieldNamesAreLiterals keeps every field a response names spelled
// as the request spells it, and keeps error text out of public args.
func TestRequestFieldNamesAreLiterals(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	for _, dir := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok {
					return true
				}
				if fieldNamers[pkg.Name][sel.Sel.Name] && !fieldNamingExempt[rel] {
					i := fieldNameArg[sel.Sel.Name]
					if i >= len(call.Args) {
						return true
					}
					if lit, ok := call.Args[i].(*ast.BasicLit); !ok || lit.Kind != token.STRING {
						t.Errorf("%s: %s.%s names its field with %s; write the field as a string literal", fset.Position(call.Pos()), pkg.Name, sel.Sel.Name, exprString(call.Args[i]))
					}
				}
				if pkg.Name == "apperror" && (sel.Sel.Name == "New" || sel.Sel.Name == "Wrap") {
					for _, arg := range call.Args {
						checkArgValues(t, fset, arg)
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

// checkArgValues reports a public arg whose value is error text.
func checkArgValues(t *testing.T, fset *token.FileSet, arg ast.Expr) {
	t.Helper()
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return
	}
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			continue
		}
		call, ok := kv.Value.(*ast.CallExpr)
		if !ok {
			continue
		}
		if sel, ok := call.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Error" && len(call.Args) == 0 {
			t.Errorf("%s: public arg %s is error text", fset.Position(kv.Pos()), exprString(kv.Key))
		}
	}
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.BasicLit:
		return v.Value
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	default:
		return "an expression"
	}
}
