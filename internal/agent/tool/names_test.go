package tools

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

const (
	sdkImportPath            = "github.com/felinics/twilight/sdk"
	mcpImportPath            = "github.com/felinics/memoh/internal/mcp"
	memoryAdaptersImportPath = "github.com/felinics/memoh/internal/memory/adapters"

	protocolTypeSDKTool           = "sdk.Tool"
	protocolTypeMCPToolDescriptor = "mcp.ToolDescriptor"
	protocolTypeShadow            = "<shadow>"
)

func TestToolNameIsOpaque(t *testing.T) {
	t.Parallel()

	toolNameType := reflect.TypeOf((*ToolName)(nil)).Elem()
	if toolNameType.Kind() != reflect.Struct {
		t.Fatalf("ToolName kind = %s, want opaque struct", toolNameType.Kind())
	}
	stringType := reflect.TypeOf("")
	if stringType.AssignableTo(toolNameType) || stringType.ConvertibleTo(toolNameType) {
		t.Fatal("raw strings must not be assignable or convertible to ToolName")
	}
	toolReadType := reflect.TypeOf(ToolRead())
	if !toolReadType.AssignableTo(toolNameType) {
		t.Fatalf("ToolRead() type %s is not assignable to ToolName", toolReadType)
	}
}

func TestBuiltInToolNamesAreRegisteredAndUnique(t *testing.T) {
	t.Parallel()

	exportedTools := toolValueNames(t)
	registryKeys := internalToolRegistryKeys(t)
	internalTools := internalToolValues(t)

	for name := range internalTools {
		if _, ok := exportedTools[name]; !ok {
			t.Fatalf("internal tool value %s must be re-exported from names.go", name)
		}
		if _, ok := registryKeys[name]; !ok {
			t.Fatalf("internal tool value %s must be listed in internal/toolname all catalog", name)
		}
	}
	for name := range exportedTools {
		if _, ok := internalTools[name]; !ok {
			t.Fatalf("exported tool value %s must come from internal/toolname", name)
		}
		if _, ok := registryKeys[name]; !ok {
			t.Fatalf("tool value %s must be listed in internal/toolname all catalog", name)
		}
	}
	for name := range registryKeys {
		if _, ok := exportedTools[name]; !ok {
			t.Fatalf("internal/toolname all catalog contains %s, but names.go does not export that tool value", name)
		}
	}

	seen := make(map[string]string)
	for name, raw := range internalTools {
		if strings.TrimSpace(raw) == "" {
			t.Fatalf("%s has an empty built-in tool name", name)
		}
		if prev, ok := seen[raw]; ok {
			t.Fatalf("duplicate built-in tool name %q for %s and %s", raw, prev, name)
		}
		seen[raw] = name
		if !IsBuiltInToolName(raw) {
			t.Fatalf("IsBuiltInToolName(%q) = false", raw)
		}
		if lookedUp, ok := lookupBuiltInToolName(raw); !ok || lookedUp.String() != raw {
			t.Fatalf("lookupBuiltInToolName(%q) = %v, %v; want raw %q, true", raw, lookedUp, ok, raw)
		}
	}
	if len(BuiltInToolNames()) != len(registryKeys) {
		t.Fatalf("BuiltInToolNames length = %d, want %d", len(BuiltInToolNames()), len(registryKeys))
	}
	if IsBuiltInToolName("not_a_builtin_tool") {
		t.Fatal("unknown tool reported as built-in")
	}
}

func TestNewAvailableToolsRecognizesOnlyBuiltIns(t *testing.T) {
	t.Parallel()

	available := NewAvailableTools([]sdk.Tool{
		{Name: " " + ToolSend().String() + " "},
		{Name: "unknown_dynamic_tool"},
		{Name: ""},
	})

	if !available.Has(ToolSend()) {
		t.Fatal("NewAvailableTools should recognize trimmed built-in tool names")
	}
	if available.Has(ToolRead()) {
		t.Fatal("NewAvailableTools should ignore tools that were not registered")
	}
}

func TestBuiltInSDKToolNamesUseCentralValues(t *testing.T) {
	t.Parallel()

	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob tools files: %v", err)
	}
	toolValues := toolValueNames(t)
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") || file == "names.go" {
			continue
		}
		src := readGoSource(t, file)
		checkGoFileForSDKToolNames(t, file, src, toolValues)
	}
}

func TestMemoryAdapterMCPToolNamesUseConstant(t *testing.T) {
	t.Parallel()

	files := globGoFiles(t, "../../memory/adapters/*.go", "../../memory/adapters/*/*.go")
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		src := readGoSource(t, file)
		checkGoFileForMCPToolNames(t, file, src)
	}
}

