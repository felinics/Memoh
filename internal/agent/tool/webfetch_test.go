package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestWebFetchProviderNativeTextIncludesProvider(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Fatal("expected user agent")
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello native"))
	}))
	t.Cleanup(server.Close)

	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	result, err := provider.callFetchProvider(context.Background(), "native", nil, server.URL, "auto")
	if err != nil {
		t.Fatalf("callFetchProvider() error = %v", err)
	}

	body := result.(map[string]any)
	if got := body["provider"]; got != "native" {
		t.Fatalf("provider = %v, want native", got)
	}
	if got := body["content"]; got != "hello native" {
		t.Fatalf("content = %v, want hello native", got)
	}
}

func TestWebFetchProviderJinaReader(t *testing.T) {
	t.Parallel()

	targetURL := "https://example.com/page?q=memoh"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodGet {
			t.Fatalf("method = %s, want GET", got)
		}
		if got := strings.TrimPrefix(r.URL.EscapedPath(), "/"); got != url.PathEscape(targetURL) {
			t.Fatalf("path = %s, want encoded target URL", r.URL.EscapedPath())
		}
		if got := r.Header.Get("Authorization"); got != "Bearer jina-key" {
			t.Fatalf("authorization = %q, want bearer key", got)
		}
		w.Header().Set("Content-Type", "text/markdown")
		_, _ = w.Write([]byte("# Reader result\n"))
	}))
	t.Cleanup(server.Close)

	config := mustJSON(t, map[string]any{
		"base_url": server.URL,
		"api_key":  "jina-key",
	})
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	result, err := provider.callFetchProvider(context.Background(), "jina", config, targetURL, "auto")
	if err != nil {
		t.Fatalf("callFetchProvider() error = %v", err)
	}

	body := result.(map[string]any)
	if got := body["provider"]; got != "jina" {
		t.Fatalf("provider = %v, want jina", got)
	}
	if got := body["content"]; got != "# Reader result" {
		t.Fatalf("content = %v, want trimmed markdown", got)
	}
}

func TestWebFetchProviderCloudflareMarkdown(t *testing.T) {
	t.Parallel()

	targetURL := "https://example.com/cloudflare"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := r.URL.Path; got != "/accounts/acct-1/browser-rendering/markdown" {
			t.Fatalf("path = %s, want Cloudflare markdown endpoint", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer cf-token" {
			t.Fatalf("authorization = %q, want bearer token", got)
		}
		rawBody, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(rawBody, &payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if got := payload["url"]; got != targetURL {
			t.Fatalf("payload url = %s, want %s", got, targetURL)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"result":"## Markdown result\n"}`))
	}))
	t.Cleanup(server.Close)

	config := mustJSON(t, map[string]any{
		"base_url":   server.URL,
		"account_id": "acct-1",
		"api_token":  "cf-token",
	})
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	result, err := provider.callFetchProvider(context.Background(), "cloudflare_markdown", config, targetURL, "auto")
	if err != nil {
		t.Fatalf("callFetchProvider() error = %v", err)
	}

	body := result.(map[string]any)
	if got := body["provider"]; got != "cloudflare_markdown" {
		t.Fatalf("provider = %v, want cloudflare_markdown", got)
	}
	if got := body["content"]; got != "## Markdown result" {
		t.Fatalf("content = %v, want trimmed markdown", got)
	}
}

func TestWebFetchProviderFirecrawlScrape(t *testing.T) {
	t.Parallel()

	const apiKey = "test-firecrawl-key"
	targetURL := "https://example.com/article"
	longMarkdown := strings.Repeat("a", webFetchMaxTextContent+25)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Method; got != http.MethodPost {
			t.Fatalf("method = %s, want POST", got)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+apiKey {
			t.Fatalf("authorization = %q, want bearer key", got)
		}
		var payload struct {
			URL                string   `json:"url"`
			Formats            []string `json:"formats"`
			OnlyMainContent    bool     `json:"onlyMainContent"`
			RemoveBase64Images bool     `json:"removeBase64Images"`
			Timeout            int64    `json:"timeout"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode body: %v", err)
		}
		if payload.URL != targetURL || len(payload.Formats) != 1 || payload.Formats[0] != "markdown" {
			t.Fatalf("payload = %#v", payload)
		}
		if !payload.OnlyMainContent || !payload.RemoveBase64Images {
			t.Fatalf("unsafe scrape options = %#v", payload)
		}
		if payload.Timeout != (300 * time.Second).Milliseconds() {
			t.Fatalf("timeout = %d, want capped %d", payload.Timeout, (300 * time.Second).Milliseconds())
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"success": true,
			"data": map[string]any{
				"markdown": longMarkdown,
				"metadata": map[string]any{
					"title": "Example article", "description": "Example description",
					"sourceURL": targetURL, "url": "https://example.com/final", "contentType": "text/html",
				},
			},
		})
	}))
	t.Cleanup(server.Close)

	config := mustJSON(t, map[string]any{
		"base_url": server.URL, "api_key": apiKey, "timeout_seconds": 400,
	})
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	result, err := provider.callFetchProvider(context.Background(), "firecrawl", config, targetURL, "auto")
	if err != nil {
		t.Fatalf("callFetchProvider() error = %v", err)
	}
	body := result.(map[string]any)
	if body["provider"] != "firecrawl" || body["format"] != "markdown" || body["url"] != "https://example.com/final" {
		t.Fatalf("result envelope = %#v", body)
	}
	if body["title"] != "Example article" || body["description"] != "Example description" {
		t.Fatalf("result metadata = %#v", body)
	}
	if body["length"] != len(longMarkdown) || len(body["content"].(string)) != webFetchMaxTextContent {
		t.Fatalf("result truncation = %#v", body)
	}
}

