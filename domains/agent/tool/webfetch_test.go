package tool

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
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

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return data
}
