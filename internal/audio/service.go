package audio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
)

type Service struct {
	queries  dbstore.Queries
	logger   *slog.Logger
	registry *Registry
}

func NewService(log *slog.Logger, queries dbstore.Queries, registry *Registry) *Service {
	return &Service{
		queries:  queries,
		logger:   log.With(slog.String("service", "audio")),
		registry: registry,
	}
}

func (s *Service) Registry() *Registry { return s.registry }

func (s *Service) ListMeta(_ context.Context) []ProviderMetaResponse {
	return s.registry.ListMeta()
}

func (s *Service) ListSpeechMeta(_ context.Context) []ProviderMetaResponse {
	return s.registry.ListSpeechMeta()
}

func (s *Service) ListTranscriptionMeta(_ context.Context) []ProviderMetaResponse {
	return s.registry.ListTranscriptionMeta()
}

func (s *Service) ListSpeechProviders(ctx context.Context) ([]SpeechProviderResponse, error) {
	rows, err := s.queries.ListSpeechProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list speech providers: %w", err)
	}
	items := make([]SpeechProviderResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toSpeechProviderResponse(row))
	}
	return items, nil
}

func (s *Service) ListTranscriptionProviders(ctx context.Context) ([]SpeechProviderResponse, error) {
	rows, err := s.queries.ListTranscriptionProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transcription providers: %w", err)
	}
	items := make([]SpeechProviderResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, toSpeechProviderResponse(row))
	}
	return items, nil
}

func (s *Service) GetSpeechProvider(ctx context.Context, id string) (SpeechProviderResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return SpeechProviderResponse{}, err
	}
	row, err := s.queries.GetProviderByID(ctx, pgID)
	if err != nil {
		return SpeechProviderResponse{}, fmt.Errorf("get speech provider: %w", err)
	}
	return toSpeechProviderResponse(row), nil
}

func (s *Service) ListSpeechModels(ctx context.Context) ([]SpeechModelResponse, error) {
	rows, err := s.queries.ListSpeechModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list speech models: %w", err)
	}
	items := make([]SpeechModelResponse, 0, len(rows))
	for _, row := range rows {
		if s.shouldHideModel(row.ProviderType, models.ModelTypeSpeech, row.ModelID) {
			continue
		}
		items = append(items, toSpeechModelFromListRow(row))
	}
	return items, nil
}

func (s *Service) ListTranscriptionModels(ctx context.Context) ([]TranscriptionModelResponse, error) {
	rows, err := s.queries.ListTranscriptionModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transcription models: %w", err)
	}
	items := make([]TranscriptionModelResponse, 0, len(rows))
	for _, row := range rows {
		if s.shouldHideModel(row.ProviderType, models.ModelTypeTranscription, row.ModelID) {
			continue
		}
		items = append(items, toTranscriptionModelFromListRow(row))
	}
	return items, nil
}

func (s *Service) ListSpeechModelsByProvider(ctx context.Context, providerID string) ([]SpeechModelResponse, error) {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	providerRow, err := s.queries.GetProviderByID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}
	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListSpeechModelsByProviderID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("list speech models by provider: %w", err)
	}
	items := make([]SpeechModelResponse, 0, len(rows))
	for _, row := range rows {
		if shouldHideTemplateModel(def, models.ModelTypeSpeech, row.ModelID) {
			continue
		}
		items = append(items, toSpeechModelFromModel(row, ""))
	}
	return items, nil
}

func (s *Service) ListTranscriptionModelsByProvider(ctx context.Context, providerID string) ([]TranscriptionModelResponse, error) {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}
	providerRow, err := s.queries.GetProviderByID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}
	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListTranscriptionModelsByProviderID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("list transcription models by provider: %w", err)
	}
	items := make([]TranscriptionModelResponse, 0, len(rows))
	for _, row := range rows {
		if shouldHideTemplateModel(def, models.ModelTypeTranscription, row.ModelID) {
			continue
		}
		items = append(items, toTranscriptionModelFromModel(row, ""))
	}
	return items, nil
}

