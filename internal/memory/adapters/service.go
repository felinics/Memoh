package adapters

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/config"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const builtinProviderName = "Built-in Memory"

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

// SetRegistry configures the runtime registry so that configuration updates
// can evict and re-instantiate provider instances automatically.
func (s *Service) SetRegistry(registry *Registry) {
	s.registry = registry
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

// GetConfig returns the team's Built-in Memory configuration. A team that has
// never saved one reads as the zero configuration.
func (s *Service) GetConfig(ctx context.Context) (MemoryConfig, error) {
	row, err := s.queries.GetBuiltinMemoryProvider(ctx)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return MemoryConfig{}, nil
		}
		return MemoryConfig{}, fmt.Errorf("get builtin memory provider: %w", err)
	}
	return memoryConfigFromProvider(s.toGetResponse(row)), nil
}

// UpdateConfig saves the team's Built-in Memory configuration, creating the
// builtin row on first use. Stored keys the request does not cover are kept.
func (s *Service) UpdateConfig(ctx context.Context, req MemoryConfigUpdateRequest) (MemoryConfig, error) {
	current, err := s.EnsureBuiltin(ctx)
	if err != nil {
		return MemoryConfig{}, err
	}
	next := make(map[string]any, len(current.Config)+2)
	maps.Copy(next, current.Config)
	next["memory_mode"] = "graph"
	if req.EmbeddingModelID != nil {
		if value := strings.TrimSpace(*req.EmbeddingModelID); value != "" {
			next["embedding_model_id"] = value
		} else {
			delete(next, "embedding_model_id")
		}
	}
	configJSON, err := json.Marshal(next)
	if err != nil {
		return MemoryConfig{}, fmt.Errorf("marshal config: %w", err)
	}
	pgID, err := db.ParseUUID(current.ID)
	if err != nil {
		return MemoryConfig{}, err
	}
	updated, err := s.queries.UpdateMemoryProvider(ctx, sqlc.UpdateMemoryProviderParams{
		ID:     pgID,
		Name:   current.Name,
		Config: configJSON,
	})
	if err != nil {
		return MemoryConfig{}, fmt.Errorf("update memory provider: %w", err)
	}
	resp := s.toGetResponse(updated)
	s.tryEvictAndReinstantiate(ctx, resp.ID, resp.Provider, resp.Config)
	return memoryConfigFromProvider(resp), nil
}

// EnsureBuiltin returns the team's Built-in Memory row, creating it on first
// use.
func (s *Service) EnsureBuiltin(ctx context.Context) (ProviderGetResponse, error) {
	row, err := s.queries.GetBuiltinMemoryProvider(ctx)
	if err == nil {
		return s.toGetResponse(row), nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ProviderGetResponse{}, fmt.Errorf("get builtin memory provider: %w", err)
	}
	configJSON, err := json.Marshal(map[string]any{"memory_mode": "graph"})
	if err != nil {
		return ProviderGetResponse{}, fmt.Errorf("marshal config: %w", err)
	}
	created, createErr := s.queries.CreateMemoryProvider(ctx, sqlc.CreateMemoryProviderParams{
		Name:      builtinProviderName,
		Provider:  string(ProviderBuiltin),
		Config:    configJSON,
		IsDefault: true,
	})
	if createErr != nil {
		// A concurrent request may have created the row first.
		if row, getErr := s.queries.GetBuiltinMemoryProvider(ctx); getErr == nil {
			return s.toGetResponse(row), nil
		}
		return ProviderGetResponse{}, fmt.Errorf("create builtin memory provider: %w", createErr)
	}
	return s.toGetResponse(created), nil
}

// EnsureBuiltinID returns the ID a memory-enabled bot should reference.
func (s *Service) EnsureBuiltinID(ctx context.Context) (string, error) {
	provider, err := s.EnsureBuiltin(ctx)
	if err != nil {
		return "", err
	}
	return provider.ID, nil
}

func memoryConfigFromProvider(provider ProviderGetResponse) MemoryConfig {
	return MemoryConfig{
		EmbeddingModelID: StringFromConfig(provider.Config, "embedding_model_id"),
	}
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
		s.logger.WarnContext(ctx, "auto-instantiate memory provider failed",
			slog.String("id", id), slog.String("provider", providerType), slog.Any("error", err))
	}
}

func (s *Service) tryEvictAndReinstantiate(ctx context.Context, id, providerType string, config map[string]any) {
	if s.registry == nil {
		return
	}
	if err := s.registry.Remove(ctx, id); err != nil {
		s.logger.WarnContext(ctx, "evict memory provider failed", slog.String("id", id), slog.Any("error", err))
		return
	}
	s.tryInstantiate(ctx, id, providerType, config)
}
