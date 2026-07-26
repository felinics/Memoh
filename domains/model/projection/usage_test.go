package projection

import (
	"context"
	"testing"

	modeldomain "github.com/memohai/memoh/domains/model"
	"github.com/memohai/memoh/domains/model/catalog"
	"github.com/memohai/memoh/domains/model/provider"
)

type usageModelCatalogStub struct {
	calls int
	rows  []catalog.GetResponse
}

func (s *usageModelCatalogStub) List(context.Context) ([]catalog.GetResponse, error) {
	s.calls++
	return s.rows, nil
}

type usageProviderCatalogStub struct {
	calls int
	rows  []provider.GetResponse
}

func (s *usageProviderCatalogStub) List(context.Context) ([]provider.GetResponse, error) {
	s.calls++
	return s.rows, nil
}

func TestUsageModelReaderBatchProjectsRequestedModels(t *testing.T) {
	t.Parallel()
	models := &usageModelCatalogStub{rows: []catalog.GetResponse{
		{
			ID:      "model-a",
			ModelID: "slug-a",
			Model: modeldomain.Model{
				ModelID:    "slug-a",
				Name:       "Model A",
				ProviderID: "provider-a",
			},
		},
		{
			ID:      "model-b",
			ModelID: "slug-b",
			Model: modeldomain.Model{
				ModelID:    "slug-b",
				Name:       "Model B",
				ProviderID: "provider-b",
			},
		},
	}}
	providers := &usageProviderCatalogStub{rows: []provider.GetResponse{
		{ID: "provider-a", Name: "Provider A"},
		{ID: "provider-b", Name: "Provider B"},
	}}

	got, err := newUsageModelReader(models, providers).GetModelProjections(t.Context(), []string{"model-b"})
	if err != nil {
		t.Fatalf("GetModelProjections() error = %v", err)
	}
	if models.calls != 1 || providers.calls != 1 {
		t.Fatalf("catalog calls = models:%d providers:%d, want one each", models.calls, providers.calls)
	}
	if len(got) != 1 || got["model-b"].ModelName != "Model B" || got["model-b"].ProviderName != "Provider B" {
		t.Fatalf("GetModelProjections() = %#v", got)
	}
}

func TestUsageModelReaderSkipsCatalogsForEmptyInput(t *testing.T) {
	t.Parallel()
	models := &usageModelCatalogStub{}
	providers := &usageProviderCatalogStub{}

	got, err := newUsageModelReader(models, providers).GetModelProjections(t.Context(), nil)
	if err != nil {
		t.Fatalf("GetModelProjections() error = %v", err)
	}
	if len(got) != 0 || models.calls != 0 || providers.calls != 0 {
		t.Fatalf("result/calls = %#v/%d/%d", got, models.calls, providers.calls)
	}
}