func (s *Service) GetSpeechModel(ctx context.Context, id string) (SpeechModelResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return SpeechModelResponse{}, err
	}
	row, err := s.queries.GetSpeechModelWithProvider(ctx, pgID)
	if err != nil {
		return SpeechModelResponse{}, fmt.Errorf("get speech model: %w", err)
	}
	return toSpeechModelWithProviderResponse(row), nil
}

func (s *Service) GetTranscriptionModel(ctx context.Context, id string) (TranscriptionModelResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return TranscriptionModelResponse{}, err
	}
	row, err := s.queries.GetTranscriptionModelWithProvider(ctx, pgID)
	if err != nil {
		return TranscriptionModelResponse{}, fmt.Errorf("get transcription model: %w", err)
	}
	return toTranscriptionModelWithProviderResponse(row), nil
}

func (s *Service) UpdateSpeechModel(ctx context.Context, id string, req UpdateSpeechModelRequest) (SpeechModelResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return SpeechModelResponse{}, err
	}
	row, err := s.queries.GetSpeechModelWithProvider(ctx, pgID)
	if err != nil {
		return SpeechModelResponse{}, fmt.Errorf("get speech model: %w", err)
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return SpeechModelResponse{}, fmt.Errorf("marshal speech config: %w", err)
	}
	name := row.Name
	if req.Name != nil {
		name = pgtype.Text{String: *req.Name, Valid: *req.Name != ""}
	}
	updated, err := s.queries.UpdateModel(ctx, sqlc.UpdateModelParams{
		ID:         pgID,
		ModelID:    row.ModelID,
		Name:       name,
		ProviderID: row.ProviderID,
		Type:       string(models.ModelTypeSpeech),
		Enable:     row.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return SpeechModelResponse{}, fmt.Errorf("update speech model: %w", err)
	}
	return toSpeechModelFromModel(updated, row.ProviderType), nil
}

func (s *Service) UpdateTranscriptionModel(ctx context.Context, id string, req UpdateSpeechModelRequest) (TranscriptionModelResponse, error) {
	pgID, err := db.ParseUUID(id)
	if err != nil {
		return TranscriptionModelResponse{}, err
	}
	row, err := s.queries.GetTranscriptionModelWithProvider(ctx, pgID)
	if err != nil {
		return TranscriptionModelResponse{}, fmt.Errorf("get transcription model: %w", err)
	}
	configJSON, err := json.Marshal(req.Config)
	if err != nil {
		return TranscriptionModelResponse{}, fmt.Errorf("marshal transcription config: %w", err)
	}
	name := row.Name
	if req.Name != nil {
		name = pgtype.Text{String: *req.Name, Valid: *req.Name != ""}
	}
	updated, err := s.queries.UpdateModel(ctx, sqlc.UpdateModelParams{
		ID:         pgID,
		ModelID:    row.ModelID,
		Name:       name,
		ProviderID: row.ProviderID,
		Type:       string(models.ModelTypeTranscription),
		Enable:     row.Enable,
		Config:     configJSON,
	})
	if err != nil {
		return TranscriptionModelResponse{}, fmt.Errorf("update transcription model: %w", err)
	}
	return toTranscriptionModelFromModel(updated, row.ProviderType), nil
}

func (s *Service) Synthesize(ctx context.Context, modelID string, text string, overrideCfg map[string]any) ([]byte, string, error) {
	params, err := s.resolveSpeechParams(ctx, modelID, text, overrideCfg)
	if err != nil {
		return nil, "", err
	}
	result, err := sdk.GenerateSpeech(ctx,
		sdk.WithSpeechModel(params.model),
		sdk.WithText(text),
		sdk.WithSpeechConfig(params.config),
	)
	if err != nil {
		return nil, "", fmt.Errorf("synthesize: %w", err)
	}
	return result.Audio, result.ContentType, nil
}

