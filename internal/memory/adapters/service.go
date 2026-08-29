package adapters

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type Service struct {
	queries  dbstore.Queries
	registry *Registry
	logger   *slog.Logger
	cfg      config.Config
}

func NewService(log *slog.Logger, queries dbstore.Queries, cfg config.Config) *Service {
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "memory_providers")),
		cfg:     cfg,
	}
}

// SetRegistry configures the runtime registry so that CRUD operations
// can instantiate/evict provider instances automatically.
func (s *Service) SetRegistry(registry *Registry) {
	s.registry = registry
}

func (*Service) ListMeta(_ context.Context) []ProviderMeta {
	return []ProviderMeta{
		{
			Provider:    string(ProviderBuiltin),
			DisplayName: "Built-in",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"memory_mode": {
						Type:        "select",
						Title:       "Memory Mode",
						Description: "graph = relationship-based recall over connected memory nodes",
						Required:    false,
					},
					"embedding_model_id": {
						Type:        "string",
						Title:       "Embedding Model",
						Description: "Optional embedding model used to maintain the dedicated pgvector semantic seed index for graph recall. local stores remain graph-only.",
						Required:    false,
					},
					"context_target_items": {
						Type:        "integer",
						Title:       "Context Target Items",
						Description: "Target number of memory snippets to inject per chat turn. Defaults to 6.",
						Required:    false,
						Example:     6,
					},
					"context_max_total_chars": {
						Type:        "integer",
						Title:       "Context Max Total Chars",
						Description: "Maximum total characters for all memory snippets combined. Defaults to 1800.",
						Required:    false,
						Example:     1800,
					},
				},
			},
		},
		{
			Provider:    string(ProviderMem0),
			DisplayName: "Mem0",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "Mem0 SaaS API base URL. Defaults to https://api.mem0.ai when empty.",
						Required:    false,
						Example:     "https://api.mem0.ai",
					},
					"api_key": {
						Type:        "string",
						Title:       "API Key",
						Description: "API key for Mem0 SaaS authentication",
						Required:    true,
						Secret:      true,
					},
					"org_id": {
						Type:        "string",
						Title:       "Organization ID",
						Description: "Organization ID for Mem0 SaaS workspace context",
					},
					"project_id": {
						Type:        "string",
						Title:       "Project ID",
						Description: "Project ID for Mem0 SaaS workspace context",
					},
				},
			},
		},
		{
			Provider:    string(ProviderOpenViking),
			DisplayName: "OpenViking",
			ConfigSchema: ProviderConfigSchema{
				Fields: map[string]ProviderFieldSchema{
					"base_url": {
						Type:        "string",
						Title:       "Base URL",
						Description: "OpenViking API base URL (self-hosted or SaaS)",
						Required:    true,
						Example:     "http://openviking:8088",
					},
					"api_key": {
						Type:        "string",
						Title:       "API Key",
						Description: "API key for OpenViking authentication",
						Secret:      true,
					},
				},
			},
		},
	}
}

func (s *Service) Create(ctx context.Context, req ProviderCreateRequest) (ProviderGetResponse, error) {
	if !isValidProviderType(req.Provider) {
		return ProviderGetResponse{}, fmt.Errorf("invalid provider type: %s", req.Provider)
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	row, err := s.queries.CreateMemoryProvider(ctx, sqlc.CreateMemoryProviderParams{
		Name:      strings.TrimSpace(req.Name),
		Provider:  string(req.Provider),
		Config:    configJSON,
		IsDefault: false,
	})
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("create memory provider: %w", err)
	}
	resp := s.toGetResponse(row)
	s.tryInstantiate(ctx, resp.ID, resp.Provider, resp.Config)
	return resp, nil
}

func (s *Service) Get(ctx context.Context, id string) (ProviderGetResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return ProviderGetResponse{}, err
	}
	row, err := s.queries.GetMemoryProviderByID(ctx, pgID)
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("get memory provider: %w", err)
	}
	return s.toGetResponse(row), nil
}

func (s *Service) Status(ctx context.Context, id string) (ProviderStatusResponse, error) {
	resp, err := s.Get(ctx, id)
	if err != nil {
		return ProviderStatusResponse{}, err
	}
	status := ProviderStatusResponse{
		ProviderType: resp.Provider,
	}
	if resp.Provider != string(ProviderBuiltin) {
		return status, nil
	}
	status.MemoryMode = StringFromConfig(resp.Config, "memory_mode")
	status.EmbeddingModelID = StringFromConfig(resp.Config, "embedding_model_id")
	return status, nil
}

