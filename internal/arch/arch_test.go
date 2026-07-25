// Package arch mechanically enforces the channel-boundary dependency rules
// from docs/superpowers/specs/2026-07-17-channel-boundary-design.md §8.
// Every exemption below is deliberate and carries its rationale; removing
// code from an exemption list is always safe, adding to one is a design
// decision that belongs in the spec.
package arch

import (
	"go/parser"
	"go/token"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const modulePrefix = "github.com/memohai/memoh/"

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate arch test file")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

// goFiles yields non-test .go files under dir (relative to root), with
// forward-slash paths relative to the repo root.
func goFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	return collectGoFiles(t, root, dir, false)
}

// allGoFiles also includes tests. It is used where test fixtures must obey the
// same dependency boundary as production code rather than reaching into an
// implementation package for convenient setup.
func allGoFiles(t *testing.T, root, dir string) []string {
	t.Helper()
	return collectGoFiles(t, root, dir, true)
}

func collectGoFiles(t *testing.T, root, dir string, includeTests bool) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") ||
			(!includeTests && strings.HasSuffix(path, "_test.go")) {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return files
}

func imports(t *testing.T, root, relPath string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, filepath.Join(root, relPath), nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse %s: %v", relPath, err)
	}
	var result []string
	for _, imp := range f.Imports {
		result = append(result, strings.Trim(imp.Path.Value, `"`))
	}
	return result
}

func isPackageOrChild(imp, packagePath string) bool {
	return imp == packagePath || strings.HasPrefix(imp, packagePath+"/")
}

// TestChannelAgentDependenciesStayOnPorts prevents Channel from reaching
// through the Agent facade into orchestration or runtime implementations.
// Cross-boundary Agent contract consumption is domains/agent only; decision
// subpackages remain allowed until that surface moves.

func TestChannelAgentDependenciesStayOnPorts(t *testing.T) {
	allowedAgentRoots := []string{
		modulePrefix + "domains/agent/decision",
	}
	allowedDomainAgent := modulePrefix + "domains/agent"
	retiredTurnRoot := modulePrefix + "internal/rpc/channel/turn"
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/channel") {
		if strings.HasPrefix(file, "domains/channel/assembly/") || strings.HasPrefix(file, "domains/channel/platformreg/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == allowedDomainAgent || strings.HasPrefix(imp, allowedDomainAgent+"/") {
				continue
			}
			if imp == retiredTurnRoot {
				t.Errorf("%s imports %s: Agent turn contract lives in domains/agent; turn transport subpackages are server-owned", file, imp)
				continue
			}
			if !isPackageOrChild(imp, modulePrefix+"internal/agent") {
				continue
			}
			allowed := false
			for _, packagePath := range allowedAgentRoots {
				if isPackageOrChild(imp, packagePath) {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Errorf("%s imports %s: Channel may only depend on domains/agent or agent/decision", file, imp)
			}
		}
	}
}

// TestDomainsAgentRootHasNoInternalImports keeps domains/agent as a leaf
// contract package with no dependency on Memoh internal packages.
func TestDomainsAgentRootHasNoInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/agent") {
		if path.Dir(file) != "domains/agent" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/agent root must not import internal packages", file, imp)
			}
		}
	}
}

// TestDomainsMediaHasNoInternalImports keeps domains/media production code free
// of Memoh internal package imports (attachment wire vocabulary must stay pure).
func TestDomainsMediaHasNoInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/media") {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/media production code must not import internal packages", file, imp)
			}
		}
	}
}

// TestDomainsMemoryRootHasNoInternalImports keeps domains/memory root contract
// free of project-root internal imports.
func TestDomainsMemoryRootHasNoInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/memory") {
		if path.Dir(file) != "domains/memory" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/memory root must not import internal packages", file, imp)
			}
		}
	}
}

// TestDomainsMemoryPublicLeavesHaveNoProjectInternalImports keeps catalog/registry/assembly
// free of project-root internal imports (SQLC/pgvector stay owner-private).
func TestDomainsMemoryPublicLeavesHaveNoProjectInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, leaf := range []string{"domains/memory/catalog", "domains/memory/registry", "domains/memory/assembly"} {
		for _, file := range goFiles(t, root, leaf) {
			for _, imp := range imports(t, root, file) {
				if strings.HasPrefix(imp, modulePrefix+"internal/") {
					t.Errorf("%s imports %s: %s must not import project-root internal packages", file, imp, leaf)
				}
			}
		}
	}
}

// TestDomainsMemoryInternalImportBoundary keeps owner-private memory packages
// importable only under domains/memory.
func TestDomainsMemoryInternalImportBoundary(t *testing.T) {
	const internalMemory = modulePrefix + "domains/memory/internal"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/memory/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalMemory || strings.HasPrefix(imp, internalMemory+"/") {
				t.Errorf("%s imports %s: only domains/memory/** may import memory internals", file, imp)
			}
		}
	}
}

// TestCmdMemoryImportsStayPublic keeps cmd composition on public domains/memory
// packages, never owner-internal providers/stores.
func TestCmdMemoryImportsStayPublic(t *testing.T) {
	allowed := map[string]struct{}{
		modulePrefix + "domains/memory":          {},
		modulePrefix + "domains/memory/catalog":  {},
		modulePrefix + "domains/memory/registry": {},
		modulePrefix + "domains/memory/assembly": {},
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			if !strings.HasPrefix(imp, modulePrefix+"domains/memory") {
				continue
			}
			if _, ok := allowed[imp]; !ok {
				t.Errorf("%s imports %s: cmd may only import public domains/memory packages", file, imp)
			}
		}
	}
}