func (s *Service) StreamToFile(ctx context.Context, modelID string, text string, w io.Writer) (string, error) {
	params, err := s.resolveSpeechParams(ctx, modelID, text, nil)
	if err != nil {
		return "", err
	}
	streamResult, err := sdk.StreamSpeech(ctx,
		sdk.WithSpeechModel(params.model),
		sdk.WithText(text),
		sdk.WithSpeechConfig(params.config),
	)
	if err != nil {
		return "", fmt.Errorf("stream: %w", err)
	}
	audio, err := streamResult.Bytes()
	if err != nil {
		return "", fmt.Errorf("stream: %w", err)
	}
	if _, writeErr := w.Write(audio); writeErr != nil {
		return "", fmt.Errorf("write chunk: %w", writeErr)
	}
	return streamResult.ContentType, nil
}

func (s *Service) GetModelCapabilities(ctx context.Context, modelID string) (*ModelCapabilities, error) {
	pgID, err := db.ParseUUID(modelID)
	if err != nil {
		return nil, err
	}
	modelRow, err := s.queries.GetSpeechModelWithProvider(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech model: %w", err)
	}
	def, err := s.registry.Get(models.ClientType(modelRow.ProviderType))
	if err != nil {
		return nil, err
	}
	template := findModelTemplate(def.Models, def.DefaultModel, modelRow.ModelID)
	if template == nil {
		return nil, fmt.Errorf("speech model capabilities not found: %s", modelRow.ModelID)
	}
	caps := template.Capabilities
	if len(caps.ConfigSchema.Fields) == 0 {
		caps.ConfigSchema = template.ConfigSchema
	}
	return &caps, nil
}

func (s *Service) GetSpeechModelCapabilities(ctx context.Context, modelID string) (*ModelCapabilities, error) {
	return s.GetModelCapabilities(ctx, modelID)
}

func (s *Service) GetTranscriptionModelCapabilities(ctx context.Context, modelID string) (*ModelCapabilities, error) {
	pgID, err := db.ParseUUID(modelID)
	if err != nil {
		return nil, err
	}
	modelRow, err := s.queries.GetTranscriptionModelWithProvider(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get transcription model: %w", err)
	}
	def, err := s.registry.Get(models.ClientType(modelRow.ProviderType))
	if err != nil {
		return nil, err
	}
	template := findModelTemplate(def.TranscriptionModels, def.DefaultTranscriptionModel, modelRow.ModelID)
	if template == nil {
		return nil, fmt.Errorf("transcription model capabilities not found: %s", modelRow.ModelID)
	}
	caps := template.Capabilities
	if len(caps.ConfigSchema.Fields) == 0 {
		caps.ConfigSchema = template.ConfigSchema
	}
	return &caps, nil
}

func (s *Service) FetchRemoteModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}

	providerRow, err := s.queries.GetProviderByID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}

	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	if !def.SupportsList || def.Factory == nil {
		return nil, fmt.Errorf("speech provider does not support model discovery: %s", providerRow.ClientType)
	}

	provider, err := def.Factory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, fmt.Errorf("build speech provider: %w", err)
	}

	remoteModels, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list speech models: %w", err)
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

func (s *Service) FetchRemoteTranscriptionModels(ctx context.Context, providerID string) ([]ModelInfo, error) {
	pgID, err := db.ParseUUID(providerID)
	if err != nil {
		return nil, err
	}

	providerRow, err := s.queries.GetProviderByID(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}

	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	if !def.SupportsTranscriptionList || def.TranscriptionFactory == nil {
		return nil, fmt.Errorf("speech provider does not support transcription model discovery: %s", providerRow.ClientType)
	}

	provider, err := def.TranscriptionFactory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, fmt.Errorf("build transcription provider: %w", err)
	}

	remoteModels, err := provider.ListModels(ctx)
	if err != nil {
		return nil, fmt.Errorf("list transcription models: %w", err)
	}

	discovered := make([]ModelInfo, 0, len(remoteModels))
	for _, remoteModel := range remoteModels {
		if remoteModel == nil || remoteModel.ID == "" {
			continue
		}
		discovered = append(discovered, mergeRemoteModelInfo(remoteModel.ID, def.TranscriptionModels))
	}
	return discovered, nil
}

