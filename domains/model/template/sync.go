package template

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

func Sync(ctx context.Context, logger *slog.Logger, store templateport.SyncStore, definitions []Definition) error {
	if logger == nil {
		logger = slog.Default()
	}
	if store == nil {
		return errors.New("provider template sync store is required")
	}
	return store.RunSyncTransaction(ctx, func(tx templateport.Transaction) error {
		if err := tx.AcquireSyncLock(ctx); err != nil {
			return fmt.Errorf("acquire provider template sync lock: %w", err)
		}
		return syncLocked(ctx, logger, tx, compactModels(definitions))
	})
}

func syncLocked(ctx context.Context, logger *slog.Logger, tx templateport.Transaction, definitions []Definition) error {
	existing, err := tx.ListTemplates(ctx)
	if err != nil {
		return fmt.Errorf("list provider templates: %w", err)
	}
	byIdentity := make(map[string]templateport.TemplateRecord, len(existing))
	for _, row := range existing {
		byIdentity[identity(row.Domain, row.Key)] = row
	}
	seen := make(map[string]struct{}, len(definitions))
	for index, raw := range definitions {
		definition, hash, err := normalizeDefinition(raw, index)
		if err != nil {
			return err
		}
		key := identity(string(definition.Domain), definition.Key)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate provider template %s", key)
		}
		seen[key] = struct{}{}

		row, exists := byIdentity[key]
		if exists && row.Active && row.ContentHash == hash {
			continue
		}
		row, err = upsertDefinition(ctx, tx, definition, hash)
		if err != nil {
			return fmt.Errorf("upsert provider template %s: %w", key, err)
		}
		if err := syncModels(ctx, tx, row.ID, definition.Models); err != nil {
			return fmt.Errorf("sync provider template models %s: %w", key, err)
		}
		logger.InfoContext(ctx, "provider template synced", slog.String("domain", row.Domain), slog.String("key", row.Key))
	}
	for _, row := range existing {
		if _, ok := seen[identity(row.Domain, row.Key)]; ok {
			continue
		}
		if err := tx.DeactivateTemplate(ctx, row.ID); err != nil {
			return fmt.Errorf("deactivate provider template %s/%s: %w", row.Domain, row.Key, err)
		}
	}
	return nil
}

// compactModels keeps the last model for each (type, model_id) pair, matching
// historical YAML catalog sync behavior.
func compactModels(definitions []Definition) []Definition {
	out := make([]Definition, len(definitions))
	for i, definition := range definitions {
		out[i] = definition
		if len(definition.Models) == 0 {
			continue
		}
		lastModelIndex := make(map[string]int, len(definition.Models))
		for modelIndex, model := range definition.Models {
			modelType := strings.TrimSpace(model.Type)
			if modelType == "" {
				modelType = "chat"
			}
			lastModelIndex[strings.ToLower(modelType)+"\x00"+strings.ToLower(strings.TrimSpace(model.ModelID))] = modelIndex
		}
		models := make([]ModelDefinition, 0, len(lastModelIndex))
		for modelIndex, model := range definition.Models {
			modelType := strings.TrimSpace(model.Type)
			if modelType == "" {
				modelType = "chat"
			}
			modelKey := strings.ToLower(modelType) + "\x00" + strings.ToLower(strings.TrimSpace(model.ModelID))
			if lastModelIndex[modelKey] != modelIndex {
				continue
			}
			model.Type = modelType
			model.SortOrder = modelIndex
			models = append(models, model)
		}
		out[i].Models = models
	}
	return out
}

