package capabilities

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/memohai/memoh/internal/models"
	"github.com/memohai/memoh/internal/reasoning"
)

// models.dev describes a model's thinking control as a list of discriminated
// options rather than a spread of boolean flags. That difference is the reason to
// prefer it: the shape a provider speaks — a toggle, a tier list, a token budget —
// is a first-class value here, while LiteLLM could only report which OpenAI-wire
// flags a model happened to set.
//
// The three option types, from the upstream schema:
//
//	toggle        the model has a dedicated on/off switch
//	effort        the model takes named tiers; the list is authoritative
//	budget_tokens the model takes a token budget, with min/max bounds
//
// An empty list is meaningful and distinct from an absent one: it declares that
// the model reasons but exposes no control at all (deepseek-reasoner, MiniMax
// M2.x). That is a fact we previously had to discover by reading two other
// catalogs by hand.
type modelsDevOption struct {
	Type   string   `toml:"type"`
	Values []string `toml:"values"`
	Min    *int     `toml:"min"`
	Max    *int     `toml:"max"`
}

type modelsDevEntry struct {
	Name             string             `toml:"name"`
	Reasoning        *bool              `toml:"reasoning"`
	ToolCall         *bool              `toml:"tool_call"`
	ReasoningOptions *[]modelsDevOption `toml:"reasoning_options"`

	Limit struct {
		Context *int `toml:"context"`
	} `toml:"limit"`

	Modalities struct {
		Input []string `toml:"input"`
	} `toml:"modalities"`
}

// ModelsDevSource resolves capabilities from a local models.dev checkout.
//
// It reads the repository's TOML rather than the published api.json: the JSON is a
// single 3.6 MB document behind a host that is unreliable from some networks,
// while the repository is a git clone that can be pinned, diffed and vendored.
// Both carry the same fields.
type ModelsDevSource struct {
	// entries is keyed by "provider/model-id", both normalized.
	entries map[string]modelsDevEntry
}

// providerAliases maps our template names onto models.dev provider directories.
// Only the ones that genuinely differ are listed; everything else matches.
var providerAliases = map[string]string{
	"moonshot": "moonshotai",
	"qwen":     "alibaba",
}

