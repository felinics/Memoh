package webhook

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicWebhookForbidsPrivateAliasFacade(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	files := parsePackageFiles(t, dir, 0, func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	})

	const privateImport = "github.com/memohai/memoh/domains/channel/internal/webhook"
	privateAlias := ""
	for filename, f := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path != privateImport {
				continue
			}
			if filepath.Base(filename) != "constructors.go" {
				t.Fatalf("%s imports private webhook; only constructors.go may", filepath.Base(filename))
			}
			if imp.Name != nil {
				privateAlias = imp.Name.Name
			} else {
				privateAlias = "webhook"
			}
		}
	}
	if privateAlias == "" {
		t.Fatal("constructors.go must import private webhook for the adapter")
	}

	for filename, f := range files {
		base := filepath.Base(filename)
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				if node.Assign.IsValid() {
					t.Fatalf("%s: forbidden type alias %q", base, node.Name.Name)
				}
			case *ast.ValueSpec:
				for i, name := range node.Names {
					if i >= len(node.Values) || node.Values[i] == nil {
						continue
					}
					if isPrivateSelector(node.Values[i], privateAlias) {
						t.Fatalf("%s: forbidden const/var forwarding of private %s via %q", base, privateImport, name.Name)
					}
				}
			case *ast.ReturnStmt:
				if base != "constructors.go" {
					break
				}
				for _, result := range node.Results {
					if call, ok := result.(*ast.CallExpr); ok && isPrivateSelector(call.Fun, privateAlias) {
						t.Fatalf("%s: NewManager must return adapter, not bare private constructor call", base)
					}
				}
			}
			return true
		})
	}
}

func isPrivateSelector(expr ast.Expr, privateAlias string) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return privateAlias != "" && ident.Name == privateAlias
}

// parsePackageFiles replaces the deprecated parser.ParseDir. These guards only
// need the non-test files of one directory keyed by filename, which os.ReadDir
// plus parser.ParseFile gives directly, without pulling go/packages into a
// test that must stay dependency-free.
func parsePackageFiles(t *testing.T, dir string, mode parser.Mode, include func(name string) bool) map[string]*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	files := make(map[string]*ast.File, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !include(entry.Name()) {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		parsed, err := parser.ParseFile(fset, path, nil, mode)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		files[path] = parsed
	}
	if len(files) == 0 {
		t.Fatalf("no Go files parsed under %s", dir)
	}
	return files
}