func normalizeDefinition(raw Definition, fallbackOrder int) (Definition, string, error) {
	definition := raw
	definition.Key = strings.ToLower(strings.TrimSpace(definition.Key))
	definition.Name = strings.TrimSpace(definition.Name)
	definition.Driver = strings.TrimSpace(definition.Driver)
	definition.Source = strings.TrimSpace(definition.Source)
	if definition.SortOrder == 0 {
		definition.SortOrder = fallbackOrder
	}
	if definition.Key == "" || definition.Name == "" || definition.Driver == "" || !IsValidDomain(definition.Domain) {
		return Definition{}, "", fmt.Errorf("invalid provider template definition %q", definition.Key)
	}
	if definition.ConfigSchema == nil {
		definition.ConfigSchema = map[string]any{}
	}
	if definition.DefaultConfig == nil {
		definition.DefaultConfig = map[string]any{}
	}
	if definition.Metadata == nil {
		definition.Metadata = map[string]any{}
	}
	for index := range definition.Models {
		model := &definition.Models[index]
		model.ModelID = strings.TrimSpace(model.ModelID)
		model.Name = strings.TrimSpace(model.Name)
		model.Type = strings.TrimSpace(model.Type)
		if model.Type == "" {
			model.Type = "chat"
		}
		if model.SortOrder == 0 {
			model.SortOrder = index
		}
		if model.Config == nil {
			model.Config = map[string]any{}
		}
		if model.Metadata == nil {
			model.Metadata = map[string]any{}
		}
		if model.ModelID == "" {
			return Definition{}, "", fmt.Errorf("provider template %s has an empty model id", definition.Key)
		}
	}
	payload, err := json.Marshal(definition)
	if err != nil {
		return Definition{}, "", fmt.Errorf("marshal provider template %s: %w", definition.Key, err)
	}
	sum := sha256.Sum256(payload)
	return definition, hex.EncodeToString(sum[:]), nil
}

func upsertDefinition(ctx context.Context, tx templateport.Transaction, definition Definition, hash string) (templateport.TemplateRecord, error) {
	configSchema, err := json.Marshal(definition.ConfigSchema)
	if err != nil {
		return templateport.TemplateRecord{}, err
	}
	defaultConfig, err := json.Marshal(definition.DefaultConfig)
	if err != nil {
		return templateport.TemplateRecord{}, err
	}
	metadata, err := json.Marshal(definition.Metadata)
	if err != nil {
		return templateport.TemplateRecord{}, err
	}
	return tx.UpsertTemplate(ctx, templateport.UpsertTemplateCommand{
		Key:           definition.Key,
		Domain:        string(definition.Domain),
		Name:          definition.Name,
		Description:   definition.Description,
		Icon:          definition.Icon,
		Driver:        definition.Driver,
		ConfigSchema:  configSchema,
		DefaultConfig: defaultConfig,
		Metadata:      metadata,
		Source:        definition.Source,
		ContentHash:   hash,
		SortOrder:     definition.SortOrder,
	})
}

func syncModels(ctx context.Context, tx templateport.Transaction, templateID string, definitions []ModelDefinition) error {
	existing, err := tx.ListModels(ctx, templateID)
	if err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		key := identity(definition.Type, definition.ModelID)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate template model %s", key)
		}
		seen[key] = struct{}{}
		config, err := json.Marshal(definition.Config)
		if err != nil {
			return err
		}
		metadata, err := json.Marshal(definition.Metadata)
		if err != nil {
			return err
		}
		if err := tx.UpsertModel(ctx, templateport.UpsertModelCommand{
			TemplateID: templateID,
			ModelID:    definition.ModelID,
			Name:       definition.Name,
			Type:       definition.Type,
			Config:     config,
			Metadata:   metadata,
			SortOrder:  definition.SortOrder,
		}); err != nil {
			return err
		}
	}
	for _, row := range existing {
		if _, ok := seen[identity(row.Type, row.ModelID)]; ok {
			continue
		}
		if err := tx.DeactivateModel(ctx, row.ID); err != nil {
			return err
		}
	}
	return nil
}

func identity(left, right string) string {
	return strings.ToLower(strings.TrimSpace(left)) + "/" + strings.ToLower(strings.TrimSpace(right))
}
