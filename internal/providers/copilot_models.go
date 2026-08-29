package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	memohcopilot "github.com/felinics/memoh/internal/copilot"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/reasoning"
)

const (
	copilotModelsResponseLimit  = 8 << 20
	copilotModelsErrorBodyLimit = 16 << 10
)

type copilotModelsResponse struct {
	Data []copilotModelInfo `json:"data"`
}

type copilotModelInfo struct {
	ID                 string                   `json:"id"`
	Name               string                   `json:"name"`
	Vendor             string                   `json:"vendor"`
	ModelPickerEnabled bool                     `json:"model_picker_enabled"`
	Policy             copilotModelPolicy       `json:"policy"`
	SupportedEndpoints []string                 `json:"supported_endpoints"`
	Capabilities       copilotModelCapabilities `json:"capabilities"`
}

type copilotModelPolicy struct {
	State string `json:"state"`
}

type copilotModelCapabilities struct {
	Type     string               `json:"type"`
	Limits   copilotModelLimits   `json:"limits"`
	Supports copilotModelSupports `json:"supports"`
}

type copilotModelLimits struct {
	MaxContextWindowTokens *int `json:"max_context_window_tokens"`
}

type copilotModelSupports struct {
	ToolCalls       bool     `json:"tool_calls"`
	Vision          bool     `json:"vision"`
	ReasoningEffort []string `json:"reasoning_effort"`
}

func (s *Service) fetchGitHubCopilotModels(ctx context.Context, provider sqlc.Provider) ([]RemoteModel, error) {
	// Model discovery is authorized by the long-lived GitHub OAuth token. The
	// short-lived Copilot inference token is valid for generation requests but
	// is rejected by the account-scoped /models endpoint.
	githubToken, err := s.GetValidAccessToken(ctx, provider.ID.String())
	if err != nil {
		return nil, fmt.Errorf("resolve GitHub Copilot OAuth token: %w", err)
	}
	return s.listGitHubCopilotRemoteModels(ctx, memohcopilot.DefaultAPIBaseURL, githubToken)
}

func (s *Service) listGitHubCopilotRemoteModels(ctx context.Context, baseURL, githubToken string) ([]RemoteModel, error) {
	endpoint, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/") + "/models")
	if err != nil {
		return nil, fmt.Errorf("parse GitHub Copilot models URL: %w", err)
	}

	requestCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create GitHub Copilot models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(githubToken))
	req.Header.Set("Accept", "application/json")

	resp, err := memohcopilot.NewHTTPClient(s.httpClient).Do(req) //nolint:gosec // The production endpoint is fixed; the parameter exists for isolated tests.
	if err != nil {
		return nil, fmt.Errorf("request GitHub Copilot models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, copilotModelsErrorBodyLimit))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("github copilot models request failed (%d): %s", resp.StatusCode, detail)
	}

	var catalog copilotModelsResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, copilotModelsResponseLimit))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode GitHub Copilot models response: %w", err)
	}

	// Some OAuth clients receive a catalog where every picker flag is false,
	// even though policy still identifies usable models. Honor picker flags when
	// the response contains an enabled picker model; otherwise fall back to all
	// models that are not explicitly disabled by account policy.
	hasPickerModels := false
	for _, model := range catalog.Data {
		if model.ModelPickerEnabled {
			hasPickerModels = true
			break
		}
	}

	remoteModels := make([]RemoteModel, 0, len(catalog.Data))
	for _, model := range catalog.Data {
		modelID := strings.TrimSpace(model.ID)
		if modelID == "" || strings.EqualFold(strings.TrimSpace(model.Policy.State), "disabled") {
			continue
		}
		if hasPickerModels && !model.ModelPickerEnabled {
			continue
		}
		modelType := strings.ToLower(strings.TrimSpace(model.Capabilities.Type))
		if modelType != "" && modelType != string(models.ModelTypeChat) {
			continue
		}
		// Older catalog responses omit supported_endpoints for ordinary chat
		// models. A non-empty endpoint list is authoritative, so reject models
		// that can only use transports Twilight's Copilot provider lacks.
		if len(model.SupportedEndpoints) > 0 && !containsFold(model.SupportedEndpoints, "/chat/completions") {
			continue
		}

		name := strings.TrimSpace(model.Name)
		if name == "" {
			name = modelID
		}
		compatibilities := make([]string, 0, 3)
		if model.Capabilities.Supports.ToolCalls {
			compatibilities = append(compatibilities, models.CompatToolCall)
		}
		if model.Capabilities.Supports.Vision {
			compatibilities = append(compatibilities, models.CompatVision)
		}
		advertisedEfforts := make([]string, 0, len(model.Capabilities.Supports.ReasoningEffort))
		for _, effort := range model.Capabilities.Supports.ReasoningEffort {
			advertisedEfforts = append(advertisedEfforts, strings.ToLower(strings.TrimSpace(effort)))
		}
		reasoningEfforts := make([]string, 0, len(advertisedEfforts))
		for _, effort := range reasoning.NormalizeAdvertised(advertisedEfforts) {
			if models.IsValidReasoningEffort(effort) && !containsFold(reasoningEfforts, effort) {
				reasoningEfforts = append(reasoningEfforts, effort)
			}
		}
		thinkingMode := models.ThinkingModeNone
		if len(reasoningEfforts) > 0 {
			compatibilities = append(compatibilities, models.CompatReasoning)
			thinkingMode = models.ThinkingModeToggle
		}

		remoteModels = append(remoteModels, RemoteModel{
			ID:                modelID,
			Name:              name,
			DisplayName:       name,
			Object:            "model",
			OwnedBy:           strings.TrimSpace(model.Vendor),
			Type:              string(models.ModelTypeChat),
			Compatibilities:   compatibilities,
			ReasoningEfforts:  reasoningEfforts,
			ThinkingMode:      thinkingMode,
			ContextWindow:     model.Capabilities.Limits.MaxContextWindowTokens,
			CapabilitiesKnown: true,
		})
	}
	return remoteModels, nil
}
