package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	mcpgw "github.com/felinics/memoh/internal/mcp"
)

type testToolInput struct {
	Query string `json:"query"`
}

type testToolOutput struct {
	Echo string `json:"echo"`
}

func newTestMCPServer() *sdkmcp.Server {
	server := sdkmcp.NewServer(&sdkmcp.Implementation{
		Name:    "test-federation-server",
		Version: "v1",
	}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name:        "echo",
		Description: "Echo query",
	}, func(_ context.Context, _ *sdkmcp.CallToolRequest, input testToolInput) (*sdkmcp.CallToolResult, testToolOutput, error) {
		return nil, testToolOutput{Echo: input.Query}, nil
	})
	return server
}

func withAuthHeader(next http.Handler, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func TestFederationGatewayHTTPConnectionViaSDK(t *testing.T) {
	server := newTestMCPServer()
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(withAuthHeader(handler, "Bearer test-token"))
	defer httpServer.Close()

	gateway := &MCPFederationGateway{
		client: httpServer.Client(),
	}
	connection := mcpgw.Connection{
		Config: map[string]any{
			"url": httpServer.URL,
			"headers": map[string]any{
				"Authorization": "Bearer test-token",
			},
		},
	}

	tools, err := gateway.ListHTTPConnectionTools(context.Background(), connection)
	if err != nil {
		t.Fatalf("list http tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tool list: %#v", tools)
	}

	payload, err := gateway.CallHTTPConnectionTool(context.Background(), connection, "echo", map[string]any{
		"query": "hello-http",
	})
	if err != nil {
		t.Fatalf("call http tool failed: %v", err)
	}
	assertEchoResult(t, payload, "hello-http")
}

func TestFederationGatewaySSEConnectionViaSDK(t *testing.T) {
	server := newTestMCPServer()
	handler := sdkmcp.NewSSEHandler(func(*http.Request) *sdkmcp.Server {
		return server
	}, nil)
	httpServer := httptest.NewServer(withAuthHeader(handler, "Bearer test-token"))
	defer httpServer.Close()

	gateway := &MCPFederationGateway{
		client: httpServer.Client(),
	}
	connection := mcpgw.Connection{
		Config: map[string]any{
			"url": httpServer.URL,
			"headers": map[string]any{
				"Authorization": "Bearer test-token",
			},
		},
	}

	tools, err := gateway.ListSSEConnectionTools(context.Background(), connection)
	if err != nil {
		t.Fatalf("list sse tools failed: %v", err)
	}
	if len(tools) != 1 || tools[0].Name != "echo" {
		t.Fatalf("unexpected tool list: %#v", tools)
	}

	payload, err := gateway.CallSSEConnectionTool(context.Background(), connection, "echo", map[string]any{
		"query": "hello-sse",
	})
	if err != nil {
		t.Fatalf("call sse tool failed: %v", err)
	}
	assertEchoResult(t, payload, "hello-sse")
}

func TestResolveSSEEndpointCandidatesCompatibility(t *testing.T) {
	tests := []struct {
		name      string
		config    map[string]any
		contains  string
		firstWant string
	}{
		{
			name:      "prefer explicit sse_url",
			config:    map[string]any{"sse_url": "http://example.com/custom-sse", "url": "http://example.com/sse"},
			firstWant: "http://example.com/custom-sse",
			contains:  "http://example.com/sse",
		},
		{
			name:      "fallback to url as endpoint",
			config:    map[string]any{"url": "http://example.com/sse"},
			firstWant: "http://example.com/sse",
			contains:  "http://example.com/sse",
		},
		{
			name:      "derive endpoint from message url",
			config:    map[string]any{"message_url": "http://example.com/message"},
			firstWant: "http://example.com/sse",
			contains:  "http://example.com/message",
		},
		{
			name:      "derive endpoint from url message suffix",
			config:    map[string]any{"url": "http://example.com/message"},
			firstWant: "http://example.com/message",
			contains:  "http://example.com/sse",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveSSEEndpointCandidates(tt.config)
			if len(got) == 0 {
				t.Fatalf("resolve sse endpoints should not be empty")
			}
			if got[0] != tt.firstWant {
				t.Fatalf("unexpected first endpoint: got=%s want=%s", got[0], tt.firstWant)
			}
			found := false
			for _, item := range got {
				if item == tt.contains {
					found = true
					break
				}
			}
			if !found {
				t.Fatalf("endpoint candidates missing expected value: %s in %#v", tt.contains, got)
			}
		})
	}
}

func assertEchoResult(t *testing.T, payload map[string]any, expected string) {
	t.Helper()
	result, ok := payload["result"].(map[string]any)
	if !ok {
		t.Fatalf("missing result payload: %#v", payload)
	}
	structured, ok := result["structuredContent"].(map[string]any)
	if !ok {
		t.Fatalf("missing structured content: %#v", result)
	}
	if got := anyToString(structured["echo"]); got != expected {
		t.Fatalf("unexpected echo result: got=%s want=%s", got, expected)
	}
}
