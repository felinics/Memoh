package arch

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const (
	internalWorkspacePath = modulePrefix + "internal/workspace"
	domainsRuntimePath    = modulePrefix + "domains/runtime"
)

// Migrated workspace image contract vocabulary that must live only in domains/runtime.
var runtimeContractVocabularyIdents = []string{
	"WorkspaceContractPath",
	"CurrentWorkspaceContractVersion",
	"WorkspaceToolkitDir",
	"WorkspaceScriptsDir",
	"ErrWorkspaceImageIncompatible",
	"WorkspaceContract",
	"WorkspaceContractPlatform",
	"WorkspaceContractPaths",
	"ValidateWorkspaceContractPayload",
}

var runtimeContractVocabularyIdentSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(runtimeContractVocabularyIdents))
	for _, name := range runtimeContractVocabularyIdents {
		out[name] = struct{}{}
	}
	return out
}()

func TestDomainsRuntimeRootContractDependsOnStdlibOnly(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/runtime") {
		if path.Dir(file) != "domains/runtime" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") ||
				strings.HasPrefix(imp, modulePrefix+"domains/runtime/bridge") ||
				imp == modulePrefix+"internal/workspace" ||
				strings.HasPrefix(imp, modulePrefix+"domains/") {
				t.Errorf("%s imports %s: domains/runtime root contract must depend on stdlib only", file, imp)
			}
		}
	}
}

func TestInternalWorkspaceDoesNotRedeclareRuntimeContractVocabulary(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal/workspace")); os.IsNotExist(err) {
		return
	}
	fset := token.NewFileSet()
	var violations []string
	for _, file := range goFiles(t, root, "internal/workspace") {
		if path.Dir(file) != "internal/workspace" {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(root, file), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		runtimeAlias := importAlias(parsed, domainsRuntimePath)
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if _, ok := runtimeContractVocabularyIdentSet[s.Name.Name]; ok {
							violations = append(violations, fmt.Sprintf("%s redeclares type %s", file, s.Name.Name))
						}
						if s.Assign.IsValid() {
							if ident, ok := s.Type.(*ast.Ident); ok {
								if _, bad := runtimeContractVocabularyIdentSet[ident.Name]; bad {
									violations = append(violations, fmt.Sprintf("%s aliases vocabulary type %s", file, s.Name.Name))
								}
							}
							if sel, ok := s.Type.(*ast.SelectorExpr); ok {
								if ident, ok := sel.X.(*ast.Ident); ok && runtimeAlias != "" && ident.Name == runtimeAlias {
									if _, bad := runtimeContractVocabularyIdentSet[sel.Sel.Name]; bad {
										violations = append(violations, fmt.Sprintf("%s type-aliases %s.%s as %s", file, runtimeAlias, sel.Sel.Name, s.Name.Name))
									}
								}
							}
						}
					case *ast.ValueSpec:
						for i, name := range s.Names {
							if _, ok := runtimeContractVocabularyIdentSet[name.Name]; ok {
								violations = append(violations, fmt.Sprintf("%s redeclares const/var %s", file, name.Name))
								continue
							}
							if runtimeAlias == "" || i >= len(s.Values) {
								continue
							}
							if sel, ok := s.Values[i].(*ast.SelectorExpr); ok {
								if ident, ok := sel.X.(*ast.Ident); ok && ident.Name == runtimeAlias {
									if _, bad := runtimeContractVocabularyIdentSet[sel.Sel.Name]; bad {
										violations = append(violations, fmt.Sprintf("%s aliases %s.%s as %s", file, runtimeAlias, sel.Sel.Name, name.Name))
									}
								}
							}
						}
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil {
					continue
				}
				if _, ok := runtimeContractVocabularyIdentSet[d.Name.Name]; ok {
					violations = append(violations, fmt.Sprintf("%s redeclares func %s", file, d.Name.Name))
				}
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("internal/workspace must not redeclare or alias migrated runtime contract vocabulary:\n  %s", strings.Join(violations, "\n  "))
	}
}

func TestProductionDoesNotSelectRuntimeContractVocabularyViaInternalWorkspace(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var violations []string
	for _, file := range goFiles(t, root, ".") {
		if strings.HasPrefix(file, "internal/arch/") ||
			strings.HasPrefix(file, "internal/workspace/") ||
			strings.HasPrefix(file, "spec/") {
			continue
		}
		srcPath := filepath.Join(root, file)
		parsed, err := parser.ParseFile(fset, srcPath, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports %s: %v", file, err)
		}
		workspaceAlias := importAlias(parsed, internalWorkspacePath)
		if workspaceAlias == "" {
			continue
		}
		parsed, err = parser.ParseFile(fset, srcPath, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok || ident.Name != workspaceAlias {
				return true
			}
			if _, bad := runtimeContractVocabularyIdentSet[sel.Sel.Name]; bad {
				violations = append(violations, fmt.Sprintf("%s uses %s.%s; import domains/runtime as runtimedomain", file, workspaceAlias, sel.Sel.Name))
			}
			return true
		})
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("production must not reach migrated contract vocabulary through internal/workspace:\n  %s", strings.Join(violations, "\n  "))
	}
}
