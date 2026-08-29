package video

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
					logger.Warn("video registry skipped provider without template",
						slog.String("provider", string(def.ClientType)),
						slog.String("display_name", def.DisplayName))
				}
				continue
			}
			return fmt.Errorf("get provider by client type %s: %w", def.ClientType, err)
		}

		synced := 0
		for _, model := range def.Models {
			modelConfigJSON, err := json.Marshal(map[string]any{})
			if err != nil {
				return fmt.Errorf("marshal video model config: %w", err)
			}
			name := pgtype.Text{String: model.Name, Valid: model.Name != ""}
			if _, err := queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
				ModelID:    model.ID,
				Name:       name,
				ProviderID: provider.ID,
				Type:       string(models.ModelTypeVideo),
				Config:     modelConfigJSON,
			}); err != nil {
				return fmt.Errorf("upsert video model %s: %w", model.ID, err)
			}
			synced++
		}

		if logger != nil {
			logger.Info("video registry synced", slog.String("provider", string(def.ClientType)), slog.Int("models", synced))
		}
	}
	return nil
}
