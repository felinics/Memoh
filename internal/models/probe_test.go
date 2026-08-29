package models

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

// probeTestQueries stubs the two rows Test needs; every other method
// nil-panics, keeping the probe path honest about its database footprint.
type probeTestQueries struct {
	dbstore.Queries
	model    sqlc.Model
	provider sqlc.Provider
}

func (q probeTestQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, nil
}

func (q probeTestQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, nil
}

func newProbeTestService(server *httptest.Server) (*Service, pgtype.UUID) {
	modelID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87}, Valid: true}
	providerID := pgtype.UUID{Bytes: [16]byte{0x10, 0x87, 0x02}, Valid: true}
	return &Service{queries: probeTestQueries{
		model: sqlc.Model{
			ID:         modelID,
			ProviderID: providerID,
			ModelID:    "real-model",
			Type:       string(ModelTypeChat),
		},
		provider: sqlc.Provider{
			ID:         providerID,
			ClientType: string(ClientTypeOpenAICompletions),
			Config:     []byte(`{"api_key":"sk-test","base_url":"` + server.URL + `"}`),
		},
	}}, modelID
}

// #1087: when the provider does not implement the models list (404), the
// model test must fall through to the real-model generation probe instead
// of reporting "Invalid API key" — that probe is the only check able to
// settle such providers.
func TestTestFallsThroughToModelProbeWhenModelsListMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models", "/models/real-model":
			w.WriteHeader(http.StatusNotFound)
		case "/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "hi"}}},
			})
		default:
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	service, modelID := newProbeTestService(server)
	resp, err := service.Test(context.Background(), modelID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusOK {
		t.Fatalf("status = %q, want %q (message: %s)", resp.Status, TestStatusOK, resp.Message)
	}
}

// An auth failure on the models list is still reported as auth_error and
// must short-circuit before any generation request is attempted.
func TestTestModelsListAuthFailureStaysAuthError(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			t.Errorf("unexpected probe request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	service, modelID := newProbeTestService(server)
	resp, err := service.Test(context.Background(), modelID.String())
	if err != nil {
		t.Fatalf("Test() error = %v", err)
	}
	if resp.Status != TestStatusAuthError {
		t.Fatalf("status = %q, want %q (message: %s)", resp.Status, TestStatusAuthError, resp.Message)
	}
}
