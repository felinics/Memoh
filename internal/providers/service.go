package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providertemplates"
	"github.com/felinics/memoh/internal/registry"
)

// Service handles provider operations.
type Service struct {
	queries      dbstore.Queries
	logger       *slog.Logger
	httpClient   *http.Client
	callbackURL  string
	templatesDir string
}

// NewService creates a new provider service.
func NewService(log *slog.Logger, queries dbstore.Queries, callbackURL string, templatesDir ...string) *Service {
	if log == nil {
		log = slog.Default()
	}
	var dir string
	if len(templatesDir) > 0 {
		dir = templatesDir[0]
	}
	return &Service{
		queries:      queries,
		logger:       log.With(slog.String("service", "providers")),
		httpClient:   &http.Client{Timeout: providerOAuthHTTPTimeout},
		callbackURL:  callbackURL,
		templatesDir: dir,
	}
}

// Create creates a new provider.
func (s *Service) Create(ctx context.Context, req CreateRequest) (GetResponse, error) {
	metadataJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal metadata: %w", err)
	}

	clientType := req.ClientType
	if clientType == "" {
		clientType = string(models.ClientTypeOpenAICompletions)
	}
	configJSON, err := json.Marshal(normalizeProviderConfig(clientType, req.Config))
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	var icon pgtype.Text
	if req.Icon != "" {
		icon = pgtype.Text{String: req.Icon, Valid: true}
	}

	provider, err := s.queries.CreateProvider(ctx, sqlc.CreateProviderParams{
		Name:       req.Name,
		ClientType: clientType,
		Icon:       icon,
		Enable:     true,
		Config:     configJSON,
		Metadata:   metadataJSON,
	})
	if err != nil {
		if isProviderNameConflict(err) {
			if provider, ok, activateErr := s.activateHiddenRegistryTemplate(ctx, req, clientType, icon, configJSON, metadataJSON); ok {
				if activateErr != nil {
					return GetResponse{}, activateErr
				}
				return s.toGetResponse(provider), nil
			}
		}
		return GetResponse{}, fmt.Errorf("create provider: %w", err)
	}

	return s.toGetResponse(provider), nil
}

func (s *Service) CreateFromTemplate(ctx context.Context, req CreateFromTemplateRequest) (GetResponse, error) {
	expectedDomain := providertemplates.Domain(strings.TrimSpace(req.Domain))
	if expectedDomain != "" && !providertemplates.IsValidDomain(expectedDomain) {
		return GetResponse{}, apperror.New(apperror.CodeProviderTemplateDomainInvalid, nil)
	}
	template, err := providertemplates.Resolve(ctx, s.queries, req.TemplateID, expectedDomain)
	if err != nil {
		return GetResponse{}, err
	}
	switch providertemplates.Domain(template.Domain) {
	case providertemplates.DomainLLM, providertemplates.DomainSpeech, providertemplates.DomainTranscription, providertemplates.DomainVideo:
	default:
		return GetResponse{}, apperror.New(apperror.CodeProviderTemplateDomainMismatch, nil)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = template.Name
	}
	config := providertemplates.MergeConfig(providertemplates.DecodeConfig(template.DefaultConfig), req.Config)
	configJSON, err := providertemplates.Marshal(normalizeProviderConfig(template.Driver, config))
	if err != nil {
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, err, nil)
	}
	metadataJSON, err := providertemplates.Marshal(providertemplates.MergeMetadata(template, req.Metadata))
	if err != nil {
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, err, nil)
	}
	provider, err := s.queries.CreateProviderFromTemplate(ctx, sqlc.CreateProviderFromTemplateParams{
		ProviderTemplateID: template.ID,
		Name:               name,
		ClientType:         template.Driver,
		Icon:               template.Icon,
		Enable:             true,
		Config:             configJSON,
		Metadata:           metadataJSON,
	})
	if err != nil {
		if isProviderNameConflict(err) {
			return GetResponse{}, apperror.Wrap(apperror.CodeProviderNameTaken, err, nil)
		}
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, fmt.Errorf("create provider from template: %w", err), nil)
	}
	return s.toGetResponse(provider), nil
}

