package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	modeldomain "github.com/memohai/memoh/domains/model"
	catalogport "github.com/memohai/memoh/domains/model/internal/port/catalog"
)

var (
	ErrModelNotFound        = catalogport.ErrModelNotFound
	ErrModelIDAlreadyExists = catalogport.ErrModelIDAlreadyExists
	ErrModelIDAmbiguous     = catalogport.ErrModelIDAmbiguous
)

// Service provides CRUD operations for models.
type Service struct {
	store            catalogport.Store
	providerResolver ProviderResolver
	logger           *slog.Logger
}

type Option func(*Service)

func WithProviderResolver(resolver ProviderResolver) Option {
	return func(s *Service) { s.providerResolver = resolver }
}

// NewService creates a new models service.

func NewService(log *slog.Logger, store catalogport.Store, options ...Option) *Service {
	if log == nil {
		log = slog.Default()
	}
	s := &Service{
		store:  store,
		logger: log.With(slog.String("service", "model_catalog")),
	}
	for _, option := range options {
		option(s)
	}
	return s
}

// Create adds a new model to the database.
func (s *Service) Create(ctx context.Context, req AddRequest) (AddResponse, error) {
	model := req.toModel(ResolveEnable(req.Enable, true))
	model.Config = normalizeModelConfig(model.Config)
	if err := model.Validate(); err != nil {
		return AddResponse{}, fmt.Errorf("validation failed: %w", err)
	}

	configJSON, err := json.Marshal(model.Config)
	if err != nil {
		return AddResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	created, err := s.store.Create(ctx, catalogport.CreateInput{
		ModelID:    model.ModelID,
		Name:       model.Name,
		ProviderID: model.ProviderID,
		Type:       model.Type,
		Enable:     model.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return AddResponse{}, fmt.Errorf("failed to create model: %w", err)
	}

	return AddResponse{
		ID:      created.ID,
		ModelID: created.ModelID,
	}, nil
}

// GetByID retrieves a model by its internal UUID.
func (s *Service) GetByID(ctx context.Context, id string) (GetResponse, error) {
	model, err := s.store.GetByID(ctx, id)
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to get model: %w", err)
	}

	return s.toGetResponse(model), nil
}

// GetByModelID retrieves a model by its model_id field.
func (s *Service) GetByModelID(ctx context.Context, modelID string) (GetResponse, error) {
	if modelID == "" {
		return GetResponse{}, errors.New("model_id is required")
	}

	model, err := s.store.GetByModelID(ctx, modelID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to get model: %w", err)
	}

	return s.toGetResponse(model), nil
}

// ModelExists reports whether a model exists by its internal UUID.
func (s *Service) ModelExists(ctx context.Context, id string) (bool, error) {
	_, err := s.store.GetByID(ctx, id)
	if errors.Is(err, ErrModelNotFound) {
		return false, nil
	}
	return err == nil, err
}

// ListModelIDs returns internal UUIDs matching an external model identifier.
func (s *Service) ListModelIDs(ctx context.Context, modelID string) ([]string, error) {
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return []string{}, nil
	}
	modelList, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}
	ids := make([]string, 0, 1)
	for _, model := range modelList {
		if model.ModelID == modelID {
			ids = append(ids, model.ID)
		}
	}
	return ids, nil
}

// List returns all models.
func (s *Service) List(ctx context.Context) ([]GetResponse, error) {
	modelList, err := s.store.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list models: %w", err)
	}

	return s.toGetResponseList(modelList), nil
}

// ListByType returns models filtered by type.
func (s *Service) ListByType(ctx context.Context, modelType modeldomain.ModelType) ([]GetResponse, error) {
	if !modeldomain.IsValidModelType(modelType) {
		return nil, fmt.Errorf("invalid model type: %s", modelType)
	}

	modelList, err := s.store.ListByType(ctx, modelType)
	if err != nil {
		return nil, fmt.Errorf("failed to list models by type: %w", err)
	}

	return s.toGetResponseList(modelList), nil
}