// NewModelsDevSource loads every model TOML under dir/providers. dir is the root
// of a models.dev checkout.
func NewModelsDevSource(dir string) (*ModelsDevSource, error) {
	root := filepath.Join(dir, "providers")
	src := &ModelsDevSource{entries: make(map[string]modelsDevEntry, 4096)}

	providers, err := os.ReadDir(root)
	if err != nil {
		return nil, fmt.Errorf("read models.dev providers: %w", err)
	}
	for _, provider := range providers {
		if !provider.IsDir() {
			continue
		}
		modelsDir := filepath.Join(root, provider.Name(), "models")
		// OpenRouter nests its models one level deeper, by vendor.
		err := filepath.WalkDir(modelsDir, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil || d.IsDir() || !strings.HasSuffix(path, ".toml") {
				return nil //nolint:nilerr // a provider without models is not an error
			}
			var entry modelsDevEntry
			if _, decErr := toml.DecodeFile(path, &entry); decErr != nil {
				// One bad file must not cost us the whole catalog. Upstream carries
				// 184 providers, most of which we never sync, and a case-insensitive
				// filesystem can surface a path it then cannot open (two vendor
				// directories differing only in case collapse onto one). Skipping
				// leaves the model unknown, which callers already handle.
				return nil //nolint:nilerr // a single unreadable entry is not fatal
			}
			rel, relErr := filepath.Rel(modelsDir, path)
			if relErr != nil {
				return relErr
			}
			modelID := strings.TrimSuffix(rel, ".toml")
			src.entries[entryKey(provider.Name(), modelID)] = entry
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	if len(src.entries) == 0 {
		return nil, fmt.Errorf("no models found under %s", root)
	}
	return src, nil
}

// Count reports how many model entries were loaded.
func (s *ModelsDevSource) Count() int { return len(s.entries) }

// Resolve returns the capabilities for a model under a provider template, and
// whether the catalog knows it.
func (s *ModelsDevSource) Resolve(provider, modelID string) (Capabilities, bool) {
	dir := provider
	if alias, ok := providerAliases[provider]; ok {
		dir = alias
	}
	entry, ok := s.entries[entryKey(dir, modelID)]
	if !ok {
		return Capabilities{}, false
	}
	return deriveFromModelsDev(entry), true
}

// entryKey normalizes an id so that cosmetic differences between our templates and
// models.dev do not read as missing data. Two real cases: xAI ships the same model
// as grok-4.20-beta-0309 and grok-4.20-0309, and vendors append release dates that
// one catalog keeps and the other drops.
//
// Normalization is deliberately narrow. Anything more aggressive risks collapsing
// two genuinely different models onto one entry, which would silently give a model
// another's capabilities — worse than reporting it as unknown.
func entryKey(provider, modelID string) string {
	id := strings.ToLower(strings.TrimSpace(modelID))
	id = strings.ReplaceAll(id, "-beta-", "-")
	id = strings.TrimSuffix(id, "-beta")
	return provider + "/" + id
}

// deriveFromModelsDev maps a models.dev entry onto our capability shape.
func deriveFromModelsDev(e modelsDevEntry) Capabilities {
	caps := Capabilities{
		ToolCall:      e.ToolCall,
		ContextWindow: e.Limit.Context,
	}
	if len(e.Modalities.Input) > 0 {
		vision := containsModality(e.Modalities.Input, "image")
		fileInput := containsModality(e.Modalities.Input, "pdf")
		caps.Vision = &vision
		caps.FileInput = &fileInput
	}

	// The option list is the stronger signal and must be read whenever present.
	// Most of the catalog carries options without also setting the top-level
	// reasoning flag — 1631 of 2737 entries at the time of writing — so keying off
	// the flag alone would discard the controls for the majority of models,
	// including every Gemini 2.5 entry.
	if e.ReasoningOptions == nil {
		switch {
		case e.Reasoning == nil:
			// Silent both ways: leave the mode unknown so a caller keeps whatever it
			// already had rather than being told "no reasoning".
			return caps
		case !*e.Reasoning:
			caps.ThinkingMode = models.ThinkingModeNone
			return caps
		default:
			// Claims reasoning but describes no control. Nothing to say about tiers.
			caps.ThinkingMode = models.ThinkingModeToggle
			return caps
		}
	}
	if e.Reasoning != nil && !*e.Reasoning {
		// Contradictory: options present but reasoning explicitly false. Trust the
		// explicit denial and report no thinking rather than inventing a control.
		caps.ThinkingMode = models.ThinkingModeNone
		return caps
	}

	caps.ThinkingMode = models.ThinkingModeToggle
	opts := *e.ReasoningOptions
	if len(opts) == 0 {
		// Reasons, but exposes no control: always on. Declaring no tiers and no off
		// switch is what keeps a picker from offering either.
		caps.EffortLevels = []string{}
		caps.ReasoningOffSupport = reasoning.OffSupportRejected
		return caps
	}

	var tiers []string
	hasToggle, hasBudget := false, false
	for _, opt := range opts {
		switch opt.Type {
		case "toggle":
			hasToggle = true
		case "effort":
			tiers = append(tiers, opt.Values...)
		case "budget_tokens":
			hasBudget = true
			caps.ThinkingBudgetMin = opt.Min
			caps.ThinkingBudgetMax = opt.Max
		}
	}

	// Which shape to *send* is not always what upstream reports as *accepted*.
	// Claude 4.6 lists both effort and budget_tokens because the API takes both,
	// but budget_tokens is deprecated there and rejected outright on 4.7+, so the
	// tier dialect is the correct choice and a naive "budget wins" rule would
	// downgrade it. When a model offers tiers, they are the dialect; a budget is
	// only the dialect when it is the sole control.
	switch {
	case len(tiers) > 0:
		caps.ReasoningDialect = reasoning.DialectTier
	case hasBudget:
		caps.ReasoningDialect = reasoning.DialectBudget
	}

	caps.EffortLevels = normalizeTiers(tiers, hasToggle)
	return caps
}

// normalizeTiers turns models.dev's tier vocabulary into ours. Upstream spells off
// as a member of the tier list ("none", OpenAI's wire word) while we keep it as a
// separate capability token, so the two spellings collapse onto ours. A dedicated
// toggle option means the same thing by another route.
func normalizeTiers(tiers []string, hasToggle bool) []string {
	out := make([]string, 0, len(tiers)+1)
	canDisable := hasToggle
	for _, tier := range tiers {
		tier = strings.ToLower(strings.TrimSpace(tier))
		switch tier {
		case reasoning.EffortNone, reasoning.EffortDisable:
			canDisable = true
			continue
		case "default":
			// Upstream's marker for "let the model choose"; not a tier a user picks.
			continue
		case "":
			continue
		}
		if !containsFold(out, tier) {
			out = append(out, tier)
		}
	}
	ordered := make([]string, 0, len(out)+1)
	if canDisable {
		ordered = append(ordered, reasoning.EffortDisable)
	}
	// Emit tiers weakest-to-strongest regardless of the order upstream listed them.
	for _, tier := range reasoning.OrderedEfforts() {
		if containsFold(out, tier) {
			ordered = append(ordered, tier)
		}
	}
	// Anything upstream knows and we do not is dropped rather than passed through:
	// an unknown tier would fail validation on write.
	return ordered
}

func containsModality(list []string, want string) bool {
	return containsFold(list, want)
}

func containsFold(list []string, want string) bool {
	for _, v := range list {
		if strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}
