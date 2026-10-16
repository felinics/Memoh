package memllm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
)

// A memory call the provider rate-limits is made again, and only the
// successful call reports usage.
func TestGenerateRetriesRateLimitedCall(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error":{"message":"slow down","type":"rate_limit_error"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"id":"r","object":"chat.completion","created":0,"model":"m",` +
			`"choices":[{"index":0,"message":{"role":"assistant","content":"{\"facts\":[]}"},"finish_reason":"stop"}],` +
			`"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`))
	}))
	t.Cleanup(server.Close)

	var usageCalls int
	client := New(Config{
		ModelID:    "m",
		BaseURL:    server.URL,
		APIKey:     "k",
		ClientType: "openai-completions",
		OnUsage:    func(context.Context, string, sdk.Usage) { usageCalls++ },
	})
	result, err := client.generate(context.Background(), OperationExtract, "system", "user")
	if err != nil {
		t.Fatalf("generate() error = %v, want the retried answer", err)
	}
	if calls.Load() != 2 || result.Text != `{"facts":[]}` || usageCalls != 1 {
		t.Fatalf("generate() = %q after %d calls with %d usage reports, want the answer after 2 with 1", result.Text, calls.Load(), usageCalls)
	}
}