// Get retrieves a provider by ID.
func (s *Service) Get(ctx context.Context, id string) (GetResponse, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return GetResponse{}, err
	}

	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get provider: %w", err)
	}

	return s.toGetResponse(provider), nil
}

// GetByName retrieves a provider by name.
func (s *Service) GetByName(ctx context.Context, name string) (GetResponse, error) {
	provider, err := s.queries.GetProviderByName(ctx, name)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get provider by name: %w", err)
	}

	return s.toGetResponse(provider), nil
}

// List retrieves all providers.
func (s *Service) List(ctx context.Context) ([]GetResponse, error) {
	providers, err := s.queries.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list providers: %w", err)
	}

	results := make([]GetResponse, 0, len(providers))
	for _, p := range providers {
		results = append(results, s.toGetResponse(p))
	}
	return results, nil
}

// Update updates an existing provider.
func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (GetResponse, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return GetResponse{}, err
	}

	existing, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get provider: %w", err)
	}

	name := existing.Name
	if req.Name != nil {
		name = *req.Name
	}

	clientType := existing.ClientType
	if req.ClientType != nil {
		clientType = *req.ClientType
	}

	icon := existing.Icon
	if req.Icon != nil {
		icon = pgtype.Text{String: *req.Icon, Valid: *req.Icon != ""}
	}

	enable := existing.Enable
	if req.Enable != nil {
		enable = *req.Enable
	}

	existingConfig := providerConfig(existing.Config)
	if req.Config != nil {
		mergedConfig := mergeProviderConfig(existingConfig, req.Config)
		preserveMaskedConfigSecret(mergedConfig, existingConfig, req.Config, "api_key")
		existingConfig = normalizeProviderConfig(clientType, mergedConfig)
	} else {
		existingConfig = normalizeProviderConfig(clientType, existingConfig)
	}
	configJSON, err := json.Marshal(existingConfig)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	metadataMap := providerMetadata(existing.Metadata)
	if req.Metadata != nil {
		metadataMap = req.Metadata
	}
	metadataJSON, err := json.Marshal(metadataMap)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal metadata: %w", err)
	}

	updated, err := s.queries.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID:         providerID,
		Name:       name,
		ClientType: clientType,
		Icon:       icon,
		Enable:     enable,
		Config:     configJSON,
		Metadata:   metadataJSON,
	})
	if err != nil {
		return GetResponse{}, fmt.Errorf("update provider: %w", err)
	}

	return s.toGetResponse(updated), nil
}

// Delete deletes a provider by ID.
func (s *Service) Delete(ctx context.Context, id string) error {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}

	if err := s.queries.DeleteProvider(ctx, providerID); err != nil {
		return fmt.Errorf("delete provider: %w", err)
	}
	return nil
}

// Count returns the total count of providers.
func (s *Service) Count(ctx context.Context) (int64, error) {
	providers, err := s.List(ctx)
	if err != nil {
		return 0, fmt.Errorf("count providers: %w", err)
	}
	return int64(len(providers)), nil
}

const probeTimeout = models.DefaultProviderProbeTimeout

const (
	registryMetadataKey = "registry"
	metadataSourceKey   = "source"
)

