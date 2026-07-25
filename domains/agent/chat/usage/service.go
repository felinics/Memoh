package usage

import (
	"context"
	"fmt"
	"time"
)

const (
	unknownModelSlug = "unknown"
	unknownName      = "Unknown"
)

// Service enriches Agent-owned usage rows with Model-owned display fields.
type Service struct {
	Reader
	models ModelProjectionReader
}

func NewService(reader Reader, models ModelProjectionReader) *Service {
	return &Service{Reader: reader, models: models}
}

func (s *Service) GetTokenUsageByModel(ctx context.Context, botID string, from, to time.Time) ([]Model, error) {
	return s.GetByModel(ctx, Filter{BotID: botID, From: from, To: to})
}

func (s *Service) GetByModel(ctx context.Context, filter Filter) ([]Model, error) {
	items, err := s.Reader.GetByModel(ctx, filter)
	if err != nil || len(items) == 0 {
		return items, err
	}
	projections, err := s.projections(ctx, modelIDsFromModels(items))
	if err != nil {
		return nil, err
	}
	for i := range items {
		enrichModel(&items[i], projections[items[i].ModelID])
	}
	return items, nil
}

func (s *Service) ListRecords(ctx context.Context, filter Filter, pagination Pagination) (Page, error) {
	page, err := s.Reader.ListRecords(ctx, filter, pagination)
	if err != nil || len(page.Items) == 0 {
		return page, err
	}
	projections, err := s.projections(ctx, modelIDsFromRecords(page.Items))
	if err != nil {
		return Page{}, err
	}
	for i := range page.Items {
		enrichRecord(&page.Items[i], projections[page.Items[i].ModelID])
	}
	return page, nil
}

func (s *Service) projections(ctx context.Context, ids []string) (map[string]ModelProjection, error) {
	if len(ids) == 0 || s.models == nil {
		return nil, nil
	}
	projections, err := s.models.GetModelProjections(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("load usage model projections: %w", err)
	}
	return projections, nil
}

func modelIDsFromModels(items []Model) []string {
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ModelID == "" {
			continue
		}
		if _, ok := seen[item.ModelID]; ok {
			continue
		}
		seen[item.ModelID] = struct{}{}
		ids = append(ids, item.ModelID)
	}
	return ids
}

func modelIDsFromRecords(items []Record) []string {
	ids := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, item := range items {
		if item.ModelID == "" {
			continue
		}
		if _, ok := seen[item.ModelID]; ok {
			continue
		}
		seen[item.ModelID] = struct{}{}
		ids = append(ids, item.ModelID)
	}
	return ids
}

func enrichModel(item *Model, projection ModelProjection) {
	item.ModelSlug = valueOr(projection.ModelSlug, unknownModelSlug)
	item.ModelName = valueOr(projection.ModelName, unknownName)
	item.ProviderName = valueOr(projection.ProviderName, unknownName)
}

func enrichRecord(item *Record, projection ModelProjection) {
	item.ModelSlug = valueOr(projection.ModelSlug, unknownModelSlug)
	item.ModelName = valueOr(projection.ModelName, unknownName)
	item.ProviderName = valueOr(projection.ProviderName, unknownName)
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

var _ Reader = (*Service)(nil)
