package codex

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	openairesponses "github.com/felinics/twilight/provider/openai/responses"

	"github.com/felinics/memoh/internal/agent/runtime/external"
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
		return external.ModelCatalog{}, fmt.Errorf("list Codex models from custom Base URL: %w", err)
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
