package handlers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
)

func TestImportModelsClassifiesGetFailures(t *testing.T) {
	cases := []struct {
		name, id string
		err      error
		status   int
	}{
		{"invalid uuid", "not-a-uuid", nil, http.StatusBadRequest},
		{"not found", "00000000-0000-0000-0000-000000000001", pgx.ErrNoRows, http.StatusNotFound},
		{"internal", "00000000-0000-0000-0000-000000000001", errors.New("database secret failure"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := testHandlerQueries{providerErr: tc.err}
			h := NewProvidersHandler(slog.New(slog.DiscardHandler), providers.NewService(nil, q, ""), nil)
			status, _, body := serveHandlerProblem(t, h.Register, "/providers/"+tc.id+"/import-models")
			if status != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", status, tc.status, body)
			}
			if strings.Contains(body, "database secret failure") {
				t.Fatalf("body leaks service error: %s", body)
			}
		})
	}
}

func TestImportModelsTranslatesUpstreamRejection(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = fmt.Fprint(w, `{"error":"provider secret failure"}`)
	}))
	defer upstream.Close()
	q := testHandlerQueries{provider: sqlc.Provider{
		ID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}, ClientType: string(models.ClientTypeOpenAICompletions),
		Config: []byte(`{"api_key":"sk-test","base_url":"` + upstream.URL + `"}`),
	}}
	h := NewProvidersHandler(slog.New(slog.DiscardHandler), providers.NewService(nil, q, ""), nil)
	status, problem, body := serveHandlerProblem(t, h.Register, "/providers/00000000-0000-0000-0000-000000000001/import-models")
	if status != http.StatusBadGateway || problem.Code != string(apperror.CodeAgentProviderRequestRejected) || problem.Fault != "dependency" {
		t.Fatalf("answer = %d %+v body=%s, want provider request rejected dependency", status, problem, body)
	}
	if strings.Contains(body, "provider secret failure") {
		t.Fatalf("body leaks upstream error: %s", body)
	}
}