// Test probes the provider using the Twilight AI SDK to check
// reachability and authentication. A successful models-list response is
// conclusive; no follow-up generation probe is made. An earlier fake-model
// probe was removed (#1042): some OpenAI-compatible gateways validate the
// model before auth and answer 401 for an unknown model, which the probe
// misclassified as "Invalid API key" even after the key had authenticated.
// Per-model availability is covered by models.Service.Test instead.
//
// Outcome semantics (#1087) — the models list is only a partial falsifier:
//   - reachable + 200: verified (ok);
//   - reachable + 401/403: auth failed (auth_error) — the request carries no
//     model parameter, so this cannot be confused with "model not found";
//   - reachable + anything else (404/5xx): unverified, NOT a failure — the
//     base URL may be wrong, or the provider may not implement model listing;
//   - unreachable (DNS/TCP): error, the only hard failure kept at this layer.
func (s *Service) Test(ctx context.Context, id string) (TestResponse, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return TestResponse{}, err
	}

	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get provider: %w", err)
	}

	cfg := providerConfig(provider.Config)
	baseURL := strings.TrimRight(configString(cfg, "base_url"), "/")

	clientType := models.ClientType(provider.ClientType)
	creds, err := s.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return TestResponse{}, err
	}

	sdkProvider := models.NewSDKProvider(baseURL, creds.APIKey, creds.CodexAccountID, clientType, probeTimeout, nil)

	start := time.Now()
	result := sdkProvider.Test(ctx)
	message := providerTestMessage(result)

	switch result.Status {
	case sdk.ProviderStatusUnreachable:
		return TestResponse{
			Status:    TestStatusError,
			Reachable: false,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   message,
		}, nil
	case sdk.ProviderStatusUnhealthy:
		if strings.Contains(result.Message, "authentication failed") {
			return TestResponse{
				Status:    TestStatusAuthError,
				Reachable: true,
				LatencyMs: time.Since(start).Milliseconds(),
				Message:   message,
			}, nil
		}
		return TestResponse{
			Status:    TestStatusUnverified,
			Reachable: true,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   message,
		}, nil
	default:
		return TestResponse{
			Status:    TestStatusOK,
			Reachable: true,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   result.Message,
		}, nil
	}
}

// errorDetailer is implemented by transport errors that can expand into a
// fuller diagnostic, including the raw upstream response body. The probe path
// only fills a short summary (e.g. "service error (404):"), so we reach for
// this richer detail when the upstream replies with an opaque, non-JSON body.
type errorDetailer interface {
	Detail() string
}

// providerTestMessage returns the most informative message for a probe result,
// preferring the upstream response detail over the short summary so that
// opaque statuses still surface the provider's actual response body.
func providerTestMessage(result *sdk.ProviderTestResult) string {
	if result == nil {
		return ""
	}
	var detailer errorDetailer
	if errors.As(result.Error, &detailer) {
		if detail := strings.TrimSpace(detailer.Detail()); detail != "" {
			return detail
		}
	}
	return result.Message
}

// FetchRemoteModels fetches available models from the provider using the Twilight AI SDK.
func (s *Service) FetchRemoteModels(ctx context.Context, id string) ([]RemoteModel, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return nil, err
	}

	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}

	clientType := models.ClientType(provider.ClientType)
	switch clientType {
	case models.ClientTypeOpenAICodex:
		return s.fetchCodexRemoteModels(ctx, provider)
	case models.ClientTypeGitHubCopilot:
		return s.fetchGitHubCopilotModels(ctx, provider)
	}

	if models, ok := s.fetchTemplateModels(ctx, provider); ok {
		return models, nil
	}

	remoteModels, err := s.fetchRemoteModelsViaSDK(ctx, provider)
	if err != nil {
		return nil, err
	}

	return remoteModels, nil
}

func (s *Service) fetchTemplateModels(ctx context.Context, provider sqlc.Provider) ([]RemoteModel, bool) {
	if provider.ProviderTemplateID.Valid {
		models, err := s.queries.ListProviderTemplateModels(ctx, provider.ProviderTemplateID)
		if err == nil {
			return remoteModelsFromCatalog(models), true
		}
		if s.logger != nil {
			s.logger.Warn("failed to load provider template model catalog", slog.Any("error", err))
		}
	}
	source := metadataSectionSource(providerMetadata(provider.Metadata), "preset")
	if source == "" {
		return nil, false
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.TrimSpace(s.templatesDir) == "" {
		return nil, false
	}

	defs, err := registry.Load(s.logger, s.templatesDir)
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("failed to load provider template models", slog.String("template_source", source), slog.Any("error", err))
		}
		return nil, false
	}
	for _, def := range defs {
		if strings.EqualFold(def.Source, source) {
			return remoteModelsFromTemplate(def), true
		}
	}
	return nil, false
}

