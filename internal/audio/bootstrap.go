package audio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
)

func SyncRegistry(ctx context.Context, logger *slog.Logger, queries dbstore.Queries, registry *Registry) error {
	for _, def := range registry.List() {
		provider, err := queries.GetProviderByClientType(ctx, string(def.ClientType))
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				if logger != nil {
					logger.Warn("audio registry skipped provider without template",
						slog.String("provider", string(def.ClientType)),
						slog.String("display_name", def.DisplayName))
				}
				continue
			}
			if logger != nil {
				logger.Warn("audio registry failed to load provider template",
					slog.String("provider", string(def.ClientType)),
					slog.String("display_name", def.DisplayName),
					slog.Any("error", err))
			}
			return fmt.Errorf("get provider by client type %s: %w", def.ClientType, err)
		}

		synced := 0
		if !isTranscriptionClientType(def.ClientType) {
			for _, model := range def.Models {
				if shouldHideTemplateModel(def, models.ModelTypeSpeech, model.ID) {
					if err := queries.DeleteModelByProviderAndType(ctx, sqlc.DeleteModelByProviderAndTypeParams{
						ProviderID: provider.ID,
						ModelID:    model.ID,
						Type:       string(models.ModelTypeSpeech),
					}); err != nil {
						return fmt.Errorf("delete hidden speech template model %s: %w", model.ID, err)
					}
					continue
				}
				modelConfigJSON, err := json.Marshal(map[string]any{})
				if err != nil {
					return fmt.Errorf("marshal speech model config: %w", err)
				}
				name := pgtype.Text{String: model.Name, Valid: model.Name != ""}
				if _, err := queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
					ModelID:    model.ID,
					Name:       name,
					ProviderID: provider.ID,
					Type:       string(models.ModelTypeSpeech),
					Config:     modelConfigJSON,
				}); err != nil {
					return fmt.Errorf("upsert speech model %s: %w", model.ID, err)
				}
				synced++
			}
		}
		for _, model := range def.TranscriptionModels {
			if shouldHideTemplateModel(def, models.ModelTypeTranscription, model.ID) {
				if err := queries.DeleteModelByProviderAndType(ctx, sqlc.DeleteModelByProviderAndTypeParams{
					ProviderID: provider.ID,
					ModelID:    model.ID,
					Type:       string(models.ModelTypeTranscription),
				}); err != nil {
					return fmt.Errorf("delete hidden transcription template model %s: %w", model.ID, err)
				}
				continue
			}
			modelConfigJSON, err := json.Marshal(map[string]any{})
			if err != nil {
				return fmt.Errorf("marshal transcription model config: %w", err)
			}
			name := pgtype.Text{String: model.Name, Valid: model.Name != ""}
			if _, err := queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
				ModelID:    model.ID,
				Name:       name,
				ProviderID: provider.ID,
				Type:       string(models.ModelTypeTranscription),
				Config:     modelConfigJSON,
			}); err != nil {
				return fmt.Errorf("upsert transcription model %s: %w", model.ID, err)
			}
		}

		if logger != nil {
			logger.Info("speech registry synced", slog.String("provider", string(def.ClientType)), slog.Int("models", synced))
		}
	}
	return nil
}
