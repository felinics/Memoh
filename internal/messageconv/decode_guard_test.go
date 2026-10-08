package messageconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Persisted rows (bot_history_messages, discuss rows, fork snapshots) hold the
// stored content shape, which differs from the SDK's JSON: arguments are
// objects, outputs are values, and Memoh annotations are nested objects under
// providerMetadata. Decoding a row with encoding/json into sdk.Message drops
// or fails on all of that, so every reader goes through this package or the
// history codec. The files listed here decode stream envelopes or in-memory
// snapshots that are SDK JSON by construction.
var sdkMessageDecodeAllowlist = map[string]string{
	"internal/agent/application/service_stream.go":   "stream envelope Messages",
	"internal/agent/application/turn_discuss.go":     "terminal stream event Messages",
	"internal/agent/runtime/native/spawn_adapter.go": "terminal stream event Messages",
	"internal/agent/tool/types.go":                   "in-memory context snapshot",
	"internal/messageconv/messageconv.go":            "the codec itself",
}

func TestNoDirectSDKMessageDecodeOutsideCodec(t *testing.T) {
	t.Parallel()

	root := filepath.Join("..", "..")
	var violations []string
	err := filepath.WalkDir(filepath.Join(root, "internal"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if _, allowed := sdkMessageDecodeAllowlist[rel]; allowed {
			return nil
		}
		for _, line := range directSDKMessageDecodes(t, path) {
			violations = append(violations, rel+":"+line)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("persisted rows must be typed through messageconv or the history codec, found direct sdk.Message decodes:\n%s", strings.Join(violations, "\n"))
	}
}

// directSDKMessageDecodes reports decodes whose target holds sdk.Message
// values: json.Unmarshal(_, &x) and json.NewDecoder(_).Decode(&x) (or a
// pointer-typed x passed as is) where x was declared in the function with a
// type that mentions sdk.Message anywhere (the value itself, a slice, a map
// value, a pointer, a struct field), as a parameter, a var, a composite
// literal, make or new.
func directSDKMessageDecodes(t *testing.T, path string) []string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	sdkAlias := ""
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) == "github.com/felinics/twilight/sdk" {
			sdkAlias = "sdk"
			if imp.Name != nil {
				sdkAlias = imp.Name.Name
			}
		}
	}
	if sdkAlias == "" {
		return nil
	}
	var found []string
	ast.Inspect(file, func(n ast.Node) bool {
		fn, ok := n.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			return true
		}
		targets := map[string]bool{}
		if fn.Type.Params != nil {
			for _, field := range fn.Type.Params.List {
				if typeMentionsSDKMessage(field.Type, sdkAlias) {
					for _, name := range field.Names {
						targets[name.Name] = true
					}
				}
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			switch decl := n.(type) {
			case *ast.ValueSpec:
				if typeMentionsSDKMessage(decl.Type, sdkAlias) {
					for _, name := range decl.Names {
						targets[name.Name] = true
					}
				}
			case *ast.AssignStmt:
				for i, rhs := range decl.Rhs {
					if i >= len(decl.Lhs) {
						break
					}
					ident, ok := decl.Lhs[i].(*ast.Ident)
					if !ok {
						continue
					}
					switch value := rhs.(type) {
					case *ast.CompositeLit:
						if typeMentionsSDKMessage(value.Type, sdkAlias) {
							targets[ident.Name] = true
						}
					case *ast.UnaryExpr:
						if lit, ok := value.X.(*ast.CompositeLit); ok && value.Op == token.AND && typeMentionsSDKMessage(lit.Type, sdkAlias) {
							targets[ident.Name] = true
						}
					case *ast.CallExpr:
						if fun, ok := value.Fun.(*ast.Ident); ok && (fun.Name == "make" || fun.Name == "new") && len(value.Args) > 0 && typeMentionsSDKMessage(value.Args[0], sdkAlias) {
							targets[ident.Name] = true
						}
					}
				}
			}
			return true
		})
		if len(targets) == 0 {
			return false
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			var target ast.Expr
			switch {
			case sel.Sel.Name == "Unmarshal" && len(call.Args) == 2 && isJSONPackage(sel.X):
				target = call.Args[1]
			case sel.Sel.Name == "Decode" && len(call.Args) == 1 && isJSONDecoder(sel.X):
				target = call.Args[0]
			default:
				return true
			}
			if unary, ok := target.(*ast.UnaryExpr); ok && unary.Op == token.AND {
				target = unary.X
			}
			// &env.Messages or &byID["k"] decodes into the declared variable
			// all the same; follow the chain back to it.
			for {
				switch chain := target.(type) {
				case *ast.SelectorExpr:
					target = chain.X
					continue
				case *ast.IndexExpr:
					target = chain.X
					continue
				}
				break
			}
			if ident, ok := target.(*ast.Ident); ok && targets[ident.Name] {
				pos := fset.Position(call.Pos())
				found = append(found, fmt.Sprintf("%d %s", pos.Line, ident.Name))
			}
			return true
		})
		return false
	})
	return found
}

