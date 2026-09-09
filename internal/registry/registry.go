package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	"gopkg.in/yaml.v3"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

const registryMetadataKey = "registry"

// Load reads all .yaml / .yml files from dir and returns parsed provider
// definitions. It returns nil (no error) when the directory does not exist.
// Malformed files are skipped with a warning logged via log.
func Load(log *slog.Logger, dir string) ([]ProviderDefinition, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read providers dir %s: %w", dir, err)
	}

	var defs []ProviderDefinition
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path) //nolint:gosec // operator-managed config directory
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		var def ProviderDefinition
		if err := yaml.Unmarshal(data, &def); err != nil {
			log.Warn("registry: skipping malformed provider file",
				slog.String("path", path), slog.Any("error", err))
			continue
		}
		if def.Name == "" {
			continue
		}
		def.Source = registrySource(e.Name())
		defs = append(defs, def)
	}
	return defs, nil
}

// Sync upserts the given provider definitions into the database. New providers
// are created with enable=false. Existing registry providers are matched by
// source file, then name, then legacy model fingerprints, so changing a YAML
// display name updates the existing provider instead of creating a duplicate.
// Existing enable state, credentials, and custom config values are preserved.
// Models are upserted by (provider_id, model_id), overwriting name/type/config.
func Sync(ctx context.Context, logger *slog.Logger, queries dbstore.Queries, defs []ProviderDefinition) error {
	existingProviders, err := listRegistryProviders(ctx, queries)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	providerIndex, err := newProviderIndex(ctx, queries, existingProviders)
	if err != nil {
		return fmt.Errorf("index providers: %w", err)
	}
	usedProviders := make(map[string]bool, len(defs))

	for _, def := range defs {
		var icon pgtype.Text
		if def.Icon != "" {
			icon = pgtype.Text{String: def.Icon, Valid: true}
		}

		providerCfg := providerConfigFromDefinition(def)
		providerConfigJSON, err := json.Marshal(providerCfg)
		if err != nil {
			logger.Warn("registry: failed to marshal provider config",
				slog.String("name", def.Name), slog.Any("error", err))
			continue
		}

		provider, err := syncProvider(ctx, queries, providerIndex, usedProviders, def, icon, providerCfg, providerConfigJSON)
		if err != nil {
			logger.Warn("registry: failed to upsert provider", slog.String("name", def.Name), slog.Any("error", err))
			continue
		}
		providerIndex.add(provider)
		usedProviders[providerKey(provider)] = true

		for _, m := range def.Models {
			configJSON, err := json.Marshal(m.Config)
			if err != nil {
				logger.Warn("registry: failed to marshal model config",
					slog.String("provider", def.Name), slog.String("model", m.ModelID), slog.Any("error", err))
				continue
			}

			var name pgtype.Text
			if m.Name != "" {
				name = pgtype.Text{String: m.Name, Valid: true}
			}

			typ := m.Type
			if typ == "" {
				typ = "chat"
			}

			_, err = queries.UpsertRegistryModel(ctx, sqlc.UpsertRegistryModelParams{
				ModelID:    m.ModelID,
				Name:       name,
				ProviderID: provider.ID,
				Type:       typ,
				Config:     configJSON,
			})
			if err != nil {
				logger.Warn("registry: failed to upsert model",
					slog.String("provider", def.Name), slog.String("model", m.ModelID), slog.Any("error", err))
				continue
			}
		}

		logger.Info("registry: synced provider", slog.String("name", def.Name), slog.Int("models", len(def.Models)))
	}
	return nil
}

type providerIndex struct {
	bySource  map[string]sqlc.Provider
	byName    map[string]sqlc.Provider
	modelIDs  map[string]map[string]bool
	providers []sqlc.Provider
}