func TestProtocolNameGuardsRejectRawStrings(t *testing.T) {
	t.Parallel()

	expr, err := parser.ParseExpr(`sdk.NewTool[map[string]any]("raw_tool", "desc", fn)`)
	if err != nil {
		t.Fatalf("parse sdk.NewTool raw expression: %v", err)
	}
	call := expr.(*ast.CallExpr)
	if err := checkSDKNewToolNameValue(call, map[string]struct{}{"ToolRead": {}}, nil); err == nil {
		t.Fatal("sdk.NewTool with a raw string name must be rejected")
	}

	expr, err = parser.ParseExpr(`sdk.NewTool[map[string]any](ToolRead().String(), "desc", fn)`)
	if err != nil {
		t.Fatalf("parse sdk.NewTool central value expression: %v", err)
	}
	call = expr.(*ast.CallExpr)
	if err := checkSDKNewToolNameValue(call, map[string]struct{}{"ToolRead": {}}, nil); err != nil {
		t.Fatalf("sdk.NewTool with ToolName.String() should pass: %v", err)
	}

	expr, err = parser.ParseExpr(`[]mcp.ToolDescriptor{{Name: "search_memory"}}`)
	if err != nil {
		t.Fatalf("parse mcp descriptor expression: %v", err)
	}
	lit := expr.(*ast.CompositeLit)
	child := lit.Elts[0].(*ast.CompositeLit)
	if err := firstMCPNameValueError(child, map[string]struct{}{"adapters": {}}); err == nil {
		t.Fatal("mcp.ToolDescriptor with a raw string name must be rejected")
	}

	expr, err = parser.ParseExpr(`sdk.Tool{"raw_tool"}`)
	if err != nil {
		t.Fatalf("parse unkeyed sdk.Tool expression: %v", err)
	}
	if err := sdkToolLiteralNameError(expr.(*ast.CompositeLit), map[string]struct{}{"ToolRead": {}}, nil, nil); err == nil {
		t.Fatal("unkeyed sdk.Tool literal must be rejected")
	}

	expr, err = parser.ParseExpr(`mcp.ToolDescriptor{"search_memory"}`)
	if err != nil {
		t.Fatalf("parse unkeyed mcp.ToolDescriptor expression: %v", err)
	}
	if err := mcpToolDescriptorLiteralNameError(expr.(*ast.CompositeLit), map[string]struct{}{"adapters": {}}); err == nil {
		t.Fatal("unkeyed mcp.ToolDescriptor literal must be rejected")
	}

	src := []byte(`package tools

func f() {
	tool.Name = "raw_tool"
	tool.Name = ToolRead().String()
	desc.Name = "search_memory"
}
`)
	parsed, err := parser.ParseFile(token.NewFileSet(), "assign.go", src, 0)
	if err != nil {
		t.Fatalf("parse raw assignment guard file: %v", err)
	}
	var sawSDKRaw, sawSDKCentral, sawMCPRaw bool
	ast.Inspect(parsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i := range assign.Lhs {
			if i >= len(assign.Rhs) {
				continue
			}
			receiver, ok := nameSelectorReceiver(assign.Lhs[i])
			if !ok {
				continue
			}
			switch receiver {
			case "tool":
				if !sawSDKRaw {
					sawSDKRaw = true
					if err := checkSDKToolNameValue(assign.Rhs[i], map[string]struct{}{"ToolRead": {}}, nil, nil); err == nil {
						t.Fatal(".Name raw string assignment must be rejected by SDK guard")
					}
					continue
				}
				sawSDKCentral = true
				if err := checkSDKToolNameValue(assign.Rhs[i], map[string]struct{}{"ToolRead": {}}, nil, nil); err != nil {
					t.Fatalf(".Name central value assignment should pass SDK guard: %v", err)
				}
			case "desc":
				sawSDKRaw = true
				if err := checkSDKToolNameValue(assign.Rhs[i], map[string]struct{}{"ToolRead": {}}, nil, nil); err == nil {
					t.Fatal(".Name raw string assignment must be rejected by SDK guard")
				}
				sawMCPRaw = true
				if err := checkMCPToolDescriptorNameValue(assign.Rhs[i], map[string]struct{}{"adapters": {}}); err == nil {
					t.Fatal(".Name raw string assignment must be rejected by MCP guard")
				}
			}
		}
		return true
	})
	if !sawSDKRaw || !sawSDKCentral || !sawMCPRaw {
		t.Fatalf("assignment guard test missed cases: sdk raw=%v sdk central=%v mcp raw=%v", sawSDKRaw, sawSDKCentral, sawMCPRaw)
	}

	typedAssignSrc := []byte(`package tools

import (
	"github.com/felinics/twilight/sdk"
	"github.com/felinics/memoh/internal/mcp"
)

func late(nameVar string, h lateHolder) {
	h.Tool.Name = nameVar
}

type lateHolder struct {
	Tool sdk.Tool
}

type holder struct {
	Tool sdk.Tool
	Name string
}

type plainHolder struct {
	Name string
}

func f(nameVar string, t sdk.Tool, descriptors []mcp.ToolDescriptor, h holder) {
	t.Name = nameVar
	for _, descriptor := range descriptors {
		descriptor.Name = nameVar
	}
	h.Tool.Name = nameVar
	h.Name = "not a tool"
}

func indexed(nameVar string, tools []sdk.Tool, descriptors []mcp.ToolDescriptor) {
	tools[0].Name = nameVar
	descriptors[0].Name = nameVar
	madeTools := make([]sdk.Tool, 1)
	madeTools[0].Name = nameVar
	copiedTool := tools[0]
	copiedTool.Name = nameVar
}

func scoped(tool sdk.Tool) {
	tool.Name = nameVar
	{
		tool := plainHolder{}
		tool.Name = "not a tool"
	}
}

func unrelated(tool holder) {
	tool.Name = "not a tool"
}

func unrelatedPlain(tool plainHolder) {
	tool.Name = "not a tool"
}

func unrelatedLocal() {
	tool := plainHolder{}
	tool.Name = "not a tool"
}
`)
	typedParsed, err := parser.ParseFile(token.NewFileSet(), "typed_assign.go", typedAssignSrc, 0)
	if err != nil {
		t.Fatalf("parse typed assignment guard file: %v", err)
	}
	typedIndex := collectProtocolTypeIndex(
		typedParsed,
		importAliases(typedParsed, sdkImportPath, "sdk"),
		importAliases(typedParsed, mcpImportPath, "mcp"),
	)
	var sdkRejected, mcpRejected int
	var unrelatedMatched bool
	ast.Inspect(typedParsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if i >= len(assign.Rhs) || !isNameSelector(lhs) {
				continue
			}
			targetType, ok := typedIndex.nameSelectorTargetType(lhs)
			if !ok {
				continue
			}
			switch targetType {
			case protocolTypeSDKTool:
				sdkRejected++
				if err := checkSDKToolNameValue(assign.Rhs[i], map[string]struct{}{"ToolRead": {}}, nil, nil); err == nil {
					t.Fatal("typed sdk.Tool.Name assignment with a non-central variable must be rejected")
				}
			case protocolTypeMCPToolDescriptor:
				mcpRejected++
				if err := checkMCPToolDescriptorNameValue(assign.Rhs[i], map[string]struct{}{"adapters": {}}); err == nil {
					t.Fatal("typed mcp.ToolDescriptor.Name assignment with a variable must be rejected")
				}
			default:
				unrelatedMatched = true
			}
		}
		return true
	})
	if sdkRejected != 7 || mcpRejected != 2 || unrelatedMatched {
		t.Fatalf("typed assignment guard sdk=%d mcp=%d unrelated=%v, want 7/2/false", sdkRejected, mcpRejected, unrelatedMatched)
	}

	assertToolNameStringExpr(t, `ToolRead().String()`, map[string]struct{}{"ToolRead": {}}, nil)
	assertToolNameStringExpr(t, `func() string {
	ToolRead := func() ToolName { return ToolWrite() }
	return ToolRead().String()
}`, map[string]struct{}{"ToolRead": {}}, func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "shadowed")
	})
	assertToolNameStringExpr(t, `func(ToolRead func() ToolName) string {
	return ToolRead().String()
}`, map[string]struct{}{"ToolRead": {}}, func(err error) bool {
		return err != nil && strings.Contains(err.Error(), "shadowed")
	})

	dynamicSrc := []byte(`package tools

func f() {
	for _, desc := range descriptors {
		_ = sdk.Tool{Name: desc.Name}
	}
	_ = sdk.Tool{Name: desc.Name}
}
`)
	dynamicParsed, err := parser.ParseFile(token.NewFileSet(), "federation.go", dynamicSrc, 0)
	if err != nil {
		t.Fatalf("parse dynamic descriptor guard file: %v", err)
	}
	allowedDynamic := allowedDynamicSDKDescriptorNames("federation.go", dynamicParsed)
	var allowedInside, allowedOutside bool
	ast.Inspect(dynamicParsed, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			sel, ok := kv.Value.(*ast.SelectorExpr)
			if !ok {
				continue
			}
			if selectorIsAllowedDynamicDescriptorName(sel, allowedDynamic) {
				if allowedInside {
					allowedOutside = true
				} else {
					allowedInside = true
				}
			}
		}
		return true
	})
	if !allowedInside || allowedOutside {
		t.Fatalf("dynamic descriptor guard allowedInside=%v allowedOutside=%v, want true/false", allowedInside, allowedOutside)
	}
}

