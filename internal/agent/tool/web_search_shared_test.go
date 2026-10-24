package tools

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/redact"
)

func TestWebProviderSearchUsesSavedProviderRequest(t *testing.T) {
	const key = "saved-search-test-credential"
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("X-Subscription-Token") != key || r.URL.Query().Get("q") != "shared search" || r.URL.Query().Get("count") != "2" {
			t.Errorf("shared Search did not use saved provider configuration and query")
		}
		_, _ = w.Write([]byte(`{"web":{"results":[{"title":"Found","url":"https://example.com","description":"Real transport"}]}}`))
	}))
	t.Cleanup(server.Close)
	config, _ := json.Marshal(map[string]any{"api_key": key, "base_url": server.URL})
	result, err := (&WebProvider{}).Search(t.Context(), sqlc.SearchProvider{Provider: "brave", Config: config}, "shared search", 2)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(result)
	if calls != 1 || !strings.Contains(string(b), "Found") {
		t.Fatalf("result=%s calls=%d", b, calls)
	}
	if strings.Contains(redact.Text("upstream echoed "+key), key) {
		t.Fatal("shared Search did not register saved credentials for redaction")
	}
}