// TestDomainsMemoryRootSurfaceStaysContractOnly rejects Store/Registry/MCP/SQLC
// leakage into the public domains/memory root package.
func TestDomainsMemoryRootSurfaceStaysContractOnly(t *testing.T) {
	root := repoRoot(t)
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^type\s+Store\b`),
		regexp.MustCompile(`(?m)^type\s+Registry\b`),
		regexp.MustCompile(`\bmodelcontextprotocol\b`),
		regexp.MustCompile(`\binternal/db/postgres/sqlc\b`),
		regexp.MustCompile(`\bdomains/agent/mcp\b`),
	}
	for _, file := range goFiles(t, root, "domains/memory") {
		if path.Dir(file) != "domains/memory" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // guard reads repository sources
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, re := range forbidden {
			if re.Match(data) {
				t.Errorf("%s matches forbidden public-surface pattern %s", file, re)
			}
		}
	}
}

// TestDomainsMemoryCatalogAndRegistryAreRealServices ensures catalog/registry
// are real implementations, not facades over retired internal/memory.
func TestDomainsMemoryCatalogAndRegistryAreRealServices(t *testing.T) {
	root := repoRoot(t)
	checks := []struct {
		rel  string
		need []string
	}{
		{"domains/memory/catalog/service.go", []string{"type Service struct", "func NewService("}},
		{"domains/memory/registry/registry.go", []string{"type Registry struct", "func NewRegistry("}},
		{"domains/memory/assembly/registry.go", []string{"type Deps struct", "func NewRegistry("}},
	}
	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.rel)) //nolint:gosec
		if err != nil {
			t.Fatalf("read %s: %v", check.rel, err)
		}
		text := string(data)
		for _, need := range check.need {
			if !strings.Contains(text, need) {
				t.Errorf("%s missing %q", check.rel, need)
			}
		}
		for _, re := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)^type\s+\w+\s*=`),
			regexp.MustCompile(`internal/memory`),
		} {
			if re.MatchString(text) {
				t.Errorf("%s contains facade/alias pattern %s", check.rel, re)
			}
		}
	}
}

// TestDomainsMemoryCatalogStaysAdminOnly keeps runtime composition out of catalog.
func TestDomainsMemoryCatalogStaysAdminOnly(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/memory/catalog") {
		base := filepath.Base(file)
		if base == "runtime.go" || base == "formation_client.go" {
			t.Errorf("%s: runtime composition belongs in domains/memory/assembly", file)
		}
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(data)
		if strings.Contains(text, "func NewRegistry(") || strings.Contains(text, "func NewFormationClient(") {
			t.Errorf("%s defines composition constructors; use domains/memory/assembly", file)
		}
		for _, imp := range imports(t, root, file) {
			if strings.Contains(imp, "domains/memory/internal/provider") ||
				strings.Contains(imp, "domains/memory/internal/formation") ||
				strings.Contains(imp, "domains/memory/internal/store") {
				t.Errorf("%s imports %s: catalog is admin-only", file, imp)
			}
		}
	}
}

// TestDomainsMediaInternalStorageImportBoundary keeps concrete storage
// providers owner-private. Only packages under domains/media may import them;
// cmd must use public asset constructors / storage ports.
func TestDomainsMediaInternalStorageImportBoundary(t *testing.T) {
	const internalStorage = modulePrefix + "domains/media/internal/storage"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/media/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalStorage || strings.HasPrefix(imp, internalStorage+"/") {
				t.Errorf("%s imports %s: only domains/media/** may import media storage providers", file, imp)
			}
		}
	}
}

// TestCmdMediaImportsStayPublic keeps cmd composition on domains/media/asset
// and domains/media/storage contracts, never owner-internal providers.
func TestCmdMediaImportsStayPublic(t *testing.T) {
	allowed := map[string]struct{}{
		modulePrefix + "domains/media":            {},
		modulePrefix + "domains/media/attachment": {},
		modulePrefix + "domains/media/asset":      {},
		modulePrefix + "domains/media/storage":    {},
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			if !strings.HasPrefix(imp, modulePrefix+"domains/media") {
				continue
			}
			if _, ok := allowed[imp]; !ok {
				t.Errorf("%s imports %s: cmd may only import public domains/media packages", file, imp)
			}
		}
	}
}

