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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/searchproviders"
)

type searchTestQueries struct {
	dbstore.Queries
	row sqlc.SearchProvider
	err error
}

func (q searchTestQueries) GetSearchProviderByID(context.Context, pgtype.UUID) (sqlc.SearchProvider, error) {
	return q.row, q.err
}

type searchTestRunner struct {
	run func(context.Context, sqlc.SearchProvider, string, int) (any, error)
}

func (s searchTestRunner) Search(c context.Context, p sqlc.SearchProvider, q string, n int) (any, error) {
	return s.run(c, p, q, n)
}

func TestSearchProviderTestUsesSavedConfigAndSharedSearch(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	saved := sqlc.SearchProvider{Provider: "brave", Config: []byte(`{"api_key":"saved-secret"}`), Enable: false}
	calls := 0
	runner := searchTestRunner{run: func(ctx context.Context, p sqlc.SearchProvider, q string, count int) (any, error) {
		calls++
		if string(p.Config) != string(saved.Config) || p.Provider != "brave" || p.Enable || q != "Memoh search" || count != 3 {
			t.Fatalf("wrong saved provider/query/count: %+v %q %d", p, q, count)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 30*time.Second {
			t.Fatal("test must bound runtime to 30 seconds")
		}
		return map[string]any{"query": q, "results": []map[string]any{{"title": "Found", "url": "https://example.com", "description": "Search result"}}}, nil
	}}
	h := NewSearchProvidersHandler(log, searchproviders.NewService(log, searchTestQueries{row: saved}), runner)
	e := echo.New()
	h.Register(e)
	r := httptest.NewRequest(http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", strings.NewReader(`{"query":"  Memoh search  ","count":3,"config":{"api_key":"untrusted"}}`))
	r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	w := httptest.NewRecorder()
	e.ServeHTTP(w, r)
	if w.Code != http.StatusOK || calls != 1 || !strings.Contains(w.Body.String(), "Found") {
		t.Fatalf("response=%d %s calls=%d", w.Code, w.Body, calls)
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("test response leaked saved config")
	}
}

func TestSearchProviderTestRejectsInvalidInputBeforeSearch(t *testing.T) {
	for _, body := range []string{`{}`, `{"query":" "}`, `{"query":"` + strings.Repeat("界", 129) + `"}`, `{"query":"ok","count":0}`, `{"query":"ok","count":21}`} {
		t.Run(body, func(t *testing.T) {
			log := slog.New(slog.DiscardHandler)
			h := NewSearchProvidersHandler(log, searchproviders.NewService(log, searchTestQueries{}), searchTestRunner{run: func(context.Context, sqlc.SearchProvider, string, int) (any, error) {
				t.Fatal("invalid request reached search")
				return nil, nil
			}})
			status, _ := serveProblem(t, h.Register, http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", body)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d", status)
			}
		})
	}
}

func TestSearchProviderTestDoesNotExposeUpstreamBody(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	h := NewSearchProvidersHandler(log, searchproviders.NewService(log, searchTestQueries{}), searchTestRunner{run: func(context.Context, sqlc.SearchProvider, string, int) (any, error) {
		return nil, errors.New("upstream-secret raw vendor body")
	}})
	status, p := serveProblem(t, h.Register, http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", `{"query":"ok"}`)
	b, _ := json.Marshal(p)
	if status != http.StatusBadGateway || p.Code != string(apperror.CodeAgentProviderRequestRejected) || strings.Contains(string(b), "upstream-secret") {
		t.Fatalf("response=%d %s", status, b)
	}
}

func TestSearchProviderTestMissingSavedProviderDoesNotRun(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	h := NewSearchProvidersHandler(log, searchproviders.NewService(log, searchTestQueries{err: pgx.ErrNoRows}), searchTestRunner{run: func(context.Context, sqlc.SearchProvider, string, int) (any, error) {
		t.Fatal("missing provider reached search")
		return nil, nil
	}})
	status, p := serveProblem(t, h.Register, http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", `{"query":"ok"}`)
	if status != http.StatusNotFound || p.Code != string(apperror.CodeSearchProviderNotFound) {
		t.Fatalf("response=%d %+v", status, p)
	}
}
