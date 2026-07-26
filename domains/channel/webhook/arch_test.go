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
	for filename, f := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if path == privateImport {
				t.Fatalf("%s imports private webhook; concrete construction belongs in webhook/tunnel", filepath.Base(filename))
			}
		}
	}

	for filename, f := range files {
		base := filepath.Base(filename)
		ast.Inspect(f, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.TypeSpec:
				if node.Assign.IsValid() {
					t.Fatalf("%s: forbidden type alias %q", base, node.Name.Name)
				}
			}
			return true
		})
	}
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
