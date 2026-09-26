package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/redact"
	"github.com/felinics/memoh/internal/searchproviders"
)

func TestCallSearXNGSearchUsesCustomBearerHeader(t *testing.T) {
	const token = "searx-test-token-that-must-stay-private"
	var receivedAuthorization string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		if got := r.URL.Query().Get("q"); got != "asymmetric query" {
			t.Errorf("query = %q, want asymmetric query", got)
		}
		if got := r.Header.Get("X-Tenant"); got != "tenant-a" {
			t.Errorf("X-Tenant = %q, want tenant-a", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"title":"second","url":"https://second.example","content":"B","score":0.2},{"title":"first","url":"https://first.example","content":"A","score":0.9}]}`))
	}))
	t.Cleanup(server.Close)

	config := []byte(`{"base_url":"` + server.URL + `","headers":{"Authorization":"Bearer ` + token + `","X-Tenant":"tenant-a"}}`)

	result, err := callSearXNGSearch(t.Context(), config, "asymmetric query", 1)
	if err != nil {
		t.Fatalf("callSearXNGSearch: %v", err)
	}
	if receivedAuthorization != "Bearer "+token {
		t.Fatal("Authorization header did not contain the configured Bearer credential")
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	if strings.Contains(string(encoded), token) {
		t.Fatal("search result leaked the header credential")
	}
	if !strings.Contains(string(encoded), `"title":"first"`) || strings.Contains(string(encoded), `"title":"second"`) {
		t.Fatalf("unexpected ranked result: %s", encoded)
	}
}

func TestRegisterSearchProviderSecretsIncludesSearXNGHeaders(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const token = "searx-redaction-token-that-must-stay-private" //nolint:gosec // G101: test-only fixed credential.
	registerSearchProviderSecrets(sqlc.SearchProvider{
		Provider: string(searchproviders.ProviderSearXNG),
		Config:   []byte(`{"headers":{"Authorization":"Bearer ` + token + `"}}`),
	})

	redacted := redact.Text("authorization=Bearer " + token + " token=" + token)
	if strings.Contains(redacted, token) || strings.Contains(redacted, "Bearer "+token) {
		t.Fatal("SearXNG Authorization credential was not registered for redaction")
	}
}

func TestSearXNGSearchErrorRedactsRegisteredHeaderValue(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const token = "searx-error-token-that-must-stay-private" //nolint:gosec // G101: test-only fixed credential.
	config := []byte(`{"headers":{"Authorization":"Bearer ` + token + `"}}`)
	registerSearchProviderSecrets(sqlc.SearchProvider{
		Provider: string(searchproviders.ProviderSearXNG),
		Config:   config,
	})

	err := buildSearchHTTPError(http.StatusUnauthorized, []byte(`{"detail":"received Bearer `+token+`"}`))
	if strings.Contains(err.Error(), token) {
		t.Fatal("search error exposed the registered header credential")
	}
}

func TestCallSearXNGSearchLive(t *testing.T) {
	baseURL := strings.TrimSpace(os.Getenv("SEARX_BASE_URL"))
	token := strings.TrimSpace(os.Getenv("SEARX_API_TOKEN"))
	if baseURL == "" || token == "" {
		t.Skip("SEARX_BASE_URL and SEARX_API_TOKEN are required for the live test")
	}
	config, err := json.Marshal(map[string]any{
		"base_url":        baseURL,
		"headers":         map[string]string{"Authorization": "Bearer " + token},
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatalf("marshal live config: %v", err)
	}

	result, err := callSearXNGSearch(t.Context(), config, "Memoh AI agent", 3)
	if err != nil {
		t.Fatalf("live SearXNG search failed: %v", err)
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("live result type = %T", result)
	}
	results, ok := resultMap["results"].([]map[string]any)
	if !ok || len(results) == 0 {
		t.Fatal("live SearXNG search returned no results")
	}
}

func TestCallSearXNGSearchKeepsLiteralBaseURLCompatibility(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	t.Cleanup(server.Close)

	config := []byte(`{"base_url":"` + server.URL + `"}`)
	if _, err := callSearXNGSearch(t.Context(), config, "compatibility", 1); err != nil {
		t.Fatalf("literal base_url should remain supported: %v", err)
	}
}
