package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
	"github.com/memohai/memoh/domains/model/internal/provider/secret"
	"github.com/memohai/memoh/domains/model/template"
)

// TemplateCatalog is the consumer-side catalog surface used when creating
// providers from templates and resolving remote model presets.
type TemplateCatalog interface {
	FindTemplate(context.Context, string) (template.CatalogTemplate, error)
	ListTemplateModels(context.Context, string) ([]template.CatalogModel, error)
}

// ProbeSDK constructs Twilight providers and probes embedding dimensions without
// importing domains/model/execution (cmd injects the execution-backed implementation).
type ProbeSDK interface {
	NewProvider(baseURL, apiKey, codexAccountID string, clientType modeldomain.ClientType, timeout time.Duration, httpClient *http.Client) sdk.Provider
	InferEmbeddingDimensions(ctx context.Context, clientType, baseURL, apiKey, modelID string, timeout time.Duration, httpClient *http.Client) (int, error)
}

// Option configures optional Service dependencies.
type Option func(*Service)

// WithProbeSDK injects the SDK/probe implementation used by Test and remote discovery.
func WithProbeSDK(probe ProbeSDK) Option {
	return func(s *Service) { s.probe = probe }
}

// Service handles provider operations.
type Service struct {
	providers    ProviderStore
	oauth        OAuthStore
	templates    TemplateCatalog
	probe        ProbeSDK
	logger       *slog.Logger
	httpClient   *http.Client
	callbackURL  string
	templatesDir string
}

// NewService creates a provider service without the provider-template catalog.
func NewService(log *slog.Logger, providers ProviderStore, oauth OAuthStore, callbackURL string, templatesDir string, opts ...Option) *Service {
	return NewServiceWithCatalog(log, providers, oauth, nil, callbackURL, templatesDir, opts...)
}

// NewServiceWithCatalog creates a provider service with provider-template support.
func NewServiceWithCatalog(
	log *slog.Logger,
	providers ProviderStore,
	oauth OAuthStore,
	templates TemplateCatalog,
	callbackURL string,
	templatesDir string,
	opts ...Option,
) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		providers:    providers,
		oauth:        oauth,
		templates:    templates,
		logger:       log.With(slog.String("service", "providers")),
		httpClient:   &http.Client{Timeout: providerOAuthHTTPTimeout},
		callbackURL:  callbackURL,
		templatesDir: templatesDir,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(s)
		}
	}
	return s
}

// Create creates a new provider.
func (s *Service) Create(ctx context.Context, req CreateRequest) (GetResponse, error) {
	metadataJSON, err := json.Marshal(req.Metadata)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal metadata: %w", err)
	}

	clientType := req.ClientType
	if clientType == "" {
		clientType = string(modeldomain.ClientTypeOpenAICompletions)
	}
	configJSON, err := json.Marshal(normalizeProviderConfig(clientType, req.Config))
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	provider, err := s.providers.CreateProvider(ctx, CreateProviderCommand{
		Name:       req.Name,
		ClientType: clientType,
		Icon:       req.Icon,
		Enable:     true,
		Config:     configJSON,
		Metadata:   metadataJSON,
	})
	if err != nil {
		if isProviderNameConflict(err) {
			if provider, ok, activateErr := s.activateHiddenRegistryTemplate(ctx, req, clientType, req.Icon, configJSON, metadataJSON); ok {
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
	expectedDomain := template.Domain(strings.TrimSpace(req.Domain))
	if expectedDomain != "" && !template.IsValidDomain(expectedDomain) {
		return GetResponse{}, template.ErrDomainInvalid
	}
	row, err := template.Resolve(ctx, s.templates, req.TemplateID, expectedDomain)
	if err != nil {
		return GetResponse{}, err
	}
	switch template.Domain(row.Domain) {
	case template.DomainLLM, template.DomainSpeech, template.DomainTranscription, template.DomainVideo:
	default:
		return GetResponse{}, template.ErrDomainMismatch
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = row.Name
	}
	config := template.MergeConfig(template.DecodeConfig(row.DefaultConfig), req.Config)
	configJSON, err := template.Marshal(normalizeProviderConfig(row.Driver, config))
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal provider config: %w", err)
	}
	metadataJSON, err := template.Marshal(template.MergeMetadata(row, req.Metadata))
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal provider metadata: %w", err)
	}
	provider, err := s.providers.CreateProviderFromTemplate(ctx, CreateProviderFromTemplateCommand{
		ProviderTemplateID: row.ID,
		Name:               name,
		ClientType:         row.Driver,
		Icon:               row.Icon,
		Enable:             true,
		Config:             configJSON,
		Metadata:           metadataJSON,
	})
	if err != nil {
		if isProviderNameConflict(err) {
			return GetResponse{}, fmt.Errorf("%w: %w", ErrProviderNameTaken, err)
		}
		return GetResponse{}, fmt.Errorf("create provider from template: %w", err)
	}
	return s.toGetResponse(provider), nil
}

