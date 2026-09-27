package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// errorCodeWriters are the helpers that put a code into an error response or
// an error stream event. Each takes a parameter named "code".
var errorCodeWriters = map[string]struct{}{
	"newI18nHTTPError": {},
	"sendError":        {},
}

// Every string literal written as a public error code by this package must be
// registered in the apperror catalog, so the client resolves it to errors.*
// copy. The check covers calls to the writer helpers and Code fields of
// error event literals.
func TestLiteralErrorCodesAreCatalogued(t *testing.T) {
	t.Parallel()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var files []*ast.File
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		src, err := os.ReadFile(path) //nolint:gosec // package-local source file
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		file, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files = append(files, file)
	}
	// Package-level helpers are visible from every file; closures and
	// function-typed parameters only within the file that declares them.
	packageIndex := make(map[string]int)
	for _, file := range files {
		for name, index := range codeParamIndexes(file, true) {
			packageIndex[name] = index
		}
	}
	for _, file := range files {
		codeIndex := make(map[string]int, len(packageIndex))
		for name, index := range packageIndex {
			codeIndex[name] = index
		}
		for name, index := range codeParamIndexes(file, false) {
			codeIndex[name] = index
		}
		check := func(expr ast.Expr) {
			lit, ok := expr.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return
			}
			code, err := strconv.Unquote(lit.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", fset.Position(lit.Pos()), lit.Value, err)
			}
			if _, ok := apperror.Lookup(apperror.Code(code)); !ok {
				t.Errorf("%s: error code %q is not in the apperror catalog", fset.Position(lit.Pos()), code)
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch n := node.(type) {
			case *ast.CallExpr:
				ident, ok := n.Fun.(*ast.Ident)
				if !ok {
					return true
				}
				if index, ok := codeIndex[ident.Name]; ok && index < len(n.Args) {
					check(n.Args[index])
				}
			case *ast.CompositeLit:
				if !isErrorType(n.Type) {
					return true
				}
				for _, elt := range n.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					if key, ok := kv.Key.(*ast.Ident); ok && key.Name == "Code" {
						check(kv.Value)
					}
				}
			}
			return true
		})
	}
}

// codeParamIndexes maps each writer helper declared in file to the position of
// its "code" parameter. With topLevel it reports function declarations;
// otherwise closure variables and function-typed parameters.
func codeParamIndexes(file *ast.File, topLevel bool) map[string]int {
	out := make(map[string]int)
	record := func(name string, fn *ast.FuncType) {
		if _, ok := errorCodeWriters[name]; !ok || fn == nil || fn.Params == nil {
			return
		}
		index := 0
		for _, field := range fn.Params.List {
			names := field.Names
			if len(names) == 0 {
				index++
				continue
			}
			for _, param := range names {
				if param.Name == "code" {
					out[name] = index
					return
				}
				index++
			}
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.FuncDecl:
			if topLevel {
				record(n.Name.Name, n.Type)
			}
		case *ast.AssignStmt:
			if topLevel {
				return true
			}
			for i, lhs := range n.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || i >= len(n.Rhs) {
					continue
				}
				if lit, ok := n.Rhs[i].(*ast.FuncLit); ok {
					record(ident.Name, lit.Type)
				}
			}
		case *ast.Field:
			if fn, ok := n.Type.(*ast.FuncType); ok && !topLevel {
				for _, name := range n.Names {
					record(name.Name, fn)
				}
			}
		}
		return true
	})
	return out
}

func isErrorType(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && strings.Contains(ident.Name, "Error")
}
