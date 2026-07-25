package template

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
)

type Service struct {
	store  templateport.CatalogStore
	logger *slog.Logger
}

func NewService(log *slog.Logger, store templateport.CatalogStore) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		store:  store,
		logger: log.With(slog.String("service", "provider_templates")),
	}
}

func (s *Service) List(ctx context.Context, domain string) ([]GetResponse, error) {
	domain = strings.TrimSpace(domain)
	if domain != "" && !IsValidDomain(Domain(domain)) {
		return nil, ErrDomainInvalid
	}
	rows, err := s.store.ListTemplates(ctx, domain)
	if err != nil {
		return nil, fmt.Errorf("list provider templates: %w", err)
	}
	items := make([]GetResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, responseFromListRow(row))
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, id, expectedDomain string) (GetResponse, error) {
	row, err := s.store.FindTemplate(ctx, strings.TrimSpace(id))
	if err != nil {
		if errors.Is(err, ErrTemplateNotFound) {
			return GetResponse{}, ErrTemplateNotFound
		}
		return GetResponse{}, fmt.Errorf("get provider template: %w", err)
	}
	if expectedDomain = strings.TrimSpace(expectedDomain); expectedDomain != "" && row.Domain != expectedDomain {
		return GetResponse{}, ErrDomainMismatch
	}
	models, err := s.store.ListTemplateModels(ctx, row.ID)
	if err != nil {
		return GetResponse{}, fmt.Errorf("list provider template models: %w", err)
	}
	response := responseFromRow(row)
	response.Models = make([]ModelResponse, 0, len(models))
	for _, model := range models {
		response.Models = append(response.Models, ModelResponse{
			ID:        model.ID,
			ModelID:   model.ModelID,
			Name:      model.Name,
			Type:      model.Type,
			Config:    decodeMap(model.Config),
			Metadata:  decodeMap(model.Metadata),
			SortOrder: model.SortOrder,
		})
	}
	return response, nil
}

func (s *Service) FindTemplate(ctx context.Context, id string) (CatalogTemplate, error) {
	row, err := s.store.FindTemplate(ctx, strings.TrimSpace(id))
	if err != nil {
		return CatalogTemplate{}, err
	}
	return publicCatalogTemplate(row), nil
}

func (s *Service) ListTemplateModels(ctx context.Context, templateID string) ([]CatalogModel, error) {
	rows, err := s.store.ListTemplateModels(ctx, templateID)
	if err != nil {
		return nil, err
	}
	models := make([]CatalogModel, 0, len(rows))
	for _, row := range rows {
		models = append(models, publicCatalogModel(row))
	}
	return models, nil
}

func responseFromListRow(row templateport.CatalogTemplate) GetResponse {
	metadata := decodeMap(row.Metadata)
	metadata["configured"] = row.Configured
	if row.Configured {
		metadata["item_type"] = "provider"
	} else {
		metadata["item_type"] = "template"
	}
	response := GetResponse{
		ID:            row.ID,
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  decodeMap(row.ConfigSchema),
		DefaultConfig: decodeMap(row.DefaultConfig),
		Metadata:      metadata,
		Source:        row.Source,
		SortOrder:     row.SortOrder,
		Configured:    row.Configured,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	response.Icon = row.Icon
	return response
}

func responseFromRow(row templateport.CatalogTemplate) GetResponse {
	response := GetResponse{
		ID:            row.ID,
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  decodeMap(row.ConfigSchema),
		DefaultConfig: decodeMap(row.DefaultConfig),
		Metadata:      decodeMap(row.Metadata),
		Source:        row.Source,
		SortOrder:     row.SortOrder,
		CreatedAt:     row.CreatedAt,
		UpdatedAt:     row.UpdatedAt,
	}
	response.Icon = row.Icon
	return response
}

func decodeMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil || out == nil {
		return map[string]any{}
	}
	return out
}
