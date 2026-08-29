package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/providers"
)

func acpCredentialsMetadata(agentID, mode string, managed map[string]any) map[string]any {
	return map[string]any{
		"acp": map[string]any{
			"agents": map[string]any{
				agentID: map[string]any{
					"enabled":    true,
					"setup_mode": mode,
					"managed":    managed,
				},
			},
		},
	}
}

func TestTestACPManagedCredentialsProbesClaudeCodeEndpoint(t *testing.T) {
	var seenAuthHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models":
			seenAuthHeader = r.Header.Get("x-api-key")
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v1/models/__ping__":
			http.Error(w, `{"error":{"message":"model not found"}}`, http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/messages":
			http.Error(w, `{"error":{"message":"invalid model"}}`, http.StatusBadRequest)
		default:
			http.Error(w, "unexpected request "+r.Method+" "+r.URL.Path, http.StatusTeapot)
		}
	}))
	defer server.Close()

	metadata := acpCredentialsMetadata("claude-code", "api_key", map[string]any{
		"api_key":  "sk-ant-test",
		"base_url": server.URL,
	})
	resp, err := testACPManagedCredentials(context.Background(), metadata, "claude-code", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != providers.TestStatusOK || !resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
	if seenAuthHeader != "sk-ant-test" {
		t.Fatalf("x-api-key = %q", seenAuthHeader)
	}
}

func TestTestACPManagedCredentialsReportsAuthError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"invalid api key"}}`, http.StatusUnauthorized)
	}))
	defer server.Close()

	metadata := acpCredentialsMetadata("codex", "api_key", map[string]any{
		"api_key":  "sk-bad",
		"base_url": server.URL,
	})
	resp, err := testACPManagedCredentials(context.Background(), metadata, "codex", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != providers.TestStatusAuthError || !resp.Reachable {
		t.Fatalf("response = %#v", resp)
	}
}

func TestTestACPManagedCredentialsRejectsUnknownAgent(t *testing.T) {
	_, err := testACPManagedCredentials(context.Background(), map[string]any{}, "unknown-agent", nil)
	assertACPCredentialsHTTPError(t, err, http.StatusNotFound)
}

func TestTestACPManagedCredentialsRejectsNonAPIKeyMode(t *testing.T) {
	metadata := acpCredentialsMetadata("claude-code", "oauth", map[string]any{
		"oauth_token": "token",
		"api_key":     "sk-ant-test",
	})
	_, err := testACPManagedCredentials(context.Background(), metadata, "claude-code", nil)
	assertACPCredentialsHTTPError(t, err, http.StatusBadRequest)
}

func TestTestACPManagedCredentialsRejectsMissingAPIKey(t *testing.T) {
	metadata := acpCredentialsMetadata("codex", "api_key", map[string]any{"api_key": " "})
	_, err := testACPManagedCredentials(context.Background(), metadata, "codex", nil)
	assertACPCredentialsHTTPError(t, err, http.StatusBadRequest)
}

func assertACPCredentialsHTTPError(t *testing.T, err error, wantCode int) {
	t.Helper()
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want echo.HTTPError", err)
	}
	if httpErr.Code != wantCode {
		t.Fatalf("status = %d, want %d", httpErr.Code, wantCode)
	}
}
