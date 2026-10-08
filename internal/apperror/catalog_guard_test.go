package apperror

import (
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// updateGolden adds the codes missing from testdata/codes.golden and leaves
// every recorded status as it is. Run it after adding a code:
// go test ./internal/apperror -run TestCatalogGolden -update-golden.
var updateGolden = flag.Bool("update-golden", false, "add missing codes to testdata/codes.golden")

const goldenPath = "testdata/codes.golden"

// localeFiles are the Web and IM copy; both carry errors.* for every code.
var localeFiles = []string{
	"../../apps/web/src/i18n/locales/en.json",
	"../../apps/web/src/i18n/locales/zh.json",
	"../../apps/web/src/i18n/locales/ja.json",
	"../i18n/locales/en.json",
	"../i18n/locales/zh.json",
	"../i18n/locales/ja.json",
}

// declaredCodes parses error.go and returns every constant declared with type
// Code. The const block cannot be enumerated at runtime, so the source is the
// only complete list.
func declaredCodes(t *testing.T) map[Code]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "error.go", nil, 0)
	if err != nil {
		t.Fatalf("parse error.go: %v", err)
	}
	codes := make(map[Code]struct{})
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
			ident, ok := value.Type.(*ast.Ident)
			if !ok || ident.Name != "Code" {
				continue
			}
			for _, expr := range value.Values {
				lit, ok := expr.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					t.Fatalf("Code constant %v must be a string literal", value.Names)
				}
				text, err := strconv.Unquote(lit.Value)
				if err != nil {
					t.Fatalf("unquote %s: %v", lit.Value, err)
				}
				codes[Code(text)] = struct{}{}
			}
		}
	}
	if len(codes) == 0 {
		t.Fatal("no Code constants found in error.go")
	}
	return codes
}

func sortedCatalogCodes() []Code {
	codes := make([]Code, 0, len(catalog))
	for code := range catalog {
		codes = append(codes, code)
	}
	sort.Slice(codes, func(i, j int) bool { return codes[i] < codes[j] })
	return codes
}

// Every declared Code has a catalog entry and every catalog entry is a
// declared Code. PublicFrom refuses codes outside the catalog, so an
// undeclared code would surface as an opaque 500 at the transport boundary.
func TestCatalogCoversEveryDeclaredCode(t *testing.T) {
	t.Parallel()
	declared := declaredCodes(t)
	for code := range declared {
		if _, ok := catalog[code]; !ok {
			t.Errorf("Code %q is declared but has no catalog entry", code)
		}
	}
	for code := range catalog {
		if _, ok := declared[code]; !ok {
			t.Errorf("catalog entry %q has no Code constant", code)
		}
	}
	for code, definition := range catalog {
		if definition.HTTPStatus < 400 || definition.HTTPStatus > 599 {
			t.Errorf("catalog entry %q has HTTP status %d, want 4xx or 5xx", code, definition.HTTPStatus)
		}
		if strings.TrimSpace(definition.Detail) == "" {
			t.Errorf("catalog entry %q has an empty Detail", code)
		}
	}
}

// Dotted codes map to nested keys under errors.* in each locale file;
// dot-less codes are direct children. Every catalog code needs a leaf in every
// locale, and every leaf under errors.* must belong to a catalog code so stale
// copy is removed together with its code.
func TestLocaleCatalogAlignment(t *testing.T) {
	t.Parallel()
	for _, path := range localeFiles {
		path := path
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()
			raw, err := os.ReadFile(path) //nolint:gosec // repo-local locale file
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			var root map[string]json.RawMessage
			if err := json.Unmarshal(raw, &root); err != nil {
				t.Fatalf("decode %s: %v", path, err)
			}
			rawErrors, ok := root["errors"]
			if !ok {
				t.Fatalf("%s has no top-level \"errors\" object", path)
			}
			var errorsNode map[string]any
			if err := json.Unmarshal(rawErrors, &errorsNode); err != nil {
				t.Fatalf("decode %s errors: %v", path, err)
			}
			leaves := make(map[string]struct{})
			collectLeaves(errorsNode, "", leaves)
			for code := range catalog {
				if _, ok := leaves[string(code)]; !ok {
					t.Errorf("%s: missing errors.%s", filepath.Base(path), code)
				}
			}
			for leaf := range leaves {
				if _, ok := catalog[Code(leaf)]; !ok {
					t.Errorf("%s: errors.%s has no catalog code", filepath.Base(path), leaf)
				}
			}
		})
	}
}

