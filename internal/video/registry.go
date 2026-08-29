package video

import (
	"fmt"
	"net/http"
	"strings"
	"sync"

	arkvideos "github.com/felinics/twilight/provider/ark/videos"
	openroutervideos "github.com/felinics/twilight/provider/openrouter/videos"
	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/models"
)

type ProviderFactory func(config map[string]any) (sdk.VideoProvider, error)

type ProviderDefinition struct {
	ClientType   models.ClientType
	DisplayName  string
	Icon         string
	Description  string
	ConfigSchema ConfigSchema
	DefaultModel string
	SupportsList bool
	Models       []ModelInfo
	Factory      ProviderFactory
	Order        int
}

type Registry struct {
	mu        sync.RWMutex
	providers map[models.ClientType]ProviderDefinition
	ordered   []models.ClientType
}

func NewRegistry() *Registry {
	r := &Registry{providers: make(map[models.ClientType]ProviderDefinition)}
	for _, def := range defaultProviderDefinitions() {
		r.Register(def)
	}
	return r
}

func (r *Registry) Register(def ProviderDefinition) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.providers[def.ClientType]; !exists {
		r.ordered = append(r.ordered, def.ClientType)
	}
	r.providers[def.ClientType] = def
}

func (r *Registry) Get(clientType models.ClientType) (ProviderDefinition, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	def, ok := r.providers[clientType]
	if !ok {
		return ProviderDefinition{}, fmt.Errorf("unsupported video provider: %s", clientType)
	}
	return def, nil
}

func (r *Registry) List() []ProviderDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	defs := make([]ProviderDefinition, 0, len(r.ordered))
	for _, key := range r.ordered {
		defs = append(defs, r.providers[key])
	}
	return defs
}

func (r *Registry) ListMeta() []ProviderMetaResponse {
	defs := r.List()
	metas := make([]ProviderMetaResponse, 0, len(defs))
	for _, def := range defs {
		metas = append(metas, ProviderMetaResponse{
			Provider:     string(def.ClientType),
			DisplayName:  def.DisplayName,
			Description:  def.Description,
			ConfigSchema: def.ConfigSchema,
			DefaultModel: def.DefaultModel,
			Models:       def.Models,
			SupportsList: def.SupportsList,
		})
	}
	return metas
}

func defaultProviderDefinitions() []ProviderDefinition {
	return []ProviderDefinition{
		{
			ClientType:  models.ClientTypeOpenRouterVideo,
			DisplayName: "OpenRouter",
			Icon:        "openrouter",
			Description: "OpenRouter video generation models",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "OpenRouter API key", true, 10),
				stringField("base_url", "Base URL", "OpenRouter API base URL", false, "https://openrouter.ai/api", 20),
			}},
			SupportsList: true,
			Factory: func(config map[string]any) (sdk.VideoProvider, error) {
				opts := []openroutervideos.Option{openroutervideos.WithHTTPClient(&http.Client{})}
				if v := configString(config, "api_key"); v != "" {
					opts = append(opts, openroutervideos.WithAPIKey(v))
				}
				if v := configString(config, "base_url"); v != "" {
					opts = append(opts, openroutervideos.WithBaseURL(v))
				}
				return openroutervideos.New(opts...), nil
			},
			Order: 10,
		},
		{
			ClientType:  models.ClientTypeModelArkVideo,
			DisplayName: "ModelArk",
			Icon:        "modelark-color",
			Description: "BytePlus ModelArk video generation",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "ModelArk API key", true, 10),
				stringField("base_url", "Base URL", "ModelArk API base URL", false, arkvideos.BytePlusBaseURL, 20),
			}},
			SupportsList: false,
			Factory:      arkFactory,
			Order:        20,
		},
		{
			ClientType:  models.ClientTypeVolcengineVideo,
			DisplayName: "Volcengine",
			Icon:        "volcengine-color",
			Description: "Volcengine Ark video generation",
			ConfigSchema: ConfigSchema{Fields: []FieldSchema{
				secretField("api_key", "API Key", "Volcengine Ark API key", true, 10),
				stringField("base_url", "Base URL", "Volcengine Ark API base URL", false, arkvideos.VolcengineBaseURL, 20),
			}},
			SupportsList: false,
			Factory:      arkFactory,
			Order:        30,
		},
	}
}

func arkFactory(config map[string]any) (sdk.VideoProvider, error) {
	opts := []arkvideos.Option{arkvideos.WithHTTPClient(&http.Client{})}
	if v := configString(config, "api_key"); v != "" {
		opts = append(opts, arkvideos.WithAPIKey(v))
	}
	if v := configString(config, "base_url"); v != "" {
		opts = append(opts, arkvideos.WithBaseURL(v))
	}
	return arkvideos.New(opts...), nil
}

func stringField(key, title, description string, required bool, example any, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "string", Title: title, Description: description, Required: required, Example: example, Order: order}
}

func secretField(key, title, description string, required bool, order int) FieldSchema {
	return FieldSchema{Key: key, Type: "secret", Title: title, Description: description, Required: required, Order: order}
}

func configString(cfg map[string]any, key string) string {
	if cfg == nil {
		return ""
	}
	if v, ok := cfg[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}
