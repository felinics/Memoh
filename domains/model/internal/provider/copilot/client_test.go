package copilot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestNewHTTPClientAddsCopilotHeaders(t *testing.T) {
	t.Parallel()

	base := &http.Client{
		Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			if got := req.Header.Get("Copilot-Integration-Id"); got != copilotIntegrationID {
				t.Fatalf("expected integration id %q, got %q", copilotIntegrationID, got)
			}
			if got := req.Header.Get("Editor-Version"); got != copilotEditorVersion {
				t.Fatalf("expected editor version %q, got %q", copilotEditorVersion, got)
			}
			if got := req.Header.Get("Editor-Plugin-Version"); got != copilotPluginVersion {
				t.Fatalf("expected plugin version %q, got %q", copilotPluginVersion, got)
			}
			if got := req.Header.Get("User-Agent"); got != copilotUserAgent {
				t.Fatalf("expected user agent %q, got %q", copilotUserAgent, got)
			}
			if got := req.Header.Get("X-GitHub-Api-Version"); got != copilotAPIVersion {
				t.Fatalf("expected api version %q, got %q", copilotAPIVersion, got)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`ok`)),
				Header:     make(http.Header),
			}, nil
		}),
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://api.githubcopilot.com/chat/completions", nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	resp, err := NewHTTPClient(base).Do(req) //nolint:gosec // Test request targets a fixed Copilot API endpoint.
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	_ = resp.Body.Close()
}

func TestNewHTTPClientWithNilBaseDoesNotPanic(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got := req.Header.Get("Copilot-Integration-Id"); got != copilotIntegrationID {
			t.Fatalf("expected integration id %q, got %q", copilotIntegrationID, got)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`ok`))
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}

	resp, err := NewHTTPClient(nil).Do(req) //nolint:gosec // Test request targets an httptest server URL.
	if err != nil {
		t.Fatalf("execute request with nil base client: %v", err)
	}
	_ = resp.Body.Close()
}