func TestProtocolNameGuardsRecognizeImportAliases(t *testing.T) {
	t.Parallel()

	src := []byte(`package tools

import (
	twilight "github.com/felinics/twilight/sdk"
	mcpgw "github.com/felinics/memoh/internal/mcp"
	memoryadapters "github.com/felinics/memoh/internal/memory/adapters"
)

var _ = []twilight.Tool{{Name: "raw_tool"}}
var _ = twilight.NewTool[map[string]any]("raw_tool", "desc", nil)
var _ = []mcpgw.ToolDescriptor{{Name: "search_memory"}, {Name: memoryadapters.ToolSearchMemory}}
`)
	parsed, err := parser.ParseFile(token.NewFileSet(), "alias.go", src, 0)
	if err != nil {
		t.Fatalf("parse alias file: %v", err)
	}
	sdkAliases := importAliases(parsed, sdkImportPath, "sdk")
	mcpAliases := importAliases(parsed, mcpImportPath, "mcp")
	memoryAdapterAliases := importAliases(parsed, memoryAdaptersImportPath, "adapters")
	if _, ok := sdkAliases["sdk"]; ok {
		t.Fatal("explicit SDK alias should not also register the default sdk name")
	}
	if _, ok := mcpAliases["mcp"]; ok {
		t.Fatal("explicit MCP alias should not also register the default mcp name")
	}
	if _, ok := memoryAdapterAliases["adapters"]; ok {
		t.Fatal("explicit memory adapters alias should not also register the default adapters name")
	}

	var sawSDKToolAlias, sawSDKNewToolAlias, sawMCPDescriptorAlias, sawMemoryAdapterAlias bool
	ast.Inspect(parsed, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			if isSDKToolSlice(lit.Type, sdkAliases) {
				sawSDKToolAlias = true
				child := lit.Elts[0].(*ast.CompositeLit)
				if err := firstSDKNameValueError(child, map[string]struct{}{"ToolRead": {}}); err == nil {
					t.Fatal("sdk.Tool alias with raw string name must be rejected")
				}
			}
			if isMCPToolDescriptorSlice(lit.Type, mcpAliases) {
				sawMCPDescriptorAlias = true
				child := lit.Elts[0].(*ast.CompositeLit)
				if err := firstMCPNameValueError(child, memoryAdapterAliases); err == nil {
					t.Fatal("mcp.ToolDescriptor alias with raw string name must be rejected")
				}
				child = lit.Elts[1].(*ast.CompositeLit)
				if err := firstMCPNameValueError(child, memoryAdapterAliases); err != nil {
					t.Fatalf("mcp.ToolDescriptor should allow aliased ToolSearchMemory: %v", err)
				}
				sawMemoryAdapterAlias = true
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && isSDKNewToolCall(call, sdkAliases) {
			sawSDKNewToolAlias = true
			if err := checkSDKNewToolNameValue(call, map[string]struct{}{"ToolRead": {}}, nil); err == nil {
				t.Fatal("sdk.NewTool alias with raw string name must be rejected")
			}
		}
		return true
	})
	if !sawSDKToolAlias || !sawSDKNewToolAlias || !sawMCPDescriptorAlias || !sawMemoryAdapterAlias {
		t.Fatalf("alias guard missed declarations: sdk.Tool=%v sdk.NewTool=%v mcp.ToolDescriptor=%v memoryAdapter=%v", sawSDKToolAlias, sawSDKNewToolAlias, sawMCPDescriptorAlias, sawMemoryAdapterAlias)
	}
}

func readGoSource(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(file) //nolint:gosec // test reads Go files from fixed repository glob patterns.
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return src
}

func globGoFiles(t *testing.T, patterns ...string) []string {
	t.Helper()
	var files []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob %s: %v", pattern, err)
		}
		files = append(files, matches...)
	}
	return files
}

func toolValueNames(t *testing.T) map[string]struct{} {
	t.Helper()

	parsed := parseGoFile(t, "names.go")
	values := map[string]struct{}{}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "Tool") {
			continue
		}
		if !functionReturnsToolName(fn) || !bodyReturnsToolnameCall(fn.Body, fn.Name.Name) {
			t.Fatalf("%s must return ToolName from toolname.%s()", fn.Name.Name, fn.Name.Name)
		}
		values[fn.Name.Name] = struct{}{}
	}
	return values
}

func internalToolValues(t *testing.T) map[string]string {
	t.Helper()

	parsed := parseGoFile(t, "internal/toolname/toolname.go")
	values := map[string]string{}
	constants := map[string]string{
		"memprovider.ToolSearchMemory": "search_memory",
		"userinput.ToolNameAskUser":    "ask_user",
	}
	for _, decl := range parsed.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil || !strings.HasPrefix(fn.Name.Name, "Tool") {
			continue
		}
		raw, ok := internalToolNameFunctionValue(fn, constants)
		if !ok {
			t.Fatalf("%s must return newName with a string literal or approved external tool-name constant", fn.Name.Name)
		}
		values[fn.Name.Name] = raw
	}
	return values
}

