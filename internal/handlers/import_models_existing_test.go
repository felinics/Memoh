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
	h := NewProvidersHandler(logger, providers.NewService(nil, q, ""), models.NewService(logger, q))
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
