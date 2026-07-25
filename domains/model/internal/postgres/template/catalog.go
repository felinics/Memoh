package template

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type catalogQueries interface {
	ListProviderTemplates(context.Context, string) ([]dbsqlc.ListProviderTemplatesRow, error)
	GetProviderTemplateByID(context.Context, pgtype.UUID) (dbsqlc.ModelProviderTemplate, error)
	ListProviderTemplateModels(context.Context, pgtype.UUID) ([]dbsqlc.ModelProviderTemplateModel, error)
}

type CatalogStore struct {
	queries catalogQueries
}

var _ templateport.CatalogStore = (*CatalogStore)(nil)

func NewCatalogStore(pool *pgxpool.Pool) *CatalogStore {
	return &CatalogStore{queries: dbsqlc.New(pool)}
}

func NewCatalogStoreWithQueries(queries catalogQueries) *CatalogStore {
	return &CatalogStore{queries: queries}
}

func (s *CatalogStore) ListTemplates(ctx context.Context, domain string) ([]templateport.CatalogTemplate, error) {
	rows, err := s.queries.ListProviderTemplates(ctx, domain)
	if err != nil {
		return nil, err
	}
	templates := make([]templateport.CatalogTemplate, 0, len(rows))
	for _, row := range rows {
		templates = append(templates, catalogTemplateFromListRow(row))
	}
	return templates, nil
}

func (s *CatalogStore) FindTemplate(ctx context.Context, id string) (templateport.CatalogTemplate, error) {
	parsed, err := db.ParseUUID(id)
	if err != nil {
		return templateport.CatalogTemplate{}, fmt.Errorf("%w: %w", templateport.ErrTemplateNotFound, err)
	}
	row, err := s.queries.GetProviderTemplateByID(ctx, parsed)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, db.ErrNotFound) {
			return templateport.CatalogTemplate{}, templateport.ErrTemplateNotFound
		}
		return templateport.CatalogTemplate{}, err
	}
	return catalogTemplateFromRow(row), nil
}

func (s *CatalogStore) ListTemplateModels(ctx context.Context, templateID string) ([]templateport.CatalogModel, error) {
	parsed, err := db.ParseUUID(templateID)
	if err != nil {
		return nil, err
	}
	rows, err := s.queries.ListProviderTemplateModels(ctx, parsed)
	if err != nil {
		return nil, err
	}
	models := make([]templateport.CatalogModel, 0, len(rows))
	for _, row := range rows {
		models = append(models, templateport.CatalogModel{
			ID:        row.ID.String(),
			ModelID:   row.ModelID,
			Name:      row.Name,
			Type:      row.Type,
			Config:    append([]byte(nil), row.Config...),
			Metadata:  append([]byte(nil), row.Metadata...),
			SortOrder: int(row.SortOrder),
		})
	}
	return models, nil
}

func catalogTemplateFromListRow(row dbsqlc.ListProviderTemplatesRow) templateport.CatalogTemplate {
	template := templateport.CatalogTemplate{
		ID:            row.ID.String(),
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  append([]byte(nil), row.ConfigSchema...),
		DefaultConfig: append([]byte(nil), row.DefaultConfig...),
		Metadata:      append([]byte(nil), row.Metadata...),
		Source:        row.Source,
		SortOrder:     int(row.SortOrder),
		Configured:    row.Configured,
		CreatedAt:     db.TimeFromPg(row.CreatedAt),
		UpdatedAt:     db.TimeFromPg(row.UpdatedAt),
	}
	if row.Icon.Valid {
		template.Icon = row.Icon.String
	}
	return template
}

func catalogTemplateFromRow(row dbsqlc.ModelProviderTemplate) templateport.CatalogTemplate {
	template := templateport.CatalogTemplate{
		ID:            row.ID.String(),
		Key:           row.Key,
		Domain:        row.Domain,
		Name:          row.Name,
		Description:   row.Description,
		Driver:        row.Driver,
		ConfigSchema:  append([]byte(nil), row.ConfigSchema...),
		DefaultConfig: append([]byte(nil), row.DefaultConfig...),
		Metadata:      append([]byte(nil), row.Metadata...),
		Source:        row.Source,
		SortOrder:     int(row.SortOrder),
		CreatedAt:     db.TimeFromPg(row.CreatedAt),
		UpdatedAt:     db.TimeFromPg(row.UpdatedAt),
	}
	if row.Icon.Valid {
		template.Icon = row.Icon.String
	}
	return template
}