func TestWebFetchProviderFirecrawlScrapeErrors(t *testing.T) {
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)

	t.Run("requires API key", func(t *testing.T) {
		_, err := provider.callFirecrawlScrape(context.Background(), nil, "https://example.com/")
		if err == nil || err.Error() != "firecrawl API key is required" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("maps API error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"success":false,"error":"rate limit exceeded"}`))
		}))
		t.Cleanup(server.Close)
		config := mustJSON(t, map[string]any{"api_key": "secret-value", "base_url": server.URL})
		_, err := provider.callFirecrawlScrape(context.Background(), config, "https://example.com/")
		if err == nil || err.Error() != "fetch request failed (HTTP 429): rate limit exceeded" {
			t.Fatalf("error = %v", err)
		}
	})

	t.Run("honors cancellation", func(t *testing.T) {
		started := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(started)
			time.Sleep(2 * time.Second)
		}))
		t.Cleanup(server.Close)
		config := mustJSON(t, map[string]any{"api_key": "test-key", "base_url": server.URL})
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() {
			_, err := provider.callFirecrawlScrape(ctx, config, "https://example.com/")
			done <- err
		}()
		<-started
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want context canceled", err)
		}
	})
}

func TestFirecrawlScrapeIntegration(t *testing.T) {
	apiKey := os.Getenv("FIRECRAWL_API_KEY")
	if apiKey == "" {
		t.Skip("FIRECRAWL_API_KEY is not configured")
	}
	config := mustJSON(t, map[string]any{"api_key": apiKey, "timeout_seconds": 30})
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, err := provider.callFirecrawlScrape(ctx, config, "https://example.com/")
	if err != nil {
		t.Fatalf("live Firecrawl scrape failed: %v", err)
	}
	body, ok := result.(map[string]any)
	if !ok || body["provider"] != "firecrawl" || body["format"] != "markdown" {
		t.Fatal("live Firecrawl scrape returned an unexpected envelope")
	}
	content, ok := body["content"].(string)
	if !ok || !strings.Contains(strings.ToLower(content), "example domain") {
		t.Fatal("live Firecrawl scrape did not return the expected page content")
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}
