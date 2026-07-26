package apperror

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// These gates live beside the package they guard rather than in a shared
// architecture-test package: a gate that another team's refactor can delete by
// moving a directory is not a gate.

// scannedRoots covers every tree that can serve a response.
var scannedRoots = []string{"domains", "internal", "cmd"}

// envelopeStatuses are the refusals about the request envelope — its method,
// size, media type and protocol — rather than about what the request asked for.
// gRPC has no equivalent codes, which is precisely why Kind does not model
// them: they are HTTP framing concerns and they stay in HTTP's own vocabulary.
//
// A handler may raise one with a bare echo.NewHTTPError(status). The global
// handler still renders it as a Problem and carries the status through
// unchanged, so this is an exemption from Kind, not from the wire format.
var envelopeStatuses = map[string]struct{}{
	"StatusMethodNotAllowed":      {},
	"StatusRequestEntityTooLarge": {},
	"StatusUnsupportedMediaType":  {},
	"StatusUpgradeRequired":       {},
}
// TestHandlersDoNotWriteTheirOwnErrorText is the contract's load-bearing gate.
//
// It does not ban echo.NewHTTPError outright, because for the four envelope
// statuses above there is nothing better to say. It bans supplying a message,
// which is the actual defect: a hand-written string is unlocalizable, is not
// machine-readable, and — in the err.Error() form this codebase used about a
// thousand times — puts driver output, file paths and query text in front of
// whoever made the request.
func TestHandlersDoNotWriteTheirOwnErrorText(t *testing.T) {
	var violations []string
	walkRepoCalls(t, func(call repoCall) {
		if call.name != "NewHTTPError" {
			return
		}
		if len(call.expr.Args) == 1 && isEnvelopeStatus(call.expr.Args[0]) {
			return
		}
		violations = append(violations, fmt.Sprintf(
			"%s:%d: build this error with internal/apperror, not echo.NewHTTPError",
			call.file, call.position.Line))
	})

	sort.Strings(violations)
	for _, violation := range violations {
		t.Error(violation)
	}
}

func isEnvelopeStatus(arg ast.Expr) bool {
	selector, ok := arg.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := selector.X.(*ast.Ident)
	if !ok || pkg.Name != "http" {
		return false
	}
	_, exempt := envelopeStatuses[selector.Sel.Name]
	return exempt
}

// kindConstructors mirrors the twelve constructors whose first argument is op.
var kindConstructors = map[string]struct{}{
	"Internal":           {},
	"Invalid":            {},
	"Unauthenticated":    {},
	"Forbidden":          {},
	"NotFound":           {},
	"Conflict":           {},
	"FailedPrecondition": {},
	"Exhausted":          {},
	"Canceled":           {},
	"DeadlineExceeded":   {},
	"Unavailable":        {},
	"Unimplemented":      {},
}

// TestConstructorsAlwaysCarryAnOp keeps op from decaying into the empty string.
// InfluxDB shipped the same field as an optional struct member and ended up
// filling it about 17% of the time; here it is a required argument, so the only
// way to skip it is to pass "" and this is what rejects that.
func TestConstructorsAlwaysCarryAnOp(t *testing.T) {
	// OfKind resolves the Kind at runtime, so its op is the second argument.
	opArgIndex := map[string]int{"OfKind": 1}

	var violations []string
	walkRepoCalls(t, func(call repoCall) {
		if !call.throughApperror {
			return
		}
		index, tracked := opArgIndex[call.name]
		if !tracked {
			if _, isKind := kindConstructors[call.name]; !isKind {
				return
			}
		}
		if index >= len(call.expr.Args) {
			return
		}
		if literal, ok := call.expr.Args[index].(*ast.BasicLit); ok && literal.Value == `""` {
			violations = append(violations, fmt.Sprintf("%s:%d: apperror.%s has an empty op", call.file, call.position.Line, call.name))
		}
	})

	sort.Strings(violations)
	for _, violation := range violations {
		t.Error(violation)
	}
}

// repoCall is one call expression found in the repository.
type repoCall struct {
	file string // forward-slash path relative to the repo root
	name string // function name, without any package qualifier
	// throughApperror reports whether the call went through the identifier
	// internal/apperror is imported under in this file, so that a local
	// Internal() is never mistaken for apperror.Internal().
	throughApperror bool
	expr            *ast.CallExpr
	position        token.Position
}

// walkRepoCalls visits every call expression in non-test Go files under the
// scanned roots.
func walkRepoCalls(t *testing.T, visit func(repoCall)) {
	t.Helper()

	root := repoRoot(t)
	fset := token.NewFileSet()

	for _, scanned := range scannedRoots {
		for _, file := range goFiles(t, root, scanned) {
			parsed, err := parser.ParseFile(fset, filepath.Join(root, file), nil, 0)
			if err != nil {
				t.Fatalf("parse %s: %v", file, err)
			}
			alias := apperrorAlias(parsed)

			ast.Inspect(parsed, func(node ast.Node) bool {
				expr, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				found := repoCall{file: file, expr: expr, position: fset.Position(expr.Pos())}
				switch fun := expr.Fun.(type) {
				case *ast.SelectorExpr:
					found.name = fun.Sel.Name
					pkg, isIdent := fun.X.(*ast.Ident)
					found.throughApperror = isIdent && alias != "" && pkg.Name == alias
				case *ast.Ident:
					found.name = fun.Name
				default:
					return true
				}
				visit(found)
				return true
			})
		}
	}
}

func apperrorAlias(file *ast.File) string {
	for _, spec := range file.Imports {
		if strings.Trim(spec.Path.Value, `"`) != "github.com/memohai/memoh/internal/apperror" {
			continue
		}
		if spec.Name != nil {
			return spec.Name.Name
		}
		return "apperror"
	}
	return ""
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// goFiles yields non-test .go files under dir, as forward-slash paths relative
// to the repo root.
func goFiles(t *testing.T, root, dir string) []string {
	t.Helper()

	var files []string
	err := filepath.WalkDir(filepath.Join(root, dir), func(current string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == "node_modules" || entry.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		relative, err := filepath.Rel(root, current)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}