// ListByProviderClientType returns models whose provider has the given client_type.
func (s *Service) ListByProviderClientType(ctx context.Context, clientType modeldomain.ClientType) ([]GetResponse, error) {
	if !modeldomain.IsValidClientType(clientType) {
		return nil, fmt.Errorf("invalid client type: %s", clientType)
	}

	modelList, err := s.store.ListByProviderClientType(ctx, clientType)
	if err != nil {
		return nil, fmt.Errorf("failed to list models by provider client type: %w", err)
	}

	return s.toGetResponseList(modelList), nil
}

// ListEnabled returns all models from enabled providers.
func (s *Service) ListEnabled(ctx context.Context) ([]GetResponse, error) {
	modelList, err := s.store.ListEnabled(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled models: %w", err)
	}
	return s.toEnabledGetResponseList(modelList), nil
}

// ListEnabledByType returns models from enabled providers filtered by type.
func (s *Service) ListEnabledByType(ctx context.Context, modelType modeldomain.ModelType) ([]GetResponse, error) {
	if !modeldomain.IsValidModelType(modelType) {
		return nil, fmt.Errorf("invalid model type: %s", modelType)
	}
	modelList, err := s.store.ListEnabledByType(ctx, modelType)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled models by type: %w", err)
	}
	return s.toEnabledGetResponseList(modelList), nil
}

// ListEnabledByProviderClientType returns models from enabled providers with
// the given client_type.
func (s *Service) ListEnabledByProviderClientType(ctx context.Context, clientType modeldomain.ClientType) ([]GetResponse, error) {
	if !modeldomain.IsValidClientType(clientType) {
		return nil, fmt.Errorf("invalid client type: %s", clientType)
	}
	modelList, err := s.store.ListEnabledByProviderClientType(ctx, clientType)
	if err != nil {
		return nil, fmt.Errorf("failed to list enabled models by provider client type: %w", err)
	}
	return s.toEnabledGetResponseList(modelList), nil
}

// ListByProviderID returns models filtered by provider ID.
func (s *Service) ListByProviderID(ctx context.Context, providerID string) ([]GetResponse, error) {
	if strings.TrimSpace(providerID) == "" {
		return nil, errors.New("provider id is required")
	}
	modelList, err := s.store.ListByProviderID(ctx, providerID)
	if err != nil {
		return nil, fmt.Errorf("failed to list models by provider: %w", err)
	}
	return s.toGetResponseList(modelList), nil
}

// ListByProviderIDAndType returns models filtered by provider ID and type.
func (s *Service) ListByProviderIDAndType(ctx context.Context, providerID string, modelType modeldomain.ModelType) ([]GetResponse, error) {
	if !modeldomain.IsValidModelType(modelType) {
		return nil, fmt.Errorf("invalid model type: %s", modelType)
	}
	if strings.TrimSpace(providerID) == "" {
		return nil, errors.New("provider id is required")
	}
	modelList, err := s.store.ListByProviderIDAndType(ctx, providerID, modelType)
	if err != nil {
		return nil, fmt.Errorf("failed to list models by provider and type: %w", err)
	}
	return s.toGetResponseList(modelList), nil
}

// GetByProviderAndModelID retrieves a model by provider and model_id.
func (s *Service) GetByProviderAndModelID(ctx context.Context, providerID, modelID string) (GetResponse, error) {
	if strings.TrimSpace(providerID) == "" {
		return GetResponse{}, errors.New("provider id is required")
	}
	if strings.TrimSpace(modelID) == "" {
		return GetResponse{}, errors.New("model_id is required")
	}
	model, err := s.store.GetByProviderAndModelID(ctx, providerID, modelID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to get model by provider and model_id: %w", err)
	}
	return s.toGetResponse(model), nil
}

