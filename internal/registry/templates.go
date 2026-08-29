package registry

import (
	"path/filepath"
	"strings"

	"github.com/felinics/memoh/internal/providertemplates"
)

func ProviderTemplateDefinitions(definitions []ProviderDefinition) []providertemplates.Definition {
	items := make([]providertemplates.Definition, 0, len(definitions))
	for index, definition := range definitions {
		key := strings.TrimSpace(definition.ID)
		if key == "" {
			key = strings.TrimSuffix(filepath.Base(definition.Source), filepath.Ext(definition.Source))
		}
		defaultConfig := make(map[string]any, len(definition.Config)+1)
		for name, value := range definition.Config {
			defaultConfig[name] = value
		}
		if strings.TrimSpace(definition.BaseURL) != "" {
			defaultConfig["base_url"] = definition.BaseURL
		}
		lastModelIndex := make(map[string]int, len(definition.Models))
		for modelIndex, model := range definition.Models {
			modelType := strings.TrimSpace(model.Type)
			if modelType == "" {
				modelType = "chat"
			}
			lastModelIndex[strings.ToLower(modelType)+"\x00"+strings.ToLower(strings.TrimSpace(model.ModelID))] = modelIndex
		}
		models := make([]providertemplates.ModelDefinition, 0, len(lastModelIndex))
		for modelIndex, model := range definition.Models {
			modelType := strings.TrimSpace(model.Type)
			if modelType == "" {
				modelType = "chat"
			}
			modelKey := strings.ToLower(modelType) + "\x00" + strings.ToLower(strings.TrimSpace(model.ModelID))
			if lastModelIndex[modelKey] != modelIndex {
				continue
			}
			models = append(models, providertemplates.ModelDefinition{
				ModelID:   model.ModelID,
				Name:      model.Name,
				Type:      modelType,
				Config:    model.Config,
				SortOrder: modelIndex,
			})
		}
		items = append(items, providertemplates.Definition{
			Key:           key,
			Domain:        providerTemplateDomain(definition),
			Name:          definition.Name,
			Icon:          definition.Icon,
			Driver:        definition.ClientType,
			ConfigSchema:  providerTemplateConfigSchema(definition),
			DefaultConfig: defaultConfig,
			Metadata: map[string]any{
				"preset": map[string]any{
					"id":     key,
					"source": definition.Source,
				},
			},
			Source:    definition.Source,
			SortOrder: index,
			Models:    models,
		})
	}
	return items
}

func providerTemplateDomain(definition ProviderDefinition) providertemplates.Domain {
	for _, model := range definition.Models {
		switch strings.TrimSpace(model.Type) {
		case "speech":
			return providertemplates.DomainSpeech
		case "transcription":
			return providertemplates.DomainTranscription
		case "video":
			return providertemplates.DomainVideo
		}
	}
	clientType := strings.TrimSpace(definition.ClientType)
	switch {
	case strings.HasSuffix(clientType, "-transcription"):
		return providertemplates.DomainTranscription
	case strings.HasSuffix(clientType, "-speech"):
		return providertemplates.DomainSpeech
	case strings.HasSuffix(clientType, "-video"):
		return providertemplates.DomainVideo
	default:
		return providertemplates.DomainLLM
	}
}

func providerTemplateConfigSchema(definition ProviderDefinition) map[string]any {
	fields := map[string]any{}
	if strings.TrimSpace(definition.BaseURL) != "" {
		fields["base_url"] = map[string]any{
			"type":     "string",
			"required": false,
			"example":  definition.BaseURL,
		}
	}
	if providerTemplateRequiresAPIKey(definition.ClientType, definition.BaseURL) {
		fields["api_key"] = map[string]any{
			"type":     "secret",
			"required": true,
		}
	}
	return map[string]any{"fields": fields}
}

func providerTemplateRequiresAPIKey(clientType, baseURL string) bool {
	switch strings.TrimSpace(clientType) {
	case "edge-speech", "openai-codex", "github-copilot":
		return false
	}
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	return !strings.Contains(baseURL, "127.0.0.1") && !strings.Contains(baseURL, "localhost")
}
