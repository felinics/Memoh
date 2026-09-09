package searchproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type Service struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "search_providers")),
	}
}

func (*Service) ListMeta(_ context.Context) []ProviderMeta {
	return []ProviderMeta{
		{
			Provider:    string(ProviderBrave),
			DisplayName: "Brave",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Brave Search API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Brave API base URL",
						Required:    false,
						Example:     "https://api.search.brave.com/res/v1/web/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderBing),
			DisplayName: "Bing",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Bing Web Search API subscription key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Bing API base URL",
						Required:    false,
						Example:     "https://api.bing.microsoft.com/v7.0/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderGoogle),
			DisplayName: "Google",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Google Custom Search API key",
						Required:    true,
					},
					"cx": {
						Type:        "string",
						Title:       "Search Engine ID",
						Description: "Google Programmable Search Engine ID (cx)",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Google Custom Search API base URL",
						Required:    false,
						Example:     "https://customsearch.googleapis.com/customsearch/v1",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderTavily),
			DisplayName: "Tavily",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Tavily Search API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Tavily API base URL",
						Required:    false,
						Example:     "https://api.tavily.com/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderSogou),
			DisplayName: "Sogou",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"secret_id": {
						Type:        "secret",
						Title:       "Secret ID",
						Description: "Tencent Cloud SecretId for Sogou search",
						Required:    true,
					},
					"secret_key": {
						Type:        "secret",
						Title:       "Secret Key",
						Description: "Tencent Cloud SecretKey for Sogou search",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Tencent Cloud TMS API host",
						Required:    false,
						Example:     "wsa.tencentcloudapi.com",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderSerper),
			DisplayName: "Serper",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Serper API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Serper API base URL",
						Required:    false,
						Example:     "https://google.serper.dev/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderSearXNG),
			DisplayName: "SearXNG",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "SearXNG instance URL (self-hosted)",
						Required:    true,
						Example:     "http://localhost:8080/search",
					},
					"language": {
						Type:        "string",
						Title:       "Language",
						Description: "Search language (e.g. all, en, zh)",
						Required:    false,
						Example:     "all",
					},
					"safesearch": {
						Type:        "string",
						Title:       "Safe Search",
						Description: "Safe search level: 0 (off), 1 (moderate), 2 (strict)",
						Required:    false,
						Enum:        []string{"0", "1", "2"},
						Example:     "1",
					},
					"categories": {
						Type:        "string",
						Title:       "Categories",
						Description: "Search categories (comma-separated, e.g. general,news)",
						Required:    false,
						Example:     "general",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderJina),
			DisplayName: "Jina",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Jina Search API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Jina Search API base URL",
						Required:    false,
						Example:     "https://s.jina.ai/",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderExa),
			DisplayName: "Exa",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Exa Search API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Exa API base URL",
						Required:    false,
						Example:     "https://api.exa.ai/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderBocha),
			DisplayName: "Bocha",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Bocha Search API key",
						Required:    true,
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Bocha API base URL",
						Required:    false,
						Example:     "https://api.bochaai.com/v1/web-search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderDuckDuckGo),
			DisplayName: "DuckDuckGo",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "DuckDuckGo HTML search URL",
						Required:    false,
						Example:     "https://html.duckduckgo.com/html/",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
					},
				},
			},
		},
		{
			Provider:    string(ProviderYandex),
			DisplayName: "Yandex",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"api_key": {
						Type:        "secret",
						Title:       "API Key",
						Description: "Yandex Search API key",
						Required:    true,
					},
					"search_type": {
						Type:        "string",
						Title:       "Search Type",
						Description: "Yandex search type (e.g. SEARCH_TYPE_RU, SEARCH_TYPE_TR, SEARCH_TYPE_COM)",
						Required:    false,
						Example:     "SEARCH_TYPE_RU",
					},
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Yandex Search API base URL",
						Required:    false,
						Example:     "https://searchapi.api.cloud.yandex.net/v2/web/search",
					},
					"timeout_seconds": {
						Type:        "number",
						Title:       "Timeout (seconds)",
						Description: "HTTP timeout in seconds",
						Required:    false,
						Example:     15,
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
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.queries.CreateSearchProvider(ctx, sqlc.CreateSearchProviderParams{
		Name:     strings.TrimSpace(req.Name),
		Provider: string(req.Provider),
		Config:   configJSON,
		Enable:   false,
	})
	if err != nil {
		return GetResponse{}, mapSearchProviderWriteError(err, "create search provider")
	}
	return s.toGetResponse(row), nil
}

