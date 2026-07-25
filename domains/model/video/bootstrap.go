package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	modeldomain "github.com/memohai/memoh/domains/model"
	videoport "github.com/memohai/memoh/domains/model/internal/port/video"
)

// SyncRegistry upserts registry video template models into the catalog store.
// Missing provider templates are skipped.
func SyncRegistry(ctx context.Context, logger *slog.Logger, store videoport.CatalogStore, registry *Registry) error {
	for _, def := range registry.List() {
		provider, err := store.GetProviderByClientType(ctx, def.ClientType)
		if err != nil {
			if errors.Is(err, videoport.ErrProviderNotFound) {
				if logger != nil {
					logger.WarnContext(ctx, "video registry skipped provider without template",
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
			if err := store.UpsertVideoCatalogModel(ctx, videoport.CatalogModelInput{
				ModelID:    model.ID,
				Name:       model.Name,
				ProviderID: provider.ID,
				Type:       modeldomain.ModelTypeVideo,
				Config:     modelConfigJSON,
			}); err != nil {
				return fmt.Errorf("upsert video model %s: %w", model.ID, err)
			}
			synced++
		}

		if logger != nil {
			logger.InfoContext(ctx, "video registry synced", slog.String("provider", string(def.ClientType)), slog.Int("models", synced))
		}
	}
	return nil
}
