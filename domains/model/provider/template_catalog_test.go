package provider

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/memohai/memoh/domains/model/template"
)

type templateCreationStore struct {
	ProviderStore
	input CreateProviderFromTemplateCommand
}

func (s *templateCreationStore) CreateProviderFromTemplate(_ context.Context, input CreateProviderFromTemplateCommand) (ProviderRecord, error) {
	s.input = input
	return ProviderRecord{
		ID:                 "02020202-0202-0202-0202-020202020202",
		ProviderTemplateID: input.ProviderTemplateID,
		Name:               input.Name,
		ClientType:         input.ClientType,
		Icon:               input.Icon,
		Enable:             input.Enable,
		Config:             input.Config,
		Metadata:           input.Metadata,
	}, nil
}

type providerCatalogStub struct {
	template        template.CatalogTemplate
	models          []template.CatalogModel
	modelTemplateID string
}

func (s *providerCatalogStub) FindTemplate(context.Context, string) (template.CatalogTemplate, error) {
	return s.template, nil
}

func (s *providerCatalogStub) ListTemplateModels(_ context.Context, templateID string) ([]template.CatalogModel, error) {
	s.modelTemplateID = templateID
	return s.models, nil
}

func TestCreateFromTemplateUsesInjectedCatalog(t *testing.T) {
	templateID := "f8475b11-1295-4183-a5e4-7c3a804ae455"
	store := &templateCreationStore{}
	catalog := &providerCatalogStub{template: template.CatalogTemplate{
		ID: templateID, Key: "openai", Domain: "llm", Name: "OpenAI", Icon: "openai-icon",
		Driver: "openai-completions", DefaultConfig: []byte(`{"base_url":"https://example.test"}`),
		Metadata: []byte(`{"origin":"registry"}`), Source: "providers/openai.yaml",
	}}
	service := NewServiceWithCatalog(nil, store, nil, catalog, "", "")

	response, err := service.CreateFromTemplate(t.Context(), CreateFromTemplateRequest{
		TemplateID: templateID,
		Domain:     "llm",
		Config:     map[string]any{"api_key": "secret"},
		Metadata:   map[string]any{"region": "test"},
	})
	if err != nil {
		t.Fatalf("CreateFromTemplate() error = %v", err)
	}
	if store.input.ProviderTemplateID != templateID || store.input.Name != "OpenAI" {
		t.Fatalf("CreateProviderFromTemplate() input = %#v", store.input)
	}
	if store.input.Icon != "openai-icon" {
		t.Fatalf("CreateProviderFromTemplate() icon = %q", store.input.Icon)
	}
	if response.ProviderTemplateID != templateID || response.ClientType != "openai-completions" {
		t.Fatalf("CreateFromTemplate() response = %#v", response)
	}

	var config map[string]any
	if err := json.Unmarshal(store.input.Config, &config); err != nil {
		t.Fatalf("decode config: %v", err)
	}
	if config["base_url"] != "https://example.test" || config["api_key"] != "secret" {
		t.Fatalf("CreateProviderFromTemplate() config = %#v", config)
	}
	var metadata map[string]any
	if err := json.Unmarshal(store.input.Metadata, &metadata); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	templateMetadata, ok := metadata["template"].(map[string]any)
	if !ok || templateMetadata["id"] != templateID || metadata["region"] != "test" {
		t.Fatalf("CreateProviderFromTemplate() metadata = %#v", metadata)
	}
}

type templateModelStore struct {
	ProviderStore
	provider ProviderRecord
}

func (s *templateModelStore) GetProvider(context.Context, string) (ProviderRecord, error) {
	return s.provider, nil
}

func TestFetchRemoteModelsUsesInjectedCatalog(t *testing.T) {
	templateID := "f8475b11-1295-4183-a5e4-7c3a804ae455"
	store := &templateModelStore{provider: ProviderRecord{
		ProviderTemplateID: templateID,
		ClientType:         "openai-completions",
	}}
	catalog := &providerCatalogStub{models: []template.CatalogModel{{
		ModelID: "gpt-test",
		Name:    "GPT Test",
		Type:    "chat",
		Config:  []byte(`{"context_window":128000,"compatibilities":["tool-call"]}`),
	}}}
	service := NewServiceWithCatalog(nil, store, nil, catalog, "", "")

	models, err := service.FetchRemoteModels(t.Context(), "d5cc6950-3a43-4912-88c0-d6127b96cd90")
	if err != nil {
		t.Fatalf("FetchRemoteModels() error = %v", err)
	}
	if catalog.modelTemplateID != templateID {
		t.Fatalf("ListTemplateModels() template ID = %q, want %q", catalog.modelTemplateID, templateID)
	}
	if len(models) != 1 || models[0].ID != "gpt-test" || models[0].Name != "GPT Test" {
		t.Fatalf("FetchRemoteModels() = %#v", models)
	}
	if models[0].ContextWindow == nil || *models[0].ContextWindow != 128000 {
		t.Fatalf("FetchRemoteModels() context window = %#v", models[0].ContextWindow)
	}
}