// UpdateByID updates a model by its internal UUID.
func (s *Service) UpdateByID(ctx context.Context, id string, req UpdateRequest) (GetResponse, error) {
	current, err := s.store.GetByID(ctx, id)
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to load model: %w", err)
	}

	model := req.toModel(ResolveEnable(req.Enable, current.Enable))
	model.Config = normalizeModelConfig(model.Config)
	if err := model.Validate(); err != nil {
		return GetResponse{}, fmt.Errorf("validation failed: %w", err)
	}

	configJSON, err := json.Marshal(model.Config)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	updated, err := s.store.Update(ctx, catalogport.UpdateInput{
		ID:         id,
		ModelID:    model.ModelID,
		Name:       model.Name,
		ProviderID: model.ProviderID,
		Type:       model.Type,
		Enable:     model.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to update model: %w", err)
	}

	return s.toGetResponse(updated), nil
}

// UpdateByProviderAndModelID updates a model within one provider namespace.
func (s *Service) UpdateByProviderAndModelID(ctx context.Context, providerID, modelID string, req UpdateRequest) (GetResponse, error) {
	current, err := s.GetByProviderAndModelID(ctx, providerID, modelID)
	if err != nil {
		return GetResponse{}, err
	}
	return s.UpdateByID(ctx, current.ID, req)
}

// UpdateByModelID updates a model by its model_id field.
func (s *Service) UpdateByModelID(ctx context.Context, modelID string, req UpdateRequest) (GetResponse, error) {
	if modelID == "" {
		return GetResponse{}, errors.New("model_id is required")
	}
	current, err := s.store.GetByModelID(ctx, modelID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to update model: %w", err)
	}

	model := req.toModel(ResolveEnable(req.Enable, current.Enable))
	model.Config = normalizeModelConfig(model.Config)
	if err := model.Validate(); err != nil {
		return GetResponse{}, fmt.Errorf("validation failed: %w", err)
	}

	configJSON, err := json.Marshal(model.Config)
	if err != nil {
		return GetResponse{}, fmt.Errorf("marshal config: %w", err)
	}

	updated, err := s.store.Update(ctx, catalogport.UpdateInput{
		ID:         current.ID,
		ModelID:    model.ModelID,
		Name:       model.Name,
		ProviderID: model.ProviderID,
		Type:       model.Type,
		Enable:     model.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return GetResponse{}, fmt.Errorf("failed to update model: %w", err)
	}

	return s.toGetResponse(updated), nil
}

// DeleteByID deletes a model by its internal UUID.
func (s *Service) DeleteByID(ctx context.Context, id string) error {
	if err := s.store.Delete(ctx, id); err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}

	return nil
}

// DeleteByModelID deletes a model by its model_id field.
func (s *Service) DeleteByModelID(ctx context.Context, modelID string) error {
	if modelID == "" {
		return errors.New("model_id is required")
	}
	current, err := s.store.GetByModelID(ctx, modelID)
	if err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}

	if err := s.store.Delete(ctx, current.ID); err != nil {
		return fmt.Errorf("failed to delete model: %w", err)
	}

	return nil
}

// Count returns the total number of models.
func (s *Service) Count(ctx context.Context) (int64, error) {
	count, err := s.store.Count(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to count models: %w", err)
	}
	return count, nil
}

// CountByType returns the number of models of a specific type.
func (s *Service) CountByType(ctx context.Context, modelType modeldomain.ModelType) (int64, error) {
	if !modeldomain.IsValidModelType(modelType) {
		return 0, fmt.Errorf("invalid model type: %s", modelType)
	}

	count, err := s.store.CountByType(ctx, modelType)
	if err != nil {
		return 0, fmt.Errorf("failed to count models by type: %w", err)
	}
	return count, nil
}

func (s *Service) toGetResponse(record catalogport.Record) GetResponse {
	resp := GetResponse{
		ID:      record.ID,
		ModelID: record.ModelID,
		Model: modeldomain.Model{
			ModelID:    record.ModelID,
			Name:       record.Name,
			ProviderID: record.ProviderID,
			Type:       record.Type,
			Enable:     record.Enable,
		},
	}

	if len(record.Config) > 0 {
		if err := json.Unmarshal(record.Config, &resp.Config); err != nil {
			s.logger.Warn("failed to unmarshal model config", slog.String("model_id", record.ModelID), slog.Any("error", err))
		}
	}

	return resp
}