// TestDomainsMediaAssetIsRealService ensures asset is the real Service
// implementation, not a facade/alias over a retired package.
func TestDomainsMediaAssetIsRealService(t *testing.T) {
	root := repoRoot(t)
	servicePath := filepath.Join(root, "domains/media/asset/service.go")
	data, err := os.ReadFile(servicePath) //nolint:gosec // guard reads repository sources
	if err != nil {
		t.Fatalf("read asset service: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "type Service struct") {
		t.Fatal("domains/media/asset must define type Service struct")
	}
	if !strings.Contains(text, "func NewService(") {
		t.Fatal("domains/media/asset must define NewService")
	}
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^type\s+Service\s*=`),
		regexp.MustCompile(`(?m)^var\s+NewService\s*=`),
		regexp.MustCompile(`internal/media`),
	}
	for _, re := range forbidden {
		if re.MatchString(text) {
			t.Errorf("domains/media/asset/service.go must not contain facade/alias pattern %s", re)
		}
	}
}

// TestDomainsChannelEmailInternalImportBoundary keeps email postgres/port/adapters
// owner-private. Only packages under domains/channel may import them.
func TestDomainsChannelEmailInternalImportBoundary(t *testing.T) {
	root := repoRoot(t)
	prefixes := []string{
		modulePrefix + "domains/channel/internal/email",
		modulePrefix + "domains/channel/internal/port/email",
	}
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/channel/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			for _, prefix := range prefixes {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					t.Errorf("%s imports %s: only domains/channel/** may import channel email internals", file, imp)
				}
			}
		}
	}
}

// TestCmdChannelEmailImportsStayPublic keeps cmd composition on public
// domains/channel/email, never owner-internal email packages.
func TestCmdChannelEmailImportsStayPublic(t *testing.T) {
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"domains/channel/internal/email") ||
				strings.HasPrefix(imp, modulePrefix+"domains/channel/internal/port/email") {
				t.Errorf("%s imports %s: cmd may only import public domains/channel/email", file, imp)
			}
		}
	}
}

// TestDomainsChannelEmailIsRealService ensures email is the real Service
// implementation, not a facade/alias over a retired package.
func TestDomainsChannelEmailIsRealService(t *testing.T) {
	root := repoRoot(t)
	servicePath := filepath.Join(root, "domains/channel/email/service.go")
	data, err := os.ReadFile(servicePath) //nolint:gosec
	if err != nil {
		t.Fatalf("read email service: %v", err)
	}
	text := string(data)
	if !strings.Contains(text, "type Service struct") {
		t.Fatal("domains/channel/email must define type Service struct")
	}
	if !strings.Contains(text, "func NewService(") {
		t.Fatal("domains/channel/email must define NewService")
	}
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^type\s+Service\s*=`),
		regexp.MustCompile(`(?m)^var\s+NewService\s*=`),
		regexp.MustCompile(`internal/email`),
	}
	for _, re := range forbidden {
		if re.MatchString(text) {
			t.Errorf("domains/channel/email/service.go must not contain facade/alias pattern %s", re)
		}
	}
}

// modelLeaf describes a closed Model vertical under domains/model/<leaf>.
type modelLeaf struct {
	name         string   // public leaf name: fetch, search, template
	retiredPaths []string // retired internal/<path> trees
}

var modelLeaves = []modelLeaf{
	{name: "fetch", retiredPaths: []string{"internal/fetchproviders"}},
	{name: "search", retiredPaths: []string{"internal/searchproviders"}},
	{name: "audio", retiredPaths: []string{"internal/audio"}},
	{name: "video", retiredPaths: []string{"internal/video"}},
	{name: "template", retiredPaths: []string{"internal/registry", "internal/providertemplates"}},
	{name: "provider", retiredPaths: []string{"internal/providers", "internal/copilot"}},
	{name: "catalog", retiredPaths: []string{"internal/models"}},
	{name: "execution", retiredPaths: []string{"internal/models"}},
}

func modelLeafPublicPath(leaf modelLeaf) string {
	return "domains/model/" + leaf.name
}

func modelLeafPublicImport(leaf modelLeaf) string {
	return modulePrefix + modelLeafPublicPath(leaf)
}

func modelLeafInternalPrefixes(leaf modelLeaf) []string {
	prefixes := []string{
		modulePrefix + "domains/model/internal/postgres/" + leaf.name,
		modulePrefix + "domains/model/internal/port/" + leaf.name,
	}
	if leaf.name == "audio" {
		prefixes = append(prefixes, modulePrefix+"domains/model/internal/audio")
	}
	if leaf.name == "provider" {
		prefixes = append(prefixes, modulePrefix+"domains/model/internal/provider")
	}
	return prefixes
}

// TestDomainsModelLeavesHaveNoProjectInternalImports keeps public Model
// leaf packages free of project-root internal imports (SQLC/db stay owner-private).
func TestDomainsModelLeavesHaveNoProjectInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, leaf := range modelLeaves {
		public := modelLeafPublicPath(leaf)
		for _, file := range goFiles(t, root, public) {
			for _, imp := range imports(t, root, file) {
				if strings.HasPrefix(imp, modulePrefix+"internal/") {
					t.Errorf("%s imports %s: %s must not import project-root internal packages", file, imp, public)
				}
			}
		}
	}
}

// TestDomainsModelLeavesInternalImportBoundary keeps postgres/port adapters
// owner-private. Only packages under domains/model may import them.
func TestDomainsModelLeavesInternalImportBoundary(t *testing.T) {
	root := repoRoot(t)
	for _, leaf := range modelLeaves {
		internalPrefixes := modelLeafInternalPrefixes(leaf)
		for _, file := range allGoFiles(t, root, ".") {
			if strings.HasPrefix(file, "domains/model/") {
				continue
			}
			for _, imp := range imports(t, root, file) {
				for _, prefix := range internalPrefixes {
					if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
						t.Errorf("%s imports %s: only domains/model/** may import model %s internals", file, imp, leaf.name)
					}
				}
			}
		}
	}
}

// TestCmdModelLeafImportsStayPublic keeps cmd composition on public
// domains/model packages, never owner-internal postgres/port packages.
func TestCmdModelLeafImportsStayPublic(t *testing.T) {
	allowed := map[string]struct{}{
		modulePrefix + "domains/model":            {},
		modulePrefix + "domains/model/capability": {},
		modulePrefix + "domains/model/assembly":   {},
	}
	for _, leaf := range modelLeaves {
		allowed[modelLeafPublicImport(leaf)] = struct{}{}
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			if !strings.HasPrefix(imp, modulePrefix+"domains/model") {
				continue
			}
			if _, ok := allowed[imp]; !ok {
				t.Errorf("%s imports %s: cmd may only import public domains/model packages", file, imp)
			}
		}
	}
}