func collectLeaves(node map[string]any, prefix string, out map[string]struct{}) {
	for key, value := range node {
		full := key
		if prefix != "" {
			full = prefix + "." + key
		}
		switch typed := value.(type) {
		case string:
			out[full] = struct{}{}
		case map[string]any:
			collectLeaves(typed, full, out)
		}
	}
}

// statusRestatement is a published status that was changed: codes.golden keeps
// from, the catalog declares to.
type statusRestatement struct {
	from, to int
}

// restatedStatuses are the published codes whose HTTP status was changed. A
// status is changed only when no client depends on it.
var restatedStatuses = map[Code]statusRestatement{
	// No HTTP route returned either code; WebSocket and IM frames carry no
	// status, and the RPC envelope is decoded by code.
	CodeAgentProviderAuthFailed:     {from: http.StatusUnauthorized, to: http.StatusBadGateway},
	CodeAgentProviderQuotaExhausted: {from: http.StatusPaymentRequired, to: http.StatusBadGateway},
}

// codes.golden is the append-only record of the public contract: a code and
// the status it was published with are never renamed or removed, and a
// status that differs from the record must be listed in restatedStatuses.
// Adding a code requires appending a line (run with -update-golden); any
// other difference fails.
func TestCatalogGolden(t *testing.T) {
	golden := make(map[Code]int)
	raw, err := os.ReadFile(goldenPath)
	switch {
	case err == nil:
		for lineNo, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			fields := strings.Split(line, "\t")
			if len(fields) != 2 {
				t.Fatalf("%s:%d: want \"code\\tstatus\", got %q", goldenPath, lineNo+1, line)
			}
			status, err := strconv.Atoi(fields[1])
			if err != nil {
				t.Fatalf("%s:%d: status %q is not a number", goldenPath, lineNo+1, fields[1])
			}
			golden[Code(fields[0])] = status
		}
	case !*updateGolden || !os.IsNotExist(err):
		t.Fatalf("read golden: %v (run with -update-golden to create it)", err)
	}
	if *updateGolden {
		var b strings.Builder
		b.Write(raw)
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			b.WriteByte('\n')
		}
		for _, code := range sortedCatalogCodes() {
			if _, ok := golden[code]; !ok {
				fmt.Fprintf(&b, "%s\t%d\n", code, catalog[code].HTTPStatus)
			}
		}
		if err := os.WriteFile(goldenPath, []byte(b.String()), 0o600); err != nil {
			t.Fatalf("write golden: %v", err)
		}
		return
	}
	for code, status := range golden {
		definition, ok := catalog[code]
		if !ok {
			t.Errorf("published code %q was removed from the catalog; keep it and mark it Deprecated instead", code)
			continue
		}
		if definition.HTTPStatus == status {
			continue
		}
		if restated := restatedStatuses[code]; restated.from != status || restated.to != definition.HTTPStatus {
			t.Errorf("published code %q changed HTTP status %d -> %d; add a new code instead, or list the change in restatedStatuses if no client depends on %d", code, status, definition.HTTPStatus, status)
		}
	}
	for code, restated := range restatedStatuses {
		if golden[code] != restated.from || catalog[code].HTTPStatus != restated.to {
			t.Errorf("restatedStatuses lists %q as %d -> %d, but codes.golden records %d and the catalog declares %d", code, restated.from, restated.to, golden[code], catalog[code].HTTPStatus)
		}
	}
	var missing []string
	for _, code := range sortedCatalogCodes() {
		if _, ok := golden[code]; !ok {
			missing = append(missing, fmt.Sprintf("%s\t%d", code, catalog[code].HTTPStatus))
		}
	}
	if len(missing) > 0 {
		t.Errorf("new codes are not recorded in %s; append these lines (or run with -update-golden):\n%s", goldenPath, strings.Join(missing, "\n"))
	}
}

