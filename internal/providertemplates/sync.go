package providertemplates

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type transactionRunner interface {
	InTx(context.Context, func(dbstore.Queries) error) error
}

func Sync(ctx context.Context, logger *slog.Logger, queries dbstore.Queries, definitions []Definition) error {
	if logger == nil {
		logger = slog.Default()
	}
	run := func(q dbstore.Queries) error {
		if err := q.AcquireProviderTemplateSyncLock(ctx); err != nil {
			return fmt.Errorf("acquire provider template sync lock: %w", err)
		}
		return syncLocked(ctx, logger, q, definitions)
	}
	if tx, ok := queries.(transactionRunner); ok {
		return tx.InTx(ctx, run)
	}
	return run(queries)
}

func syncLocked(ctx context.Context, logger *slog.Logger, queries dbstore.Queries, definitions []Definition) error {
	existing, err := queries.ListAllProviderTemplates(ctx)
	if err != nil {
		return fmt.Errorf("list provider templates: %w", err)
	}
	byIdentity := make(map[string]sqlc.TemplateProviderTemplate, len(existing))
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
		row, err = upsertDefinition(ctx, queries, definition, hash)
		if err != nil {
			return fmt.Errorf("upsert provider template %s: %w", key, err)
		}
		if err := syncModels(ctx, queries, row.ID, definition.Models); err != nil {
			return fmt.Errorf("sync provider template models %s: %w", key, err)
		}
		logger.Info("provider template synced", slog.String("domain", row.Domain), slog.String("key", row.Key))
	}
	for _, row := range existing {
		if _, ok := seen[identity(row.Domain, row.Key)]; ok {
			continue
		}
		if err := queries.SetProviderTemplateActive(ctx, sqlc.SetProviderTemplateActiveParams{ID: row.ID, Active: false}); err != nil {
			return fmt.Errorf("deactivate provider template %s/%s: %w", row.Domain, row.Key, err)
		}
	}
	return nil
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

func upsertDefinition(ctx context.Context, queries dbstore.Queries, definition Definition, hash string) (sqlc.TemplateProviderTemplate, error) {
	configSchema, err := json.Marshal(definition.ConfigSchema)
	if err != nil {
		return sqlc.TemplateProviderTemplate{}, err
	}
	defaultConfig, err := json.Marshal(definition.DefaultConfig)
	if err != nil {
		return sqlc.TemplateProviderTemplate{}, err
	}
	metadata, err := json.Marshal(definition.Metadata)
	if err != nil {
		return sqlc.TemplateProviderTemplate{}, err
	}
	icon := pgtype.Text{}
	if strings.TrimSpace(definition.Icon) != "" {
		icon = pgtype.Text{String: definition.Icon, Valid: true}
	}
	return queries.UpsertProviderTemplate(ctx, sqlc.UpsertProviderTemplateParams{
		Key:           definition.Key,
		Domain:        string(definition.Domain),
		Name:          definition.Name,
		Description:   definition.Description,
		Icon:          icon,
		Driver:        definition.Driver,
		ConfigSchema:  configSchema,
		DefaultConfig: defaultConfig,
		Metadata:      metadata,
		Source:        definition.Source,
		ContentHash:   hash,
		SortOrder:     int32(definition.SortOrder), //nolint:gosec // Catalog sizes are bounded by checked-in configuration.
	})
}

func syncModels(ctx context.Context, queries dbstore.Queries, templateID pgtype.UUID, definitions []ModelDefinition) error {
	existing, err := queries.ListAllProviderTemplateModels(ctx, templateID)
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
		if _, err := queries.UpsertProviderTemplateModel(ctx, sqlc.UpsertProviderTemplateModelParams{
			ProviderTemplateID: templateID,
			ModelID:            definition.ModelID,
			Name:               definition.Name,
			Type:               definition.Type,
			Config:             config,
			Metadata:           metadata,
			SortOrder:          int32(definition.SortOrder), //nolint:gosec // Catalog sizes are bounded by checked-in configuration.
		}); err != nil {
			return err
		}
	}
	for _, row := range existing {
		if _, ok := seen[identity(row.Type, row.ModelID)]; ok {
			continue
		}
		if err := queries.SetProviderTemplateModelActive(ctx, sqlc.SetProviderTemplateModelActiveParams{ID: row.ID, Active: false}); err != nil {
			return err
		}
	}
	return nil
}

func identity(left, right string) string {
	return strings.ToLower(strings.TrimSpace(left)) + "/" + strings.ToLower(strings.TrimSpace(right))
}
