package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func TestCallFirecrawlSearch(t *testing.T) {
	const apiKey = "test-firecrawl-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Errorf("Authorization = %q", got)
		}
		var body struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Query != "memoh agents" || body.Limit != 3 {
			t.Errorf("request = %+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"data":{"web":[{"title":"Memoh","url":"https://memoh.example/","description":"Agent platform"}]}}`))
	}))
	defer server.Close()

	config, _ := json.Marshal(map[string]any{"api_key": apiKey, "base_url": server.URL})
	got, err := callFirecrawlSearch(context.Background(), config, "memoh agents", 3)
	if err != nil {
		t.Fatalf("callFirecrawlSearch: %v", err)
	}
	result := got.(map[string]any)
	items := result["results"].([]map[string]any)
	if len(items) != 1 || items[0]["title"] != "Memoh" || items[0]["url"] != "https://memoh.example/" || items[0]["description"] != "Agent platform" {
		t.Fatalf("results = %#v", items)
	}
}

func TestCallFirecrawlSearchErrors(t *testing.T) {
	t.Run("requires API key", func(t *testing.T) {
		_, err := callFirecrawlSearch(context.Background(), nil, "query", 1)
		if err == nil || err.Error() != "firecrawl API key is required" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("maps API error without exposing credentials", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"error":"rate limit exceeded"}`))
		}))
		defer server.Close()
		config, _ := json.Marshal(map[string]any{"api_key": "secret-value", "base_url": server.URL})
		_, err := callFirecrawlSearch(context.Background(), config, "query", 1)
		if err == nil || err.Error() != "search request failed (HTTP 429): rate limit exceeded" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("honors cancellation", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			time.Sleep(2 * time.Second)
		}))
		defer server.Close()
		config, _ := json.Marshal(map[string]any{"api_key": "test-key", "base_url": server.URL, "timeout_seconds": 10})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := callFirecrawlSearch(ctx, config, "query", 1)
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})

	t.Run("honors configured timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			time.Sleep(time.Second)
		}))
		defer server.Close()
		config, _ := json.Marshal(map[string]any{"api_key": "test-key", "base_url": server.URL, "timeout_seconds": 0.05})
		started := time.Now()
		_, err := callFirecrawlSearch(context.Background(), config, "query", 1)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline exceeded", err)
		}
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("timeout took %v", elapsed)
		}
	})
}

func TestFirecrawlSearchIntegration(t *testing.T) {
	apiKey := os.Getenv("FIRECRAWL_API_KEY")
	if apiKey == "" {
		t.Skip("FIRECRAWL_API_KEY is not configured")
	}
	config, err := json.Marshal(map[string]any{
		"api_key":         apiKey,
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	got, err := callFirecrawlSearch(ctx, config, "Memoh AI agent GitHub", 3)
	if err != nil {
		t.Fatalf("live Firecrawl search failed: %v", err)
	}
	result, ok := got.(map[string]any)
	if !ok || result["query"] != "Memoh AI agent GitHub" {
		t.Fatalf("unexpected response envelope")
	}
	items, ok := result["results"].([]map[string]any)
	if !ok || len(items) == 0 {
		t.Fatal("live Firecrawl search returned no web results")
	}
	for i, item := range items {
		if item["title"] == "" || item["url"] == "" {
			t.Fatalf("result %d is missing title or URL", i)
		}
	}
}