func internalToolRegistryKeys(t *testing.T) map[string]struct{} {
	t.Helper()

	parsed := parseGoFile(t, "internal/toolname/toolname.go")
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			valueSpec, ok := spec.(*ast.ValueSpec)
			if !ok || len(valueSpec.Names) != 1 || valueSpec.Names[0].Name != "all" || len(valueSpec.Values) != 1 {
				continue
			}
			lit, ok := valueSpec.Values[0].(*ast.CompositeLit)
			if !ok {
				t.Fatal("internal/toolname all catalog must be a []Name literal")
			}
			keys := map[string]struct{}{}
			for _, elt := range lit.Elts {
				call, ok := elt.(*ast.CallExpr)
				if !ok || len(call.Args) != 0 {
					t.Fatal("internal/toolname all catalog entries must be Tool*() calls")
				}
				name, ok := call.Fun.(*ast.Ident)
				if !ok || !strings.HasPrefix(name.Name, "Tool") {
					t.Fatalf("internal/toolname all catalog entry at %s must be a Tool*() call", token.NewFileSet().Position(call.Pos()))
				}
				keys[name.Name] = struct{}{}
			}
			return keys
		}
	}
	t.Fatal("internal/toolname all catalog not found")
	return nil
}

func functionReturnsToolName(fn *ast.FuncDecl) bool {
	if fn.Type == nil || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
		return false
	}
	ident, ok := fn.Type.Results.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "ToolName"
}

func bodyReturnsToolnameCall(body *ast.BlockStmt, want string) bool {
	if len(body.List) != 1 {
		return false
	}
	ret, ok := body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 0 {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != want {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	return ok && pkg.Name == "toolname"
}

func internalToolNameFunctionValue(fn *ast.FuncDecl, constants map[string]string) (string, bool) {
	if len(fn.Body.List) != 1 {
		return "", false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return "", false
	}
	call, ok := ret.Results[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return "", false
	}
	name, ok := call.Fun.(*ast.Ident)
	if !ok || name.Name != "newName" {
		return "", false
	}
	return rawStringValue(call.Args[0], constants)
}

func rawStringValue(expr ast.Expr, constants map[string]string) (string, bool) {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		raw, err := strconv.Unquote(value.Value)
		if err != nil {
			return "", false
		}
		return raw, true
	case *ast.SelectorExpr:
		pkg, ok := value.X.(*ast.Ident)
		if !ok {
			return "", false
		}
		raw, ok := constants[pkg.Name+"."+value.Sel.Name]
		return raw, ok
	default:
		return "", false
	}
}

type tokenSpan struct {
	start token.Pos
	end   token.Pos
}

type toolShadowSet map[string][]tokenSpan

func (s toolShadowSet) has(name string, pos token.Pos) bool {
	for _, span := range s[name] {
		if pos >= span.start && pos <= span.end {
			return true
		}
	}
	return false
}

func collectShadowedToolValues(parsed ast.Node, toolValues map[string]struct{}) toolShadowSet {
	scopes := shadowScopeRanges(parsed)
	shadowed := toolShadowSet{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.AssignStmt:
			if node.Tok != token.DEFINE {
				return true
			}
			for _, lhs := range node.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					if _, isTool := toolValues[ident.Name]; isTool {
						shadowed[ident.Name] = append(shadowed[ident.Name], tokenSpan{
							start: ident.Pos(),
							end:   enclosingShadowScopeEnd(ident.Pos(), scopes, parsed.End()),
						})
					}
				}
			}
		case *ast.ValueSpec:
			for _, name := range node.Names {
				if _, isTool := toolValues[name.Name]; isTool {
					shadowed[name.Name] = append(shadowed[name.Name], tokenSpan{
						start: name.Pos(),
						end:   enclosingShadowScopeEnd(name.Pos(), scopes, parsed.End()),
					})
				}
			}
		case *ast.FuncDecl:
			addFieldListShadowSpans(shadowed, toolValues, scopes, parsed.End(), node.Type.Params)
			addFieldListShadowSpans(shadowed, toolValues, scopes, parsed.End(), node.Type.Results)
		case *ast.FuncLit:
			addFieldListShadowSpans(shadowed, toolValues, scopes, parsed.End(), node.Type.Params)
			addFieldListShadowSpans(shadowed, toolValues, scopes, parsed.End(), node.Type.Results)
		case *ast.RangeStmt:
			addIdentShadowSpan(shadowed, toolValues, scopes, parsed.End(), node.Key)
			addIdentShadowSpan(shadowed, toolValues, scopes, parsed.End(), node.Value)
		}
		return true
	})
	return shadowed
}

func addFieldListShadowSpans(shadowed toolShadowSet, toolValues map[string]struct{}, scopes []tokenSpan, fallback token.Pos, fields *ast.FieldList) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		for _, name := range field.Names {
			addIdentShadowSpan(shadowed, toolValues, scopes, fallback, name)
		}
	}
}

func addIdentShadowSpan(shadowed toolShadowSet, toolValues map[string]struct{}, scopes []tokenSpan, fallback token.Pos, expr ast.Expr) {
	ident, ok := expr.(*ast.Ident)
	if !ok || ident.Name == "_" {
		return
	}
	if _, isTool := toolValues[ident.Name]; !isTool {
		return
	}
	shadowed[ident.Name] = append(shadowed[ident.Name], tokenSpan{
		start: ident.Pos(),
		end:   enclosingShadowScopeEnd(ident.Pos(), scopes, fallback),
	})
}

func shadowScopeRanges(parsed ast.Node) []tokenSpan {
	var scopes []tokenSpan
	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.File:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.FuncDecl:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.FuncLit:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.BlockStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.IfStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.ForStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.RangeStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.SwitchStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.TypeSwitchStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.SelectStmt:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.CaseClause:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		case *ast.CommClause:
			scopes = append(scopes, tokenSpan{start: node.Pos(), end: node.End()})
		}
		return true
	})
	return scopes
}

func enclosingShadowScopeEnd(pos token.Pos, scopes []tokenSpan, fallback token.Pos) token.Pos {
	best := tokenSpan{end: fallback}
	for _, scope := range scopes {
		if pos < scope.start || pos > scope.end {
			continue
		}
		if best.start == token.NoPos || scope.end < best.end {
			best = scope
		}
	}
	return best.end
}

func parseGoFile(t *testing.T, file string) *ast.File {
	t.Helper()
	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	return parsed
}

