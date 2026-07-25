package template

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

const registryMetadataKey = "registry"

// SyncProviders upserts YAML template definitions into the provider/model
// catalogs. New providers are created with enable=false. Existing registry
// providers are matched by source file, then name, then legacy model
// fingerprints, so changing a YAML display name updates the existing provider
// instead of creating a duplicate. Existing enable state, credentials, and
// custom config values are preserved. Models are upserted by
// (provider_id, model_id), overwriting name/type/config.
func SyncProviders(ctx context.Context, logger *slog.Logger, providers templateport.ProviderCatalog, models templateport.ModelCatalog, defs []Definition) error {
	if logger == nil {
		logger = slog.Default()
	}
	existingProviders, err := providers.ListProviders(ctx)
	if err != nil {
		return fmt.Errorf("list providers: %w", err)
	}
	providerIndex, err := newProviderIndex(ctx, models, existingProviders)
	if err != nil {
		return fmt.Errorf("index providers: %w", err)
	}
	usedProviders := make(map[string]bool, len(defs))

	for _, def := range defs {
		providerCfg := providerConfigFromDefinition(def)
		providerConfigJSON, err := json.Marshal(providerCfg)
		if err != nil {
			logger.WarnContext(ctx, "registry: failed to marshal provider config",
				slog.String("name", def.Name), slog.Any("error", err))
			continue
		}

		provider, err := syncProvider(ctx, providers, providerIndex, usedProviders, def, providerCfg, providerConfigJSON)
		if err != nil {
			logger.WarnContext(ctx, "registry: failed to upsert provider", slog.String("name", def.Name), slog.Any("error", err))
			continue
		}
		providerIndex.add(provider)
		usedProviders[providerKey(provider)] = true

		for _, m := range def.Models {
			configJSON, err := json.Marshal(m.Config)
			if err != nil {
				logger.WarnContext(ctx, "registry: failed to marshal model config",
					slog.String("provider", def.Name), slog.String("model", m.ModelID), slog.Any("error", err))
				continue
			}

			typ := m.Type
			if typ == "" {
				typ = "chat"
			}

			err = models.UpsertModel(ctx, templateport.ModelSeed{
				ModelID:    m.ModelID,
				Name:       m.Name,
				ProviderID: provider.ID,
				Type:       typ,
				Config:     configJSON,
			})
			if err != nil {
				logger.WarnContext(ctx, "registry: failed to upsert model",
					slog.String("provider", def.Name), slog.String("model", m.ModelID), slog.Any("error", err))
				continue
			}
		}

		logger.InfoContext(ctx, "registry: synced provider", slog.String("name", def.Name), slog.Int("models", len(def.Models)))
	}
	return nil
}

type providerIndex struct {
	bySource  map[string]templateport.ProviderRecord
	byName    map[string]templateport.ProviderRecord
	modelIDs  map[string]map[string]bool
	providers []templateport.ProviderRecord
}

func newProviderIndex(ctx context.Context, models templateport.ModelCatalog, providers []templateport.ProviderRecord) (*providerIndex, error) {
	idx := &providerIndex{
		bySource:  make(map[string]templateport.ProviderRecord, len(providers)),
		byName:    make(map[string]templateport.ProviderRecord, len(providers)),
		modelIDs:  make(map[string]map[string]bool, len(providers)),
		providers: make([]templateport.ProviderRecord, 0, len(providers)),
	}
	for _, provider := range providers {
		idx.add(provider)
		modelIDs, err := models.ListModelIDs(ctx, provider.ID)
		if err != nil {
			return nil, err
		}
		indexedModelIDs := make(map[string]bool, len(modelIDs))
		for _, model := range modelIDs {
			modelID := strings.TrimSpace(model)
			if modelID != "" {
				indexedModelIDs[modelID] = true
			}
		}
		idx.modelIDs[providerKey(provider)] = indexedModelIDs
	}
	return idx, nil
}

func (idx *providerIndex) add(provider templateport.ProviderRecord) {
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
	providers templateport.ProviderCatalog,
	idx *providerIndex,
	used map[string]bool,
	def Definition,
	registryCfg map[string]any,
	registryConfigJSON []byte,
) (templateport.ProviderRecord, error) {
	if existing, ok := matchExistingProvider(idx, used, def, registryCfg); ok {
		configJSON, err := json.Marshal(mergeRegistryProviderConfig(registryCfg, parseJSONMap(existing.Config)))
		if err != nil {
			return templateport.ProviderRecord{}, fmt.Errorf("marshal merged provider config: %w", err)
		}
		metadataJSON, err := json.Marshal(withRegistryMetadata(parseJSONMap(existing.Metadata), def.Source))
		if err != nil {
			return templateport.ProviderRecord{}, fmt.Errorf("marshal provider metadata: %w", err)
		}
		return providers.UpdateProvider(ctx, templateport.ProviderUpdate{
			ID:         existing.ID,
			Name:       def.Name,
			ClientType: def.Driver,
			Icon:       def.Icon,
			Enable:     existing.Enable,
			Config:     configJSON,
			Metadata:   metadataJSON,
		})
	}

	created, err := providers.UpsertProvider(ctx, templateport.ProviderSeed{
		Name:       def.Name,
		ClientType: def.Driver,
		Icon:       def.Icon,
		Config:     registryConfigJSON,
	})
	if err != nil {
		return templateport.ProviderRecord{}, err
	}
	metadataJSON, err := json.Marshal(withRegistryMetadata(parseJSONMap(created.Metadata), def.Source))
	if err != nil {
		return templateport.ProviderRecord{}, fmt.Errorf("marshal provider metadata: %w", err)
	}
	return providers.UpdateProvider(ctx, templateport.ProviderUpdate{
		ID:         created.ID,
		Name:       created.Name,
		ClientType: created.ClientType,
		Icon:       created.Icon,
		Enable:     created.Enable,
		Config:     created.Config,
		Metadata:   metadataJSON,
	})
}

func matchExistingProvider(idx *providerIndex, used map[string]bool, def Definition, registryCfg map[string]any) (templateport.ProviderRecord, bool) {
	if idx == nil {
		return templateport.ProviderRecord{}, false
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

func matchLegacyProviderByModels(idx *providerIndex, used map[string]bool, def Definition, registryCfg map[string]any) (templateport.ProviderRecord, bool) {
	defModelIDs := definitionModelIDs(def)
	if len(defModelIDs) == 0 {
		return templateport.ProviderRecord{}, false
	}
	baseURL := normalizedBaseURL(configString(registryCfg, "base_url"))
	var best templateport.ProviderRecord
	bestScore := 0
	tied := false
	for _, provider := range idx.providers {
		key := providerKey(provider)
		if used[key] || provider.ClientType != def.Driver {
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
		return templateport.ProviderRecord{}, false
	}
	return best, true
}

func definitionModelIDs(def Definition) map[string]bool {
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

func providerConfigFromDefinition(def Definition) map[string]any {
	providerCfg := make(map[string]any, len(def.DefaultConfig))
	for k, v := range def.DefaultConfig {
		providerCfg[k] = v
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

func providerRegistrySource(provider templateport.ProviderRecord) string {
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

func providerKey(provider templateport.ProviderRecord) string {
	return provider.ID
}
