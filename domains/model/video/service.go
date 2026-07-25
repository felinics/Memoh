package video

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"
	sdk "github.com/memohai/twilight-ai/sdk"

	modeldomain "github.com/memohai/memoh/domains/model"
	videoport "github.com/memohai/memoh/domains/model/internal/port/video"
)

type Service struct {
	store    videoport.Store
	logger   *slog.Logger
	registry *Registry
}

func NewService(log *slog.Logger, store videoport.Store, registry *Registry) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:    store,
		logger:   log.With(slog.String("service", "video")),
		registry: registry,
	}
}

func (s *Service) Registry() *Registry { return s.registry }

func (s *Service) ListMeta(_ context.Context) []ProviderMetaResponse {
	return s.registry.ListMeta()
}

func (s *Service) ListProviders(ctx context.Context) ([]ProviderResponse, error) {
	rows, err := s.store.ListProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list video providers: %w", err)
	}
	items := make([]ProviderResponse, 0, len(rows))
	for _, row := range rows {
		def, err := s.registry.Get(row.ClientType)
		if err != nil {
			return nil, err
		}
		items = append(items, toProviderResponse(row, def))
	}
	return items, nil
}

func (s *Service) GetProvider(ctx context.Context, id string) (ProviderResponse, error) {
	if err := validateUUID(id); err != nil {
		return ProviderResponse{}, err
	}
	row, err := s.store.GetProvider(ctx, id)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("get video provider: %w", err)
	}
	def, err := s.registry.Get(row.ClientType)
	if err != nil {
		return ProviderResponse{}, fmt.Errorf("get video provider: %w", err)
	}
	return toProviderResponse(row, def), nil
}

func (s *Service) ListModels(ctx context.Context) ([]ModelResponse, error) {
	rows, err := s.store.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list video models: %w", err)
	}
	items := make([]ModelResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toModelResponse(row))
	}
	return items, nil
}

func (s *Service) ListModelsByProvider(ctx context.Context, providerID string) ([]ModelResponse, error) {
	if err := validateUUID(providerID); err != nil {
		return nil, err
	}
	providerRow, err := s.store.GetProvider(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get video provider: %w", err)
	}
	if _, err := s.registry.Get(providerRow.ClientType); err != nil {
		return nil, fmt.Errorf("get video provider: %w", err)
	}
	rows, err := s.store.ListModelsByProvider(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("list video models by provider: %w", err)
	}
	items := make([]ModelResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toModelResponse(row))
	}
	return items, nil
}

func (s *Service) GetModel(ctx context.Context, id string) (ModelResponse, error) {
	if err := validateUUID(id); err != nil {
		return ModelResponse{}, err
	}
	row, err := s.store.GetModel(ctx, id)
	if err != nil {
		return ModelResponse{}, fmt.Errorf("get video model: %w", err)
	}
	return toModelResponse(row), nil
}

func (s *Service) UpdateModel(ctx context.Context, id string, req UpdateModelRequest) (ModelResponse, error) {
	if err := validateUUID(id); err != nil {
		return ModelResponse{}, err
	}
	row, err := s.store.GetModel(ctx, id)
	if err != nil {
		return ModelResponse{}, fmt.Errorf("get video model: %w", err)
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return ModelResponse{}, fmt.Errorf("marshal video config: %w", err)
	}
	name := row.Name
	if req.Name != nil {
		name = *req.Name
	}
	updated, err := s.store.UpdateVideoModel(ctx, videoport.UpdateModelInput{
		ID:         id,
		ModelID:    row.ModelID,
		Name:       name,
		ProviderID: row.ProviderID,
		Type:       modeldomain.ModelTypeVideo,
		Enable:     row.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return ModelResponse{}, fmt.Errorf("update video model: %w", err)
	}
	updated.ProviderType = row.ProviderType
	return toModelResponse(updated), nil
}

func (s *Service) FetchRemoteModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	if err := validateUUID(providerID); err != nil {
		return nil, err
	}
	providerRow, err := s.store.GetProvider(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("get video provider: %w", err)
	}
	def, err := s.registry.Get(providerRow.ClientType)
	if err != nil {
		return nil, err
	}
	if !def.SupportsList || def.Factory == nil {
		return []ModelInfo{}, nil
	}
	provider, err := def.Factory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, fmt.Errorf("build video provider: %w", err)
	}
	remoteModels, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list video models: %w", err)
	}
	discovered := make([]ModelInfo, 0, len(remoteModels))
	for _, remoteModel := range remoteModels {
		if remoteModel == nil || remoteModel.ID == "" {
			continue
		}
		discovered = append(discovered, mergeRemoteModelInfo(remoteModel.ID, def.Models))
	}
	return discovered, nil
}