func checkGoFileForSDKToolNames(t *testing.T, file string, src []byte, toolValues map[string]struct{}) {
	t.Helper()

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	sdkAliases := importAliases(parsed, sdkImportPath, "sdk")
	dynamicDescriptorNames := allowedDynamicSDKDescriptorNames(file, parsed)
	shadowedTools := collectShadowedToolValues(parsed, toolValues)
	protocolTypes := collectProtocolTypeIndex(parsed, sdkAliases, nil)
	ast.Inspect(parsed, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			if isSDKToolComposite(lit.Type, sdkAliases) {
				checkSDKToolName(t, fset, file, lit, toolValues, dynamicDescriptorNames, shadowedTools)
			}
			if isSDKToolSlice(lit.Type, sdkAliases) {
				for _, elt := range lit.Elts {
					child, ok := elt.(*ast.CompositeLit)
					if ok && child.Type == nil {
						checkSDKToolName(t, fset, file, child, toolValues, dynamicDescriptorNames, shadowedTools)
					}
				}
			}
		}
		if call, ok := n.(*ast.CallExpr); ok && isSDKNewToolCall(call, sdkAliases) {
			if err := checkSDKNewToolNameValue(call, toolValues, shadowedTools); err != nil {
				t.Fatalf("%s:%d registers sdk.NewTool with invalid name: %v", file, fset.Position(call.Pos()).Line, err)
			}
		}
		if assign, ok := n.(*ast.AssignStmt); ok {
			checkSDKNameAssignments(t, fset, file, assign, protocolTypes, toolValues, dynamicDescriptorNames, shadowedTools)
		}
		return true
	})
}

func allowedDynamicSDKDescriptorNames(file string, parsed *ast.File) map[token.Pos]struct{} {
	switch filepath.Base(file) {
	case "federation.go", "memory.go":
	default:
		return nil
	}
	descLoopSpans := []tokenSpan{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		rangeStmt, ok := n.(*ast.RangeStmt)
		if !ok {
			return true
		}
		value, ok := rangeStmt.Value.(*ast.Ident)
		if !ok || value.Name != "desc" {
			return true
		}
		x, ok := rangeStmt.X.(*ast.Ident)
		if !ok || x.Name != "descriptors" || rangeStmt.Body == nil {
			return true
		}
		descLoopSpans = append(descLoopSpans, tokenSpan{
			start: rangeStmt.Body.Pos(),
			end:   rangeStmt.Body.End(),
		})
		return true
	})
	dirtyDescLoops := map[int]struct{}{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for _, lhs := range assign.Lhs {
			receiver, ok := nameSelectorReceiver(lhs)
			if !ok || receiver != "desc" {
				continue
			}
			if idx, ok := enclosingDescLoopIndex(assign.Pos(), descLoopSpans); ok {
				dirtyDescLoops[idx] = struct{}{}
			}
		}
		return true
	})
	allowed := map[token.Pos]struct{}{}
	ast.Inspect(parsed, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Name" {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != "desc" {
			return true
		}
		idx, ok := enclosingDescLoopIndex(sel.Pos(), descLoopSpans)
		if !ok {
			return true
		}
		if _, dirty := dirtyDescLoops[idx]; dirty {
			return true
		}
		allowed[sel.Pos()] = struct{}{}
		return true
	})
	return allowed
}

func enclosingDescLoopIndex(pos token.Pos, loopSpans []tokenSpan) (int, bool) {
	best := -1
	var bestStart token.Pos
	for idx, span := range loopSpans {
		if pos < span.start || pos > span.end {
			continue
		}
		if best < 0 || span.start > bestStart {
			best = idx
			bestStart = span.start
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}

func selectorIsAllowedDynamicDescriptorName(value *ast.SelectorExpr, allowed map[token.Pos]struct{}) bool {
	if len(allowed) == 0 {
		return false
	}
	_, ok := allowed[value.Pos()]
	return ok
}

func checkGoFileForMCPToolNames(t *testing.T, file string, src []byte) {
	t.Helper()

	fset := token.NewFileSet()
	parsed, err := parser.ParseFile(fset, file, src, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	mcpAliases := importAliases(parsed, mcpImportPath, "mcp")
	memoryAdapterAliases := importAliases(parsed, memoryAdaptersImportPath, "adapters")
	protocolTypes := collectProtocolTypeIndex(parsed, nil, mcpAliases)
	ast.Inspect(parsed, func(n ast.Node) bool {
		if lit, ok := n.(*ast.CompositeLit); ok {
			if isMCPToolDescriptorComposite(lit.Type, mcpAliases) {
				checkMCPToolDescriptorName(t, fset, file, lit, memoryAdapterAliases)
			}
			if isMCPToolDescriptorSlice(lit.Type, mcpAliases) {
				for _, elt := range lit.Elts {
					child, ok := elt.(*ast.CompositeLit)
					if ok && child.Type == nil {
						checkMCPToolDescriptorName(t, fset, file, child, memoryAdapterAliases)
					}
				}
			}
		}
		if assign, ok := n.(*ast.AssignStmt); ok {
			checkMCPNameAssignments(t, fset, file, assign, protocolTypes, memoryAdapterAliases)
		}
		return true
	})
}

func importAliases(file *ast.File, importPath, defaultName string) map[string]struct{} {
	aliases := map[string]struct{}{}
	for _, spec := range file.Imports {
		raw, err := strconv.Unquote(spec.Path.Value)
		if err != nil || raw != importPath {
			continue
		}
		if spec.Name == nil {
			aliases[defaultName] = struct{}{}
			continue
		}
		if spec.Name.Name != "" && spec.Name.Name != "." && spec.Name.Name != "_" {
			aliases[spec.Name.Name] = struct{}{}
		}
	}
	return aliases
}

type protocolTypeIndex struct {
	names  map[string][]protocolNameSpan
	fields map[string]map[string]string
}

type protocolNameSpan struct {
	tokenSpan
	typ string
}

func collectProtocolTypeIndex(parsed *ast.File, sdkAliases, mcpAliases map[string]struct{}) protocolTypeIndex {
	scopes := shadowScopeRanges(parsed)
	index := protocolTypeIndex{
		names:  map[string][]protocolNameSpan{},
		fields: map[string]map[string]string{},
	}
	ast.Inspect(parsed, func(n ast.Node) bool {
		node, ok := n.(*ast.TypeSpec)
		if !ok {
			return true
		}
		fields := protocolFieldTypes(node.Type, sdkAliases, mcpAliases)
		if len(fields) > 0 {
			index.fields[node.Name.Name] = fields
		}
		return true
	})
	ast.Inspect(parsed, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ValueSpec:
			for i, name := range node.Names {
				if typ := protocolDeclaredTypeName(node.Type, index, sdkAliases, mcpAliases); typ != "" {
					index.addName(name.Name, typ, name.Pos(), enclosingShadowScopeEnd(name.Pos(), scopes, parsed.End()))
					continue
				}
				if i < len(node.Values) {
					if typ := protocolValueType(node.Values[i], index, name.Pos(), sdkAliases, mcpAliases); typ != "" {
						index.addName(name.Name, typ, name.Pos(), enclosingShadowScopeEnd(name.Pos(), scopes, parsed.End()))
					}
				}
				index.addShadow(name.Name, name.Pos(), enclosingShadowScopeEnd(name.Pos(), scopes, parsed.End()))
			}
		case *ast.AssignStmt:
			for i, lhs := range node.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok || ident.Name == "_" || i >= len(node.Rhs) {
					continue
				}
				if typ := protocolValueType(node.Rhs[i], index, ident.Pos(), sdkAliases, mcpAliases); typ != "" {
					index.addName(ident.Name, typ, ident.Pos(), enclosingShadowScopeEnd(ident.Pos(), scopes, parsed.End()))
					continue
				}
				if node.Tok == token.DEFINE {
					index.addShadow(ident.Name, ident.Pos(), enclosingShadowScopeEnd(ident.Pos(), scopes, parsed.End()))
				}
			}
		case *ast.FuncDecl:
			index.addFieldListNames(node.Type.Params, scopes, parsed.End(), sdkAliases, mcpAliases)
			index.addFieldListNames(node.Type.Results, scopes, parsed.End(), sdkAliases, mcpAliases)
		case *ast.FuncLit:
			index.addFieldListNames(node.Type.Params, scopes, parsed.End(), sdkAliases, mcpAliases)
			index.addFieldListNames(node.Type.Results, scopes, parsed.End(), sdkAliases, mcpAliases)
		case *ast.RangeStmt:
			value, ok := node.Value.(*ast.Ident)
			if !ok || value.Name == "_" {
				return true
			}
			if typ := protocolSliceElementType(node.X, index, value.Pos(), sdkAliases, mcpAliases); typ != "" {
				index.addName(value.Name, typ, value.Pos(), node.Body.End())
				return true
			}
			index.addShadow(value.Name, value.Pos(), node.Body.End())
		}
		return true
	})
	return index
}

