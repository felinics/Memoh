package audio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	modeldomain "github.com/memohai/memoh/domains/model"
	audioport "github.com/memohai/memoh/domains/model/internal/port/audio"
)

// SyncRegistry upserts registry speech/transcription template models into the
// catalog store. Missing provider templates are skipped.
func SyncRegistry(ctx context.Context, logger *slog.Logger, store audioport.CatalogStore, registry *Registry) error {
	for _, def := range registry.List() {
		provider, err := store.GetProviderByClientType(ctx, def.ClientType)
		if err != nil {
			if errors.Is(err, audioport.ErrProviderNotFound) {
				if logger != nil {
					logger.WarnContext(ctx, "audio registry skipped provider without template",
						slog.String("provider", string(def.ClientType)),
						slog.String("display_name", def.DisplayName))
				}
				continue
			}
			if logger != nil {
				logger.WarnContext(ctx, "audio registry failed to load provider template",
					slog.String("provider", string(def.ClientType)),
					slog.String("display_name", def.DisplayName),
					slog.Any("error", err))
			}
			return fmt.Errorf("get provider by client type %s: %w", def.ClientType, err)
		}

		synced := 0
		if !isTranscriptionClientType(def.ClientType) {
			for _, model := range def.Models {
				if shouldHideTemplateModel(def, modeldomain.ModelTypeSpeech, model.ID) {
					if err := store.DeleteAudioCatalogModel(ctx, provider.ID, model.ID, modeldomain.ModelTypeSpeech); err != nil {
						return fmt.Errorf("delete hidden speech template model %s: %w", model.ID, err)
					}
					continue
				}
				modelConfigJSON, err := json.Marshal(map[string]any{})
				if err != nil {
					return fmt.Errorf("marshal speech model config: %w", err)
				}
				if _, err := store.UpsertAudioCatalogModel(ctx, audioport.CatalogModelInput{
					ModelID:    model.ID,
					Name:       model.Name,
					ProviderID: provider.ID,
					Type:       modeldomain.ModelTypeSpeech,
					Config:     modelConfigJSON,
				}); err != nil {
					return fmt.Errorf("upsert speech model %s: %w", model.ID, err)
				}
				synced++
			}
		}
		for _, model := range def.TranscriptionModels {
			if shouldHideTemplateModel(def, modeldomain.ModelTypeTranscription, model.ID) {
				if err := store.DeleteAudioCatalogModel(ctx, provider.ID, model.ID, modeldomain.ModelTypeTranscription); err != nil {
					return fmt.Errorf("delete hidden transcription template model %s: %w", model.ID, err)
				}
				continue
			}
			modelConfigJSON, err := json.Marshal(map[string]any{})
			if err != nil {
				return fmt.Errorf("marshal transcription model config: %w", err)
			}
			if _, err := store.UpsertAudioCatalogModel(ctx, audioport.CatalogModelInput{
				ModelID:    model.ID,
				Name:       model.Name,
				ProviderID: provider.ID,
				Type:       modeldomain.ModelTypeTranscription,
				Config:     modelConfigJSON,
			}); err != nil {
				return fmt.Errorf("upsert transcription model %s: %w", model.ID, err)
			}
		}

		if logger != nil {
			logger.InfoContext(ctx, "speech registry synced", slog.String("provider", string(def.ClientType)), slog.Int("models", synced))
		}
	}
	return nil
}