// TestDomainsModelLeavesAreRealServices ensures each Model leaf is the real
// Service (or execution Resolver) implementation, not a facade/alias over a
// retired package.
func TestDomainsModelLeavesAreRealServices(t *testing.T) {
	root := repoRoot(t)
	for _, leaf := range modelLeaves {
		public := modelLeafPublicPath(leaf)
		switch leaf.name {
		case "execution":
			resolverPath := filepath.Join(root, public, "resolver.go")
			data, err := os.ReadFile(resolverPath) //nolint:gosec // guard reads repository sources
			if err != nil {
				t.Fatalf("read %s resolver: %v", leaf.name, err)
			}
			text := string(data)
			if !strings.Contains(text, "type Resolver struct") {
				t.Errorf("%s must define type Resolver struct", public)
			}
			if !strings.Contains(text, "func NewResolver(") {
				t.Errorf("%s must define NewResolver", public)
			}
			forbidden := []*regexp.Regexp{
				regexp.MustCompile(`(?m)^type\s+Resolver\s*=`),
				regexp.MustCompile(`(?m)^var\s+NewResolver\s*=`),
				regexp.MustCompile(`internal/models`),
			}
			for _, re := range forbidden {
				if re.MatchString(text) {
					t.Errorf("%s/resolver.go must not contain facade/alias pattern %s", public, re)
				}
			}
		default:
			servicePath := filepath.Join(root, public, "service.go")
			data, err := os.ReadFile(servicePath) //nolint:gosec // guard reads repository sources
			if err != nil {
				t.Fatalf("read %s service: %v", leaf.name, err)
			}
			text := string(data)
			if !strings.Contains(text, "type Service struct") {
				t.Errorf("%s must define type Service struct", public)
			}
			if !strings.Contains(text, "func NewService(") {
				t.Errorf("%s must define NewService", public)
			}
			forbidden := []*regexp.Regexp{
				regexp.MustCompile(`(?m)^type\s+Service\s*=`),
				regexp.MustCompile(`(?m)^var\s+NewService\s*=`),
			}
			for _, retiredPath := range leaf.retiredPaths {
				forbidden = append(forbidden, regexp.MustCompile(regexp.QuoteMeta(retiredPath)))
			}
			for _, re := range forbidden {
				if re.MatchString(text) {
					t.Errorf("%s/service.go must not contain facade/alias pattern %s", public, re)
				}
			}
		}
	}
}

// TestDomainsRuntimeRootHasNoInternalImports keeps domains/runtime root as a
// leaf contract package with no dependency on Memoh internal packages.
func TestDomainsRuntimeRootHasNoInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/runtime") {
		if path.Dir(file) != "domains/runtime" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/runtime root must not import internal packages", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeContainerPublicLeavesHaveNoProjectInternalImports keeps
// public container/client/display/network free of project-root internal imports.
// assembly remains the composition seam and may still bridge transitional
// internal/config + sqlc until those move under domains/runtime.
func TestDomainsRuntimeContainerPublicLeavesHaveNoProjectInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, leaf := range []string{"domains/runtime/container", "domains/runtime/client", "domains/runtime/display", "domains/runtime/network"} {
		for _, file := range goFiles(t, root, leaf) {
			for _, imp := range imports(t, root, file) {
				if strings.HasPrefix(imp, modulePrefix+"internal/") {
					t.Errorf("%s imports %s: %s must not import project-root internal packages", file, imp, leaf)
				}
			}
		}
	}
}

// TestDomainsRuntimeInternalContainerImportBoundary keeps concrete container
// backends owner-private. Only packages under domains/runtime may import them.
func TestDomainsRuntimeInternalContainerImportBoundary(t *testing.T) {
	const internalContainer = modulePrefix + "domains/runtime/internal/container"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/runtime/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalContainer || strings.HasPrefix(imp, internalContainer+"/") {
				t.Errorf("%s imports %s: only domains/runtime/** may import runtime container backends", file, imp)
			}
		}
	}
}