func newProviderIndex(ctx context.Context, queries dbstore.Queries, providers []sqlc.Provider) (*providerIndex, error) {
	idx := &providerIndex{
		bySource:  make(map[string]sqlc.Provider, len(providers)),
		byName:    make(map[string]sqlc.Provider, len(providers)),
		modelIDs:  make(map[string]map[string]bool, len(providers)),
		providers: make([]sqlc.Provider, 0, len(providers)),
	}
	for _, provider := range providers {
		idx.add(provider)
		models, err := queries.ListModelsByProviderID(ctx, provider.ID)
		if err != nil {
			return nil, err
		}
		modelIDs := make(map[string]bool, len(models))
		for _, model := range models {
			modelID := strings.TrimSpace(model.ModelID)
			if modelID != "" {
				modelIDs[modelID] = true
			}
		}
		idx.modelIDs[providerKey(provider)] = modelIDs
	}
	return idx, nil
}

func (idx *providerIndex) add(provider sqlc.Provider) {
	if idx == nil {
		return
	}
	idx.providers = append(idx.providers, provider)
	if name := strings.TrimSpace(provider.Name); name != "" {
		idx.byName[name] = provider
	}
	if source := providerRegistrySource(provider); source != "" {
		idx.bySource[source] = provider
	}
}

func syncProvider(
	ctx context.Context,
	queries dbstore.Queries,
	idx *providerIndex,
	used map[string]bool,
	def ProviderDefinition,
	icon pgtype.Text,
	registryCfg map[string]any,
	registryConfigJSON []byte,
) (sqlc.Provider, error) {
	if existing, ok := matchExistingProvider(idx, used, def, registryCfg); ok {
		configJSON, err := json.Marshal(mergeRegistryProviderConfig(registryCfg, parseJSONMap(existing.Config)))
		if err != nil {
			return sqlc.Provider{}, fmt.Errorf("marshal merged provider config: %w", err)
		}
		metadataJSON, err := json.Marshal(withRegistryMetadata(parseJSONMap(existing.Metadata), def.Source))
		if err != nil {
			return sqlc.Provider{}, fmt.Errorf("marshal provider metadata: %w", err)
		}
		return queries.UpdateProvider(ctx, sqlc.UpdateProviderParams{
			ID:         existing.ID,
			Name:       def.Name,
			ClientType: def.ClientType,
			Icon:       icon,
			Enable:     existing.Enable,
			Config:     configJSON,
			Metadata:   metadataJSON,
		})
	}

	created, err := queries.UpsertRegistryProvider(ctx, sqlc.UpsertRegistryProviderParams{
		Name:       def.Name,
		ClientType: def.ClientType,
		Icon:       icon,
		Config:     registryConfigJSON,
	})
	if err != nil {
		return sqlc.Provider{}, err
	}
	metadataJSON, err := json.Marshal(withRegistryMetadata(parseJSONMap(created.Metadata), def.Source))
	if err != nil {
		return sqlc.Provider{}, fmt.Errorf("marshal provider metadata: %w", err)
	}
	return queries.UpdateProvider(ctx, sqlc.UpdateProviderParams{
		ID:         created.ID,
		Name:       created.Name,
		ClientType: created.ClientType,
		Icon:       created.Icon,
		Enable:     created.Enable,
		Config:     created.Config,
		Metadata:   metadataJSON,
	})
}

func matchExistingProvider(idx *providerIndex, used map[string]bool, def ProviderDefinition, registryCfg map[string]any) (sqlc.Provider, bool) {
	if idx == nil {
		return sqlc.Provider{}, false
	}
	if source := registrySource(def.Source); source != "" {
		if provider, ok := idx.bySource[source]; ok && !used[providerKey(provider)] {
			return provider, true
		}
	}
	if provider, ok := idx.byName[def.Name]; ok && !used[providerKey(provider)] {
		return provider, true
	}
	return matchLegacyProviderByModels(idx, used, def, registryCfg)
}

