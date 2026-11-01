package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/errs"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providertemplates"
)

// Service handles provider operations.
type Service struct {
	queries     dbstore.Queries
	logger      *slog.Logger
	httpClient  *http.Client
	callbackURL string
}

// NewService creates a new provider service.
func NewService(log *slog.Logger, queries dbstore.Queries, callbackURL string) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries:     queries,
		logger:      log.With(slog.String("service", "providers")),
		httpClient:  &http.Client{Timeout: providerOAuthHTTPTimeout},
		callbackURL: callbackURL,
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
		if db.IsUniqueViolation(err) {
			if provider, ok, activateErr := s.activateHiddenRegistryTemplate(ctx, req, clientType, icon, configJSON, metadataJSON); ok {
				if activateErr != nil {
					return GetResponse{}, activateErr
				}
				return s.toGetResponse(provider), nil
			}
			return GetResponse{}, fmt.Errorf("create provider: %w: %w", ErrNameTaken, err)
		}
		return GetResponse{}, errs.Wrap(err, "create provider")
	}

	return s.toGetResponse(provider), nil
}

func (s *Service) CreateFromTemplate(ctx context.Context, req CreateFromTemplateRequest) (GetResponse, error) {
	expectedDomain := providertemplates.Domain(strings.TrimSpace(req.Domain))
	if expectedDomain != "" && !providertemplates.IsValidDomain(expectedDomain) {
		return GetResponse{}, fmt.Errorf("%w: %s", providertemplates.ErrDomainInvalid, expectedDomain)
	}
	template, err := providertemplates.Resolve(ctx, s.queries, req.TemplateID, expectedDomain)
	if err != nil {
		return GetResponse{}, err
	}
	switch providertemplates.Domain(template.Domain) {
	case providertemplates.DomainLLM, providertemplates.DomainSpeech, providertemplates.DomainTranscription, providertemplates.DomainVideo:
	default:
		return GetResponse{}, fmt.Errorf("%w: %s", providertemplates.ErrDomainMismatch, template.Domain)
	}

	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = template.Name
	}
	config := providertemplates.MergeConfig(providertemplates.DecodeConfig(template.DefaultConfig), req.Config)
	configJSON, err := providertemplates.Marshal(normalizeProviderConfig(template.Driver, config))
	if err != nil {
		return GetResponse{}, err
	}
	metadataJSON, err := providertemplates.Marshal(providertemplates.MergeMetadata(template, req.Metadata))
	if err != nil {
		return GetResponse{}, err
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
		if db.IsUniqueViolation(err) {
			return GetResponse{}, fmt.Errorf("create provider from template: %w: %w", ErrNameTaken, err)
		}
		return GetResponse{}, errs.Wrap(err, "create provider from template")
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
		return GetResponse{}, errs.Wrap(err, "get provider")
	}

	return s.toGetResponse(provider), nil
}

// GetByName retrieves a provider by name.
func (s *Service) GetByName(ctx context.Context, name string) (GetResponse, error) {
	provider, err := s.queries.GetProviderByName(ctx, name)
	if err != nil {
		return GetResponse{}, errs.Wrap(err, "get provider by name")
	}

	return s.toGetResponse(provider), nil
}

