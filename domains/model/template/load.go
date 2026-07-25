package template

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type yamlProvider struct {
	ID         string         `yaml:"id,omitempty"`
	Name       string         `yaml:"name"`
	ClientType string         `yaml:"client_type"`
	Icon       string         `yaml:"icon,omitempty"`
	BaseURL    string         `yaml:"base_url,omitempty"`
	Config     map[string]any `yaml:"config,omitempty"`
	Models     []yamlModel    `yaml:"models"`
}

type yamlModel struct {
	ModelID string         `yaml:"model_id"`
	Name    string         `yaml:"name"`
	Type    string         `yaml:"type"`
	Config  map[string]any `yaml:"config"`
}

// Load reads all .yaml / .yml files from dir and returns canonical template
// definitions. It returns nil (no error) when the directory does not exist.
// Malformed files are skipped with a warning logged via log.
// Model lists keep source order (including duplicates); catalog sync compacts them.
func Load(log *slog.Logger, dir string) ([]Definition, error) {
	if log == nil {
		log = slog.Default()
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read providers dir %s: %w", dir, err)
	}

	var defs []Definition
	for index, e := range entries {
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
		var raw yamlProvider
		if err := yaml.Unmarshal(data, &raw); err != nil {
			log.Warn("registry: skipping malformed provider file",
				slog.String("path", path), slog.Any("error", err))
			continue
		}
		if raw.Name == "" {
			continue
		}
		defs = append(defs, definitionFromYAML(raw, registrySource(e.Name()), index))
	}
	return defs, nil
}

func definitionFromYAML(raw yamlProvider, source string, sortOrder int) Definition {
	key := strings.TrimSpace(raw.ID)
	if key == "" {
		key = strings.TrimSuffix(filepath.Base(source), filepath.Ext(source))
	}
	defaultConfig := make(map[string]any, len(raw.Config)+1)
	for name, value := range raw.Config {
		defaultConfig[name] = value
	}
	if strings.TrimSpace(raw.BaseURL) != "" {
		defaultConfig["base_url"] = raw.BaseURL
	}
	models := make([]ModelDefinition, 0, len(raw.Models))
	for modelIndex, model := range raw.Models {
		modelType := strings.TrimSpace(model.Type)
		if modelType == "" {
			modelType = "chat"
		}
		models = append(models, ModelDefinition{
			ModelID:   model.ModelID,
			Name:      model.Name,
			Type:      modelType,
			Config:    model.Config,
			SortOrder: modelIndex,
		})
	}
	return Definition{
		Key:           key,
		Domain:        domainFromYAML(raw),
		Name:          raw.Name,
		Icon:          raw.Icon,
		Driver:        raw.ClientType,
		ConfigSchema:  configSchemaFromYAML(raw),
		DefaultConfig: defaultConfig,
		Metadata: map[string]any{
			"preset": map[string]any{
				"id":     key,
				"source": source,
			},
		},
		Source:    source,
		SortOrder: sortOrder,
		Models:    models,
	}
}

func domainFromYAML(raw yamlProvider) Domain {
	for _, model := range raw.Models {
		switch strings.TrimSpace(model.Type) {
		case "speech":
			return DomainSpeech
		case "transcription":
			return DomainTranscription
		case "video":
			return DomainVideo
		}
	}
	clientType := strings.TrimSpace(raw.ClientType)
	switch {
	case strings.HasSuffix(clientType, "-transcription"):
		return DomainTranscription
	case strings.HasSuffix(clientType, "-speech"):
		return DomainSpeech
	case strings.HasSuffix(clientType, "-video"):
		return DomainVideo
	default:
		return DomainLLM
	}
}

func configSchemaFromYAML(raw yamlProvider) map[string]any {
	fields := map[string]any{}
	if strings.TrimSpace(raw.BaseURL) != "" {
		fields["base_url"] = map[string]any{
			"type":     "string",
			"required": false,
			"example":  raw.BaseURL,
		}
	}
	if requiresAPIKey(raw.ClientType, raw.BaseURL) {
		fields["api_key"] = map[string]any{
			"type":     "secret",
			"required": true,
		}
	}
	return map[string]any{"fields": fields}
}

func requiresAPIKey(clientType, baseURL string) bool {
	switch strings.TrimSpace(clientType) {
	case "edge-speech", "openai-codex", "github-copilot":
		return false
	}
	baseURL = strings.ToLower(strings.TrimSpace(baseURL))
	return !strings.Contains(baseURL, "127.0.0.1") && !strings.Contains(baseURL, "localhost")
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