func remoteModelsFromCatalog(items []sqlc.TemplateProviderTemplateModel) []RemoteModel {
	out := make([]RemoteModel, 0, len(items))
	for _, model := range items {
		cfg := providerConfig(model.Config)
		out = append(out, RemoteModel{
			ID:                  model.ModelID,
			Name:                model.Name,
			Description:         configStringPtr(cfg, "description"),
			Type:                model.Type,
			Compatibilities:     configStringSlice(cfg, "compatibilities"),
			ReasoningEfforts:    configStringSlice(cfg, "reasoning_efforts"),
			ThinkingMode:        configString(cfg, "thinking_mode"),
			ReasoningDialect:    configString(cfg, "reasoning_dialect"),
			ReasoningOffSupport: configString(cfg, "reasoning_off_support"),
			ReasoningDefaultOn:  configBoolPtr(cfg, "reasoning_default_on"),
			ThinkingBudgetMin:   configNonNegativeIntPtr(cfg, "thinking_budget_min"),
			ThinkingBudgetMax:   configIntPtr(cfg, "thinking_budget_max"),
			ContextWindow:       configIntPtr(cfg, "context_window"),
			Dimensions:          configIntPtr(cfg, "dimensions"),
			CapabilitiesKnown:   true,
		})
	}
	return out
}

func remoteModelsFromTemplate(def registry.ProviderDefinition) []RemoteModel {
	out := make([]RemoteModel, 0, len(def.Models))
	for _, model := range def.Models {
		modelType := strings.TrimSpace(model.Type)
		if modelType == "" {
			modelType = string(models.ModelTypeChat)
		}
		cfg := model.Config
		out = append(out, RemoteModel{
			ID:                  model.ModelID,
			Name:                model.Name,
			Description:         configStringPtr(cfg, "description"),
			Type:                modelType,
			Compatibilities:     configStringSlice(cfg, "compatibilities"),
			ReasoningEfforts:    configStringSlice(cfg, "reasoning_efforts"),
			ThinkingMode:        configString(cfg, "thinking_mode"),
			ReasoningDialect:    configString(cfg, "reasoning_dialect"),
			ReasoningOffSupport: configString(cfg, "reasoning_off_support"),
			ReasoningDefaultOn:  configBoolPtr(cfg, "reasoning_default_on"),
			ThinkingBudgetMin:   configNonNegativeIntPtr(cfg, "thinking_budget_min"),
			ThinkingBudgetMax:   configIntPtr(cfg, "thinking_budget_max"),
			ContextWindow:       configIntPtr(cfg, "context_window"),
			Dimensions:          configIntPtr(cfg, "dimensions"),
			CapabilitiesKnown:   true,
		})
	}
	return out
}

func (s *Service) fetchRemoteModelsViaSDK(ctx context.Context, provider sqlc.Provider) ([]RemoteModel, error) {
	cfg := providerConfig(provider.Config)
	baseURL := strings.TrimRight(configString(cfg, "base_url"), "/")
	clientType := models.ClientType(provider.ClientType)

	if clientType == models.ClientTypeAnthropicMessages && baseURL != "" && !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}

	creds, err := s.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials: %w", err)
	}

	sdkProvider := models.NewSDKProvider(baseURL, creds.APIKey, creds.CodexAccountID, clientType, probeTimeout, nil)

	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	sdkModels, err := sdkProvider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}

	remoteModels := make([]RemoteModel, 0, len(sdkModels))
	for _, m := range sdkModels {
		modelType := m.Type
		if modelType == "" {
			modelType = sdk.ModelTypeChat
		}
		if modelType != sdk.ModelTypeChat && modelType != sdk.ModelTypeEmbedding {
			continue
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		var dimensions *int
		if modelType == sdk.ModelTypeEmbedding {
			dim, err := models.InferEmbeddingDimensions(ctx, string(clientType), baseURL, creds.APIKey, m.ID, probeTimeout, nil)
			if err != nil {
				logger := s.logger
				if logger == nil {
					logger = slog.Default()
				}
				logger.Warn("skip embedding model import because dimensions probe failed", slog.String("model_id", m.ID), slog.Any("error", err))
				continue
			}
			dimensions = &dim
		}
		remoteModels = append(remoteModels, RemoteModel{
			ID:         m.ID,
			Name:       name,
			Type:       string(modelType),
			Dimensions: dimensions,
		})
	}
	return remoteModels, nil
}

