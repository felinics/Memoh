package email

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

func TestPublicEmailForbidsPrivateAliasFacade(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	files := parsePackageFiles(t, dir, 0, func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	})

	const privateImport = "github.com/memohai/memoh/domains/channel/internal/email"
	for filename, f := range files {
		base := filepath.Base(filename)
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, privateImport) && base != "constructors.go" {
				t.Fatalf("%s imports private email (%s); only constructors.go may", base, path)
			}
		}
		ast.Inspect(f, func(n ast.Node) bool {
			if node, ok := n.(*ast.TypeSpec); ok && node.Assign.IsValid() {
				t.Fatalf("%s: forbidden type alias %q", base, node.Name.Name)
			}
			return true
		})
	}

	servicePath := filepath.Join(dir, "service.go")
	data, err := os.ReadFile(servicePath) //nolint:gosec
	if err != nil {
		t.Fatalf("read service.go: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "type Service struct") {
		t.Fatal("public email must define type Service struct")
	}
	if !strings.Contains(text, "func NewService(") {
		t.Fatal("public email must define NewService")
	}
	if strings.Contains(text, "internal/email") {
		t.Fatal("service.go must not reference retired internal/email")
	}
}

func TestPrivateEmailDoesNotImportPublic(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	privateRoot := filepath.Join(filepath.Dir(file), "..", "internal", "email")
	const publicImport = "github.com/memohai/memoh/domains/channel/email"
	err := filepath.WalkDir(privateRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == publicImport {
				t.Errorf("%s must not import public email package", filepath.Base(path))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk private email: %v", err)
	}
}

func TestPublicEmailHasNoProjectRootInternalImports(t *testing.T) {
	t.Parallel()

	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(file)
	files := parsePackageFiles(t, dir, parser.ImportsOnly, func(name string) bool {
		return strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go")
	})
	const rootInternal = "github.com/memohai/memoh/internal/"
	for filename, f := range files {
		for _, imp := range f.Imports {
			path := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(path, rootInternal) {
				t.Errorf("%s imports %s: public email must not import project-root internal", filepath.Base(filename), path)
			}
		}
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
