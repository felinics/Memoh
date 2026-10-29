package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/searchproviders"
	"github.com/felinics/memoh/internal/server"
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
	cause := errors.New("upstream-secret raw vendor body")
	h := NewSearchProvidersHandler(log, searchproviders.NewService(log, searchTestQueries{}), searchTestRunner{run: func(context.Context, sqlc.SearchProvider, string, int) (any, error) {
		return nil, cause
	}})
	e := echo.New()
	r := httptest.NewRequest(http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", strings.NewReader(`{"query":"ok"}`))
	r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	c := e.NewContext(r, httptest.NewRecorder())
	c.SetParamNames("id")
	c.SetParamValues("00000000-0000-0000-0000-000000000001")
	translated := h.Test(c)
	var public *apperror.Error
	if !errors.As(translated, &public) || !errors.Is(public.Cause(), cause) {
		t.Fatalf("test failure discarded diagnostic cause: %v", translated)
	}
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

func TestSearchProviderTestAccessLogOmitsUpstreamBody(t *testing.T) {
	const secret = "saved-search-private-key"
	const privateBody = "private-upstream-response-content"
	for _, tc := range []struct {
		provider string
		status   int
		body     string
	}{
		{"brave", http.StatusUnauthorized, `{"error":{"message":"rejected ` + secret + ` ` + privateBody + `"}}`},
		{"sogou", http.StatusOK, `{"Response":{"Error":{"Code":"InternalError","Message":"rejected ` + secret + ` ` + privateBody + `"}}}`},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer upstream.Close()
			transport := http.DefaultTransport
			http.DefaultTransport = upstream.Client().Transport
			defer func() { http.DefaultTransport = transport }()
			baseURL := upstream.URL
			if tc.provider == "sogou" {
				baseURL = strings.TrimPrefix(baseURL, "https://")
			}
			config, err := json.Marshal(map[string]string{"base_url": baseURL, "api_key": secret, "secret_id": "test-id", "secret_key": secret})
			if err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			service := searchproviders.NewService(log, searchTestQueries{row: sqlc.SearchProvider{Provider: tc.provider, Config: config}})
			e := echo.New()
			e.HTTPErrorHandler = server.NewHTTPErrorHandler(log)
			e.Use(server.AccessLog(log))
			NewSearchProvidersHandler(log, service, tools.NewWebProvider(log, nil, service)).Register(e)
			r := httptest.NewRequest(http.MethodPost, "/search-providers/00000000-0000-0000-0000-000000000001/test", strings.NewReader(`{"query":"Memoh"}`))
			r.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
			w := httptest.NewRecorder()
			e.ServeHTTP(w, r)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			for _, text := range []string{secret, privateBody} {
				if strings.Contains(w.Body.String()+logs.String(), text) {
					t.Fatal("search test exposed upstream credentials or response body")
				}
			}
			if !strings.Contains(logs.String(), strconv.Itoa(tc.status)) {
				t.Fatalf("search test lost upstream HTTP status: %s", logs.String())
			}
		})
	}
}