// Get retrieves a provider by ID.
func (s *Service) Get(ctx context.Context, id string) (GetResponse, error) {
	provider, err := s.providers.GetProvider(ctx, id)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get provider: %w", err)
	}

	return s.toGetResponse(provider), nil
}

// LookupProvider returns the persistence-neutral provider record for credential
// resolution and execution wiring. Secrets are not masked.
func (s *Service) LookupProvider(ctx context.Context, id string) (ProviderRecord, error) {
	provider, err := s.providers.GetProvider(ctx, id)
	if err != nil {
		return ProviderRecord{}, fmt.Errorf("get provider: %w", err)
	}
	return provider, nil
}

// GetByName retrieves a provider by name.
func (s *Service) GetByName(ctx context.Context, name string) (GetResponse, error) {
	provider, err := s.providers.GetProviderByName(ctx, name)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get provider by name: %w", err)
	}

	return s.toGetResponse(provider), nil
}

// List retrieves all providers.
func (s *Service) List(ctx context.Context) ([]GetResponse, error) {
	providers, err := s.providers.ListProviders(ctx)
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
	existing, err := s.providers.GetProvider(ctx, id)
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
		icon = *req.Icon
	}

	enable := existing.Enable
	if req.Enable != nil {
		enable = *req.Enable
	}

	existingConfig := providerConfig(existing.Config)
	if req.Config != nil {
		mergedConfig := mergeProviderConfig(existingConfig, req.Config)
		preserveMaskedConfigSecret(mergedConfig, existingConfig, req.Config, "api_key")
		preserveMaskedConfigSecret(mergedConfig, existingConfig, req.Config, secret.OAuthClientSecretKey)
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

	updated, err := s.providers.UpdateProvider(ctx, UpdateProviderCommand{
		ID:         id,
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
	if err := s.providers.DeleteProvider(ctx, id); err != nil {
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

const probeTimeout = DefaultProbeTimeout

const (
	registryMetadataKey = "registry"
	metadataSourceKey   = "source"
)

// Test probes the provider using the Twilight AI SDK to check
// reachability and authentication.
func (s *Service) Test(ctx context.Context, id string) (TestResponse, error) {
	provider, err := s.providers.GetProvider(ctx, id)
	if err != nil {
		return TestResponse{}, fmt.Errorf("get provider: %w", err)
	}

	cfg := providerConfig(provider.Config)
	baseURL := strings.TrimRight(configString(cfg, "base_url"), "/")

	clientType := modeldomain.ClientType(provider.ClientType)
	creds, err := s.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return TestResponse{}, err
	}

	sdkProvider := s.resolveProbe().NewProvider(baseURL, creds.APIKey, creds.CodexAccountID, clientType, probeTimeout, nil)

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
		status := TestStatusError
		if strings.Contains(result.Message, "authentication failed") {
			status = TestStatusAuthError
		}
		return TestResponse{
			Status:    status,
			Reachable: true,
			LatencyMs: time.Since(start).Milliseconds(),
			Message:   message,
		}, nil
	default:
		if _, probeErr := sdkProvider.TestModel(ctx, "__ping__"); probeErr != nil {
			if strings.Contains(probeErr.Error(), "authentication failed") {
				return TestResponse{
					Status:    TestStatusAuthError,
					Reachable: true,
					LatencyMs: time.Since(start).Milliseconds(),
					Message:   probeErr.Error(),
				}, nil
			}
		}
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
	provider, err := s.providers.GetProvider(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("get provider: %w", err)
	}

	clientType := modeldomain.ClientType(provider.ClientType)
	switch clientType {
	case modeldomain.ClientTypeOpenAICodex:
		return s.fetchCodexRemoteModels(ctx, provider)
	case modeldomain.ClientTypeGitHubCopilot:
		return s.fetchGitHubCopilotModels(ctx, provider)
	}

	if models, ok := s.fetchTemplateModels(ctx, provider.ProviderTemplateID, provider.Metadata); ok {
		return models, nil
	}

	remoteModels, err := s.fetchRemoteModelsViaSDK(ctx, provider)
	if err != nil {
		return nil, err
	}

	return remoteModels, nil
}

func (s *Service) fetchTemplateModels(ctx context.Context, templateID string, metadata []byte) ([]RemoteModel, bool) {
	if s.templates != nil && strings.TrimSpace(templateID) != "" {
		models, err := s.templates.ListTemplateModels(ctx, templateID)
		if err == nil {
			return remoteModelsFromCatalog(models), true
		}
		if s.logger != nil {
			s.logger.WarnContext(ctx, "failed to load provider template model catalog", slog.Any("error", err))
		}
	}
	source := metadataSectionSource(providerMetadata(metadata), "preset")
	if source == "" {
		return nil, false
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if strings.TrimSpace(s.templatesDir) == "" {
		return nil, false
	}

	defs, err := template.Load(s.logger, s.templatesDir)
	if err != nil {
		if s.logger != nil {
			s.logger.WarnContext(ctx, "failed to load provider template models", slog.String("template_source", source), slog.Any("error", err))
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

func remoteModelsFromCatalog(items []template.CatalogModel) []RemoteModel {
	out := make([]RemoteModel, 0, len(items))
	for _, model := range items {
		cfg := providerConfig(model.Config)
		out = append(out, RemoteModel{
			ID:               model.ModelID,
			Name:             model.Name,
			Description:      configStringPtr(cfg, "description"),
			Type:             model.Type,
			Compatibilities:  configStringSlice(cfg, "compatibilities"),
			ReasoningEfforts: configStringSlice(cfg, "reasoning_efforts"),
			ThinkingMode:     configString(cfg, "thinking_mode"),
			ContextWindow:    configIntPtr(cfg, "context_window"),
			Dimensions:       configIntPtr(cfg, "dimensions"),
		})
	}
	return out
}

func remoteModelsFromTemplate(def template.Definition) []RemoteModel {
	out := make([]RemoteModel, 0, len(def.Models))
	for _, model := range def.Models {
		modelType := strings.TrimSpace(model.Type)
		if modelType == "" {
			modelType = string(modeldomain.ModelTypeChat)
		}
		cfg := model.Config
		out = append(out, RemoteModel{
			ID:               model.ModelID,
			Name:             model.Name,
			Description:      configStringPtr(cfg, "description"),
			Type:             modelType,
			Compatibilities:  configStringSlice(cfg, "compatibilities"),
			ReasoningEfforts: configStringSlice(cfg, "reasoning_efforts"),
			ThinkingMode:     configString(cfg, "thinking_mode"),
			ContextWindow:    configIntPtr(cfg, "context_window"),
			Dimensions:       configIntPtr(cfg, "dimensions"),
		})
	}
	return out
}

func (s *Service) fetchRemoteModelsViaSDK(ctx context.Context, provider ProviderRecord) ([]RemoteModel, error) {
	cfg := providerConfig(provider.Config)
	baseURL := strings.TrimRight(configString(cfg, "base_url"), "/")
	clientType := modeldomain.ClientType(provider.ClientType)

	if clientType == modeldomain.ClientTypeAnthropicMessages && baseURL != "" && !strings.HasSuffix(baseURL, "/v1") {
		baseURL += "/v1"
	}

	creds, err := s.ResolveModelCredentials(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("resolve credentials: %w", err)
	}

	sdkProvider := s.resolveProbe().NewProvider(baseURL, creds.APIKey, creds.CodexAccountID, clientType, probeTimeout, nil)

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
			dim, err := s.resolveProbe().InferEmbeddingDimensions(ctx, string(clientType), baseURL, creds.APIKey, m.ID, probeTimeout, nil)
			if err != nil {
				logger := s.logger
				if logger == nil {
					logger = slog.Default()
				}
				logger.WarnContext(ctx, "skip embedding model import because dimensions probe failed", slog.String("model_id", m.ID), slog.Any("error", err))
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

// toGetResponse converts persisted provider state to an API response.
func (s *Service) toGetResponse(provider ProviderRecord) GetResponse {
	var metadata map[string]any
	if len(provider.Metadata) > 0 {
		if err := json.Unmarshal(provider.Metadata, &metadata); err != nil {
			if s.logger != nil {
				s.logger.Warn("provider metadata unmarshal failed", slog.String("id", provider.ID), slog.Any("error", err))
			}
		}
	}

	cfg := providerConfig(provider.Config)
	maskedCfg := maskConfigSecrets(provider.ClientType, cfg)

	return GetResponse{
		ID:                 provider.ID,
		ProviderTemplateID: provider.ProviderTemplateID,
		Name:               provider.Name,
		ClientType:         provider.ClientType,
		Icon:               provider.Icon,
		Enable:             provider.Enable,
		Config:             maskedCfg,
		Metadata:           metadata,
		CreatedAt:          provider.CreatedAt,
		UpdatedAt:          provider.UpdatedAt,
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

// ProviderConfigString extracts a string from persisted provider config JSON.
func ProviderConfigString(config []byte, key string) string {
	return configString(providerConfig(config), key)
}

func mergeProviderConfig(existing, incoming map[string]any) map[string]any {
	return secret.Merge(existing, incoming)
}

func preserveMaskedConfigSecret(merged, existing, incoming map[string]any, key string) {
	secret.PreserveMasked(merged, existing, incoming, key)
}

func normalizeProviderConfig(clientType string, cfg map[string]any) map[string]any {
	return secret.Normalize(clientType, cfg)
}

func maskConfigSecrets(clientType string, cfg map[string]any) map[string]any {
	return secret.MaskConfig(clientType, cfg)
}

func maskAPIKey(apiKey string) string {
	return secret.MaskAPIKey(apiKey)
}

func (s *Service) activateHiddenRegistryTemplate(
	ctx context.Context,
	req CreateRequest,
	clientType string,
	icon string,
	configJSON []byte,
	metadataJSON []byte,
) (ProviderRecord, bool, error) {
	existing, err := s.providers.GetProviderByName(ctx, req.Name)
	if err != nil {
		return ProviderRecord{}, false, nil
	}
	if !isHiddenRegistryTemplate(existing) {
		return ProviderRecord{}, false, nil
	}
	if icon == "" {
		icon = existing.Icon
	}

	updated, err := s.providers.UpdateProvider(ctx, UpdateProviderCommand{
		ID:         existing.ID,
		Name:       req.Name,
		ClientType: clientType,
		Icon:       icon,
		Enable:     true,
		Config:     configJSON,
		Metadata:   metadataJSON,
	})
	if err != nil {
		return ProviderRecord{}, true, fmt.Errorf("activate registry provider template: %w", err)
	}
	return updated, true, nil
}

func isProviderNameConflict(err error) bool {
	if errors.Is(err, ErrProviderNameTaken) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "unique") &&
		strings.Contains(message, "providers") &&
		strings.Contains(message, "name")
}

func isHiddenRegistryTemplate(provider ProviderRecord) bool {
	if provider.Enable || registryMetadataSource(provider.Metadata) == "" {
		return false
	}
	cfg := providerConfig(provider.Config)
	return strings.TrimSpace(configString(cfg, "api_key")) == "" &&
		strings.TrimSpace(configString(cfg, secret.OAuthClientSecretKey)) == ""
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