func (s *Service) Get(ctx context.Context, id string) (GetResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return GetResponse{}, err
	}
	row, err := s.queries.GetSearchProviderByID(ctx, pgID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get search provider: %w", err)
	}
	return s.toGetResponse(row), nil
}

func (s *Service) GetRawByID(ctx context.Context, id string) (sqlc.SearchProvider, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return sqlc.SearchProvider{}, err
	}
	return s.queries.GetSearchProviderByID(ctx, pgID)
}

func (s *Service) List(ctx context.Context, provider string) ([]GetResponse, error) {
	provider = strings.TrimSpace(provider)
	var (
		rows []sqlc.SearchProvider
		err  error
	)
	if provider == "" {
		rows, err = s.queries.ListSearchProviders(ctx)
	} else {
		rows, err = s.queries.ListSearchProvidersByProvider(ctx, provider)
	}
	if err != nil {
		return nil, fmt.Errorf("list search providers: %w", err)
	}
	items := make([]GetResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toGetResponse(row))
	}
	return items, nil
}

func (s *Service) Update(ctx context.Context, id string, req UpdateRequest) (GetResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return GetResponse{}, err
	}
	current, err := s.queries.GetSearchProviderByID(ctx, pgID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("get search provider: %w", err)
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
	updated, err := s.queries.UpdateSearchProvider(ctx, sqlc.UpdateSearchProviderParams{
		ID:       pgID,
		Name:     name,
		Provider: provider,
		Config:   config,
		Enable:   enable,
	})
	if err != nil {
		return GetResponse{}, mapSearchProviderWriteError(err, "update search provider")
	}
	return s.toGetResponse(updated), nil
}

func mapSearchProviderWriteError(err error, operation string) error {
	if !db.IsUniqueViolation(err) {
		return fmt.Errorf("%s: %w", operation, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && strings.HasSuffix(pgErr.ConstraintName, "provider_unique") {
		return apperror.Wrap(apperror.CodeSearchProviderTypeConflict, err, nil)
	}
	return apperror.Wrap(apperror.CodeProviderNameTaken, err, nil)
}

func (s *Service) Delete(ctx context.Context, id string) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	return s.queries.DeleteSearchProvider(ctx, pgID)
}

func (s *Service) toGetResponse(row sqlc.SearchProvider) GetResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			s.logger.Warn("search provider config unmarshal failed", slog.String("id", row.ID.String()), slog.Any("error", err))
		}
	}
	return GetResponse{
		ID:        row.ID.String(),
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    cfg,
		Enable:    row.Enable,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

var defaultProviders = []struct {
	Name        ProviderName
	DisplayName string
}{
	{ProviderBrave, "Brave"},
	{ProviderBing, "Bing"},
	{ProviderGoogle, "Google"},
	{ProviderTavily, "Tavily"},
	{ProviderSogou, "Sogou"},
	{ProviderSerper, "Serper"},
	{ProviderSearXNG, "SearXNG"},
	{ProviderJina, "Jina"},
	{ProviderExa, "Exa"},
	{ProviderBocha, "Bocha"},
	{ProviderDuckDuckGo, "DuckDuckGo"},
	{ProviderYandex, "Yandex"},
}

func (s *Service) EnsureDefaults(ctx context.Context) error {
	rows, err := s.queries.ListSearchProviders(ctx)
	if err != nil {
		return fmt.Errorf("list search providers: %w", err)
	}

	existing := make(map[string]struct{}, len(rows))
	for _, row := range rows {
		existing[row.Provider] = struct{}{}
	}

	for _, dp := range defaultProviders {
		if _, ok := existing[string(dp.Name)]; ok {
			continue
		}
		_, err := s.queries.CreateSearchProvider(ctx, sqlc.CreateSearchProviderParams{
			Name:     dp.DisplayName,
			Provider: string(dp.Name),
			Config:   []byte("{}"),
			Enable:   false,
		})
		if err != nil {
			s.logger.Warn("failed to create default search provider",
				slog.String("provider", string(dp.Name)),
				slog.Any("error", err),
			)
			continue
		}
		s.logger.Info("created default search provider", slog.String("provider", string(dp.Name)))
	}
	return nil
}

func isValidProviderName(name ProviderName) bool {
	switch name {
	case ProviderBrave, ProviderBing, ProviderGoogle,
		ProviderTavily,
		ProviderSogou,
		ProviderSerper,
		ProviderSearXNG,
		ProviderJina,
		ProviderExa,
		ProviderBocha,
		ProviderDuckDuckGo,
		ProviderYandex:
		return true
	default:
		return false
	}
}