// toGetResponse converts a database provider to a response.
func (s *Service) toGetResponse(provider sqlc.Provider) GetResponse {
	var metadata map[string]any
	if len(provider.Metadata) > 0 {
		if err := json.Unmarshal(provider.Metadata, &metadata); err != nil {
			if s.logger != nil {
				s.logger.Warn("provider metadata unmarshal failed", slog.String("id", provider.ID.String()), slog.Any("error", err))
			}
		}
	}

	cfg := providerConfig(provider.Config)
	maskedCfg := maskConfigSecrets(provider.ClientType, cfg)

	var icon string
	if provider.Icon.Valid {
		icon = provider.Icon.String
	}
	var templateID string
	if provider.ProviderTemplateID.Valid {
		templateID = provider.ProviderTemplateID.String()
	}

	return GetResponse{
		ID:                 provider.ID.String(),
		ProviderTemplateID: templateID,
		Name:               provider.Name,
		ClientType:         provider.ClientType,
		Icon:               icon,
		Enable:             provider.Enable,
		Config:             maskedCfg,
		Metadata:           metadata,
		CreatedAt:          provider.CreatedAt.Time,
		UpdatedAt:          provider.UpdatedAt.Time,
	}
}

// providerConfig parses the provider config JSONB.
func providerConfig(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return map[string]any{}
	}
	if cfg == nil {
		return map[string]any{}
	}
	return cfg
}

// configString extracts a string from the config map.
func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	v, _ := cfg[key].(string)
	return v
}

