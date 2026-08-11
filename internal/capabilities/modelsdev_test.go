package capabilities

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/reasoning"
)

// writeCatalog builds a minimal models.dev checkout on disk. Fixtures rather than
// the real clone, so the test says what it depends on.
//
// A key starting with "models/" lands in the canonical tree; everything else is a
// provider overlay under providers/.
func writeCatalog(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, "providers", rel)
		if strings.HasPrefix(rel, "models/") {
			path = filepath.Join(dir, rel)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func TestModelsDevDerivesTheWireShape(t *testing.T) {
	t.Parallel()

	dir := writeCatalog(t, map[string]string{
		// Tiers plus a budget: the API accepts both, but tiers are what a caller
		// picks and what should reach the wire. Claude 4.6 is the live example —
		// budget_tokens is deprecated there and rejected on 4.7+.
		"anthropic/models/claude-x.toml": `
reasoning = true
[[reasoning_options]]
type = "effort"
values = ["low", "medium", "high", "max"]
[[reasoning_options]]
type = "budget_tokens"
min = 1024
`,
		// Budget only: the model wants a number, and the bounds decide off-ability.
		"google/models/gemini-budget.toml": `
[[reasoning_options]]
type = "budget_tokens"
min = 128
max = 32768
`,
		// Tiers including upstream's own spelling of off.
		"openai/models/gpt-tiers.toml": `
reasoning = true
[[reasoning_options]]
type = "effort"
values = ["none", "low", "medium", "high", "xhigh"]
`,
		// Reasons but exposes no control: always on.
		"minimax/models/always-on.toml": `
reasoning = true
reasoning_options = []
`,
		// Explicitly no reasoning.
		"openai/models/plain.toml": `
reasoning = false
`,
		// Silent on both: nothing may be concluded.
		"openai/models/silent.toml": `
name = "Quiet"
`,
	})

	src, err := NewModelsDevSource(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	cases := []struct {
		name        string
		provider    string
		modelID     string
		wantMode    string
		wantDialect string
		wantTiers   []string
		wantOff     string
	}{
		{
			name:     "tiers win the dialect even when a budget is also accepted",
			provider: "anthropic", modelID: "claude-x",
			wantMode: models.ThinkingModeToggle, wantDialect: reasoning.DialectTier,
			wantTiers: []string{"low", "medium", "high", "max"},
		},
		{
			name:     "a budget-only model takes the budget dialect",
			provider: "google", modelID: "gemini-budget",
			wantMode: models.ThinkingModeToggle, wantDialect: reasoning.DialectBudget,
			wantTiers: []string{},
		},
		{
			// Upstream lists off inside the tier list; we keep it as a capability
			// token, so the two spellings collapse onto ours and off leads.
			name:     "upstream's off tier becomes our disable token",
			provider: "openai", modelID: "gpt-tiers",
			wantMode: models.ThinkingModeToggle, wantDialect: reasoning.DialectTier,
			wantTiers: []string{"disable", "low", "medium", "high", "xhigh"},
		},
		{
			// Its own mode, not a toggle with an empty list: "you cannot turn this
			// off" and "you can turn it off but there are no tiers" are different
			// things to tell a user.
			name:     "no controls at all is the always mode",
			provider: "minimax", modelID: "always-on",
			wantMode: reasoning.ModeAlways, wantTiers: []string{},
			wantOff: reasoning.OffSupportRejected,
		},
		{
			name:     "an explicit denial is reported as no thinking",
			provider: "openai", modelID: "plain",
			wantMode: models.ThinkingModeNone,
		},
		{
			name:     "silence leaves the mode unknown",
			provider: "openai", modelID: "silent",
			wantMode: "",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			caps, ok := src.Resolve(tt.provider, tt.modelID)
			if !ok {
				t.Fatalf("%s/%s not found", tt.provider, tt.modelID)
			}
			if caps.ThinkingMode != tt.wantMode {
				t.Errorf("mode = %q, want %q", caps.ThinkingMode, tt.wantMode)
			}
			if caps.ReasoningDialect != tt.wantDialect {
				t.Errorf("dialect = %q, want %q", caps.ReasoningDialect, tt.wantDialect)
			}
			if tt.wantTiers != nil && !slices.Equal(caps.EffortLevels, tt.wantTiers) {
				t.Errorf("tiers = %v, want %v", caps.EffortLevels, tt.wantTiers)
			}
			if caps.ReasoningOffSupport != tt.wantOff {
				t.Errorf("off support = %q, want %q", caps.ReasoningOffSupport, tt.wantOff)
			}
		})
	}
}

// The majority of the catalog carries reasoning_options without also setting the
// top-level reasoning flag. Keying off the flag would discard the controls for most
// models, every Gemini 2.5 entry among them.
func TestModelsDevReadsOptionsWithoutTheReasoningFlag(t *testing.T) {
	t.Parallel()

	dir := writeCatalog(t, map[string]string{
		"google/models/no-flag.toml": `
name = "Options but no flag"
[[reasoning_options]]
type = "budget_tokens"
min = 128
max = 24576
`,
	})
	src, err := NewModelsDevSource(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	caps, ok := src.Resolve("google", "no-flag")
	if !ok {
		t.Fatal("model not found")
	}
	if caps.ThinkingMode != models.ThinkingModeToggle {
		t.Errorf("mode = %q, want toggle", caps.ThinkingMode)
	}
	if caps.ThinkingBudgetMin == nil || *caps.ThinkingBudgetMin != 128 {
		t.Errorf("budget min = %v, want 128", caps.ThinkingBudgetMin)
	}
	if caps.ThinkingBudgetMax == nil || *caps.ThinkingBudgetMax != 24576 {
		t.Errorf("budget max = %v, want 24576", caps.ThinkingBudgetMax)
	}
}

// Provider directory names differ for two of ours, and ids differ cosmetically for
// xAI. Both are lookup concerns, not data gaps — treating them as gaps is how a
// coverage comparison ends up wrong.
func TestModelsDevResolvesAliasesAndCosmeticIDs(t *testing.T) {
	t.Parallel()

	dir := writeCatalog(t, map[string]string{
		"moonshotai/models/kimi.toml":              "reasoning = true\nreasoning_options = []\n",
		"alibaba/models/qwen-x.toml":               "reasoning = true\nreasoning_options = []\n",
		"xai/models/grok-4.20-0309-reasoning.toml": "reasoning = true\nreasoning_options = []\n",
		// OpenRouter nests by vendor.
		"openrouter/models/openai/gpt-x.toml": "reasoning = true\nreasoning_options = []\n",
	})
	src, err := NewModelsDevSource(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	for _, tt := range []struct{ provider, modelID string }{
		{"moonshot", "kimi"},                     // template name differs from the directory
		{"qwen", "qwen-x"},                       // ditto
		{"xai", "grok-4.20-beta-0309-reasoning"}, // our id carries a -beta- marker
		{"openrouter", "openai/gpt-x"},           // nested path
	} {
		if _, ok := src.Resolve(tt.provider, tt.modelID); !ok {
			t.Errorf("%s/%s should resolve", tt.provider, tt.modelID)
		}
	}
}

// A single unreadable file must not cost the whole catalog: upstream ships 184
// providers, most of which we never sync, and a case-insensitive filesystem can
// surface a path it cannot then open.
func TestModelsDevSkipsUnreadableEntries(t *testing.T) {
	t.Parallel()

	dir := writeCatalog(t, map[string]string{
		"openai/models/good.toml": "reasoning = true\nreasoning_options = []\n",
		"openai/models/bad.toml":  "this is not = valid = toml [[[\n",
	})
	src, err := NewModelsDevSource(dir)
	if err != nil {
		t.Fatalf("a broken entry should not fail the load: %v", err)
	}
	if _, ok := src.Resolve("openai", "good"); !ok {
		t.Error("the readable entry should still resolve")
	}
	if _, ok := src.Resolve("openai", "bad"); ok {
		t.Error("the broken entry should be absent, not guessed at")
	}
}

// Provider entries are overlays: they carry what differs and inherit the rest.
// Of 2994 overlays in the real catalog, 2751 have no reasoning flag of their own,
// 2255 no modalities and 1608 no context limit — so reading them without resolving
// base_model misses most of the capability data.
func TestModelsDevResolvesBaseModelInheritance(t *testing.T) {
	t.Parallel()

	dir := writeCatalog(t, map[string]string{
		"models/google/gemini-x.toml": `
name = "Gemini X"
reasoning = true
tool_call = true
[[reasoning_options]]
type = "budget_tokens"
min = 0
max = 24576
[limit]
context = 1048576
[modalities]
input = ["text", "image", "pdf"]
`,
		// The overlay states only what differs for this provider.
		"google/models/gemini-x.toml": `
base_model = "google/gemini-x"
[cost]
input = 0.30
`,
		// An overlay may also override: what it says wins over the base.
		"openrouter/models/google/gemini-x.toml": `
base_model = "google/gemini-x"
[[reasoning_options]]
type = "effort"
values = ["low", "high"]
`,
	})

	src, err := NewModelsDevSource(dir)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	inherited, ok := src.Resolve("google", "gemini-x")
	if !ok {
		t.Fatal("overlay should resolve")
	}
	if inherited.ThinkingMode != models.ThinkingModeToggle {
		t.Errorf("mode = %q, want toggle (inherited)", inherited.ThinkingMode)
	}
	if inherited.ReasoningDialect != reasoning.DialectBudget {
		t.Errorf("dialect = %q, want budget (inherited)", inherited.ReasoningDialect)
	}
	if inherited.ContextWindow == nil || *inherited.ContextWindow != 1048576 {
		t.Errorf("context = %v, want 1048576 (inherited)", inherited.ContextWindow)
	}
	if inherited.Vision == nil || !*inherited.Vision {
		t.Error("vision should be inherited from the base modalities")
	}
	if inherited.ThinkingBudgetMax == nil || *inherited.ThinkingBudgetMax != 24576 {
		t.Errorf("budget max = %v, want 24576 (inherited)", inherited.ThinkingBudgetMax)
	}

	// The overlay's own options replace the base's rather than merging with them:
	// a provider that exposes different controls is stating a fact about itself.
	overridden, ok := src.Resolve("openrouter", "google/gemini-x")
	if !ok {
		t.Fatal("nested overlay should resolve")
	}
	if overridden.ReasoningDialect != reasoning.DialectTier {
		t.Errorf("dialect = %q, want tier (overlay wins)", overridden.ReasoningDialect)
	}
	if !slices.Equal(overridden.EffortLevels, []string{"low", "high"}) {
		t.Errorf("tiers = %v, want [low high] (overlay wins)", overridden.EffortLevels)
	}
	// Fields the overlay is silent about still come from the base.
	if overridden.ContextWindow == nil || *overridden.ContextWindow != 1048576 {
		t.Errorf("context = %v, want 1048576 (still inherited)", overridden.ContextWindow)
	}
}