// TestCmdRuntimeContainerImportsStayPublic keeps cmd composition on public
// domains/runtime/{container,client,display,network,assembly}, never owner-internal backends.
func TestCmdRuntimeContainerImportsStayPublic(t *testing.T) {
	allowed := map[string]struct{}{
		modulePrefix + "domains/runtime":                 {},
		modulePrefix + "domains/runtime/assembly":        {},
		modulePrefix + "domains/runtime/container":       {},
		modulePrefix + "domains/runtime/client":          {},
		modulePrefix + "domains/runtime/display":         {},
		modulePrefix + "domains/runtime/network":         {},
		modulePrefix + "domains/runtime/workspace":       {},
		modulePrefix + "domains/runtime/bridge/client":   {},
		modulePrefix + "domains/runtime/bridge/server":   {},
		modulePrefix + "domains/runtime/bridge/bridgepb": {},
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			if !strings.HasPrefix(imp, modulePrefix+"domains/runtime") {
				continue
			}
			if _, ok := allowed[imp]; !ok {
				t.Errorf("%s imports %s: cmd may only import public domains/runtime packages", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeClientPublicLeavesHaveNoProjectInternalImports keeps
// client free of project-root internal imports (SQLC stays owner-private).
func TestDomainsRuntimeClientPublicLeavesHaveNoProjectInternalImports(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/runtime/client") {
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, modulePrefix+"internal/") {
				t.Errorf("%s imports %s: domains/runtime/client must not import project-root internal packages", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeInternalClientImportBoundary keeps postgres/secret adapters
// owner-private. Only packages under domains/runtime may import them.
func TestDomainsRuntimeInternalClientImportBoundary(t *testing.T) {
	const internalClient = modulePrefix + "domains/runtime/internal/client"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/runtime/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalClient || strings.HasPrefix(imp, internalClient+"/") {
				t.Errorf("%s imports %s: only domains/runtime/** may import runtime client internals", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeInternalDisplayImportBoundary keeps WebRTC/RFB concrete
// implementation owner-private. Only packages under domains/runtime may import it.
func TestDomainsRuntimeInternalDisplayImportBoundary(t *testing.T) {
	const internalDisplay = modulePrefix + "domains/runtime/internal/display"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/runtime/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalDisplay || strings.HasPrefix(imp, internalDisplay+"/") {
				t.Errorf("%s imports %s: only domains/runtime/** may import runtime display internals", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeInternalNetworkImportBoundary keeps overlay/postgres
// adapters owner-private. Only packages under domains/runtime may import them.
func TestDomainsRuntimeInternalNetworkImportBoundary(t *testing.T) {
	const internalNetwork = modulePrefix + "domains/runtime/internal/network"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/runtime/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == internalNetwork || strings.HasPrefix(imp, internalNetwork+"/") {
				t.Errorf("%s imports %s: only domains/runtime/** may import runtime network internals", file, imp)
			}
		}
	}
}

// TestDomainsRuntimeContainerAndAssemblyAreReal ensures public container types
// and assembly constructors are real, not facades/aliases over retired paths.
func TestDomainsRuntimeContainerAndAssemblyAreReal(t *testing.T) {
	root := repoRoot(t)
	checks := []struct {
		rel  string
		need []string
	}{
		{"domains/runtime/container/service.go", []string{"type Service interface", "type ImageService interface"}},
		{"domains/runtime/container/types.go", []string{"var (", "ErrNotFound", "type ContainerInfo struct"}},
		{"domains/runtime/assembly/container.go", []string{"type Deps struct", "func NewService("}},
		{"domains/runtime/client/service.go", []string{"type Service struct", "func NewService("}},
		{"domains/runtime/client/hub.go", []string{"type Hub struct", "func NewHub("}},
		{"domains/runtime/assembly/client.go", []string{"type ClientDeps struct", "func NewClient("}},
		{"domains/runtime/display/service.go", []string{"type Service interface", "type Workspace interface", "ErrDisplayUnavailable"}},
		{"domains/runtime/assembly/display.go", []string{"type DisplayDeps struct", "func NewDisplay("}},
		{"domains/runtime/network/service.go", []string{"type Service struct", "func New("}},
		{"domains/runtime/network/persistence.go", []string{"type ConfigReader interface", "type WorkspaceReader interface"}},
		{"domains/runtime/assembly/network.go", []string{"type NetworkDeps struct", "func NewNetwork("}},
	}
	for _, check := range checks {
		data, err := os.ReadFile(filepath.Join(root, check.rel)) //nolint:gosec
		if err != nil {
			t.Fatalf("read %s: %v", check.rel, err)
		}
		text := string(data)
		for _, need := range check.need {
			if !strings.Contains(text, need) {
				t.Errorf("%s missing %q", check.rel, need)
			}
		}
		for _, re := range []*regexp.Regexp{
			regexp.MustCompile(`(?m)^type\s+\w+\s*=`),
			regexp.MustCompile(`memoh/internal/container`),
			regexp.MustCompile(`memoh/internal/userruntime`),
			regexp.MustCompile(`memoh/internal/display`),
			regexp.MustCompile(`memoh/internal/network`),
			regexp.MustCompile(`func \(s \*Service\) SetController`),
		} {
			if re.MatchString(text) {
				t.Errorf("%s contains facade/alias pattern %s", check.rel, re)
			}
		}
	}
}

// TestBusinessPackagesDoNotImportMediaStorageProviders keeps
// messaging/channel/handlers off concrete media storage providers.
func TestBusinessPackagesDoNotImportMediaStorageProviders(t *testing.T) {
	const forbidden = modulePrefix + "domains/media/internal/storage"
	root := repoRoot(t)
	for _, dir := range []string{
		"domains/channel",
		"domains/api/http",
	} {
		for _, file := range goFiles(t, root, dir) {
			if strings.HasPrefix(file, "domains/channel/assembly/") {
				continue
			}
			for _, imp := range imports(t, root, file) {
				if imp == forbidden || strings.HasPrefix(imp, forbidden+"/") {
					t.Errorf("%s imports %s: business packages must use consumer ports or domains/media/asset", file, imp)
				}
			}
		}
	}
}

// TestNativeRuntimeHasNoStreamEventAliases prevents reintroducing native
// StreamEvent / Event* compatibility aliases after the domains/agent move.
func TestNativeRuntimeHasNoStreamEventAliases(t *testing.T) {
	root := repoRoot(t)
	forbidden := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^type\s+StreamEvent\b`),
		regexp.MustCompile(`(?m)^type\s+StreamEventType\b`),
		regexp.MustCompile(`(?m)^\s+Event(?:AgentStart|Start|TextStart|TextDelta|TextEnd|ReasoningStart|ReasoningDelta|ReasoningEnd|ToolCallInputStart|ToolCallStart|ToolCallMetadata|ToolCallProgress|ToolCallEnd|ToolApprovalRequest|UserInputRequest|AttachmentDelta|Attachment|Reaction|Speech|AgentEnd|End|AgentAbort|Abort|Retry|Progress|Error)\s*=`),
	}
	for _, file := range goFiles(t, root, "domains/agent/engine") {
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // guard reads repository sources
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, re := range forbidden {
			if re.Match(data) {
				t.Errorf("%s defines native StreamEvent/Event aliases; consumers must use domains/agent", file)
			}
		}
	}
}

// TestTimelineOnlyDependsOnTurnPort keeps the chat timeline independent of
// both Channel and Agent implementations. Timeline may translate a completed
// turn response, but it may not start or orchestrate one.
func TestTimelineOnlyDependsOnTurnPort(t *testing.T) {
	root := repoRoot(t)
	allowedDomainAgent := modulePrefix + "domains/agent"
	for _, file := range goFiles(t, root, "domains/agent/chat/timeline") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"domains/channel"):
				t.Errorf("%s imports %s: chat/timeline must not depend on Channel", file, imp)
			case imp == allowedDomainAgent || strings.HasPrefix(imp, allowedDomainAgent+"/"):
				// domains/agent is the only Agent contract timeline may consume.
			case isPackageOrChild(imp, modulePrefix+"internal/agent"):
				t.Errorf("%s imports %s: chat/timeline may only depend on domains/agent", file, imp)
			}
		}
	}
}

// TestChatStorageDomainsDoNotDependOnUpperLayers keeps persisted thread and
// message state below both Agent execution and Channel delivery. Timeline has
// its separate, narrower turn-port allowance above.
func TestChatStorageDomainsDoNotDependOnUpperLayers(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"domains/agent/chat/thread", "domains/agent/chat/message"} {
		for _, file := range allGoFiles(t, root, dir) {
			for _, imp := range imports(t, root, file) {
				switch {
				case isPackageOrChild(imp, modulePrefix+"internal/agent"):
					t.Errorf("%s imports %s: chat storage domains must not depend on Agent", file, imp)
				case isPackageOrChild(imp, modulePrefix+"domains/channel"):
					t.Errorf("%s imports %s: chat storage domains must not depend on Channel", file, imp)
				case isPackageOrChild(imp, modulePrefix+"domains/api/http"):
					t.Errorf("%s imports %s: chat storage domains must not depend on HTTP handlers", file, imp)
				}
			}
		}
	}
}

// TestApplicationDoesNotDependOnChannel keeps use-case orchestration on
// neutral Agent/Chat ports. Platform identity and conversation vocabulary
// must enter through adapters at the composition root.
func TestApplicationDoesNotDependOnChannel(t *testing.T) {
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "domains/agent/application") {
		for _, imp := range imports(t, root, file) {
			if isPackageOrChild(imp, modulePrefix+"domains/channel") {
				t.Errorf("%s imports %s: Agent application must consume neutral ports, not Channel", file, imp)
			}
		}
	}
}

// TestAgentContextDoesNotDependOnExecutionOrDelivery keeps context assembly
// reusable by application and runtimes without creating a reverse dependency.
func TestAgentContextDoesNotDependOnExecutionOrDelivery(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/agent/chat/context") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"domains/agent/application"),
				isPackageOrChild(imp, modulePrefix+"domains/agent/engine"),
				isPackageOrChild(imp, modulePrefix+"domains/agent/acp"),
				isPackageOrChild(imp, modulePrefix+"domains/agent/tool"),
				isPackageOrChild(imp, modulePrefix+"domains/agent/adapter"):
				t.Errorf("%s imports %s: agent/context must not depend on Agent execution layers", file, imp)
			case isPackageOrChild(imp, modulePrefix+"domains/channel"):
				t.Errorf("%s imports %s: agent/context must not depend on Channel", file, imp)
			case isPackageOrChild(imp, modulePrefix+"domains/api/http"):
				t.Errorf("%s imports %s: agent/context must not depend on HTTP handlers", file, imp)
			case strings.HasPrefix(imp, "github.com/labstack/echo"):
				t.Errorf("%s imports Echo", file)
			case strings.HasPrefix(imp, "go.uber.org/fx"):
				t.Errorf("%s imports fx", file)
			}
		}
	}
}

// TestBusinessPackagesUseOwnerPersistencePorts keeps the transitional broad
// database interface out of business packages and generated SQLC inside
// PostgreSQL adapters.
func TestBusinessPackagesUseOwnerPersistencePorts(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal") {
		if strings.HasPrefix(file, "internal/db/") {
			continue
		}
		isPostgresAdapter := strings.Contains(file, "/postgres/")
		for _, imp := range imports(t, root, file) {
			switch {
			case imp == modulePrefix+"internal/db/store":
				t.Errorf("%s imports transitional broad database store", file)
			case imp == modulePrefix+"internal/db/postgres/sqlc" && !isPostgresAdapter:
				t.Errorf("%s imports generated SQLC outside a PostgreSQL adapter", file)
			case strings.HasPrefix(imp, "github.com/jackc/pgx") && !isPostgresAdapter:
				t.Errorf("%s imports PostgreSQL types outside a PostgreSQL adapter", file)
			}
		}
	}
}

// TestNativeRuntimeStaysBelowApplicationAndDelivery keeps the in-process
// runtime reusable behind the application service. Native execution may use
// Agent ports and lower-level domains, but it must not reach back into turn
// orchestration or either delivery layer.
func TestNativeRuntimeStaysBelowApplicationAndDelivery(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/agent/engine") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"domains/agent/application"):
				t.Errorf("%s imports %s: native runtime must not depend on Agent application orchestration", file, imp)
			case isPackageOrChild(imp, modulePrefix+"domains/channel"):
				t.Errorf("%s imports %s: native runtime must not depend on Channel delivery", file, imp)
			case isPackageOrChild(imp, modulePrefix+"domains/api/http"):
				t.Errorf("%s imports %s: native runtime must not depend on HTTP handlers", file, imp)
			}
		}
	}
}

// TestAgentToolStaysOnNeutralPorts prevents tool providers from reaching into
// orchestration or Channel implementation. Public Channel consumer ports
// (delivery/email) are the allowed Sender/Email surfaces.
func TestAgentToolStaysOnNeutralPorts(t *testing.T) {
	root := repoRoot(t)
	allowedChannelPorts := []string{
		modulePrefix + "domains/channel/delivery",
		modulePrefix + "domains/channel/email",
	}
	for _, file := range allGoFiles(t, root, "domains/agent/tool") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"domains/agent/application"):
				t.Errorf("%s imports %s: Agent tools must not depend on application orchestration", file, imp)
			case isPackageOrChild(imp, modulePrefix+"domains/channel"):
				allowed := false
				for _, port := range allowedChannelPorts {
					if imp == port || strings.HasPrefix(imp, port+"/") {
						allowed = true
						break
					}
				}
				if !allowed {
					t.Errorf("%s imports %s: Agent tools may only use domains/channel/delivery or domains/channel/email ports", file, imp)
				}
			case isPackageOrChild(imp, modulePrefix+"domains/api/http"):
				t.Errorf("%s imports %s: Agent tools must not depend on HTTP handlers", file, imp)
			}
		}
	}
}

// TestAgentRuntimeDoesNotDependOnChat keeps runtime snapshots and execution
// independent from Thread/Message/view implementations.
func TestAgentRuntimeDoesNotDependOnChat(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"domains/agent/engine", "domains/agent/acp"} {
		for _, file := range goFiles(t, root, dir) {
			for _, imp := range imports(t, root, file) {
				if isPackageOrChild(imp, modulePrefix+"domains/agent/chat/thread") ||
					isPackageOrChild(imp, modulePrefix+"domains/agent/chat/message") ||
					isPackageOrChild(imp, modulePrefix+"domains/agent/view") {
					t.Errorf("%s imports %s: Agent runtime must consume Agent-owned contracts", file, imp)
				}
			}
		}
	}
}

// TestThreadStorageDoesNotReachRouteDB pins the ownership split: Thread may
// carry an opaque route_id, while Channel owns active-thread selection and
// route metadata projection.
func TestThreadStorageDoesNotReachRouteDB(t *testing.T) {
	root := repoRoot(t)
	forbidden := []string{
		"bot_channel_routes",
		"GetActiveSessionForRoute",
		"SetRouteActiveSession",
	}
	for _, file := range allGoFiles(t, root, "domains/agent/chat/thread") {
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // guard reads repository sources
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Errorf("%s contains %q: route DB ownership belongs to domains/channel/route", file, token)
			}
		}
	}
	queryFiles, err := filepath.Glob(filepath.Join(root, "db/postgres/agent/queries/*.sql"))
	if err != nil {
		t.Fatalf("list agent queries: %v", err)
	}
	if len(queryFiles) == 0 {
		t.Fatal("no agent query files found; the guard would pass vacuously")
	}
	for _, queryFile := range queryFiles {
		data, err := os.ReadFile(queryFile) //nolint:gosec // repository-owned query path
		if err != nil {
			t.Fatalf("read %s: %v", queryFile, err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Errorf(
					"%s contains %q: Agent queries must not join or mutate Route DB",
					filepath.Base(queryFile), token,
				)
			}
		}
	}
}

// TestChannelDoesNotImportEcho: webhook endpoints are the only HTTP surface
// the channel package owns (spec §8 exemption).
func TestChannelDoesNotImportEcho(t *testing.T) {
	exempt := map[string]string{
		"domains/channel/gateway/webhook_handler.go":                 "channel-owned webhook HTTP endpoint",
		"domains/channel/internal/adapter/feishu/webhook_handler.go": "platform webhook endpoint",
		"domains/channel/internal/adapter/line/adapter.go":           "platform webhook endpoint",
		"domains/channel/internal/adapter/wechatoa/inbound.go":       "platform webhook endpoint",
		"domains/channel/internal/adapter/weixin/qr_handler.go":      "weixin QR login HTTP endpoint",
		"domains/channel/http/email_webhook.go":                      "channel-owned email webhook HTTP endpoint",
		"domains/channel/http/public_media.go":                       "channel-owned public media HTTP endpoint",
		"domains/channel/http/public_media_test.go":                  "channel-owned public media HTTP endpoint test",
		"domains/channel/http/error_alias.go":                        "channel HTTP swagger error alias",
	}
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/channel") {
		if strings.HasPrefix(file, "domains/channel/assembly/") || strings.HasPrefix(file, "domains/channel/http/") {
			continue
		}
		if _, ok := exempt[file]; ok {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if strings.HasPrefix(imp, "github.com/labstack/echo") {
				t.Errorf("%s imports Echo outside the webhook-endpoint exemptions", file)
			}
		}
	}
}

// TestChannelAndTimelineDoNotImportFx: assembly lives in cmd/** only.
func TestChannelAndTimelineDoNotImportFx(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"domains/channel", "domains/agent/chat/timeline"} {
		for _, file := range goFiles(t, root, dir) {
			if strings.HasPrefix(file, "domains/channel/assembly/") {
				continue
			}
			for _, imp := range imports(t, root, file) {
				if strings.HasPrefix(imp, "go.uber.org/fx") {
					t.Errorf("%s imports fx: assembly belongs to domains/channel/assembly or cmd/**", file)
				}
			}
		}
	}
}

// TestTurnPortStaysPure: domains/agent and remaining turn transports must not
// depend on HTTP frameworks, assembly, generated SQL, the channel side, or
// the application implementation. domains/agent root production files must
// also stay free of Memoh internal imports.
func TestTurnPortStaysPure(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "domains/agent") {
		if path.Dir(file) != "domains/agent" {
			continue
		}
		for _, imp := range imports(t, root, file) {
			switch {
			case strings.HasPrefix(imp, "github.com/labstack/echo"):
				t.Errorf("%s imports Echo", file)
			case strings.HasPrefix(imp, "go.uber.org/fx"):
				t.Errorf("%s imports fx", file)
			case strings.HasPrefix(imp, modulePrefix+"internal/"):
				t.Errorf("%s imports %s: domains/agent root must not import internal packages", file, imp)
			case turnForbiddenLayerImport(imp):
				t.Errorf("%s imports %s: the agent contract must not depend on retired domains, application, or runtime", file, imp)
			}
		}
	}
	for _, file := range goFiles(t, root, "internal/rpc/channel/turn") {
		for _, imp := range imports(t, root, file) {
			switch {
			case strings.HasPrefix(imp, "github.com/labstack/echo"):
				t.Errorf("%s imports Echo", file)
			case strings.HasPrefix(imp, "go.uber.org/fx"):
				t.Errorf("%s imports fx", file)
			case imp == modulePrefix+"internal/db/postgres/sqlc":
				t.Errorf("%s imports generated sqlc", file)
			case imp == modulePrefix+"domains/channel" || strings.HasPrefix(imp, modulePrefix+"internal/channel/"):
				t.Errorf("%s imports the channel side", file)
			case turnForbiddenLayerImport(imp):
				t.Errorf("%s imports %s: turn transports must not depend on retired domains, application, or runtime", file, imp)
			case imp == modulePrefix+"internal/rpc/channel/turn":
				t.Errorf("%s imports retired turn root; use domains/agent", file)
			}
		}
	}
}

func turnForbiddenLayerImport(imp string) bool {
	for _, packagePath := range []string{
		modulePrefix + "internal/conversation",
		modulePrefix + "internal/session",
		modulePrefix + "internal/message",
		modulePrefix + "internal/pipeline",
		modulePrefix + "internal/toolapproval",
		modulePrefix + "internal/userinput",
		modulePrefix + "domains/agent/application",
		modulePrefix + "domains/agent/engine",
		modulePrefix + "domains/agent/acp",
	} {
		if isPackageOrChild(imp, packagePath) {
			return true
		}
	}
	return false
}

// TestDefaultTeamIDReferences: business packages must not hardcode the
// single-team assumption. Each exemption is a deliberate singleton-team
// touchpoint that a hosted multi-team runtime replaces wholesale.
func TestDefaultTeamIDReferences(t *testing.T) {
	allowedPrefixes := []string{
		"cmd/",
		"internal/db/",
		"domains/iam/team/",
	}
	exempt := map[string]string{
		"domains/memory/registry/registry.go":                        "memory provider registry resolves the singleton team when no scope is bound",
		"domains/memory/internal/provider/builtin/pgvector_index.go": "builtin pgvector index binds the singleton team resolver",
		"domains/channel/gateway/service.go":                         "configless channels (web/cli) synthesize a config; agentdomain.Service fails closed on empty TeamID",
	}
	ref := regexp.MustCompile(`\bteam\.DefaultTeamID\b`)
	root := repoRoot(t)
	for _, dir := range []string{"internal", "cmd"} {
		for _, file := range goFiles(t, root, dir) {
			data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // guard test walks repo-tracked files by design
			if err != nil {
				t.Fatalf("read %s: %v", file, err)
			}
			if !ref.Match(data) {
				continue
			}
			if _, ok := exempt[file]; ok {
				continue
			}
			allowed := false
			for _, prefix := range allowedPrefixes {
				if strings.HasPrefix(file, prefix) {
					allowed = true
					break
				}
			}
			if !allowed {
				t.Errorf("%s references team.DefaultTeamID outside the allowed layers (internal/db, cmd/**, tests) without a documented exemption", file)
			}
		}
	}
}

// TestAPIInternalIsPrivateToAPIOwner keeps domains/api/internal visible only to
// domains/api itself (including assembly).
func TestAPIInternalIsPrivateToAPIOwner(t *testing.T) {
	const forbidden = modulePrefix + "domains/api/internal"
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "domains/api/") || strings.HasPrefix(file, "internal/arch/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			if imp == forbidden || strings.HasPrefix(imp, forbidden+"/") {
				t.Errorf("%s imports %s: API private packages are owner-internal", file, imp)
			}
		}
	}
}

// TestHealthCheckerInternalsStayOwnerPrivate keeps health checker implementations
// importable only within their owning domain tree (plus that domain's assembly).
func TestHealthCheckerInternalsStayOwnerPrivate(t *testing.T) {
	boundaries := []struct {
		prefix  string
		allowed string
	}{
		{modulePrefix + "domains/channel/internal/health", "domains/channel/"},
		{modulePrefix + "domains/agent/internal/health", "domains/agent/"},
		{modulePrefix + "domains/model/internal/health", "domains/model/"},
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, ".") {
		if strings.HasPrefix(file, "internal/arch/") {
			continue
		}
		for _, imp := range imports(t, root, file) {
			for _, b := range boundaries {
				if imp != b.prefix && !strings.HasPrefix(imp, b.prefix+"/") {
					continue
				}
				if strings.HasPrefix(file, b.allowed) {
					continue
				}
				t.Errorf("%s imports %s: only %s** may import that health checker", file, imp, b.allowed)
			}
		}
	}
}

// TestCmdHealthCheckerImportsStayPublic keeps cmd on owner assembly constructors,
// never owner-private health packages. The shared framework internal/healthcheck
// remains allowed.
func TestCmdHealthCheckerImportsStayPublic(t *testing.T) {
	prefixes := []string{
		modulePrefix + "domains/channel/internal/health",
		modulePrefix + "domains/agent/internal/health",
		modulePrefix + "domains/model/internal/health",
	}
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "cmd") {
		for _, imp := range imports(t, root, file) {
			for _, prefix := range prefixes {
				if imp == prefix || strings.HasPrefix(imp, prefix+"/") {
					t.Errorf("%s imports %s: cmd must use owner assembly NewHealthChecker", file, imp)
				}
			}
		}
	}
}
