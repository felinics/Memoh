package chatgptplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/openaicatalog"
)

type tokenSourceFunc func(context.Context) (string, error)

func (f tokenSourceFunc) AccessToken(ctx context.Context) (string, error) { return f(ctx) }
func (tokenSourceFunc) RejectAccessToken(context.Context, string) error   { return nil }

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func streamResponse(events string) *http.Response {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(events))}
}

func TestGenerateUsesPlanWireAndRequiresCompletion(t *testing.T) {
	for _, test := range []struct {
		name, terminal string
		want           error
	}{
		{"completed", `{"type":"response.completed","response":{"id":"r","model":"test","usage":{"input_tokens":3,"output_tokens":1}}}`, nil},
		{"quota after text", `{"type":"response.failed","response":{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"private upstream text"}}}`, ErrQuota},
		{"incomplete", `{"type":"response.incomplete","response":{"incomplete_details":{"reason":"max_output_tokens"}}}`, ErrInterrupted},
		{"missing terminal", "", ErrInterrupted},
	} {
		t.Run(test.name, func(t *testing.T) {
			refreshed := 0
			p := NewProvider("stale", tokenSourceFunc(func(context.Context) (string, error) { refreshed++; return "current", nil }), &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.String() != APIBaseURL+"/responses" || r.Header.Get("Authorization") != "Bearer current" {
					t.Fatalf("wrong endpoint or credential")
				}
				var wire map[string]any
				if err := json.NewDecoder(r.Body).Decode(&wire); err != nil {
					t.Fatal(err)
				}
				if wire["stream"] != true || wire["store"] != false {
					t.Fatalf("wrong wire flags: %v", wire)
				}
				if _, ok := wire["max_output_tokens"]; ok {
					t.Fatal("unsupported token limit sent")
				}
				if _, ok := wire["temperature"]; ok {
					t.Fatal("unsupported sampling sent")
				}
				data := "data: {\"type\":\"response.created\",\"response\":{\"id\":\"r\",\"model\":\"test\"}}\n\n" +
					"data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"OK\"}\n\n"
				if test.terminal != "" {
					data += "data: " + test.terminal + "\n\n"
				}
				return streamResponse(data), nil
			})})
			temperature := 0.3
			maxTokens := 20
			result, err := p.DoGenerate(context.Background(), sdk.Request{Model: "test", System: "Help", Messages: []sdk.Message{sdk.UserMessage("Hello")}, MaxTokens: &maxTokens, Temperature: &temperature})
			if test.want == nil {
				if err != nil || result.Text != "OK" {
					t.Fatalf("result=%+v err=%v", result, err)
				}
			} else if !errors.Is(err, test.want) {
				t.Fatalf("err=%v, want %v", err, test.want)
			}
			if refreshed != 1 {
				t.Fatalf("credential resolved %d times", refreshed)
			}
		})
	}
}

func TestNamespaceReplayAndDeveloperMessages(t *testing.T) {
	input := map[string]any{"input": []any{map[string]any{"role": "system", "content": "follow up"}, map[string]any{"type": "function_call", "name": "exec", "call_id": "call-1", "arguments": "{}"}, map[string]any{"type": "function_call_output", "call_id": "call-1", "output": "done"}}, "tools": []any{map[string]any{"type": "function", "name": "exec", "parameters": map[string]any{"type": "object"}}}, "tool_choice": map[string]any{"type": "function", "name": "exec"}, "previous_response_id": "stored", "temperature": 1}
	adaptRequest(input)
	items := input["input"].([]any)
	if items[0].(map[string]any)["role"] != "developer" || items[1].(map[string]any)["namespace"] != "memoh" {
		t.Fatalf("invalid replay: %v", items)
	}
	if _, ok := items[2].(map[string]any)["namespace"]; ok {
		t.Fatal("tool result carries namespace")
	}
	tools := input["tools"].([]any)
	if tools[0].(map[string]any)["type"] != "namespace" || input["tool_choice"].(map[string]any)["namespace"] != "memoh" {
		t.Fatalf("invalid tools: %v", input)
	}
	if _, ok := input["previous_response_id"]; ok {
		t.Fatal("stored response reused")
	}
}

func TestCatalogUsesAccountVisibilityAndOrder(t *testing.T) {
	p := NewProvider("token", nil, &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/models" || r.URL.Host != "api.openai.com" || r.URL.Query().Get("client_version") != openaicatalog.ClientVersion || r.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("catalog did not use the documented endpoint and credential")
		}
		return streamResponse(`{"models":[{"slug":"z","display_name":"Last alphabetically","visibility":"list","context_window":272000,"input_modalities":["text","image"]},{"slug":"secret","visibility":"hide"},{"slug":"a","display_name":"First alphabetically","visibility":"list"}]}`), nil
	})})
	catalog, err := p.ListModels(context.Background())
	if err != nil || len(catalog) != 2 || catalog[0].ID != "z" || catalog[1].ID != "a" {
		t.Fatalf("catalog=%v err=%v", catalog, err)
	}
	rich, err := p.ModelCatalog(context.Background())
	if err != nil || len(rich) != 2 || rich[0].ContextWindow == nil || *rich[0].ContextWindow != 272000 || len(rich[0].InputModalities) != 2 {
		t.Fatalf("catalog metadata = %+v, err = %v", rich, err)
	}
}

func TestCredentialsNeverFollowConfigurableRoutes(t *testing.T) {
	called := false
	transport := &transport{token: "secret", base: roundTripFunc(func(*http.Request) (*http.Response, error) { called = true; return nil, nil })}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.com/v1/responses", bytes.NewBufferString(`{}`))
	resp, err := transport.RoundTrip(req)
	if resp != nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, ErrInvalidAuthorization) || called {
		t.Fatalf("credential escaped: %v", err)
	}
}

func TestHTTPFailuresKeepIdentityAndHideDiagnostics(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
		want   error
	}{
		{"quota code overrides status", http.StatusBadRequest, `{"error":{"code":"subscription_sharing_usage_limit_exceeded","message":"SECRET diagnostic"}}`, ErrQuota},
		{"authentication", http.StatusUnauthorized, `{"error":{"code":"invalid_token","message":"SECRET diagnostic"}}`, ErrNotConnected},
		{"unstructured upstream failure", http.StatusServiceUnavailable, "SECRET diagnostic", ErrUpstream},
	} {
		t.Run(test.name, func(t *testing.T) {
			p := NewProvider("token", nil, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: test.status, Body: io.NopCloser(strings.NewReader(test.body))}, nil
			})})
			for _, call := range []struct {
				name string
				run  func() error
			}{
				{"models", func() error { _, err := p.ListModels(context.Background()); return err }},
				{"inference", func() error {
					_, err := p.DoGenerate(context.Background(), sdk.Request{Model: "test", Messages: []sdk.Message{sdk.UserMessage("Hello")}})
					return err
				}},
			} {
				t.Run(call.name, func(t *testing.T) {
					err := call.run()
					if !errors.Is(err, test.want) || strings.Contains(err.Error(), "SECRET") {
						t.Fatalf("unexpected public failure: %v", err)
					}
				})
			}
		})
	}
}
