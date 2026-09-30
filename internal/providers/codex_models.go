package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/openaicatalog"
	"github.com/felinics/memoh/internal/reasoning"
)

const (
	codexDefaultBaseURL       = "https://chatgpt.com/backend-api"
	codexModelsClientVersion  = openaicatalog.ClientVersion
	codexModelsResponseLimit  = 8 << 20
	codexModelsErrorBodyLimit = 16 << 10
)

type codexModelsResponse struct {
	Models []openaicatalog.Model `json:"models"`
}

func (s *Service) fetchCodexRemoteModels(ctx context.Context, provider sqlc.Provider) ([]RemoteModel, error) {
	creds, err := s.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve Codex credentials: %w", err)
	}
	baseURL := strings.TrimSpace(configString(providerConfig(provider.Config), "base_url"))
	return s.listCodexRemoteModels(ctx, baseURL, creds)
}

func (s *Service) listCodexRemoteModels(ctx context.Context, baseURL string, creds ModelCredentials) ([]RemoteModel, error) {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = codexDefaultBaseURL
	}
	endpoint, err := url.Parse(strings.TrimRight(baseURL, "/") + "/codex/models")
	if err != nil {
		return nil, fmt.Errorf("parse Codex models URL: %w", err)
	}
	query := endpoint.Query()
	query.Set("client_version", codexModelsClientVersion)
	endpoint.RawQuery = query.Encode()

	requestCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create Codex models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+creds.APIKey)
	req.Header.Set("chatgpt-account-id", creds.CodexAccountID)
	req.Header.Set("OpenAI-Beta", "responses=experimental")
	req.Header.Set("originator", "codex_cli_rs")
	req.Header.Set("Accept", "application/json")

	httpClient := s.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: providerOAuthHTTPTimeout}
	}
	resp, err := httpClient.Do(req) //nolint:gosec // Provider base URLs are explicitly user-configurable throughout model discovery.
	if err != nil {
		return nil, fmt.Errorf("request Codex models: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, codexModelsErrorBodyLimit))
		detail := strings.TrimSpace(string(body))
		if detail == "" {
			detail = http.StatusText(resp.StatusCode)
		}
		return nil, fmt.Errorf("codex models request failed (%d): %s", resp.StatusCode, detail)
	}

	var catalog codexModelsResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, codexModelsResponseLimit))
	if err := decoder.Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decode Codex models response: %w", err)
	}

	return remoteModelsFromOpenAI(catalog.Models, models.ClientTypeOpenAICodex), nil
}

func remoteModelsFromOpenAI(catalog []openaicatalog.Model, clientType models.ClientType) []RemoteModel {
	remoteModels := make([]RemoteModel, 0, len(catalog))
	for _, model := range catalog {
		modelID := strings.TrimSpace(model.Slug)
		if modelID == "" || !strings.EqualFold(strings.TrimSpace(model.Visibility), "list") {
			continue
		}
		name := strings.TrimSpace(model.DisplayName)
		if name == "" {
			name = modelID
		}

		compatibilities := []string{models.CompatToolCall}
		if containsFold(model.InputModalities, "image") {
			compatibilities = append(compatibilities, models.CompatVision)
		}
		advertisedEfforts := make([]string, 0, len(model.SupportedReasoningLevels))
		for _, level := range model.SupportedReasoningLevels {
			advertisedEfforts = append(advertisedEfforts, strings.ToLower(strings.TrimSpace(level.Effort)))
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
		contextWindow := model.ContextWindow
		if contextWindow == nil {
			contextWindow = model.MaxContextWindow
		}

		var catalogOrder *int
		if clientType == models.ClientTypeOpenAIChatGPT {
			order := len(remoteModels)
			catalogOrder = &order
		}
		remoteModels = append(remoteModels, RemoteModel{
			ID:                modelID,
			Name:              name,
			DisplayName:       name,
			Object:            "model",
			OwnedBy:           string(clientType),
			Type:              string(models.ModelTypeChat),
			Compatibilities:   compatibilities,
			ReasoningEfforts:  reasoningEfforts,
			ThinkingMode:      thinkingMode,
			ContextWindow:     contextWindow,
			CatalogOrder:      catalogOrder,
			CapabilitiesKnown: true,
		})
	}
	return remoteModels
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