// List retrieves all providers.
func (s *Service) List(ctx context.Context) ([]GetResponse, error) {
	providers, err := s.queries.ListProviders(ctx)
	if err != nil {
		return nil, errs.Wrap(err, "list providers")
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
		return GetResponse{}, errs.Wrap(err, "get provider")
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
		for _, key := range SecretConfigKeys {
			preserveMaskedConfigSecret(mergedConfig, existingConfig, req.Config, key)
		}
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
		if db.IsUniqueViolation(err) {
			return GetResponse{}, fmt.Errorf("update provider: %w: %w", ErrNameTaken, err)
		}
		return GetResponse{}, errs.Wrap(err, "update provider")
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
		return errs.Wrap(err, "delete provider")
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

// remoteModelsResponseLimit bounds the /models body read for extension fields.
const remoteModelsResponseLimit = 8 << 20

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
// Outcome semantics (#1087): the models list is only a partial falsifier,
// so the outcome follows what sdk.Provider.Test returned:
//   - nil: verified (ok), except for OpenCode Go, whose models list is
//     public and answers without valid credentials (unverified);
//   - an *sdk.APIError of kind authentication or permission_denied: auth
//     failed (auth_error). The request carries no model parameter, so this
//     cannot be confused with "model not found";
//   - any other *sdk.APIError (404, 429, 5xx, ...): unverified, NOT a
//     failure. The endpoint answered, but the base URL may be wrong or the
//     provider may not implement model listing;
//   - no *sdk.APIError in the chain (DNS, TCP, TLS, an ended context): error,
//     the only hard failure kept at this layer.
//
// Every outcome other than ok carries the SDK error as Cause.
func (s *Service) Test(ctx context.Context, id string) (TestResponse, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return TestResponse{}, err
	}

	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return TestResponse{}, errs.Wrap(err, "get provider")
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
	resp := providerTestOutcome(sdkProvider.Test(ctx))
	if clientType == models.ClientTypeOpenCodeGo && resp.Status == TestStatusOK {
		// Go's public catalog succeeds without valid credentials. Keep the UI
		// verdict unverified until the user runs a real model generation probe.
		resp.Status = TestStatusUnverified
	}
	resp.LatencyMs = time.Since(start).Milliseconds()
	return resp, nil
}

// providerTestOutcome maps the result of sdk.Provider.Test to a TestResponse
// by the rules documented on Service.Test.
func providerTestOutcome(err error) TestResponse {
	if err == nil {
		return TestResponse{Status: TestStatusOK, Reachable: true}
	}
	cause := errs.WrapDependency(err, "test provider")
	if kind := sdk.KindOf(err); kind == sdk.KindAuthentication || kind == sdk.KindPermissionDenied {
		return TestResponse{Status: TestStatusAuthError, Reachable: true, Cause: cause}
	}
	var apiErr *sdk.APIError
	if errors.As(err, &apiErr) {
		return TestResponse{Status: TestStatusUnverified, Reachable: true, Cause: cause}
	}
	return TestResponse{Status: TestStatusError, Cause: cause}
}

// FetchRemoteModels fetches available models from the provider using the Twilight AI SDK.
func (s *Service) FetchRemoteModels(ctx context.Context, id string) ([]RemoteModel, error) {
	providerID, err := db.ParseUUID(id)
	if err != nil {
		return nil, err
	}

	provider, err := s.queries.GetProviderByID(ctx, providerID)
	if err != nil {
		return nil, errs.Wrap(err, "get provider")
	}

	clientType := models.ClientType(provider.ClientType)
	switch clientType {
	case models.ClientTypeOpenAICodex:
		return s.fetchCodexRemoteModels(ctx, provider)
	case models.ClientTypeGitHubCopilot:
		return s.fetchGitHubCopilotModels(ctx, provider)
	}

	return s.fetchRemoteModelsViaSDK(ctx, provider)
}

// listedModel is one entry of an endpoint's model listing.
type listedModel struct {
	ID          string
	DisplayName string
	Type        string
}

// embeddingModelID matches an id in which "embed" starts a word (text-embedding-3,
// nomic-embed-text, embeddinggemma-300m, but not an id that merely contains it),
// or that starts a part with an embedding family whose names do not say so:
// bge-m3, gte-Qwen2, e5-mistral, multilingual-e5, m3e-base, voyage-3,
// jina-clip-v2. A reranker of those families is not an embedding model.
var (
	embeddingModelID = regexp.MustCompile(`(?i)(?:^|[^a-z])embed|(?:^|[/_.:-])(?:bge|gte|e5|m3e|voyage|jina-clip)(?:[-_.:]|$)`)
	rerankerModelID  = regexp.MustCompile(`(?i)rerank`)
)

// looksLikeEmbeddingModel reports whether a model id names an embedding model.
func looksLikeEmbeddingModel(modelID string) bool {
	return embeddingModelID.MatchString(modelID) && !rerankerModelID.MatchString(modelID)
}

// listGoogleEmbeddingModels lists the embedding models of a Google provider
// that listed does not already hold. Google lists them through a second request
// to the same endpoint, so it runs on its own time, and it is best effort: the
// chat models have already been listed, so a failure here is logged and keeps
// them. A model serving both methods is listed by both requests and kept once.
func (s *Service) listGoogleEmbeddingModels(ctx context.Context, baseURL, apiKey string, listed []listedModel) []listedModel {
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	embeddingModels, err := models.ListGoogleEmbeddingModels(ctx, baseURL, apiKey, probeTimeout, nil)
	if err != nil {
		logger := s.logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.WarnContext(ctx, "skip google embedding models because listing failed", slog.Any("error", err))
		return nil
	}
	seen := make(map[string]struct{}, len(listed))
	for _, m := range listed {
		seen[m.ID] = struct{}{}
	}
	out := make([]listedModel, 0, len(embeddingModels))
	for _, m := range embeddingModels {
		if _, dup := seen[m.ID]; dup {
			continue
		}
		out = append(out, listedModel{ID: m.ID, DisplayName: m.DisplayName, Type: string(models.ModelTypeEmbedding)})
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

	parent := ctx
	ctx, cancel := context.WithTimeout(parent, probeTimeout)
	defer cancel()

	sdkModels, err := sdkProvider.ListModels(ctx)
	if err != nil {
		return nil, errs.WrapDependency(err, "list models")
	}
	listed := make([]listedModel, 0, len(sdkModels))
	for _, m := range sdkModels {
		listed = append(listed, listedModel{ID: m.ID, DisplayName: m.DisplayName, Type: string(models.ModelTypeChat)})
	}
	if clientType == models.ClientTypeGoogleGenerativeAI {
		listed = append(listed, s.listGoogleEmbeddingModels(parent, baseURL, creds.APIKey, listed)...)
	}

	liveMaxOutput := fetchMaxOutputTokens(ctx, clientType, baseURL, creds.APIKey)

	remoteModels := make([]RemoteModel, 0, len(listed))
	for _, m := range listed {
		modelType := models.ModelType(m.Type)
		// The OpenAI protocol has no model types, so completions and responses
		// endpoints list embedding models as chat; their ids say what they are.
		if modelType == models.ModelTypeChat && looksLikeEmbeddingModel(m.ID) {
			modelType = models.ModelTypeEmbedding
		}
		name := m.DisplayName
		if name == "" {
			name = m.ID
		}
		// An embedding model's vector size is not listed. The model service
		// probes it when the model is created, on its own time, so a slow or
		// refused probe skips that one model instead of spending this list's
		// budget or dropping the model before anyone sees it.
		remote := RemoteModel{
			ID:   m.ID,
			Name: name,
			Type: string(modelType),
		}
		if clientType == models.ClientTypeOpenCodeGo && modelType == models.ModelTypeChat {
			remote.Compatibilities = []string{models.CompatToolCall, models.CompatReasoning}
			remote.ThinkingMode = models.ThinkingModeAlways
			remote.CapabilitiesKnown = true
		}
		// The endpoint may report an output limit of its own.
		if limit, ok := liveMaxOutput[m.ID]; ok {
			remote.MaxOutputTokens = &limit
		}
		remoteModels = append(remoteModels, remote)
	}
	return remoteModels, nil
}

// fetchMaxOutputTokens reads the optional max_output_tokens field that an
// OpenAI-compatible /models endpoint may attach to each entry; the SDK's model
// list keeps only ids. It is best effort: any failure, or an entry without a
// positive integer, leaves the model's limit unknown.
func fetchMaxOutputTokens(ctx context.Context, clientType models.ClientType, baseURL, apiKey string) map[string]int {
	if clientType != models.ClientTypeOpenAICompletions && clientType != models.ClientTypeOpenAIResponses {
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	resp, err := models.NewProviderHTTPClient(probeTimeout).Do(req) //nolint:gosec // The URL is the provider's own configured base URL, like the SDK's list request.
	if err != nil {
		return nil
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var body struct {
		Data []struct {
			ID              string `json:"id"`
			MaxOutputTokens *int   `json:"max_output_tokens"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, remoteModelsResponseLimit)).Decode(&body); err != nil {
		return nil
	}
	limits := make(map[string]int)
	for _, item := range body.Data {
		if item.MaxOutputTokens != nil && *item.MaxOutputTokens > 0 {
			limits[item.ID] = *item.MaxOutputTokens
		}
	}
	return limits
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
	metadata = withLegacyGoSource(provider.ClientType, cfg, metadata)
	maskedCfg := maskConfigSecrets(provider.ClientType, cfg)

	var icon string
	if provider.Icon.Valid {
		icon = provider.Icon.String
	}
	// Instances created before the Go preset had an icon retain an empty value.
	if icon == "" && provider.ClientType == string(models.ClientTypeOpenCodeGo) {
		icon = "opencode-go"
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
	if newValue == MaskAPIKey(existingValue) {
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
	for _, key := range SecretConfigKeys {
		if value, _ := result[key].(string); value != "" {
			result[key] = MaskAPIKey(value)
		}
	}
	return result
}

// SecretConfigKeys is the single registry of provider-config fields that hold
// secrets. Every read surface that masks (here and the audio speech/
// transcription endpoints) and every write surface that preserves masked
// round-trips must draw from THIS list and mask with MaskAPIKey — the speech
// endpoints once masked with their own shape, and any voice-settings save
// then wrote the masked literal back over the real key.
var SecretConfigKeys = []string{"api_key", configOAuthClientSecretKey, "access_key", "secret_key", "app_key"}

// MaskAPIKey masks an API key for security.
func MaskAPIKey(apiKey string) string {
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
		return sqlc.Provider{}, true, errs.Wrap(err, "activate registry provider template")
	}
	return updated, true, nil
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
