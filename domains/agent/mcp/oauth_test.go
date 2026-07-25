package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeForAuthUsesMCPInitializeRequest(t *testing.T) {
	t.Parallel()

	var gotMethod string
	var gotAccept string
	var gotRequest struct {
		JSONRPC string `json:"jsonrpc"`
		Method  string `json:"method"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotAccept = r.Header.Get("Accept")
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Fatalf("decode probe request: %v", err)
		}
		w.Header().Set("WWW-Authenticate", `Bearer resource_metadata="https://example.test/.well-known/oauth-protected-resource",scope="mcp:connect"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	svc := NewOAuthService(nil, nil, "")
	resourceMetaURL, scope, err := svc.probeForAuth(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("probeForAuth() error = %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotMethod)
	}
	if gotRequest.JSONRPC != "2.0" || gotRequest.Method != "initialize" {
		t.Fatalf("request = %#v, want JSON-RPC initialize", gotRequest)
	}
	if gotAccept != "application/json, text/event-stream" {
		t.Fatalf("Accept = %q", gotAccept)
	}
	if resourceMetaURL != "https://example.test/.well-known/oauth-protected-resource" {
		t.Fatalf("resourceMetaURL = %q", resourceMetaURL)
	}
	if scope != "mcp:connect" {
		t.Fatalf("scope = %q", scope)
	}
}
