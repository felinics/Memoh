package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/server"
)

type testHandlerQueries struct {
	dbstore.Queries
	model       sqlc.Model
	provider    sqlc.Provider
	modelErr    error
	providerErr error
}

func (q testHandlerQueries) GetModelByID(context.Context, pgtype.UUID) (sqlc.Model, error) {
	return q.model, q.modelErr
}

func (q testHandlerQueries) GetProviderByID(context.Context, pgtype.UUID) (sqlc.Provider, error) {
	return q.provider, q.providerErr
}

func serveHandlerProblem(t *testing.T, register func(*echo.Echo), target string) (int, server.Problem, string) {
	t.Helper()
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(slog.New(slog.DiscardHandler))
	register(e)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, target, nil))
	var problem server.Problem
	_ = json.Unmarshal(rec.Body.Bytes(), &problem)
	return rec.Code, problem, rec.Body.String()
}

func TestProviderAndModelTestClassifyServiceErrors(t *testing.T) {
	dbFailure := errors.New("database secret failure")
	cases := []struct {
		name   string
		model  bool
		id     string
		q      testHandlerQueries
		status int
		leak   string
	}{
		{"provider invalid uuid", false, "not-a-uuid", testHandlerQueries{}, http.StatusBadRequest, ""},
		{"provider not found", false, "00000000-0000-0000-0000-000000000001", testHandlerQueries{providerErr: pgx.ErrNoRows}, http.StatusNotFound, ""},
		{"provider internal", false, "00000000-0000-0000-0000-000000000001", testHandlerQueries{providerErr: dbFailure}, http.StatusInternalServerError, "database secret failure"},
		{"model invalid uuid", true, "not-a-uuid", testHandlerQueries{}, http.StatusBadRequest, ""},
		{"model not found", true, "00000000-0000-0000-0000-000000000001", testHandlerQueries{modelErr: pgx.ErrNoRows}, http.StatusNotFound, ""},
		{"model internal", true, "00000000-0000-0000-0000-000000000001", testHandlerQueries{modelErr: dbFailure}, http.StatusInternalServerError, "database secret failure"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			providerSvc := providers.NewService(log, tc.q, "")
			var register func(*echo.Echo)
			if tc.model {
				register = NewModelsHandler(log, models.NewService(log, tc.q), nil).Register
			} else {
				register = NewProvidersHandler(log, providerSvc, nil).Register
			}
			status, _, body := serveHandlerProblem(t, register, func() string {
				if tc.model {
					return "/models/" + tc.id + "/test"
				}
				return "/providers/" + tc.id + "/test"
			}())
			if status != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", status, tc.status, body)
			}
			if tc.leak != "" && strings.Contains(body, tc.leak) {
				t.Fatalf("body leaks service error: %s", body)
			}
		})
	}
}
