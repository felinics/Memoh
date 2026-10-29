package tools

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

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

func TestWebProviderHTTPErrorsKeepOnlyStatusAndClassification(t *testing.T) {
	for _, builder := range []struct {
		name  string
		build func(int, []byte) error
	}{{"search", buildSearchHTTPError}, {"fetch", buildFetchHTTPError}} {
		t.Run(builder.name, func(t *testing.T) {
			for _, tc := range []struct {
				status int
				kind   sdk.ErrorKind
			}{
				{400, sdk.KindUnknown},
				{401, sdk.KindAuthentication},
				{402, sdk.KindQuotaExhausted},
				{403, sdk.KindPermissionDenied},
				{429, sdk.KindRateLimited},
				{500, sdk.KindServerError},
				{501, sdk.KindUnknown},
				{503, sdk.KindServerError},
				{505, sdk.KindUnknown},
			} {
				err := builder.build(tc.status, []byte(`{"error":{"message":"private-secret private-user-content"}}`))
				var apiErr *sdk.APIError
				if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status || sdk.KindOf(err) != tc.kind {
					t.Fatalf("status %d: error=%v", tc.status, err)
				}
				if apiErr.Provider != "web "+builder.name || apiErr.Message != "" || len(apiErr.Body) != 0 || len(apiErr.Header) != 0 || apiErr.RequestID != "" || apiErr.Type != "" || apiErr.Code != "" {
					t.Fatalf("status %d: retained upstream response fields", tc.status)
				}
			}
		})
	}
}
