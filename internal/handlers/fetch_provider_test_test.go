package handlers_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	tools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/fetchproviders"
	"github.com/felinics/memoh/internal/handlers"
	"github.com/felinics/memoh/internal/server"
)

const fetchTestProviderID = "a0000000-0000-0000-0000-000000000001"

type fetchTestQueries struct {
	dbstore.Queries
	row sqlc.FetchProvider
}

func (q *fetchTestQueries) GetFetchProviderByID(context.Context, pgtype.UUID) (sqlc.FetchProvider, error) {
	return q.row, nil
}

func TestFetchProviderTestUsesSavedConfigurationAndRealRuntime(t *testing.T) {
	var called bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tools/fetch" || r.Header.Get("Authorization") != "Bearer saved-key" {
			t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["url"] != "https://example.com/article" {
			t.Errorf("fetch body = %v, error = %v", body, err)
		}
		_, _ = w.Write([]byte(`{"title":"Example article","content":"# Actual fetched content"}`))
	}))
	defer upstream.Close()
	e := fetchTestServer(t, upstream.URL)
	rec := fetchTestRequest(e, `{"url":"https://example.com/article","config":{"api_key":"unsaved-key"}}`)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("status = %d, provider called = %v, body = %s", rec.Code, called, rec.Body.String())
	}
	var result map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result["content"] != "# Actual fetched content" || result["title"] != "Example article" || result["providerName"] != "Saved DeepSeek" {
		t.Fatalf("result = %v", result)
	}
}

func TestFetchProviderTestRejectsUnsafeURLsBeforeCallingProvider(t *testing.T) {
	for _, target := range []string{"", "file:///etc/passwd", "https://user:password@example.com/", "https:///missing-host"} {
		t.Run(target, func(t *testing.T) {
			e := fetchTestServer(t, "http://127.0.0.1:1")
			body, _ := json.Marshal(map[string]string{"url": target})
			rec := fetchTestRequest(e, string(body))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"field":"url"`) {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestFetchProviderTestDoesNotExposeUpstreamErrors(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"rejected saved-key private-provider-detail"}}`))
	}))
	defer upstream.Close()
	rec := fetchTestRequest(fetchTestServer(t, upstream.URL), `{"url":"https://example.com"}`)
	if rec.Code < 400 || rec.Code == http.StatusUnauthorized || strings.Contains(rec.Body.String(), "saved-key") || strings.Contains(rec.Body.String(), "private-provider-detail") {
		t.Fatalf("unsafe error response: status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func fetchTestServer(t *testing.T, baseURL string) *echo.Echo {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	config, err := json.Marshal(map[string]any{"base_url": baseURL, "api_key": "saved-key"})
	if err != nil {
		t.Fatal(err)
	}
	queries := &fetchTestQueries{row: sqlc.FetchProvider{Name: "Saved DeepSeek", Provider: "deepseek", Config: config, Enable: false}}
	service := fetchproviders.NewService(log, queries)
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(log)
	handlers.NewFetchProvidersHandler(log, service, tools.NewWebFetchProvider(log, nil, service)).Register(e)
	return e
}

func fetchTestRequest(e *echo.Echo, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/fetch-providers/"+fetchTestProviderID+"/test", strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}