func (s *Service) Transcribe(ctx context.Context, modelID string, audio []byte, filename string, contentType string, overrideCfg map[string]any) (*sdk.TranscriptionResult, error) {
	params, err := s.resolveTranscriptionParams(ctx, modelID, audio, filename, contentType, overrideCfg)
	if err != nil {
		return nil, err
	}
	result, err := sdk.Transcribe(ctx,
		sdk.WithTranscriptionModel(params.model),
		sdk.WithAudio(audio, filename, contentType),
		sdk.WithTranscriptionConfig(params.config),
	)
	if err != nil {
		return nil, fmt.Errorf("transcribe: %w", err)
	}
	return result, nil
}

type resolvedSpeechParams struct {
	model  *sdk.SpeechModel
	config map[string]any
}

type resolvedTranscriptionParams struct {
	model  *sdk.TranscriptionModel
	config map[string]any
}

func (s *Service) resolveSpeechParams(ctx context.Context, modelID string, text string, overrideCfg map[string]any) (*resolvedSpeechParams, error) {
	_ = text
	pgID, err := db.ParseUUID(modelID)
	if err != nil {
		return nil, err
	}

	modelRow, err := s.queries.GetSpeechModelWithProvider(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get speech model: %w", err)
	}
	if !modelRow.Enable {
		return nil, fmt.Errorf("speech model %s is disabled", modelRow.ModelID)
	}
	providerRow, err := s.queries.GetProviderByID(ctx, modelRow.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}

	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	provider, err := def.Factory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, fmt.Errorf("build speech provider: %w", err)
	}

	cfg := mergeConfig(parseConfig(providerRow.Config), parseConfig(modelRow.Config), overrideCfg)
	return &resolvedSpeechParams{
		model:  &sdk.SpeechModel{ID: modelRow.ModelID, Provider: provider},
		config: cfg,
	}, nil
}

func (s *Service) resolveTranscriptionParams(ctx context.Context, modelID string, audio []byte, filename string, contentType string, overrideCfg map[string]any) (*resolvedTranscriptionParams, error) {
	_ = audio
	_ = filename
	_ = contentType
	pgID, err := db.ParseUUID(modelID)
	if err != nil {
		return nil, err
	}

	modelRow, err := s.queries.GetTranscriptionModelWithProvider(ctx, pgID)
	if err != nil {
		return nil, fmt.Errorf("get transcription model: %w", err)
	}
	if !modelRow.Enable {
		return nil, fmt.Errorf("transcription model %s is disabled", modelRow.ModelID)
	}
	providerRow, err := s.queries.GetProviderByID(ctx, modelRow.ProviderID)
	if err != nil {
		return nil, fmt.Errorf("get speech provider: %w", err)
	}

	def, err := s.registry.Get(models.ClientType(providerRow.ClientType))
	if err != nil {
		return nil, err
	}
	provider, err := def.TranscriptionFactory(parseConfig(providerRow.Config))
	if err != nil {
		return nil, fmt.Errorf("build transcription provider: %w", err)
	}

	cfg := mergeConfig(parseConfig(providerRow.Config), parseConfig(modelRow.Config), overrideCfg)
	return &resolvedTranscriptionParams{
		model:  &sdk.TranscriptionModel{ID: modelRow.ModelID, Provider: provider},
		config: cfg,
	}, nil
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
	return ModelInfo{
		ID:   modelID,
		Name: modelID,
	}
}