func (i protocolTypeIndex) addName(name, typ string, start, end token.Pos) {
	if name == "" || typ == "" {
		return
	}
	i.names[name] = append(i.names[name], protocolNameSpan{
		tokenSpan: tokenSpan{start: start, end: end},
		typ:       typ,
	})
}

func (i protocolTypeIndex) addShadow(name string, start, end token.Pos) {
	i.addName(name, protocolTypeShadow, start, end)
}

func (i protocolTypeIndex) addFieldListNames(fields *ast.FieldList, scopes []tokenSpan, fallback token.Pos, sdkAliases, mcpAliases map[string]struct{}) {
	if fields == nil {
		return
	}
	for _, field := range fields.List {
		typ := protocolDeclaredTypeName(field.Type, i, sdkAliases, mcpAliases)
		for _, name := range field.Names {
			if typ != "" {
				i.addName(name.Name, typ, name.Pos(), enclosingShadowScopeEnd(name.Pos(), scopes, fallback))
			} else {
				i.addShadow(name.Name, name.Pos(), enclosingShadowScopeEnd(name.Pos(), scopes, fallback))
			}
		}
	}
}

func protocolFieldTypes(expr ast.Expr, sdkAliases, mcpAliases map[string]struct{}) map[string]string {
	st, ok := expr.(*ast.StructType)
	if !ok || st.Fields == nil {
		return nil
	}
	out := map[string]string{}
	for _, field := range st.Fields.List {
		typ := protocolTypeName(field.Type, sdkAliases, mcpAliases)
		if typ == "" {
			continue
		}
		for _, name := range field.Names {
			out[name.Name] = typ
		}
	}
	return out
}

func protocolTypeName(expr ast.Expr, sdkAliases, mcpAliases map[string]struct{}) string {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		pkg, ok := value.X.(*ast.Ident)
		if !ok {
			return ""
		}
		if value.Sel.Name == "Tool" {
			if _, ok := sdkAliases[pkg.Name]; ok {
				return protocolTypeSDKTool
			}
		}
		if value.Sel.Name == "ToolDescriptor" {
			if _, ok := mcpAliases[pkg.Name]; ok {
				return protocolTypeMCPToolDescriptor
			}
		}
	case *ast.StarExpr:
		return protocolTypeName(value.X, sdkAliases, mcpAliases)
	}
	return ""
}

func protocolDeclaredTypeName(expr ast.Expr, index protocolTypeIndex, sdkAliases, mcpAliases map[string]struct{}) string {
	if typ := protocolTypeName(expr, sdkAliases, mcpAliases); typ != "" {
		return typ
	}
	switch value := expr.(type) {
	case *ast.ArrayType:
		if typ := protocolTypeName(value.Elt, sdkAliases, mcpAliases); typ != "" {
			return "[]" + typ
		}
	case *ast.Ident:
		if _, ok := index.fields[value.Name]; ok {
			return value.Name
		}
	case *ast.StarExpr:
		return protocolDeclaredTypeName(value.X, index, sdkAliases, mcpAliases)
	}
	return ""
}

func protocolValueType(expr ast.Expr, index protocolTypeIndex, pos token.Pos, sdkAliases, mcpAliases map[string]struct{}) string {
	switch value := expr.(type) {
	case *ast.CompositeLit:
		return protocolDeclaredTypeName(value.Type, index, sdkAliases, mcpAliases)
	case *ast.UnaryExpr:
		if value.Op == token.AND {
			return protocolValueType(value.X, index, pos, sdkAliases, mcpAliases)
		}
	case *ast.CallExpr:
		if fun, ok := value.Fun.(*ast.Ident); ok && fun.Name == "make" && len(value.Args) > 0 {
			return protocolDeclaredTypeName(value.Args[0], index, sdkAliases, mcpAliases)
		}
	case *ast.Ident, *ast.SelectorExpr, *ast.IndexExpr:
		if typ, ok := index.exprType(value, pos); ok {
			return typ
		}
	}
	return ""
}

func protocolSliceElementType(expr ast.Expr, index protocolTypeIndex, pos token.Pos, sdkAliases, mcpAliases map[string]struct{}) string {
	switch value := expr.(type) {
	case *ast.CompositeLit:
		return protocolArrayElementType(value.Type, sdkAliases, mcpAliases)
	case *ast.CallExpr:
		if fun, ok := value.Fun.(*ast.Ident); ok && fun.Name == "make" && len(value.Args) > 0 {
			return protocolArrayElementType(value.Args[0], sdkAliases, mcpAliases)
		}
	case *ast.Ident:
		nameType, _ := index.nameTypeAt(value.Name, pos)
		if typ := protocolArrayElementTypeFromName(nameType); typ != "" {
			return typ
		}
	}
	return ""
}

