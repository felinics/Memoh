package usage

import (
	"context"
	"testing"
)

type serviceReaderStub struct {
	Reader
	modelFilter  Filter
	models       []Model
	recordFilter Filter
	pagination   Pagination
	page         Page
}

func (s *serviceReaderStub) GetByModel(_ context.Context, filter Filter) ([]Model, error) {
	s.modelFilter = filter
	return s.models, nil
}

func (s *serviceReaderStub) ListRecords(_ context.Context, filter Filter, pagination Pagination) (Page, error) {
	s.recordFilter = filter
	s.pagination = pagination
	return s.page, nil
}

type modelProjectionReaderStub struct {
	calls       int
	modelIDs    []string
	projections map[string]ModelProjection
}

func (s *modelProjectionReaderStub) GetModelProjections(_ context.Context, modelIDs []string) (map[string]ModelProjection, error) {
	s.calls++
	s.modelIDs = append([]string(nil), modelIDs...)
	return s.projections, nil
}

func TestServiceBatchEnrichesModelsAndPreservesFilterOrder(t *testing.T) {
	t.Parallel()
	const (
		firstID  = "11111111-1111-1111-1111-111111111111"
		secondID = "22222222-2222-2222-2222-222222222222"
	)
	raw := &serviceReaderStub{models: []Model{
		{ModelID: firstID, InputTokens: 30},
		{ModelID: secondID, InputTokens: 20},
		{ModelID: firstID, InputTokens: 10},
	}}
	models := &modelProjectionReaderStub{projections: map[string]ModelProjection{
		firstID:  {ModelSlug: "model-a", ModelName: "Model A", ProviderName: "Provider A"},
		secondID: {ModelSlug: "model-b", ModelName: "Model B", ProviderName: "Provider B"},
	}}
	filter := Filter{BotID: "bot-id", ModelID: firstID, SessionType: "discuss"}

	got, err := NewService(raw, models).GetByModel(t.Context(), filter)
	if err != nil {
		t.Fatalf("GetByModel() error = %v", err)
	}
	if models.calls != 1 {
		t.Fatalf("projection calls = %d, want 1", models.calls)
	}
	if len(models.modelIDs) != 2 || models.modelIDs[0] != firstID || models.modelIDs[1] != secondID {
		t.Fatalf("projection model IDs = %#v", models.modelIDs)
	}
	if raw.modelFilter != filter {
		t.Fatalf("filter = %#v, want %#v", raw.modelFilter, filter)
	}
	if len(got) != 3 || got[0].ModelName != "Model A" || got[1].ProviderName != "Provider B" || got[2].InputTokens != 10 {
		t.Fatalf("GetByModel() = %#v", got)
	}
}

func TestServiceUsesUnknownProjectionForMissingModel(t *testing.T) {
	t.Parallel()
	const modelID = "33333333-3333-3333-3333-333333333333"
	raw := &serviceReaderStub{page: Page{
		Items: []Record{{ID: "record-id", ModelID: modelID}},
		Total: 7,
	}}
	models := &modelProjectionReaderStub{projections: map[string]ModelProjection{}}
	filter := Filter{BotID: "bot-id", ModelID: modelID}
	pagination := Pagination{Limit: 20, Offset: 40}

	got, err := NewService(raw, models).ListRecords(t.Context(), filter, pagination)
	if err != nil {
		t.Fatalf("ListRecords() error = %v", err)
	}
	if got.Total != 7 || len(got.Items) != 1 {
		t.Fatalf("ListRecords() = %#v", got)
	}
	item := got.Items[0]
	if item.ModelSlug != "unknown" || item.ModelName != "Unknown" || item.ProviderName != "Unknown" {
		t.Fatalf("missing model projection = %#v", item)
	}
	if raw.recordFilter != filter || raw.pagination != pagination {
		t.Fatalf("filter/pagination = %#v/%#v", raw.recordFilter, raw.pagination)
	}
}

func TestServiceSkipsProjectionReadForEmptyResults(t *testing.T) {
	t.Parallel()
	raw := &serviceReaderStub{models: []Model{}, page: Page{Items: []Record{}}}
	models := &modelProjectionReaderStub{}
	service := NewService(raw, models)

	if _, err := service.GetByModel(t.Context(), Filter{BotID: "bot-id"}); err != nil {
		t.Fatalf("GetByModel() error = %v", err)
	}
	if _, err := service.ListRecords(t.Context(), Filter{BotID: "bot-id"}, Pagination{}); err != nil {
		t.Fatalf("ListRecords() error = %v", err)
	}
	if models.calls != 0 {
		t.Fatalf("projection calls = %d, want 0", models.calls)
	}
}
