package codex

import (
	"context"
	"net/http"
	"strings"

	openairesponses "github.com/felinics/twilight/provider/openai/responses"

	"github.com/felinics/memoh/internal/agent/runtime/external"
	"github.com/felinics/memoh/internal/errs"
	modelspkg "github.com/felinics/memoh/internal/models"
)

// customBaseURLModelCatalog asks the configured OpenAI-compatible endpoint for
// its actual model IDs. Codex's native model/list catalog describes OpenAI and
// ChatGPT availability; it cannot represent a relay with a different catalog.
func customBaseURLModelCatalog(ctx context.Context, cfg Config, httpClient *http.Client) (external.ModelCatalog, error) {
	if httpClient == nil {
		httpClient = modelspkg.NewProviderHTTPClient(modelspkg.DefaultProviderProbeTimeout)
	}
	provider := openairesponses.New(
		openairesponses.WithAPIKey(cfg.APIKey),
		openairesponses.WithBaseURL(strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")),
		openairesponses.WithHTTPClient(httpClient),
	)
	available, err := provider.ListModels(ctx)
	if err != nil {
		return external.ModelCatalog{}, errs.WrapDependency(err, "list Codex models from custom Base URL")
	}

	models := make([]external.ModelOption, 0, len(available))
	seen := make(map[string]struct{}, len(available))
	for _, model := range available {
		id := strings.TrimSpace(model.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		name := strings.TrimSpace(model.DisplayName)
		if name == "" {
			name = id
		}
		models = append(models, external.ModelOption{
			ID:               id,
			Name:             name,
			ReasoningEfforts: []external.ReasoningEffortOption{},
		})
	}

	return external.ModelCatalog{
		Models:                    models,
		ConfiguredModelID:         cfg.Model,
		ConfiguredReasoningEffort: cfg.ReasoningEffort,
	}, nil
}

// genericReasoningEfforts are the Responses API levels offered for a model
// Codex has no metadata for. Whether the endpoint honors, remaps, or rejects
// one is its own decision, so none of them is ever the default. `ultra` is
// left out: Codex rewrites it to `medium` for such a model before sending.
var genericReasoningEfforts = []string{"low", "medium", "high", "xhigh", "max"}

// applyNativeReasoning gives each endpoint model the reasoning levels Codex
// itself will assume for that ID. Models Codex has no metadata for get the
// generic levels without a default, which leaves the effort to the endpoint
// until the user picks one.
func applyNativeReasoning(models, native []external.ModelOption) {
	generic := make([]external.ReasoningEffortOption, 0, len(genericReasoningEfforts))
	for _, id := range genericReasoningEfforts {
		generic = append(generic, external.ReasoningEffortOption{ID: id, Name: id})
	}
	for i := range models {
		models[i].ReasoningEfforts = generic
		if match, ok := nativeModelFor(models[i].ID, native); ok {
			models[i].DefaultReasoningEffort = match.DefaultReasoningEffort
			models[i].ReasoningEfforts = match.ReasoningEfforts
		}
	}
}

// nativeModelFor follows Codex's own metadata lookup for a model ID
// (models-manager construct_model_info_from_candidates): the longest catalog
// ID that prefixes it, then one retry without a leading `provider/` segment.
// Matching any other way would show levels the runtime does not apply.
func nativeModelFor(id string, native []external.ModelOption) (external.ModelOption, bool) {
	if match, ok := longestPrefixModel(id, native); ok {
		return match, true
	}
	namespace, suffix, found := strings.Cut(id, "/")
	if !found || namespace == "" || strings.Contains(suffix, "/") {
		return external.ModelOption{}, false
	}
	for _, r := range namespace {
		isSimple := r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !isSimple {
			return external.ModelOption{}, false
		}
	}
	return longestPrefixModel(suffix, native)
}

func longestPrefixModel(id string, native []external.ModelOption) (external.ModelOption, bool) {
	var best external.ModelOption
	found := false
	for _, candidate := range native {
		if !strings.HasPrefix(id, candidate.ID) || (found && len(candidate.ID) <= len(best.ID)) {
			continue
		}
		best, found = candidate, true
	}
	return best, found
}