func matchLegacyProviderByModels(idx *providerIndex, used map[string]bool, def ProviderDefinition, registryCfg map[string]any) (sqlc.Provider, bool) {
	defModelIDs := definitionModelIDs(def)
	if len(defModelIDs) == 0 {
		return sqlc.Provider{}, false
	}
	baseURL := normalizedBaseURL(configString(registryCfg, "base_url"))
	var best sqlc.Provider
	bestScore := 0
	tied := false
	for _, provider := range idx.providers {
		key := providerKey(provider)
		if used[key] || provider.ClientType != def.ClientType {
			continue
		}
		if providerRegistrySource(provider) != "" {
			continue
		}
		overlap := modelOverlap(defModelIDs, idx.modelIDs[key])
		if overlap == 0 {
			continue
		}
		score := overlap
		if baseURL != "" && normalizedBaseURL(configString(parseJSONMap(provider.Config), "base_url")) == baseURL {
			score += 1000
		}
		if score > bestScore {
			best = provider
			bestScore = score
			tied = false
			continue
		}
		if score == bestScore {
			tied = true
		}
	}
	if bestScore == 0 || tied {
		return sqlc.Provider{}, false
	}
	return best, true
}

func definitionModelIDs(def ProviderDefinition) map[string]bool {
	modelIDs := make(map[string]bool, len(def.Models))
	for _, model := range def.Models {
		modelID := strings.TrimSpace(model.ModelID)
		if modelID != "" {
			modelIDs[modelID] = true
		}
	}
	return modelIDs
}

func modelOverlap(left, right map[string]bool) int {
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	overlap := 0
	for modelID := range left {
		if right[modelID] {
			overlap++
		}
	}
	return overlap
}

func listRegistryProviders(ctx context.Context, queries dbstore.Queries) ([]sqlc.Provider, error) {
	var out []sqlc.Provider
	seen := make(map[string]bool)
	for _, list := range []func(context.Context) ([]sqlc.Provider, error){
		queries.ListProviders,
		queries.ListSpeechProviders,
		queries.ListTranscriptionProviders,
	} {
		providers, err := list(ctx)
		if err != nil {
			return nil, err
		}
		for _, provider := range providers {
			key := providerKey(provider)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, provider)
		}
	}
	return out, nil
}

func providerConfigFromDefinition(def ProviderDefinition) map[string]any {
	providerCfg := make(map[string]any, len(def.Config)+1)
	for k, v := range def.Config {
		providerCfg[k] = v
	}
	if def.BaseURL != "" {
		providerCfg["base_url"] = def.BaseURL
	}
	return providerCfg
}

func mergeRegistryProviderConfig(registryCfg, existingCfg map[string]any) map[string]any {
	merged := make(map[string]any, len(registryCfg)+len(existingCfg))
	for k, v := range registryCfg {
		merged[k] = v
	}
	for k, v := range existingCfg {
		merged[k] = v
	}
	return merged
}

func withRegistryMetadata(metadata map[string]any, source string) map[string]any {
	if metadata == nil {
		metadata = map[string]any{}
	}
	source = registrySource(source)
	if source == "" {
		return metadata
	}
	registryMeta, _ := metadata[registryMetadataKey].(map[string]any)
	if registryMeta == nil {
		registryMeta = map[string]any{}
	}
	registryMeta["source"] = source
	metadata[registryMetadataKey] = registryMeta
	return metadata
}

func providerRegistrySource(provider sqlc.Provider) string {
	metadata := parseJSONMap(provider.Metadata)
	registryMeta, _ := metadata[registryMetadataKey].(map[string]any)
	if registryMeta == nil {
		return ""
	}
	return registrySource(configString(registryMeta, "source"))
}

func parseJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	value, _ := cfg[key].(string)
	return value
}

func normalizedBaseURL(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func registrySource(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	base := strings.TrimSpace(filepath.Base(value))
	if base == "." {
		return ""
	}
	return strings.ToLower(base)
}

func providerKey(provider sqlc.Provider) string {
	return provider.ID.String()
}
