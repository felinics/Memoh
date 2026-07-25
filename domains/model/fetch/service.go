package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	fetchport "github.com/memohai/memoh/domains/model/internal/port/fetch"
)

var ErrManagedNativeProvider = errors.New("native fetch provider is managed by the system")

// Service manages fetch provider configuration.
type Service struct {
	store  fetchport.Store
	logger *slog.Logger
}

// NewService creates a fetch service with the given persistence store.
// Callers outside domains/model should prefer NewPostgresService or NewMemoryService.
func NewService(log *slog.Logger, store fetchport.Store) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:  store,
		logger: log.With(slog.String("service", "fetch_providers")),
	}
}

func (*Service) ListMeta(_ context.Context) []ProviderMeta {
	return []ProviderMeta{
		{
			Provider:    string(ProviderNative),
			DisplayName: "Native",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{},
			},
		},
		{
			Provider:    string(ProviderJina),
			DisplayName: "Jina Reader",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Optional Jina API key for higher Reader API limits",
						Required:    false,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Jina Reader API base URL",
						Required:    false,
						Example:     "https://r.jina.ai/",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     30,
					},
				},
			},
		},
		{
			Provider:    string(ProviderCloudflareMarkdown),
			DisplayName: "Cloudflare Markdown",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"account_id": {
						Type:        "string",
						Title:       "Account ID",
						Description: "Cloudflare account ID",
						Required:    true,
					},
					"api_token": {
						Type:        "secret",
						Title:       "API Token",
						Description: "Cloudflare API token with Browser Rendering - Edit permission",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Cloudflare API base URL",
						Required:    false,
						Example:     "https://api.cloudflare.com/client/v4",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     30,
					},
				},
			},
		},
	}
}

func (s *Service) Create(ctx context.Context, req CreateRequest) (GetResponse, error) {
	if !isValidProviderName(req.Provider) {
		return GetResponse{}, fmt.Errorf("invalid provider: %s", req.Provider)
	}
	if req.Provider == ProviderNative {
		return GetResponse{}, ErrManagedNativeProvider
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.store.CreateProvider(ctx, fetchport.ProviderWrite{
		Name:     strings.TrimSpace(req.Name),
		Provider: string(req.Provider),
		Config:   configJSON,
		Enable:   false,
	})
	if err != nil {
		return GetResponse{}, fmt.Errorf("create fetch provider: %w", err)
	}
	return s.toGetResponse(row), nil
}

func (s *Service) Get(ctx context.Context, id string) (GetResponse, error) {
	row, err := s.store.FindProvider(ctx, id)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get fetch provider: %w", err)
	}
	return s.toGetResponse(row), nil
}

func (s *Service) GetRawByID(ctx context.Context, id string) (ProviderRecord, error) {
	row, err := s.store.FindProvider(ctx, id)
	if err != nil {
		return ProviderRecord{}, err
	}
	return publicRecord(row), nil
}

func (s *Service) List(ctx context.Context, provider string) ([]GetResponse, error) {
	provider = strings.TrimSpace(provider)
	rows, err := s.store.ListProviders(ctx, provider)
	if err != nil {
		return nil, fmt.Errorf("list fetch providers: %w", err)
	}
	items := make([]GetResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toGetResponse(row))
	}
	return items, nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (GetResponse, error) {
	current, err := s.store.FindProvider(ctx, id)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get fetch provider: %w", err)
	}
	if current.Provider == string(ProviderNative) {
		if req.Enable != nil && !*req.Enable {
			return GetResponse{}, ErrManagedNativeProvider
		}
		req.Provider = nil
		req.Config = nil
	}
	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	provider := current.Provider
	if req.Provider != nil {
		if !isValidProviderName(*req.Provider) {
			return GetResponse{}, fmt.Errorf("invalid provider: %s", *req.Provider)
		}
		if *req.Provider == ProviderNative {
			return GetResponse{}, ErrManagedNativeProvider
		}
		provider = string(*req.Provider)
	}
	config := current.Config
	if req.Config != nil {
		configJSON, marshalErr := json.Marshal(req.Config)
		if marshalErr != nil {
			return GetResponse{}, fmt.Errorf("marshal config: %w", marshalErr)
		}
		config = configJSON
	}
	enable := current.Enable
	if req.Enable != nil {
		enable = *req.Enable
	}
	if provider == string(ProviderNative) {
		enable = true
	}
	updated, err := s.store.UpdateProvider(ctx, fetchport.ProviderWrite{
		ID:       id,
		Name:     name,
		Provider: provider,
		Config:   config,
		Enable:   enable,
	})
	if err != nil {
		return GetResponse{}, fmt.Errorf("update fetch provider: %w", err)
	}
	return s.toGetResponse(updated), nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	current, err := s.store.FindProvider(ctx, id)
	if err != nil {
		return fmt.Errorf("get fetch provider: %w", err)
	}
	if current.Provider == string(ProviderNative) {
		return ErrManagedNativeProvider
	}
	return s.store.DeleteProvider(ctx, id)
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	rows, err := s.store.ListProviders(ctx, "")
	if err != nil {
		return fmt.Errorf("list fetch providers: %w", err)
	}

	existing := make(map[string]fetchport.ProviderRecord, len(rows))
	for _, row := range rows {
		existing[row.Provider] = row
	}

	for _, dp := range defaultProviders {
		if row, ok := existing[string(dp.Name)]; ok {
			if dp.Name == ProviderNative && !row.Enable {
				_, err := s.store.UpdateProvider(ctx, fetchport.ProviderWrite{
					ID:       row.ID,
					Name:     row.Name,
					Provider: row.Provider,
					Config:   row.Config,
					Enable:   true,
				})
				if err != nil {
					s.logger.WarnContext(ctx, "failed to enable native fetch provider", slog.Any("error", err))
				}
			}
			continue
		}
		_, err := s.store.CreateProvider(ctx, fetchport.ProviderWrite{
			Name:     dp.DisplayName,
			Provider: string(dp.Name),
			Config:   []byte("{}"),
			Enable:   dp.Enable,
		})
		if err != nil {
			s.logger.WarnContext(ctx, "failed to create default fetch provider",
				slog.String("provider", string(dp.Name)),
				slog.Any("error", err),
			)
			continue
		}
		s.logger.InfoContext(ctx, "created default fetch provider", slog.String("provider", string(dp.Name)))
	}
	return nil
}

func (s *Service) toGetResponse(row fetchport.ProviderRecord) GetResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			s.logger.Warn("fetch provider config unmarshal failed", slog.String("id", row.ID), slog.Any("error", err))
		}
	}
	return GetResponse{
		ID:        row.ID,
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    cfg,
		Enable:    row.Enable,
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

var defaultProviders = []struct {
	Name        ProviderName
	DisplayName string
	Enable      bool
}{
	{ProviderCloudflareMarkdown, "Cloudflare Markdown", false},
	{ProviderJina, "Jina Reader", false},
	{ProviderNative, "Native", true},
}

func isValidProviderName(name ProviderName) bool {
	switch name {
	case ProviderNative, ProviderJina, ProviderCloudflareMarkdown:
		return true
	default:
		return false
	}
}