func configStringSlice(cfg map[string]any, key string) []string {
	if cfg == nil {
		return nil
	}
	switch value := cfg[key].(type) {
	case []string:
		return append([]string(nil), value...)
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func configStringPtr(cfg map[string]any, key string) *string {
	if cfg == nil {
		return nil
	}
	value, ok := cfg[key].(string)
	if !ok {
		return nil
	}
	value = strings.TrimSpace(value)
	return &value
}

func configIntPtr(cfg map[string]any, key string) *int {
	if cfg == nil {
		return nil
	}
	switch value := cfg[key].(type) {
	case int:
		if value > 0 {
			return &value
		}
	case int64:
		if value > 0 {
			out := int(value)
			return &out
		}
	case float64:
		if value > 0 {
			out := int(value)
			return &out
		}
	}
	return nil
}

func configNonNegativeIntPtr(cfg map[string]any, key string) *int {
	if cfg == nil {
		return nil
	}
	switch value := cfg[key].(type) {
	case int:
		if value >= 0 {
			return &value
		}
	case int64:
		if value >= 0 {
			out := int(value)
			return &out
		}
	case float64:
		if value >= 0 {
			out := int(value)
			return &out
		}
	}
	return nil
}

// ProviderConfigString is a public helper for extracting a string from the config JSONB.
func ProviderConfigString(provider sqlc.Provider, key string) string {
	return configString(providerConfig(provider.Config), key)
}

func cloneConfig(cfg map[string]any) map[string]any {
	result := make(map[string]any, len(cfg))
	for k, v := range cfg {
		result[k] = v
	}
	return result
}

func mergeProviderConfig(existing, incoming map[string]any) map[string]any {
	result := cloneConfig(existing)
	for k, v := range incoming {
		result[k] = v
	}
	return result
}

func preserveMaskedConfigSecret(merged, existing, incoming map[string]any, key string) {
	existingValue := strings.TrimSpace(configString(existing, key))
	newValue := strings.TrimSpace(configString(incoming, key))
	if existingValue == "" || newValue == "" {
		return
	}
	if newValue == maskAPIKey(existingValue) {
		merged[key] = existingValue
	}
}

// normalizeProviderConfig keeps provider-specific secrets under stable keys while
// preserving backward compatibility for legacy stored configs.
func normalizeProviderConfig(clientType string, cfg map[string]any) map[string]any {
	result := cloneConfig(cfg)
	if models.ClientType(clientType) == models.ClientTypeGitHubCopilot {
		delete(result, "api_key")
		delete(result, configOAuthClientSecretKey)
	}
	return result
}

// maskConfigSecrets returns a copy of config with all known secret fields masked.
func maskConfigSecrets(clientType string, cfg map[string]any) map[string]any {
	result := normalizeProviderConfig(clientType, cfg)
	for _, key := range []string{"api_key", configOAuthClientSecretKey} {
		if value, _ := result[key].(string); value != "" {
			result[key] = maskAPIKey(value)
		}
	}
	return result
}

// maskAPIKey masks an API key for security.
func maskAPIKey(apiKey string) string {
	if apiKey == "" {
		return ""
	}
	if len(apiKey) <= 8 {
		return strings.Repeat("*", len(apiKey))
	}
	return apiKey[:8] + strings.Repeat("*", len(apiKey)-8)
}

func (s *Service) activateHiddenRegistryTemplate(
	ctx context.Context,
	req CreateRequest,
	clientType string,
	icon pgtype.Text,
	configJSON []byte,
	metadataJSON []byte,
) (sqlc.Provider, bool, error) {
	existing, err := s.queries.GetProviderByName(ctx, req.Name)
	if err != nil {
		return sqlc.Provider{}, false, nil
	}
	if !isHiddenRegistryTemplate(existing) {
		return sqlc.Provider{}, false, nil
	}
	if !icon.Valid {
		icon = existing.Icon
	}

	updated, err := s.queries.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID:         existing.ID,
		Name:       req.Name,
		ClientType: clientType,
		Icon:       icon,
		Enable:     true,
		Config:     configJSON,
		Metadata:   metadataJSON,
	})
	if err != nil {
		return sqlc.Provider{}, true, fmt.Errorf("activate registry provider template: %w", err)
	}
	return updated, true, nil
}

func isProviderNameConflict(err error) bool {
	if db.IsUniqueViolation(err) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") &&
		strings.Contains(message, "providers") &&
		strings.Contains(message, "name")
}

func isHiddenRegistryTemplate(provider sqlc.Provider) bool {
	if provider.Enable || registryMetadataSource(provider.Metadata) == "" {
		return false
	}
	cfg := providerConfig(provider.Config)
	return strings.TrimSpace(configString(cfg, "api_key")) == "" &&
		strings.TrimSpace(configString(cfg, configOAuthClientSecretKey)) == ""
}

func registryMetadataSource(raw []byte) string {
	return metadataSectionSource(providerMetadata(raw), registryMetadataKey)
}

func metadataSectionSource(metadata map[string]any, section string) string {
	nested, _ := metadata[section].(map[string]any)
	if nested == nil {
		return ""
	}
	return strings.TrimSpace(stringValue(nested, metadataSourceKey))
}

// configBoolPtr reads an optional boolean, distinguishing "absent" from "false".
// reasoning_default_on needs that distinction: unknown means the adaptor keeps its
// conservative behaviour, while an explicit false is a fact about the model.
func configBoolPtr(cfg map[string]any, key string) *bool {
	if cfg == nil {
		return nil
	}
	if value, ok := cfg[key].(bool); ok {
		return &value
	}
	return nil
}
