package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/modelcatalog"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

type existingModelQueries struct {
	testHandlerQueries
	existing sqlc.Model
	created  atomic.Int32
}

func (q *existingModelQueries) ListModelsByProviderID(context.Context, pgtype.UUID) ([]sqlc.Model, error) {
	return []sqlc.Model{q.existing}, nil
}

func (q *existingModelQueries) GetModelByProviderAndModelID(context.Context, sqlc.GetModelByProviderAndModelIDParams) (sqlc.Model, error) {
	return q.existing, nil
}

func (q *existingModelQueries) CreateModel(context.Context, sqlc.CreateModelParams) (sqlc.Model, error) {
	q.created.Add(1)
	return sqlc.Model{}, errors.New("create was not expected")
}

// Refreshing a provider sends a model it already has straight to the refresh.
// Through Create, an embedding model would first have its dimensions probed,
// a billed request, on every refresh before the duplicate was refused.
func TestImportModelsRefreshesAnExistingEmbeddingModelWithoutProbing(t *testing.T) {
	var embeddings atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			embeddings.Add(1)
			_, _ = w.Write([]byte(`{"data":[{"embedding":[0.1,0.2,0.3]}]}`))
		case strings.HasSuffix(r.URL.Path, "/models"):
			_, _ = w.Write([]byte(`{"data":[{"id":"text-embedding-3-small","object":"model"}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	providerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	q := &existingModelQueries{
		testHandlerQueries: testHandlerQueries{provider: sqlc.Provider{
			ID: providerID, ClientType: string(models.ClientTypeOpenAICompletions),
			Config: []byte(`{"api_key":"sk-test","base_url":"` + upstream.URL + `"}`),
		}},
		existing: sqlc.Model{
			ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, ProviderID: providerID,
			ModelID: "text-embedding-3-small", Type: string(models.ModelTypeEmbedding),
			Config: []byte(`{"schema_version":2,"dimensions":1536}`),
		},
	}
	logger := slog.New(slog.DiscardHandler)
	h := NewProvidersHandler(logger, providers.NewService(nil, q, ""), models.NewService(logger, q), nil)
	status, _, body := serveHandlerProblem(t, h.Register, "/providers/00000000-0000-0000-0000-000000000001/import-models")
	if status != http.StatusOK {
		t.Fatalf("status = %d, body %s", status, body)
	}
	var resp providers.ImportModelsResponse
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Created != 0 || resp.Skipped != 1 {
		t.Fatalf("response = %+v, want the existing model skipped", resp)
	}
	if n := embeddings.Load(); n != 0 {
		t.Fatalf("embedding probes = %d, want none for a model that exists", n)
	}
	if n := q.created.Load(); n != 0 {
		t.Fatalf("creates = %d, want none for a model that exists", n)
	}
}

type catalogImportQueries struct {
	testHandlerQueries
	existing sqlc.Model
	created  []sqlc.CreateModelParams
	updated  []sqlc.UpdateModelParams
}

func (q *catalogImportQueries) ListModelsByProviderID(context.Context, pgtype.UUID) ([]sqlc.Model, error) {
	return []sqlc.Model{q.existing}, nil
}

func (q *catalogImportQueries) GetModelByProviderAndModelID(context.Context, sqlc.GetModelByProviderAndModelIDParams) (sqlc.Model, error) {
	return q.existing, nil
}

func (q *catalogImportQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.existing, nil
}

func (q *catalogImportQueries) UpdateModel(_ context.Context, arg sqlc.UpdateModelParams) (sqlc.Model, error) {
	q.updated = append(q.updated, arg)
	return sqlc.Model{ID: arg.ID, ModelID: arg.ModelID, ProviderID: arg.ProviderID, Type: arg.Type, Config: arg.Config}, nil
}

func (q *catalogImportQueries) CreateModel(_ context.Context, arg sqlc.CreateModelParams) (sqlc.Model, error) {
	q.created = append(q.created, arg)
	return sqlc.Model{ModelID: arg.ModelID}, nil
}

// A catalog import sends a model the provider already has straight to the
// refresh instead of a failing Create, and stores no name that only repeats a
// new model's id.
func TestImportCatalogModelsRefreshesKnownModelsAndOmitsIDNames(t *testing.T) {
	providerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	q := &catalogImportQueries{
		testHandlerQueries: testHandlerQueries{provider: sqlc.Provider{ID: providerID, ClientType: string(models.ClientTypeOpenAICompletions), Config: []byte(`{}`)}},
		existing: sqlc.Model{
			ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, ProviderID: providerID,
			ModelID: "known", Type: string(models.ModelTypeChat), Config: []byte(`{"schema_version":2}`),
		},
	}
	logger := slog.New(slog.DiscardHandler)
	h := NewProvidersHandler(logger, providers.NewService(nil, q, ""), models.NewService(logger, q), nil)
	resp := h.importCatalogModels(context.Background(), providers.GetResponse{ID: providerID.String(), ClientType: string(models.ClientTypeOpenAICompletions)}, []modelcatalog.Model{
		{ModelID: "known", Config: models.ModelConfig{ToolCall: true}},
		{ModelID: "fresh", Config: models.ModelConfig{}},
	})
	if resp.Created != 1 || resp.Updated != 1 {
		t.Fatalf("response = %+v, want one created and one refreshed", resp)
	}
	if len(q.created) != 1 || q.created[0].ModelID != "fresh" || q.created[0].Name.Valid {
		t.Fatalf("created = %+v, want only fresh, without a name", q.created)
	}
	if len(q.updated) != 1 || q.updated[0].ModelID != "known" {
		t.Fatalf("updated = %+v, want known refreshed", q.updated)
	}
}

// The old custom subscription form recorded only client_type/base_url. The
// provider read and the catalog import must agree on its recovered source.
func TestImportCatalogModelsRepairsLegacyGoLimits(t *testing.T) {
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("provider credentials must not reach the public catalog")
		}
		_, _ = w.Write([]byte(`{"opencode-go":{"id":"opencode-go","models":{"deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","name":"DeepSeek","tool_call":true,"limit":{"context":1000000,"output":384000}}}}}`))
	}))
	defer catalog.Close()
	providerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	q := &catalogImportQueries{
		testHandlerQueries: testHandlerQueries{provider: sqlc.Provider{
			ID: providerID, ClientType: "opencode-go",
			Config: []byte(`{"base_url":"https://opencode.ai/zen/go/v1","api_key":"test-secret"}`), Metadata: []byte(`{}`),
		}},
		existing: sqlc.Model{
			ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, ProviderID: providerID,
			ModelID: "deepseek-v4.1-flash", Type: "chat", Enable: true,
			Config: []byte(`{"schema_version":2,"description":"kept"}`),
		},
	}
	logger := slog.New(slog.DiscardHandler)
	h := NewProvidersHandler(logger, providers.NewService(logger, q, ""), models.NewService(logger, q), modelcatalog.New(logger, modelcatalog.Options{URL: catalog.URL}))
	status, _, body := serveHandlerProblem(t, h.Register, "/providers/00000000-0000-0000-0000-000000000001/import-catalog-models")
	if status != http.StatusOK || len(q.updated) != 1 || len(q.created) != 0 {
		t.Fatalf("status = %d, updates = %d, creates = %d, body = %s", status, len(q.updated), len(q.created), body)
	}
	var config models.ModelConfig
	if err := json.Unmarshal(q.updated[0].Config, &config); err != nil {
		t.Fatal(err)
	}
	if config.ContextWindow == nil || *config.ContextWindow != 1000000 || config.MaxOutputTokens == nil || *config.MaxOutputTokens != 384000 {
		t.Fatalf("catalog limits were not stored: %s", q.updated[0].Config)
	}
	if config.Description == nil || *config.Description != "kept" {
		t.Fatal("catalog refresh overwrote the user's description")
	}
}