func protocolArrayElementType(expr ast.Expr, sdkAliases, mcpAliases map[string]struct{}) string {
	switch value := expr.(type) {
	case *ast.ArrayType:
		return protocolTypeName(value.Elt, sdkAliases, mcpAliases)
	case *ast.StarExpr:
		return protocolArrayElementType(value.X, sdkAliases, mcpAliases)
	}
	return ""
}

func protocolArrayElementTypeFromName(name string) string {
	switch {
	case strings.HasPrefix(name, "[]"):
		return strings.TrimPrefix(name, "[]")
	case strings.HasPrefix(name, "*[]"):
		return strings.TrimPrefix(name, "*[]")
	default:
		return ""
	}
}

func (i protocolTypeIndex) nameSelectorTargetType(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Name" {
		return "", false
	}
	typ, ok := i.exprType(sel.X, sel.Pos())
	if !ok {
		return "", false
	}
	switch typ {
	case protocolTypeSDKTool, protocolTypeMCPToolDescriptor:
		return typ, true
	default:
		return "", false
	}
}

func (i protocolTypeIndex) exprType(expr ast.Expr, pos token.Pos) (string, bool) {
	switch value := expr.(type) {
	case *ast.Ident:
		return i.nameTypeAt(value.Name, pos)
	case *ast.SelectorExpr:
		parentType, ok := i.exprType(value.X, pos)
		if !ok {
			return "", false
		}
		fields := i.fields[parentType]
		typ, ok := fields[value.Sel.Name]
		return typ, ok
	case *ast.IndexExpr:
		parentType, ok := i.exprType(value.X, pos)
		if !ok {
			return "", false
		}
		typ := protocolArrayElementTypeFromName(parentType)
		return typ, typ != ""
	}
	return "", false
}

func (i protocolTypeIndex) nameTypeAt(name string, pos token.Pos) (string, bool) {
	var (
		best    protocolNameSpan
		hasBest bool
	)
	for _, span := range i.names[name] {
		if pos < span.start || pos > span.end {
			continue
		}
		if !hasBest || span.start > best.start {
			best = span
			hasBest = true
		}
	}
	if !hasBest {
		return "", false
	}
	return best.typ, true
}

func isSDKToolSlice(expr ast.Expr, sdkAliases map[string]struct{}) bool {
	array, ok := expr.(*ast.ArrayType)
	return ok && isSDKToolComposite(array.Elt, sdkAliases)
}

func isSDKToolComposite(expr ast.Expr, sdkAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Tool" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = sdkAliases[pkg.Name]
	return ok
}

func isMCPToolDescriptorSlice(expr ast.Expr, mcpAliases map[string]struct{}) bool {
	array, ok := expr.(*ast.ArrayType)
	return ok && isMCPToolDescriptorComposite(array.Elt, mcpAliases)
}

func isMCPToolDescriptorComposite(expr ast.Expr, mcpAliases map[string]struct{}) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "ToolDescriptor" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = mcpAliases[pkg.Name]
	return ok
}

func checkSDKToolName(t *testing.T, fset *token.FileSet, file string, lit *ast.CompositeLit, toolValues map[string]struct{}, dynamicDescriptorNames map[token.Pos]struct{}, shadowedTools toolShadowSet) {
	t.Helper()

	if err := sdkToolLiteralNameError(lit, toolValues, dynamicDescriptorNames, shadowedTools); err != nil {
		t.Fatalf("%s:%d registers sdk.Tool with invalid Name: %v", file, fset.Position(lit.Pos()).Line, err)
	}
}

func sdkToolLiteralNameError(lit *ast.CompositeLit, toolValues map[string]struct{}, dynamicDescriptorNames map[token.Pos]struct{}, shadowedTools toolShadowSet) error {
	sawName := false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return &toolNameError{"sdk.Tool literals must use keyed fields and declare Name with a central ToolName value or dynamic descriptor name"}
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		sawName = true
		if err := checkSDKToolNameValue(kv.Value, toolValues, dynamicDescriptorNames, shadowedTools); err != nil {
			return err
		}
	}
	if !sawName {
		return &toolNameError{"sdk.Tool literal must declare Name with a central ToolName value or dynamic descriptor name"}
	}
	return nil
}

func checkSDKNameAssignments(t *testing.T, fset *token.FileSet, file string, assign *ast.AssignStmt, protocolTypes protocolTypeIndex, toolValues map[string]struct{}, dynamicDescriptorNames map[token.Pos]struct{}, shadowedTools toolShadowSet) {
	t.Helper()
	checkNameAssignments(t, fset, file, assign, protocolTypes, protocolTypeSDKTool, func(expr ast.Expr) error {
		return checkSDKToolNameValue(expr, toolValues, dynamicDescriptorNames, shadowedTools)
	})
}

func checkSDKToolNameValue(expr ast.Expr, toolValues map[string]struct{}, dynamicDescriptorNames map[token.Pos]struct{}, shadowedTools toolShadowSet) error {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			raw, _ := strconv.Unquote(value.Value)
			return &toolNameError{"string literal " + strconv.Quote(raw) + "; use a central ToolName value or dynamic descriptor name"}
		}
	case *ast.CallExpr:
		sel, ok := value.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "String" {
			return &toolNameError{"call expression is not ToolName.String()"}
		}
		call, ok := sel.X.(*ast.CallExpr)
		if !ok || len(call.Args) != 0 {
			return &toolNameError{"ToolName.String() receiver must be a central Tool*() call"}
		}
		ident, ok := call.Fun.(*ast.Ident)
		if !ok {
			return &toolNameError{"ToolName.String() receiver must be a central Tool*() call"}
		}
		if shadowedTools.has(ident.Name, ident.Pos()) {
			return &toolNameError{ident.Name + " is shadowed; use the package-level ToolName accessor"}
		}
		if _, ok := toolValues[ident.Name]; !ok {
			return &toolNameError{ident.Name + "().String() is not declared in names.go and listed in the built-in tool catalog"}
		}
		return nil
	case *ast.SelectorExpr:
		if selectorIsAllowedDynamicDescriptorName(value, dynamicDescriptorNames) {
			return nil
		}
		return &toolNameError{"selector expression is not an allowed dynamic descriptor name"}
	}
	return &toolNameError{"use a central ToolName value or dynamic descriptor name"}
}