func (s *Service) List(ctx context.Context) ([]ProviderGetResponse, error) {
	rows, err := s.queries.ListMemoryProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list memory providers: %w", err)
	}
	items := make([]ProviderGetResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, s.toGetResponse(row))
	}
	return items, nil
}

func (s *Service) InstantiateAll(ctx context.Context) (int, error) {
	rows, err := s.queries.ListMemoryProviders(ctx)
	if err != nil {
		return 0, fmt.Errorf("list memory providers: %w", err)
	}
	if s.registry == nil {
		return 0, nil
	}
	loaded := 0
	for _, row := range rows {
		resp := s.toGetResponse(row)
		if _, err := s.registry.Instantiate(ctx, resp.ID, resp.Provider, resp.Config); err != nil {
			s.logger.Warn("auto-instantiate memory provider failed",
				slog.String("id", resp.ID), slog.String("provider", resp.Provider), slog.Any("error", err))
			continue
		}
		loaded++
	}
	return loaded, nil
}

func (s *Service) Update(ctx context.Context, id string, req ProviderUpdateRequest) (ProviderGetResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return ProviderGetResponse{}, err
	}
	current, err := s.queries.GetMemoryProviderByID(ctx, pgID)
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("get memory provider: %w", err)
	}
	name := current.Name
	if req.Name != nil {
		name = strings.TrimSpace(*req.Name)
	}
	config := current.Config
	if req.Config != nil {
		configJSON, marshalErr := json.Marshal(req.Config)
		if marshalErr != nil {
			return ProviderGetResponse{}, fmt.Errorf("marshal config: %w", marshalErr)
		}
		config = configJSON
	}
	updated, err := s.queries.UpdateMemoryProvider(ctx, sqlc.UpdateMemoryProviderParams{
		ID:     pgID,
		Name:   name,
		Config: config,
	})
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("update memory provider: %w", err)
	}
	resp := s.toGetResponse(updated)
	s.tryEvictAndReinstantiate(ctx, resp.ID, resp.Provider, resp.Config)
	return resp, nil
}

func (s *Service) Delete(ctx context.Context, id string) error {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return err
	}
	if err := s.queries.DeleteMemoryProvider(ctx, pgID); err != nil {
		return err
	}
	if s.registry != nil {
		if err := s.registry.Remove(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

// EnsureDefault creates a default builtin provider if none exists.
func (s *Service) EnsureDefault(ctx context.Context) (ProviderGetResponse, error) {
	row, err := s.queries.GetDefaultMemoryProvider(ctx)
	if err == nil {
		return s.toGetResponse(row), nil
	}
	configJSON, _ := json.Marshal(map[string]any{})
	created, err := s.queries.CreateMemoryProvider(ctx, sqlc.CreateMemoryProviderParams{
		Name:      "Built-in Memory",
		Provider:  string(ProviderBuiltin),
		Config:    configJSON,
		IsDefault: true,
	})
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("create default memory provider: %w", err)
	}
	return s.toGetResponse(created), nil
}

func (s *Service) toGetResponse(row sqlc.MemoryProvider) ProviderGetResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		if err := json.Unmarshal(row.Config, &cfg); err != nil {
			s.logger.Warn("memory provider config unmarshal failed", slog.String("id", row.ID.String()), slog.Any("error", err))
		}
	}
	return ProviderGetResponse{
		ID:        row.ID.String(),
		Name:      row.Name,
		Provider:  row.Provider,
		Config:    cfg,
		IsDefault: row.IsDefault,
		CreatedAt: row.CreatedAt.Time,
		UpdatedAt: row.UpdatedAt.Time,
	}
}

func (s *Service) tryInstantiate(ctx context.Context, id, providerType string, config map[string]any) {
	if s.registry == nil {
		return
	}
	if _, err := s.registry.Instantiate(ctx, id, providerType, config); err != nil {
		s.logger.Warn("auto-instantiate memory provider failed",
			slog.String("id", id), slog.String("provider", providerType), slog.Any("error", err))
	}
}

func (s *Service) tryEvictAndReinstantiate(ctx context.Context, id, providerType string, config map[string]any) {
	if s.registry == nil {
		return
	}
	if err := s.registry.Remove(ctx, id); err != nil {
		s.logger.Warn("evict memory provider failed", slog.String("id", id), slog.Any("error", err))
		return
	}
	s.tryInstantiate(ctx, id, providerType, config)
}

func isValidProviderType(t ProviderType) bool {
	switch t {
	case ProviderBuiltin, ProviderMem0, ProviderOpenViking:
		return true
	default:
		return false
	}
}