// sessionCodes are the codes that answer 401. The Web client signs out on a
// 401, so only a missing or invalid Memoh session answers with it.
var sessionCodes = map[Code]struct{}{
	CodeHTTPUnauthorized:                       {},
	CodeContextLifecycleAuthenticationRequired: {},
}

func TestCatalogUnauthorizedIsSessionOnly(t *testing.T) {
	t.Parallel()
	for code, definition := range catalog {
		_, session := sessionCodes[code]
		if definition.HTTPStatus == http.StatusUnauthorized && !session {
			t.Errorf("catalog entry %q answers 401, which signs the Web client out; only a Memoh session failure may", code)
		}
		if session && definition.HTTPStatus != http.StatusUnauthorized {
			t.Errorf("session code %q answers %d, want 401", code, definition.HTTPStatus)
		}
	}
}

// declaredFaults is every catalog entry that declares its fault, and the fault
// it declares. A code whose status gives the wrong attribution is listed here;
// the reasons are in the catalog comments and docs/errors.md.
var declaredFaults = map[Code]Fault{
	CodeAgentProviderAuthFailed:            FaultDependency,
	CodeAgentProviderPermissionDenied:      FaultDependency,
	CodeAgentProviderQuotaExhausted:        FaultDependency,
	CodeAgentProviderRateLimited:           FaultDependency,
	CodeAgentProviderOverloaded:            FaultDependency,
	CodeAgentProviderRequestRejected:       FaultDependency,
	CodeAgentProviderUnreachable:           FaultDependency,
	CodeAgentResponseInterrupted:           FaultDependency,
	CodeAgentResponseTimeout:               FaultDependency,
	CodeRuntimePromptFailed:                FaultDependency,
	CodeExternalRuntimeSessionResumeFailed: FaultDependency,
	CodeExternalRuntimeUsageLimited:        FaultDependency,
	CodeACPConfigUpdateFailed:              FaultDependency,
	CodeExternalRuntimeRateLimited:         FaultDependency,
	CodeExternalRuntimeOverloaded:          FaultDependency,
	CodeExternalRuntimeUpstreamUnreachable: FaultDependency,
}

// providerCodePrefixes name the codes a model provider's answer produces. A
// provider is a dependency whatever status it answers with, so a new code
// under these prefixes must declare its fault rather than take the client
// fault its 4xx status would give.
var providerCodePrefixes = []string{"agent.provider_", "agent.response_"}

func TestCatalogDeclaredFaults(t *testing.T) {
	t.Parallel()
	for code, definition := range catalog {
		switch definition.Fault {
		case "", FaultClient, FaultServer, FaultDependency:
		default:
			t.Errorf("catalog entry %q declares unknown fault %q", code, definition.Fault)
		}
		if want, listed := declaredFaults[code]; definition.Fault != want {
			if listed {
				t.Errorf("catalog entry %q declares fault %q, want %q", code, definition.Fault, want)
			} else {
				t.Errorf("catalog entry %q declares fault %q; add it to declaredFaults", code, definition.Fault)
			}
		}
		if definition.Fault == FaultDependency && definition.HTTPStatus < http.StatusInternalServerError && definition.HTTPStatus != http.StatusTooManyRequests {
			t.Errorf("dependency code %q answers %d; a dependency's failure answers 5xx, or 429 when the client should back off", code, definition.HTTPStatus)
		}
		for _, prefix := range providerCodePrefixes {
			if strings.HasPrefix(string(code), prefix) && definition.Fault != FaultDependency {
				t.Errorf("provider code %q declares fault %q, want %q", code, definition.Fault, FaultDependency)
			}
		}
	}
	for code := range declaredFaults {
		if _, ok := catalog[code]; !ok {
			t.Errorf("declaredFaults lists %q, which is not in the catalog", code)
		}
	}
}
