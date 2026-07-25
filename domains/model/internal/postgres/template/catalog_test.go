package template_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	templateport "github.com/memohai/memoh/domains/model/internal/port/template"
	dbsqlc "github.com/memohai/memoh/domains/model/internal/postgres/sqlc"
	templatepostgres "github.com/memohai/memoh/domains/model/internal/postgres/template"
	"github.com/memohai/memoh/internal/db"
)

type catalogQueriesStub struct {
	domain     string
	templateID pgtype.UUID
	list       []dbsqlc.ListProviderTemplatesRow
	template   dbsqlc.ModelProviderTemplate
	models     []dbsqlc.ModelProviderTemplateModel
	err        error
}

func (s *catalogQueriesStub) ListProviderTemplates(_ context.Context, domain string) ([]dbsqlc.ListProviderTemplatesRow, error) {
	s.domain = domain
	return s.list, s.err
}

func (s *catalogQueriesStub) GetProviderTemplateByID(_ context.Context, id pgtype.UUID) (dbsqlc.ModelProviderTemplate, error) {
	s.templateID = id
	return s.template, s.err
}

func (s *catalogQueriesStub) ListProviderTemplateModels(_ context.Context, id pgtype.UUID) ([]dbsqlc.ModelProviderTemplateModel, error) {
	s.templateID = id
	return s.models, s.err
}

func TestCatalogStoreMapsGeneratedRows(t *testing.T) {
	templateID := mustCatalogUUID(t, "f8475b11-1295-4183-a5e4-7c3a804ae455")
	modelID := mustCatalogUUID(t, "e03043c5-b65d-4479-a36a-3fa20b86110e")
	now := time.Date(2026, time.July, 23, 10, 0, 0, 0, time.UTC)
	queries := &catalogQueriesStub{
		list: []dbsqlc.ListProviderTemplatesRow{{
			ID: templateID, Key: "openai", Domain: "llm", Name: "OpenAI",
			Icon: pgtype.Text{String: "icon", Valid: true}, Driver: "openai",
			Metadata: []byte(`{"origin":"registry"}`), SortOrder: 3, Configured: true,
			CreatedAt: pgtype.Timestamptz{Time: now, Valid: true}, UpdatedAt: pgtype.Timestamptz{Time: now, Valid: true},
		}},
		template: dbsqlc.ModelProviderTemplate{ID: templateID, Key: "openai", Domain: "llm", Icon: pgtype.Text{String: "icon", Valid: true}},
		models:   []dbsqlc.ModelProviderTemplateModel{{ID: modelID, ModelID: "gpt", Name: "GPT", Type: "chat", SortOrder: 2}},
	}
	store := templatepostgres.NewCatalogStoreWithQueries(queries)

	templates, err := store.ListTemplates(t.Context(), "llm")
	if err != nil {
		t.Fatalf("ListTemplates() error = %v", err)
	}
	if queries.domain != "llm" || len(templates) != 1 || templates[0].ID != templateID.String() || templates[0].Icon != "icon" || !templates[0].Configured {
		t.Fatalf("ListTemplates() = %#v, domain = %q", templates, queries.domain)
	}

	row, err := store.FindTemplate(t.Context(), templateID.String())
	if err != nil {
		t.Fatalf("FindTemplate() error = %v", err)
	}
	if row.ID != templateID.String() || row.Key != "openai" {
		t.Fatalf("FindTemplate() = %#v", row)
	}

	models, err := store.ListTemplateModels(t.Context(), templateID.String())
	if err != nil {
		t.Fatalf("ListTemplateModels() error = %v", err)
	}
	if len(models) != 1 || models[0].ID != modelID.String() || models[0].SortOrder != 2 {
		t.Fatalf("ListTemplateModels() = %#v", models)
	}
}

func TestCatalogStoreNormalizesMissingTemplates(t *testing.T) {
	tests := []struct {
		name string
		id   string
		err  error
	}{
		{name: "invalid id", id: "not-a-uuid"},
		{name: "pgx no rows", id: "f8475b11-1295-4183-a5e4-7c3a804ae455", err: pgx.ErrNoRows},
		{name: "database not found", id: "f8475b11-1295-4183-a5e4-7c3a804ae455", err: db.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := templatepostgres.NewCatalogStoreWithQueries(&catalogQueriesStub{err: tt.err}).FindTemplate(t.Context(), tt.id)
			if !errors.Is(err, templateport.ErrTemplateNotFound) {
				t.Fatalf("FindTemplate() error = %v, want ErrTemplateNotFound", err)
			}
		})
	}
}

func mustCatalogUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("ParseUUID(%q): %v", value, err)
	}
	return id
}