func (s *Service) ResolveVideoModel(ctx context.Context, modelID string) (*sdk.VideoModel, map[string]any, error) {
	if err := validateUUID(modelID); err != nil {
		return nil, nil, err
	}
	modelRow, err := s.store.GetModel(ctx, modelID)
	if err != nil {
		return nil, nil, fmt.Errorf("get video model: %w", err)
	}
	if !modelRow.Enable {
		return nil, nil, fmt.Errorf("video model %s is disabled", modelRow.ModelID)
	}
	providerRow, err := s.store.GetProvider(ctx, modelRow.ProviderID)
	if err != nil {
		return nil, nil, fmt.Errorf("get video provider: %w", err)
	}
	if !providerRow.Enable {
		return nil, nil, fmt.Errorf("video provider %s is disabled", providerRow.Name)
	}
	def, err := s.registry.Get(providerRow.ClientType)
	if err != nil {
		return nil, nil, err
	}
	provider, err := def.Factory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, nil, fmt.Errorf("build video provider: %w", err)
	}
	cfg := mergeConfig(parseConfig(providerRow.Config), parseConfig(modelRow.Config))
	return &sdk.VideoModel{ID: modelRow.ModelID, Provider: provider}, cfg, nil
}

func toProviderResponse(row videoport.ProviderRecord, def ProviderDefinition) ProviderResponse {
	return ProviderResponse{
		ID:         row.ID,
		Name:       row.Name,
		ClientType: string(row.ClientType),
		Icon:       row.Icon,
		Enable:     row.Enable,
		Config:     maskProviderConfig(parseConfig(row.Config), def.ConfigSchema),
		CreatedAt:  row.CreatedAt,
		UpdatedAt:  row.UpdatedAt,
	}
}

func maskProviderConfig(cfg map[string]any, schema ConfigSchema) map[string]any {
	if len(cfg) == 0 {
		return map[string]any{}
	}
	secretKeys := make(map[string]struct{})
	for _, field := range schema.Fields {
		if field.Type == "secret" {
			secretKeys[field.Key] = struct{}{}
		}
	}
	out := make(map[string]any, len(cfg))
	for key, value := range cfg {
		if _, ok := secretKeys[key]; ok {
			if s, ok := value.(string); ok && s != "" {
				out[key] = maskSecret(s)
				continue
			}
		}
		out[key] = value
	}
	return out
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func toModelResponse(row videoport.ModelRecord) ModelResponse {
	return ModelResponse{
		ID:           row.ID,
		ModelID:      row.ModelID,
		Name:         row.Name,
		ProviderID:   row.ProviderID,
		ProviderType: string(row.ProviderType),
		Config:       parseConfig(row.Config),
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

func parseConfig(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var cfg map[string]any
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg == nil {
		return map[string]any{}
	}
	return cfg
}

func mergeConfig(parts ...map[string]any) map[string]any {
	out := make(map[string]any)
	for _, part := range parts {
		for key, value := range part {
			out[key] = value
		}
	}
	return out
}

func mergeRemoteModelInfo(modelID string, defaults []ModelInfo) ModelInfo {
	for _, model := range defaults {
		if model.ID == modelID {
			return model
		}
	}
	return ModelInfo{ID: modelID, Name: modelID}
}

func validateUUID(id string) error {
	if _, err := uuid.Parse(strings.TrimSpace(id)); err != nil {
		return fmt.Errorf("invalid UUID: %w", err)
	}
	return nil
}