func assertToolNameStringExpr(t *testing.T, source string, toolValues map[string]struct{}, wantErr func(error) bool) {
	t.Helper()
	expr, err := parser.ParseExpr(source)
	if err != nil {
		t.Fatalf("parse ToolName.String expression: %v", err)
	}
	shadowedTools := collectShadowedToolValues(expr, toolValues)
	var saw bool
	ast.Inspect(expr, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "String" {
			return true
		}
		saw = true
		err := checkSDKToolNameValue(call, toolValues, nil, shadowedTools)
		if wantErr == nil {
			if err != nil {
				t.Fatalf("ToolName.String expression should pass: %v", err)
			}
		} else if !wantErr(err) {
			t.Fatalf("ToolName.String expression error = %v, did not match expectation", err)
		}
		return true
	})
	if !saw {
		t.Fatalf("source did not contain a ToolName.String call: %s", source)
	}
}

func checkMCPToolDescriptorName(t *testing.T, fset *token.FileSet, file string, lit *ast.CompositeLit, memoryAdapterAliases map[string]struct{}) {
	t.Helper()

	if err := mcpToolDescriptorLiteralNameError(lit, memoryAdapterAliases); err != nil {
		t.Fatalf("%s:%d registers mcp.ToolDescriptor with invalid Name: %v", file, fset.Position(lit.Pos()).Line, err)
	}
}

func mcpToolDescriptorLiteralNameError(lit *ast.CompositeLit, memoryAdapterAliases map[string]struct{}) error {
	sawName := false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return &toolNameError{"mcp.ToolDescriptor literals must use keyed fields and declare Name with an allowed tool-name constant"}
		}
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Name" {
			continue
		}
		sawName = true
		if err := checkMCPToolDescriptorNameValue(kv.Value, memoryAdapterAliases); err != nil {
			return err
		}
	}
	if !sawName {
		return &toolNameError{"mcp.ToolDescriptor literal must declare Name with an allowed tool-name constant"}
	}
	return nil
}

func checkMCPNameAssignments(t *testing.T, fset *token.FileSet, file string, assign *ast.AssignStmt, protocolTypes protocolTypeIndex, memoryAdapterAliases map[string]struct{}) {
	t.Helper()
	checkNameAssignments(t, fset, file, assign, protocolTypes, protocolTypeMCPToolDescriptor, func(expr ast.Expr) error {
		return checkMCPToolDescriptorNameValue(expr, memoryAdapterAliases)
	})
}

func checkNameAssignments(t *testing.T, fset *token.FileSet, file string, assign *ast.AssignStmt, protocolTypes protocolTypeIndex, targetType string, validate func(ast.Expr) error) {
	t.Helper()
	for i, lhs := range assign.Lhs {
		if i >= len(assign.Rhs) {
			continue
		}
		if !isNameSelector(lhs) {
			continue
		}
		if isAllowedProtocolNameAssignment(file, lhs, assign.Rhs[i]) {
			continue
		}
		actualType, ok := protocolTypes.nameSelectorTargetType(lhs)
		if !ok || actualType != targetType {
			continue
		}
		if err := validate(assign.Rhs[i]); err != nil {
			t.Fatalf("%s:%d assigns .Name with invalid tool name: %v", file, fset.Position(assign.Rhs[i].Pos()).Line, err)
		}
	}
}

func isNameSelector(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Name"
}

func isAllowedProtocolNameAssignment(file string, lhs, rhs ast.Expr) bool {
	if filepath.Base(file) != "native_source.go" {
		return false
	}
	if !selectorChainEquals(lhs, "item", "tool", "Name") {
		return false
	}
	ident, ok := rhs.(*ast.Ident)
	return ok && ident.Name == "name"
}

func selectorChainEquals(expr ast.Expr, parts ...string) bool {
	if len(parts) == 0 {
		return false
	}
	switch value := expr.(type) {
	case *ast.Ident:
		return len(parts) == 1 && value.Name == parts[0]
	case *ast.SelectorExpr:
		return len(parts) > 1 && value.Sel.Name == parts[len(parts)-1] && selectorChainEquals(value.X, parts[:len(parts)-1]...)
	default:
		return false
	}
}

func nameSelectorReceiver(expr ast.Expr) (string, bool) {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok || sel.Sel.Name != "Name" {
		return "", false
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return "", false
	}
	return ident.Name, true
}

func checkMCPToolDescriptorNameValue(expr ast.Expr, memoryAdapterAliases map[string]struct{}) error {
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind == token.STRING {
			raw, _ := strconv.Unquote(value.Value)
			return &toolNameError{"string literal " + strconv.Quote(raw) + "; use the memory adapters ToolSearchMemory constant"}
		}
	case *ast.SelectorExpr:
		pkg, ok := value.X.(*ast.Ident)
		if ok && value.Sel.Name == "ToolSearchMemory" {
			if _, ok := memoryAdapterAliases[pkg.Name]; ok {
				return nil
			}
		}
		return &toolNameError{"selector expression is not an allowed memory tool constant"}
	}
	return &toolNameError{"use the memory adapters ToolSearchMemory constant"}
}

func isSDKNewToolCall(call *ast.CallExpr, sdkAliases map[string]struct{}) bool {
	return isSDKNewToolFun(call.Fun, sdkAliases)
}

func isSDKNewToolFun(expr ast.Expr, sdkAliases map[string]struct{}) bool {
	switch value := expr.(type) {
	case *ast.SelectorExpr:
		return selectorIsSDKNewTool(value, sdkAliases)
	case *ast.IndexExpr:
		return isSDKNewToolFun(value.X, sdkAliases)
	case *ast.IndexListExpr:
		return isSDKNewToolFun(value.X, sdkAliases)
	default:
		return false
	}
}

func selectorIsSDKNewTool(sel *ast.SelectorExpr, sdkAliases map[string]struct{}) bool {
	if sel.Sel.Name != "NewTool" {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	_, ok = sdkAliases[pkg.Name]
	return ok
}

func checkSDKNewToolNameValue(call *ast.CallExpr, toolValues map[string]struct{}, shadowedTools toolShadowSet) error {
	if len(call.Args) == 0 {
		return &toolNameError{"sdk.NewTool missing name argument"}
	}
	return checkSDKToolNameValue(call.Args[0], toolValues, nil, shadowedTools)
}

func firstSDKNameValueError(lit *ast.CompositeLit, toolValues map[string]struct{}) error {
	return sdkToolLiteralNameError(lit, toolValues, nil, nil)
}

func firstMCPNameValueError(lit *ast.CompositeLit, memoryAdapterAliases map[string]struct{}) error {
	return mcpToolDescriptorLiteralNameError(lit, memoryAdapterAliases)
}

type toolNameError struct {
	msg string
}

func (e *toolNameError) Error() string {
	return e.msg
}