func (s *Service) shouldHideModel(clientType string, modelType models.ModelType, modelID string) bool {
	def, err := s.registry.Get(models.ClientType(clientType))
	if err != nil {
		return false
	}
	return shouldHideTemplateModel(def, modelType, modelID)
}

func shouldHideTemplateModel(def ProviderDefinition, modelType models.ModelType, modelID string) bool {
	switch modelType {
	case models.ModelTypeSpeech:
		if !def.SupportsList {
			return false
		}
		for _, model := range def.Models {
			if model.ID == modelID {
				return model.TemplateOnly
			}
		}
	case models.ModelTypeTranscription:
		if !def.SupportsTranscriptionList {
			return false
		}
		for _, model := range def.TranscriptionModels {
			if model.ID == modelID {
				return model.TemplateOnly
			}
		}
	}
	return false
}

func findModelTemplate(modelsList []ModelInfo, defaultModel string, modelID string) *ModelInfo {
	for i := range modelsList {
		if modelsList[i].ID == modelID {
			return &modelsList[i]
		}
	}
	if defaultModel != "" {
		for i := range modelsList {
			if modelsList[i].ID == defaultModel {
				return &modelsList[i]
			}
		}
	}
	if len(modelsList) > 0 {
		return &modelsList[0]
	}
	return nil
}

func toSpeechProviderResponse(row sqlc.Provider) SpeechProviderResponse {
	icon := ""
	if row.Icon.Valid {
		icon = row.Icon.String
	}
	return SpeechProviderResponse{
		ID:         row.ID.String(),
		Name:       row.Name,
		ClientType: row.ClientType,
		Icon:       icon,
		Enable:     row.Enable,
		Config:     maskSpeechProviderConfig(parseConfig(row.Config)),
		CreatedAt:  row.CreatedAt.Time,
		UpdatedAt:  row.UpdatedAt.Time,
	}
}

func maskSpeechProviderConfig(cfg map[string]any) map[string]any {
	if len(cfg) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(cfg))
	for key, value := range cfg {
		if s, ok := value.(string); ok && s != "" && isSpeechSecretKey(key) {
			out[key] = maskSpeechSecret(s)
			continue
		}
		out[key] = value
	}
	return out
}

func isSpeechSecretKey(key string) bool {
	switch key {
	case "api_key", "access_key", "secret_key", "app_key":
		return true
	default:
		return false
	}
}

func maskSpeechSecret(value string) string {
	if len(value) <= 8 {
		return "********"
	}
	return value[:4] + "****" + value[len(value)-4:]
}

func toSpeechModelFromListRow(row sqlc.ListSpeechModelsRow) SpeechModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return SpeechModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: row.ProviderType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toSpeechModelFromModel(row sqlc.Model, providerType string) SpeechModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return SpeechModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: providerType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toSpeechModelWithProviderResponse(row sqlc.GetSpeechModelWithProviderRow) SpeechModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return SpeechModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: row.ProviderType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toTranscriptionModelFromListRow(row sqlc.ListTranscriptionModelsRow) TranscriptionModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return TranscriptionModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: row.ProviderType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toTranscriptionModelFromModel(row sqlc.Model, providerType string) TranscriptionModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return TranscriptionModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: providerType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}

func toTranscriptionModelWithProviderResponse(row sqlc.GetTranscriptionModelWithProviderRow) TranscriptionModelResponse {
	var cfg map[string]any
	if len(row.Config) > 0 {
		_ = json.Unmarshal(row.Config, &cfg)
	}
	name := ""
	if row.Name.Valid {
		name = row.Name.String
	}
	return TranscriptionModelResponse{
		ID:           row.ID.String(),
		ModelID:      row.ModelID,
		Name:         name,
		ProviderID:   row.ProviderID.String(),
		ProviderType: row.ProviderType,
		Config:       cfg,
		CreatedAt:    row.CreatedAt.Time,
		UpdatedAt:    row.UpdatedAt.Time,
	}
}
