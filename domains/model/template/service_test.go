package template

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

type catalogStoreStub struct {
	listDomain string
	templates  []templateport.CatalogTemplate
	template   templateport.CatalogTemplate
	models     []templateport.CatalogModel
	err        error
}

func (s *catalogStoreStub) ListTemplates(_ context.Context, domain string) ([]templateport.CatalogTemplate, error) {
	s.listDomain = domain
	return s.templates, s.err
}

func (s *catalogStoreStub) FindTemplate(context.Context, string) (templateport.CatalogTemplate, error) {
	return s.template, s.err
}

func (s *catalogStoreStub) ListTemplateModels(context.Context, string) ([]templateport.CatalogModel, error) {
	return s.models, s.err
}

func TestServiceListMapsCatalogRecords(t *testing.T) {
	now := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	store := &catalogStoreStub{templates: []templateport.CatalogTemplate{{
		ID: "template-id", Key: "openai", Domain: "llm", Name: "OpenAI", Icon: "icon",
		Driver: "openai", ConfigSchema: json.RawMessage(`{"type":"object"}`), DefaultConfig: json.RawMessage(`{"url":"default"}`),
		Metadata: json.RawMessage(`{"source":"catalog"}`), Source: "registry", SortOrder: 4, Configured: true,
		CreatedAt: now, UpdatedAt: now,
	}}}

	items, err := NewService(nil, store).List(t.Context(), " llm ")
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if store.listDomain != "llm" {
		t.Fatalf("ListTemplates() domain = %q, want llm", store.listDomain)
	}
	if len(items) != 1 {
		t.Fatalf("List() returned %d items, want 1", len(items))
	}
	item := items[0]
	if item.ID != "template-id" || item.Icon != "icon" || !item.Configured {
		t.Fatalf("List() item = %#v", item)
	}
	if item.Metadata["configured"] != true || item.Metadata["item_type"] != "provider" {
		t.Fatalf("List() metadata = %#v", item.Metadata)
	}
}

func TestServiceGetMapsModels(t *testing.T) {
	store := &catalogStoreStub{
		template: templateport.CatalogTemplate{ID: "template-id", Domain: "speech", Name: "Speech"},
		models:   []templateport.CatalogModel{{ID: "model-id", ModelID: "voice", Name: "Voice", Type: "tts", SortOrder: 2}},
	}

	item, err := NewService(nil, store).Get(t.Context(), "template-id", "speech")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if len(item.Models) != 1 || item.Models[0].ID != "model-id" || item.Models[0].SortOrder != 2 {
		t.Fatalf("Get() models = %#v", item.Models)
	}
}

func TestServiceMapsCatalogErrors(t *testing.T) {
	tests := []struct {
		name    string
		call    func(*Service) error
		wantErr error
	}{
		{
			name:    "invalid domain",
			call:    func(service *Service) error { _, err := service.List(t.Context(), "unknown"); return err },
			wantErr: ErrDomainInvalid,
		},
		{
			name:    "not found",
			call:    func(service *Service) error { _, err := service.Get(t.Context(), "missing", ""); return err },
			wantErr: ErrTemplateNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &catalogStoreStub{err: errors.New("query failed")}
			if errors.Is(tt.wantErr, ErrTemplateNotFound) {
				store.err = ErrTemplateNotFound
			}
			if err := tt.call(NewService(nil, store)); !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