func (s *Service) toGetResponseList(records []catalogport.Record) []GetResponse {
	responses := make([]GetResponse, 0, len(records))
	for _, record := range records {
		responses = append(responses, s.toGetResponse(record))
	}
	return responses
}

func (s *Service) toEnabledGetResponseList(records []catalogport.Record) []GetResponse {
	responses := make([]GetResponse, 0, len(records))
	for _, record := range records {
		response := s.toGetResponse(record)
		// Catalog availability is independent from the user's enable choice. Keeping
		// that choice stored lets a temporarily removed model return enabled later.
		if response.Config.CatalogAvailable != nil && !*response.Config.CatalogAvailable {
			continue
		}
		responses = append(responses, response)
	}
	return responses
}

// SelectMemoryModel selects a chat model for memory operations.
// It only considers models from enabled providers.
func SelectMemoryModel(ctx context.Context, modelsService *Service, resolver ProviderResolver) (GetResponse, ResolvedProvider, error) {
	if modelsService == nil {
		return GetResponse{}, ResolvedProvider{}, errors.New("models service not configured")
	}
	if resolver == nil {
		return GetResponse{}, ResolvedProvider{}, errors.New("provider resolver not configured")
	}
	candidates, err := modelsService.ListEnabledByType(ctx, modeldomain.ModelTypeChat)
	if err != nil || len(candidates) == 0 {
		return GetResponse{}, ResolvedProvider{}, errors.New("no enabled chat models available for memory operations")
	}
	selected := candidates[0]
	provider, err := resolver.ResolveModelProvider(ctx, selected.ProviderID)
	if err != nil {
		return GetResponse{}, ResolvedProvider{}, err
	}
	return selected, provider, nil
}

// SelectMemoryModelForBot selects a chat model for memory operations.
// If botID is provided, it attempts to use the bot's configured chat model first,
// falling back to the first enabled chat model globally. Models or providers
// that the user has disabled are skipped so memory extraction/decision/compact
// never quietly run on a row hidden from the UI.
func SelectMemoryModelForBot(ctx context.Context, modelsService *Service, resolver ProviderResolver, chatModelID string) (GetResponse, ResolvedProvider, error) {
	if modelsService == nil {
		return GetResponse{}, ResolvedProvider{}, errors.New("models service not configured")
	}
	if resolver == nil {
		return GetResponse{}, ResolvedProvider{}, errors.New("provider resolver not configured")
	}
	// If a specific model is configured (e.g. bot's chat_model_id), try to use it.
	if chatModelID = strings.TrimSpace(chatModelID); chatModelID != "" {
		model, err := modelsService.GetByModelID(ctx, chatModelID)
		if err == nil && model.Type == modeldomain.ModelTypeChat && model.Enable {
			provider, pErr := resolver.ResolveModelProvider(ctx, model.ProviderID)
			if pErr == nil && provider.Enable {
				return model, provider, nil
			}
		}
		// UUID-based lookup fallback
		model, err = modelsService.GetByID(ctx, chatModelID)
		if err == nil && model.Type == modeldomain.ModelTypeChat && model.Enable {
			provider, pErr := resolver.ResolveModelProvider(ctx, model.ProviderID)
			if pErr == nil && provider.Enable {
				return model, provider, nil
			}
		}
	}
	// Fallback: pick first enabled chat model globally.
	return SelectMemoryModel(ctx, modelsService, resolver)
}

// FetchProviderByID resolves provider metadata and credentials through the
// explicit Providers-owned port. The name is retained for transitional callers.
func FetchProviderByID(ctx context.Context, resolver ProviderResolver, providerID string) (ResolvedProvider, error) {
	if strings.TrimSpace(providerID) == "" {
		return ResolvedProvider{}, errors.New("provider id missing")
	}
	if resolver == nil {
		return ResolvedProvider{}, errors.New("provider resolver not configured")
	}
	return resolver.ResolveModelProvider(ctx, providerID)
}