func isJSONPackage(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && ident.Name == "json"
}

// isJSONDecoder recognises json.NewDecoder(r).Decode(x) written inline; a
// decoder held in a variable is out of reach for this syntactic check.
func isJSONDecoder(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "NewDecoder" && isJSONPackage(sel.X)
}

// typeMentionsSDKMessage reports whether sdk.Message appears anywhere in the
// type expression: the value itself, an element, a map value, a pointer or a
// struct field.
func typeMentionsSDKMessage(expr ast.Expr, sdkAlias string) bool {
	if expr == nil {
		return false
	}
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		if found {
			return false
		}
		if sel, ok := n.(*ast.SelectorExpr); ok {
			if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == sdkAlias && sel.Sel.Name == "Message" {
				found = true
				return false
			}
		}
		return true
	})
	return found
}

// The guard recognises the decode shapes a reader could plausibly write; each
// snippet below must be caught, and the last group must not be.
func TestDecodeGuardRecognisesDecodeShapes(t *testing.T) {
	t.Parallel()
	caught := []string{
		"func f(raw []byte) { var m sdk.Message; _ = json.Unmarshal(raw, &m) }",
		"func f(raw []byte) { var ms []sdk.Message; _ = json.Unmarshal(raw, &ms) }",
		"func f(raw []byte) { var env struct{ Messages []sdk.Message }; _ = json.Unmarshal(raw, &env) }",
		"func f(raw []byte) { m := new(sdk.Message); _ = json.Unmarshal(raw, m) }",
		"func f(raw []byte) { ms := make([]sdk.Message, 0); _ = json.Unmarshal(raw, &ms) }",
		"func f(raw []byte) { byID := map[string]sdk.Message{}; _ = json.Unmarshal(raw, &byID) }",
		"func f(raw []byte, m *sdk.Message) { _ = json.Unmarshal(raw, m) }",
		"func f(r io.Reader) { var m sdk.Message; _ = json.NewDecoder(r).Decode(&m) }",
		"func f(raw []byte) { env := &struct{ Messages []sdk.Message }{}; _ = json.Unmarshal(raw, env) }",
		"func f(raw []byte) { var env struct{ Messages []sdk.Message }; _ = json.Unmarshal(raw, &env.Messages) }",
		"func f(raw []byte) { byID := map[string]sdk.Message{}; m := byID[\"k\"]; _ = m; _ = json.Unmarshal(raw, &byID) }",
	}
	clean := []string{
		"func f(raw []byte) { var v map[string]any; _ = json.Unmarshal(raw, &v) }",
		"func f(raw []byte) { var parts []sdk.MessagePart; _ = json.Unmarshal(raw, &parts) }",
	}
	header := "package probe\n\nimport (\n\t\"encoding/json\"\n\t\"io\"\n\n\tsdk \"github.com/felinics/twilight/sdk\"\n)\n\nvar _ = io.EOF\n\n"
	dir := t.TempDir()
	for i, snippet := range caught {
		path := filepath.Join(dir, fmt.Sprintf("caught_%d.go", i))
		if err := os.WriteFile(path, []byte(header+snippet+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := directSDKMessageDecodes(t, path); len(got) != 1 {
			t.Errorf("snippet %d not caught: %s\n%v", i, snippet, got)
		}
	}
	for i, snippet := range clean {
		path := filepath.Join(dir, fmt.Sprintf("clean_%d.go", i))
		if err := os.WriteFile(path, []byte(header+snippet+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := directSDKMessageDecodes(t, path); len(got) != 0 {
			t.Errorf("clean snippet %d flagged: %s\n%v", i, snippet, got)
		}
	}
}
