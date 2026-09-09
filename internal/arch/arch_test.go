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
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
)

const modulePrefix = "github.com/felinics/memoh/"

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
func TestChannelAgentDependenciesStayOnPorts(t *testing.T) {
	allowedAgentRoots := []string{
		modulePrefix + "internal/agent/turn",
		modulePrefix + "internal/agent/event",
		modulePrefix + "internal/agent/decision",
	}
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal/channel") {
		for _, imp := range imports(t, root, file) {
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
				t.Errorf("%s imports %s: Channel may only depend on agent/turn, agent/event, or agent/decision", file, imp)
			}
		}
	}
}

// TestTimelineOnlyDependsOnTurnPort keeps the chat timeline independent of
// both Channel and Agent implementations. Timeline may translate a completed
// turn response, but it may not start or orchestrate one.
func TestTimelineOnlyDependsOnTurnPort(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal/chat/timeline") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"internal/channel"):
				t.Errorf("%s imports %s: chat/timeline must not depend on Channel", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/agent") &&
				!isPackageOrChild(imp, modulePrefix+"internal/agent/turn"):
				t.Errorf("%s imports %s: chat/timeline may only depend on the Agent turn contract", file, imp)
			}
		}
	}
}

// TestChatStorageDomainsDoNotDependOnUpperLayers keeps persisted thread and
// message state below both Agent execution and Channel delivery. Timeline has
// its separate, narrower turn-port allowance above.
func TestChatStorageDomainsDoNotDependOnUpperLayers(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal/chat/thread", "internal/chat/message"} {
		for _, file := range allGoFiles(t, root, dir) {
			for _, imp := range imports(t, root, file) {
				switch {
				case isPackageOrChild(imp, modulePrefix+"internal/agent"):
					t.Errorf("%s imports %s: chat storage domains must not depend on Agent", file, imp)
				case isPackageOrChild(imp, modulePrefix+"internal/channel"):
					t.Errorf("%s imports %s: chat storage domains must not depend on Channel", file, imp)
				case isPackageOrChild(imp, modulePrefix+"internal/handlers"):
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
	for _, file := range allGoFiles(t, root, "internal/agent/application") {
		for _, imp := range imports(t, root, file) {
			if isPackageOrChild(imp, modulePrefix+"internal/channel") {
				t.Errorf("%s imports %s: Agent application must consume neutral ports, not Channel", file, imp)
			}
		}
	}
}

// TestAgentContextDoesNotDependOnExecutionOrDelivery keeps context assembly
// reusable by application and runtimes without creating a reverse dependency.
// Persistence-backed compaction is intentionally allowed to use db/sqlc until
// its store adapter is extracted.
func TestAgentContextDoesNotDependOnExecutionOrDelivery(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal/agent/context") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"internal/agent/application"),
				isPackageOrChild(imp, modulePrefix+"internal/agent/runtime"),
				isPackageOrChild(imp, modulePrefix+"internal/agent/tool"),
				isPackageOrChild(imp, modulePrefix+"internal/agent/adapter"):
				t.Errorf("%s imports %s: agent/context must not depend on Agent execution layers", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/channel"):
				t.Errorf("%s imports %s: agent/context must not depend on Channel", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/handlers"):
				t.Errorf("%s imports %s: agent/context must not depend on HTTP handlers", file, imp)
			case strings.HasPrefix(imp, "github.com/labstack/echo"):
				t.Errorf("%s imports Echo", file)
			case strings.HasPrefix(imp, "go.uber.org/fx"):
				t.Errorf("%s imports fx", file)
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
	for _, file := range goFiles(t, root, "internal/agent/runtime/native") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"internal/agent/application"):
				t.Errorf("%s imports %s: native runtime must not depend on Agent application orchestration", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/channel"):
				t.Errorf("%s imports %s: native runtime must not depend on Channel delivery", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/handlers"):
				t.Errorf("%s imports %s: native runtime must not depend on HTTP handlers", file, imp)
			}
		}
	}
}

// TestAgentToolStaysOnNeutralPorts prevents tool providers from reaching into
// orchestration or either delivery implementation.
func TestAgentToolStaysOnNeutralPorts(t *testing.T) {
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "internal/agent/tool") {
		for _, imp := range imports(t, root, file) {
			switch {
			case isPackageOrChild(imp, modulePrefix+"internal/agent/application"):
				t.Errorf("%s imports %s: Agent tools must not depend on application orchestration", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/channel"):
				t.Errorf("%s imports %s: Agent tools must use neutral messaging/contact ports", file, imp)
			case isPackageOrChild(imp, modulePrefix+"internal/handlers"):
				t.Errorf("%s imports %s: Agent tools must not depend on HTTP handlers", file, imp)
			}
		}
	}
}

// TestAgentRuntimeDoesNotDependOnChat keeps runtime snapshots and execution
// independent from Thread/Message/view implementations.
func TestAgentRuntimeDoesNotDependOnChat(t *testing.T) {
	root := repoRoot(t)
	for _, file := range allGoFiles(t, root, "internal/agent/runtime") {
		for _, imp := range imports(t, root, file) {
			if isPackageOrChild(imp, modulePrefix+"internal/chat") {
				t.Errorf("%s imports %s: Agent runtime must consume Agent-owned contracts", file, imp)
			}
		}
	}
}

func TestDirectExternalRuntimesDoNotDependOnACP(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{"internal/agent/runtime/codex", "internal/agent/runtime/claudecode"} {
		for _, file := range goFiles(t, root, dir) {
			for _, imp := range imports(t, root, file) {
				if isPackageOrChild(imp, modulePrefix+"internal/agent/runtime/acp") {
					t.Errorf("%s imports %s: direct External Agent runtimes must not depend on ACP", file, imp)
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
	for _, file := range allGoFiles(t, root, "internal/chat/thread") {
		data, err := os.ReadFile(filepath.Join(root, file)) //nolint:gosec // guard reads repository sources
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		for _, token := range forbidden {
			if strings.Contains(string(data), token) {
				t.Errorf("%s contains %q: route DB ownership belongs to internal/channel/route", file, token)
			}
		}
	}
	queryFile := filepath.Join(root, "db/postgres/queries/sessions.sql")
	data, err := os.ReadFile(queryFile) //nolint:gosec // fixed repository path
	if err != nil {
		t.Fatalf("read sessions.sql: %v", err)
	}
	for _, token := range forbidden {
		if strings.Contains(string(data), token) {
			t.Errorf("db/postgres/queries/sessions.sql contains %q: Thread queries must not join or mutate Route DB", token)
		}
	}
}

// TestChannelDoesNotImportEcho: webhook endpoints are the only HTTP surface
// the channel package owns (spec §8 exemption).
func TestChannelDoesNotImportEcho(t *testing.T) {
	exempt := map[string]string{
		"internal/channel/webhook_handler.go":                 "channel-owned webhook HTTP endpoint",
		"internal/channel/adapters/feishu/webhook_handler.go": "platform webhook endpoint",
		"internal/channel/adapters/line/adapter.go":           "platform webhook endpoint",
		"internal/channel/adapters/wechatoa/inbound.go":       "platform webhook endpoint",
		"internal/channel/adapters/weixin/qr_handler.go":      "weixin QR login HTTP endpoint",
	}
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal/channel") {
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
	for _, dir := range []string{"internal/channel", "internal/chat/timeline"} {
		for _, file := range goFiles(t, root, dir) {
			for _, imp := range imports(t, root, file) {
				if strings.HasPrefix(imp, "go.uber.org/fx") {
					t.Errorf("%s imports fx: assembly belongs to cmd/**", file)
				}
			}
		}
	}
}

// TestTurnPortStaysPure: the contract package (and its transports) must not
// depend on HTTP frameworks, assembly, generated SQL, the channel side, or
// the application implementation.
func TestTurnPortStaysPure(t *testing.T) {
	root := repoRoot(t)
	for _, file := range goFiles(t, root, "internal/agent/turn") {
		for _, imp := range imports(t, root, file) {
			switch {
			case strings.HasPrefix(imp, "github.com/labstack/echo"):
				t.Errorf("%s imports Echo", file)
			case strings.HasPrefix(imp, "go.uber.org/fx"):
				t.Errorf("%s imports fx", file)
			case imp == modulePrefix+"internal/db/postgres/sqlc":
				t.Errorf("%s imports generated sqlc", file)
			case imp == modulePrefix+"internal/channel" || strings.HasPrefix(imp, modulePrefix+"internal/channel/"):
				t.Errorf("%s imports the channel side", file)
			case turnForbiddenLayerImport(imp):
				t.Errorf("%s imports %s: the turn contract must not depend on retired domains, application, or runtime", file, imp)
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
		modulePrefix + "internal/agent/application",
		modulePrefix + "internal/agent/runtime",
	} {
		if isPackageOrChild(imp, packagePath) {
			return true
		}
	}
	return false
}

// TestRetiredDomainPackagesStayRemoved prevents compatibility imports from
// quietly recreating the pre-split package boundaries.
func TestRetiredDomainPackagesStayRemoved(t *testing.T) {
	root := repoRoot(t)
	for _, dir := range []string{
		"internal/acpagent",
		"internal/acpclient",
		"internal/acpfeedback",
		"internal/agentfeedback",
		"internal/acpprofile",
		"internal/agentpayload",
		"internal/conversation",
		"internal/decision",
		"internal/session",
		"internal/message",
		"internal/pipeline",
		"internal/toolapproval",
		"internal/userinput",
		"internal/contextfrag",
		"internal/historyfrag",
		"internal/compaction",
		"internal/contextlimit",
		"internal/sessionruntime",
		"internal/agent/tools",
	} {
		var files []string
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, entry os.DirEntry, err error) error {
			if os.IsNotExist(err) {
				return filepath.SkipDir
			}
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasSuffix(path, ".go") {
				files = append(files, path)
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("walk retired package %s: %v", dir, err)
		}
		if len(files) != 0 {
			t.Errorf("%s contains Go files after its responsibilities moved to agent/* or chat/*", dir)
		}
	}
}

// TestAgentRootContainsNoGoFiles reserves internal/agent as a package
// namespace. Executable code must live in one of its explicit subpackages.
func TestAgentRootContainsNoGoFiles(t *testing.T) {
	root := repoRoot(t)
	entries, err := os.ReadDir(filepath.Join(root, "internal/agent"))
	if err != nil {
		t.Fatalf("read internal/agent: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".go") {
			t.Errorf("internal/agent/%s: Agent root must not contain Go files", entry.Name())
		}
	}
}

// TestDefaultTeamIDReferences: business packages must not hardcode the
// single-team assumption. Each exemption is a deliberate singleton-team
// touchpoint that a hosted multi-team runtime replaces wholesale.
func TestDefaultTeamIDReferences(t *testing.T) {
	allowedPrefixes := []string{
		"cmd/",
		"internal/db/",
		"internal/team/",
	}
	exempt := map[string]string{
		"internal/memory/adapters/registry.go":               "memory provider registry resolves the singleton team when no scope is bound",
		"internal/memory/adapters/builtin/pgvector_index.go": "builtin pgvector index binds the singleton team resolver",
		"internal/channel/service.go":                        "configless channels (web/cli) synthesize a config; turn.Service fails closed on empty TeamID",
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
