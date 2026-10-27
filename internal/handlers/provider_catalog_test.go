package handlers

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/modelcatalog"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/providers"
	"github.com/felinics/memoh/internal/server"
)

func catalogHandler(t *testing.T, metadata string, upstream http.Handler) (*echo.Echo, *catalogImportQueries, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); upstream.ServeHTTP(w, r) }))
	t.Cleanup(source.Close)
	providerID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	q := &catalogImportQueries{
		testHandlerQueries: testHandlerQueries{provider: sqlc.Provider{ID: providerID, ClientType: string(models.ClientTypeOpenAICompletions), Metadata: []byte(metadata), Config: []byte(`{"api_key":"PRIVATE","base_url":"http://must-not-be-contacted.invalid"}`)}},
		existing:           sqlc.Model{ID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true}, ProviderID: providerID, ModelID: "existing", Type: "chat", Config: []byte(`{"schema_version":2}`)},
	}
	log := slog.New(slog.DiscardHandler)
	service := providers.NewService(log, q, "")
	catalog := modelcatalog.New(log, modelcatalog.Options{URL: source.URL})
	h := NewProvidersHandler(log, service, models.NewService(log, q), catalog)
	e := echo.New()
	e.HTTPErrorHandler = server.NewHTTPErrorHandler(log)
	h.Register(e)
	return e, q, calls
}

func catalogRequest(e *echo.Echo, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)
	return rec
}

const (
	catalogImportPath    = "/providers/00000000-0000-0000-0000-000000000001/import-catalog-models"
	publicCatalogFixture = `{"deepseek":{"id":"deepseek","models":{"existing":{"id":"existing","tool_call":true},"new":{"id":"new","tool_call":true}}}}`
)

func TestServerCatalogPreviewAndImportShareConversion(t *testing.T) {
	e, q, calls := catalogHandler(t, `{"source":"deepseek"}`, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("provider credentials leaked")
		}
		_, _ = w.Write([]byte(publicCatalogFixture))
	}))
	preview := catalogRequest(e, http.MethodGet, "/providers/catalog/models?source=deepseek&client_type=openai-completions", "")
	if preview.Code != 200 {
		t.Fatal(preview.Code, preview.Body.String())
	}
	if len(q.created) != 0 || len(q.updated) != 0 {
		t.Fatal("preview wrote tenant models")
	}
	// Stored provider identity is authoritative. Skip a model just edited by the user.
	imported := catalogRequest(e, http.MethodPost, catalogImportPath, `{"source":"another","client_type":"google-generative-ai","skip_model_ids":["existing"]}`)
	if imported.Code != 200 {
		t.Fatal(imported.Code, imported.Body.String())
	}
	if len(q.created) != 1 || q.created[0].ModelID != "new" || q.created[0].Enable || len(q.updated) != 0 {
		t.Fatalf("writes=%+v updates=%+v", q.created, q.updated)
	}
	if calls.Load() != 1 {
		t.Fatal("preview and import did not share cache")
	}
	var p modelcatalog.Response
	var result providers.ImportModelsResponse
	if err := json.Unmarshal(preview.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(imported.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Catalog == nil || result.Catalog.Revision != p.Revision {
		t.Fatal("import lost catalog revision")
	}
}

func TestManagedCatalogCannotEnterPublicImport(t *testing.T) {
	for _, metadata := range []string{`{"managed":true,"source":"builtin"}`, `{"source":"builtin"}`, `{"managed":true,"source":"deepseek"}`} {
		for _, endpoint := range []string{catalogImportPath, strings.Replace(catalogImportPath, "import-catalog-models", "import-models", 1)} {
			body := `{}`
			e, q, calls := catalogHandler(t, metadata, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(publicCatalogFixture)) }))
			rec := catalogRequest(e, http.MethodPost, endpoint, body)
			if rec.Code != 403 || !strings.Contains(rec.Body.String(), "provider.managed_readonly") || calls.Load() != 0 || len(q.created)+len(q.updated) != 0 {
				t.Fatal(metadata, body, rec.Code, rec.Body.String())
			}
		}
	}
}

func TestCatalogFailureNeverFallsThroughAndHidesDiagnostics(t *testing.T) {
	e, q, _ := catalogHandler(t, `{"source":"deepseek"}`, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("PRIVATE upstream diagnostics"))
	}))
	rec := catalogRequest(e, http.MethodPost, catalogImportPath, `{}`)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "provider.catalog_unavailable") || strings.Contains(rec.Body.String(), "PRIVATE") || len(q.created)+len(q.updated) != 0 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Header().Get(echo.HeaderContentType), "application/problem+json") {
		t.Fatal(rec.Header())
	}
}
