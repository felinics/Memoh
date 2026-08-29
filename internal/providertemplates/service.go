package providertemplates

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type Service struct {
	queries dbstore.Queries
	logger  *slog.Logger
}

func NewService(log *slog.Logger, queries dbstore.Queries) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		queries: queries,
		logger:  log.With(slog.String("service", "provider_templates")),
	}
}

func (s *Service) List(ctx context.Context, domain string) ([]GetResponse, error) {
	domain = strings.TrimSpace(domain)
	if domain != "" && !IsValidDomain(Domain(domain)) {
		return nil, apperror.New(apperror.CodeProviderTemplateDomainInvalid, nil)
	}
	rows, err := s.queries.ListProviderTemplates(ctx, domain)
	if err != nil {
		return nil, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, fmt.Errorf("list provider templates: %w", err), nil)
	}
	items := make([]GetResponse, 0, len(rows))
	for _, row := range rows {
		items = append(items, responseFromListRow(row))
	}
	return items, nil
}

func (s *Service) Get(ctx context.Context, id, expectedDomain string) (GetResponse, error) {
	pgID, err := db.ParseUUID(strings.TrimSpace(id))
	if err != nil {
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateNotFound, err, nil)
	}
	row, err := s.queries.GetProviderTemplateByID(ctx, pgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return GetResponse{}, apperror.New(apperror.CodeProviderTemplateNotFound, nil)
		}
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, fmt.Errorf("get provider template: %w", err), nil)
	}
	if expectedDomain = strings.TrimSpace(expectedDomain); expectedDomain != "" && row.Domain != expectedDomain {
		return GetResponse{}, apperror.New(apperror.CodeProviderTemplateDomainMismatch, nil)
	}
	models, err := s.queries.ListProviderTemplateModels(ctx, row.ID)
	if err != nil {
		return GetResponse{}, apperror.Wrap(apperror.CodeProviderTemplateOperationFailed, fmt.Errorf("list provider template models: %w", err), nil)
	}
	response := responseFromRow(row)
	response.Models = make([]ModelResponse, 0, len(models))
	for _, model := range models {
		response.Models = append(response.Models, ModelResponse{
			ID:        model.ID.String(),
			ModelID:   model.ModelID,
			Name:      model.Name,
			Type:      model.Type,
			Config:    decodeMap(model.Config),
			Metadata:  decodeMap(model.Metadata),
			SortOrder: int(model.SortOrder),
		})
	}
	return response, nil
}

func responseFromListRow(row sqlc.ListProviderTemplatesRow) GetResponse {
	metadata := decodeMap(row.Metadata)
	metadata["configured"] = row.Configured
	if row.Configured {
		metadata["item_type"] = "provider"
	} else {
		metadata["item_type"] = "template"
	}
	response := GetResponse{
		ID:            row.ID.String(),
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  decodeMap(row.ConfigSchema),
		DefaultConfig: decodeMap(row.DefaultConfig),
		Metadata:      metadata,
		Source:        row.Source,
		SortOrder:     int(row.SortOrder),
		Configured:    row.Configured,
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}
	if row.Icon.Valid {
		response.Icon = row.Icon.String
	}
	return response
}

func responseFromRow(row sqlc.TemplateProviderTemplate) GetResponse {
	response := GetResponse{
		ID:            row.ID.String(),
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  decodeMap(row.ConfigSchema),
		DefaultConfig: decodeMap(row.DefaultConfig),
		Metadata:      decodeMap(row.Metadata),
		Source:        row.Source,
		SortOrder:     int(row.SortOrder),
		CreatedAt:     row.CreatedAt.Time,
		UpdatedAt:     row.UpdatedAt.Time,
	}
	if row.Icon.Valid {
		response.Icon = row.Icon.String
	}
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
