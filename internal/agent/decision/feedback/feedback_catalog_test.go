package feedback_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/apperror"
)

// Feedback codes reach clients as public error codes, so each one must be
// registered in the apperror catalog under the same value. The constants are
// read from source so a newly added code cannot skip the check.
func TestFeedbackCodesAreCatalogued(t *testing.T) {
	t.Parallel()
	codes := stringConstants(t, "feedback.go", "Code")
	if len(codes) == 0 {
		t.Fatal("no Code* constants found in feedback.go")
	}
	for name, code := range codes {
		if _, ok := apperror.Lookup(apperror.Code(code)); !ok {
			t.Errorf("%s = %q is not in the apperror catalog", name, code)
		}
	}
}

func stringConstants(t *testing.T, path, prefix string) map[string]string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	out := make(map[string]string)
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range value.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(value.Values) {
					continue
				}
				lit, ok := value.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					continue
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				out[name.Name] = text
			}
		}
	}
	return out
}
