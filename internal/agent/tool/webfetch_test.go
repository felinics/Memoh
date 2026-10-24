package tools

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

func TestWebFetchProviderDeepSeek(t *testing.T) {
	t.Parallel()

	targetURL := "https://example.com/article?lang=zh"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/tools/fetch" {
			t.Errorf("request = %s %s, want POST /v1/tools/fetch", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer deepseek-key" {
			t.Errorf("authorization = %q, want bearer key", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if len(payload) != 1 || payload["url"] != targetURL {
			t.Errorf("payload = %v, want only target url", payload)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"title":"Example article","content":"# Example article\n\nArticle content.\n"}`))
	}))
	t.Cleanup(server.Close)

	config := mustJSON(t, map[string]any{"base_url": server.URL + "/", "api_key": "deepseek-key"})
	provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
	result, err := provider.callFetchProvider(context.Background(), "deepseek", config, targetURL, "auto")
	if err != nil {
		t.Fatalf("callFetchProvider() error = %v", err)
	}
	body := result.(map[string]any)
	for key, want := range map[string]any{
		"success": true, "url": targetURL, "provider": "deepseek", "format": "markdown",
		"title": "Example article", "content": "# Example article\n\nArticle content.",
		"length": len("# Example article\n\nArticle content."),
	} {
		if got := body[key]; got != want {
			t.Errorf("%s = %v, want %v", key, got, want)
		}
	}
}

func TestWebFetchProviderDeepSeekRejectsInvalidRequests(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		url    string
		apiKey string
		want   string
	}{
		{"missing key", "https://example.com", "", "deepseek fetch requires api_key"},
		{"blank key", "https://example.com", "  ", "deepseek fetch requires api_key"},
		{"relative URL", "/article", "key", "deepseek fetch requires an HTTP(S) URL with a host and no credentials"},
		{"unsupported scheme", "file:///etc/passwd", "key", "deepseek fetch requires an HTTP(S) URL with a host and no credentials"},
		{"missing host", "https:///article", "key", "deepseek fetch requires an HTTP(S) URL with a host and no credentials"},
		{"credentials", "https://user:password@example.com", "key", "deepseek fetch requires an HTTP(S) URL with a host and no credentials"},
		{"long URL", "https://example.com/" + strings.Repeat("字", 8192), "key", "deepseek fetch url exceeds 8192 characters"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				t.Error("invalid request reached upstream")
				w.WriteHeader(http.StatusBadRequest)
			}))
			t.Cleanup(server.Close)
			provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
			_, err := provider.callFetchProvider(context.Background(), "deepseek", mustJSON(t, map[string]any{
				"base_url": server.URL, "api_key": tt.apiKey,
			}), tt.url, "auto")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestWebFetchProviderDeepSeekRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"upstream error", http.StatusPaymentRequired, `{"error":{"message":"Insufficient balance","type":"insufficient_balance"}}`, "fetch request failed (HTTP 402): Insufficient balance"},
		{"malformed JSON", http.StatusOK, `<html>Not JSON</html>`, "invalid fetch response"},
		{"missing content", http.StatusOK, `{"title":"No content"}`, "deepseek fetch returned empty content"},
		{"empty content", http.StatusOK, `{"title":"No content","content":" \n "}`, "deepseek fetch returned empty content"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			t.Cleanup(server.Close)
			provider := NewWebFetchProvider(slog.New(slog.DiscardHandler), nil, nil)
			_, err := provider.callFetchProvider(context.Background(), "deepseek", mustJSON(t, map[string]any{
				"base_url": server.URL, "api_key": "key",
			}), "https://example.com", "auto")
			if err == nil || err.Error() != tt.want {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
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
