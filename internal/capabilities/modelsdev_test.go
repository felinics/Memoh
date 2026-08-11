package capabilities

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/reasoning"
)

// writeCatalog builds a minimal models.dev checkout on disk. Fixtures rather than
// the real clone, so the test says what it depends on.
func writeCatalog(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(dir, "providers", rel)
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
			name:     "no controls means always on",
			provider: "minimax", modelID: "always-on",
			wantMode: models.ThinkingModeToggle, wantTiers: []string{},
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
