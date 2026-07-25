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

const internalModelsPath = modulePrefix + "internal/models"

// Migrated model vocabulary that must live only in domains/model.
var modelVocabularyIdents = []string{
	"ModelType",
	"ModelTypeChat",
	"ModelTypeEmbedding",
	"ModelTypeSpeech",
	"ModelTypeTranscription",
	"ModelTypeVideo",
	"ClientType",
	"ClientTypeOpenAIResponses",
	"ClientTypeOpenAICompletions",
	"ClientTypeAnthropicMessages",
	"ClientTypeGoogleGenerativeAI",
	"ClientTypeOpenAICodex",
	"ClientTypeGitHubCopilot",
	"ClientTypeEdgeSpeech",
	"ClientTypeOpenAISpeech",
	"ClientTypeOpenAITranscription",
	"ClientTypeOpenRouterSpeech",
	"ClientTypeOpenRouterTranscription",
	"ClientTypeElevenLabsSpeech",
	"ClientTypeElevenLabsTranscription",
	"ClientTypeDeepgramSpeech",
	"ClientTypeDeepgramTranscription",
	"ClientTypeMiniMaxSpeech",
	"ClientTypeVolcengineSpeech",
	"ClientTypeAlibabaSpeech",
	"ClientTypeMicrosoftSpeech",
	"ClientTypeGoogleSpeech",
	"ClientTypeGoogleTranscription",
	"ClientTypeOpenRouterVideo",
	"ClientTypeModelArkVideo",
	"ClientTypeVolcengineVideo",
	"CompatVision",
	"CompatToolCall",
	"CompatImageOutput",
	"CompatReasoning",
	"ReasoningEffortNone",
	"ReasoningEffortMinimal",
	"ReasoningEffortLow",
	"ReasoningEffortMedium",
	"ReasoningEffortHigh",
	"ReasoningEffortXHigh",
	"ReasoningEffortMax",
	"ThinkingModeAdaptive",
	"ThinkingModeToggle",
	"ThinkingModeOnlyAdaptive",
	"ThinkingModeNone",
	"ModelConfig",
	"Model",
	"IsValidReasoningEffort",
	"IsValidClientType",
	"IsValidModelType",
	"IsLLMClientType",
}

var modelVocabularyIdentSet = func() map[string]struct{} {
	out := make(map[string]struct{}, len(modelVocabularyIdents))
	for _, name := range modelVocabularyIdents {
		out[name] = struct{}{}
	}
	return out
}()

var allowedModelDomainMethods = map[string]struct{}{
	"Model.Validate":            {},
	"Model.HasCompatibility":    {},
	"Model.SupportsReasoning":   {},
	"Model.ResolveThinkingMode": {},
}

func TestDomainsModelRootHasNoInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/model") {
		if path.Dir(file) != "domains/model" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/model root must not import internal packages", file, imp)
			}
		}
	}
}

func TestInternalModelsDoesNotRedeclareModelVocabulary(t *testing.T) {
	root := repoRoot(t)
	if _, err := os.Stat(filepath.Join(root, "internal/models")); err == nil {
		t.Fatalf("internal/models must be deleted after catalog/execution cutover")
	} else if !os.IsNotExist(err) {
		t.Fatalf("stat internal/models: %v", err)
	}
}

func TestProductionDoesNotSelectModelVocabularyViaInternalModels(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var violations []string
	for _, file := range goFiles(t, root, ".") {
		if strings.HasPrefix(file, "internal/arch/") ||
			strings.HasPrefix(file, "internal/models/") ||
			strings.HasPrefix(file, "spec/") {
			continue
		}
		srcPath := filepath.Join(root, file)
		parsed, err := parser.ParseFile(fset, srcPath, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports %s: %v", file, err)
		}
		modelsAlias := importAlias(parsed, internalModelsPath)
		if modelsAlias == "" {
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
			if !ok || ident.Name != modelsAlias {
				return true
			}
			if _, bad := modelVocabularyIdentSet[sel.Sel.Name]; bad {
				violations = append(violations, fmt.Sprintf("%s uses %s.%s; import domains/model as modeldomain", file, modelsAlias, sel.Sel.Name))
			}
			return true
		})
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("production must not reach migrated vocabulary through internal/models:\n  %s", strings.Join(violations, "\n  "))
	}
}

func TestDomainsModelPublicSurfaceIsVocabularyOnly(t *testing.T) {
	root := repoRoot(t)
	fset := token.NewFileSet()
	var violations []string
	for _, file := range goFiles(t, root, "domains/model") {
		if path.Dir(file) != "domains/model" {
			continue
		}
		parsed, err := parser.ParseFile(fset, filepath.Join(root, file), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		for _, decl := range parsed.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					switch s := spec.(type) {
					case *ast.TypeSpec:
						if ast.IsExported(s.Name.Name) {
							if _, allowed := modelVocabularyIdentSet[s.Name.Name]; !allowed {
								violations = append(violations, fmt.Sprintf("%s exports unexpected type %s", file, s.Name.Name))
							}
						}
					case *ast.ValueSpec:
						for _, name := range s.Names {
							if ast.IsExported(name.Name) {
								if _, allowed := modelVocabularyIdentSet[name.Name]; !allowed {
									violations = append(violations, fmt.Sprintf("%s exports unexpected value %s", file, name.Name))
								}
							}
						}
					}
				}
			case *ast.FuncDecl:
				if !ast.IsExported(d.Name.Name) {
					continue
				}
				if d.Recv == nil {
					if _, allowed := modelVocabularyIdentSet[d.Name.Name]; !allowed {
						violations = append(violations, fmt.Sprintf("%s exports unexpected func %s", file, d.Name.Name))
					}
					continue
				}
				method := receiverTypeName(d) + "." + d.Name.Name
				if _, allowed := allowedModelDomainMethods[method]; !allowed {
					violations = append(violations, fmt.Sprintf("%s exports unexpected method %s", file, method))
				}
			}
		}
	}
	if len(violations) > 0 {
		slices.Sort(violations)
		t.Fatalf("domains/model public surface must match the vocabulary allowlist:\n  %s", strings.Join(violations, "\n  "))
	}
}

func receiverTypeName(fn *ast.FuncDecl) string {
	if fn.Recv == nil || len(fn.Recv.List) != 1 {
		return ""
	}
	receiver := fn.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	ident, _ := receiver.(*ast.Ident)
	if ident == nil {
		return ""
	}
	return ident.Name
}

func importAlias(file *ast.File, importPath string) string {
	for _, imp := range file.Imports {
		if strings.Trim(imp.Path.Value, `"`) != importPath {
			continue
		}
		if imp.Name != nil {
			if imp.Name.Name == "_" || imp.Name.Name == "." {
				return ""
			}
			return imp.Name.Name
		}
		parts := strings.Split(importPath, "/")
		return parts[len(parts)-1]
	}
	return ""
}
