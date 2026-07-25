package template

import (
	"context"
	"errors"
	"testing"
)

type templateFinderStub struct {
	id       string
	template CatalogTemplate
	err      error
}

func (s *templateFinderStub) FindTemplate(_ context.Context, id string) (CatalogTemplate, error) {
	s.id = id
	return s.template, s.err
}

func TestResolveUsesConsumerTemplate(t *testing.T) {
	store := &templateFinderStub{template: CatalogTemplate{ID: "template-id", Domain: "llm"}}
	row, err := Resolve(t.Context(), store, " template-id ", DomainLLM)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if store.id != "template-id" || row.ID != "template-id" {
		t.Fatalf("Resolve() id = %q, template = %#v", store.id, row)
	}
}

func TestResolveMapsErrorsAndDomainMismatch(t *testing.T) {
	tests := []struct {
		name    string
		store   *templateFinderStub
		domain  Domain
		wantErr error
	}{
		{name: "not found", store: &templateFinderStub{err: ErrTemplateNotFound}, wantErr: ErrTemplateNotFound},
		{name: "query failure", store: &templateFinderStub{err: errors.New("query failed")}, wantErr: nil},
		{name: "domain mismatch", store: &templateFinderStub{template: CatalogTemplate{Domain: "speech"}}, domain: DomainLLM, wantErr: ErrDomainMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Resolve(t.Context(), tt.store, "template-id", tt.domain)
			if tt.wantErr == nil {
				if err == nil {
					t.Fatal("Resolve() error = nil, want query failure")
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resolve() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestResolveRequiresCatalogStore(t *testing.T) {
	_, err := Resolve(t.Context(), nil, "template-id", DomainLLM)
	if err == nil {
		t.Fatal("Resolve() error = nil, want catalog store required")
	}
}

func TestMergeMetadataUsesPersistenceNeutralTemplate(t *testing.T) {
	row := CatalogTemplate{ID: "template-id", Key: "openai", Domain: "llm", Source: "registry", Metadata: []byte(`{"old":true}`)}
	metadata := MergeMetadata(row, map[string]any{"new": true})
	if metadata["old"] != true || metadata["new"] != true {
		t.Fatalf("MergeMetadata() = %#v", metadata)
	}
	templateMetadata, ok := metadata["template"].(map[string]any)
	if !ok || templateMetadata["id"] != "template-id" || templateMetadata["source"] != "registry" {
		t.Fatalf("MergeMetadata() template = %#v", metadata["template"])
	}
}
