package application

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/models"
)

// A title call the provider answers with a server error is made again.
func TestGenerateTitleRetriesServerError(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"message":"overloaded","type":"server_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","object":"chat.completion","created":0,"model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"Trip planning"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(server.Close)

	svc := &Service{logger: slog.New(slog.DiscardHandler)}
	provider := sqlc.Provider{ClientType: "openai-completions", Config: []byte(`{"base_url":"` + server.URL + `","api_key":"k"}`)}
	title, err := svc.generateTitle(context.Background(), "user-1", models.GetResponse{ModelID: "m"}, provider, "help me plan a trip")
	if err != nil {
		t.Fatalf("generateTitle() error = %v, want the retried title", err)
	}
	if calls.Load() != 2 || title != "Trip planning" {
		t.Fatalf("generateTitle() = %q after %d calls, want the title after 2", title, calls.Load())
	}
}
