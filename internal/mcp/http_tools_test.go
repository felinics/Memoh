package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/memoh/internal/runtimefence"
)

type fenceCapturingToolSource struct {
	fence runtimefence.Fence
	calls int
}

func (*fenceCapturingToolSource) ListTools(context.Context, ToolSessionContext) ([]ToolDescriptor, error) {
	return []ToolDescriptor{{Name: "fenced_tool", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (s *fenceCapturingToolSource) CallTool(ctx context.Context, _ ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	s.calls++
	s.fence, _ = runtimefence.FromContext(ctx)
	return BuildToolSuccessResult(map[string]any{"ok": true}), nil
}

func TestToolGatewayMiddlewareChecksRuntimeGuardBeforeToolEffect(t *testing.T) {
	guardErr := errors.New("runtime ownership lost")
	source := &fenceCapturingToolSource{}
	service := NewToolGatewayService(nil, []ToolSource{source})
	middleware := ToolGatewayMiddleware(service, nil, ToolSessionContext{
		BotID: "bot-1", RuntimeID: "rt-guarded", RuntimeActive: true,
		RuntimeGuard: func(context.Context) error { return guardErr },
	})(nil)

	if _, err := middleware(context.Background(), "tools/call", callToolRequest("fenced_tool")); !errors.Is(err, guardErr) {
		t.Fatalf("tools/call error = %v, want runtime guard error", err)
	}
	if source.calls != 0 {
		t.Fatalf("tool source calls = %d, want zero after guard rejection", source.calls)
	}
}

func TestToolGatewayMiddlewareStopsWhenOwningRunIsCanceled(t *testing.T) {
	runCtx, cancelRun := context.WithCancel(context.Background())
	cancelRun()
	source := &fenceCapturingToolSource{}
	service := NewToolGatewayService(nil, []ToolSource{source})
	middleware := ToolGatewayMiddleware(service, nil, ToolSessionContext{
		BotID: "bot-1", RuntimeID: "rt-canceled", RuntimeActive: true, RunContext: runCtx,
	})(nil)

	if _, err := middleware(context.Background(), "tools/call", callToolRequest("fenced_tool")); !errors.Is(err, context.Canceled) {
		t.Fatalf("tools/call error = %v, want context.Canceled", err)
	}
	if source.calls != 0 {
		t.Fatalf("tool source calls = %d, want zero for canceled run", source.calls)
	}
}

func TestToolGatewayMiddlewareScopesRuntimeToolCallsToActivePrompts(t *testing.T) {
	provider := &gatewayTestProvider{
		tools:      []ToolDescriptor{{Name: "echo_tool", InputSchema: map[string]any{"type": "object"}}},
		callResult: map[string]map[string]any{"echo_tool": BuildToolSuccessResult(map[string]any{"ok": true})},
		callErr:    map[string]error{},
	}
	service := NewToolGatewayService(nil, []ToolSource{provider})
	idle := ToolGatewayMiddleware(service, nil, ToolSessionContext{BotID: "bot-1", RuntimeID: "rt_idle"})(nil)

	result, err := idle(context.Background(), "tools/list", &sdkmcp.ServerRequest[*sdkmcp.ListToolsParams]{Params: &sdkmcp.ListToolsParams{}})
	if err != nil {
		t.Fatalf("idle runtime tools/list should succeed: %v", err)
	}
	list, ok := result.(*sdkmcp.ListToolsResult)
	if !ok || len(list.Tools) != 1 || list.Tools[0].Name != "echo_tool" {
		t.Fatalf("tools/list result = %#v", result)
	}

	if _, err := idle(context.Background(), "tools/call", callToolRequest("echo_tool")); err == nil || !strings.Contains(err.Error(), "not processing a prompt") {
		t.Fatalf("idle runtime tools/call error = %v", err)
	}

	active := ToolGatewayMiddleware(service, nil, ToolSessionContext{BotID: "bot-1", RuntimeID: "rt_active", RuntimeActive: true})(nil)
	result, err = active(context.Background(), "tools/call", callToolRequest("echo_tool"))
	if err != nil {
		t.Fatalf("active runtime tools/call should succeed: %v", err)
	}
	if _, ok := result.(*sdkmcp.CallToolResult); !ok {
		t.Fatalf("tools/call result = %#v", result)
	}
}

func TestToolGatewayMiddlewareRestoresRuntimeFenceForToolCall(t *testing.T) {
	want := runtimefence.Fence{BotID: "bot-1", SessionID: "session-1", Token: 19}
	source := &fenceCapturingToolSource{}
	service := NewToolGatewayService(nil, []ToolSource{source})
	middleware := ToolGatewayMiddleware(service, nil, ToolSessionContext{
		BotID:         want.BotID,
		SessionID:     want.SessionID,
		RuntimeID:     "rt-fenced",
		RuntimeActive: true,
		RuntimeFence:  want,
	})(nil)

	if _, err := middleware(context.Background(), "tools/call", callToolRequest("fenced_tool")); err != nil {
		t.Fatalf("tools/call error = %v", err)
	}
	if source.fence != want {
		t.Fatalf("tool call fence = %#v, want %#v", source.fence, want)
	}
}

func TestToolSessionContextFromHTTPParsesSupportsImageInput(t *testing.T) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "http://example.test/tools", nil)
	if err != nil {
		t.Fatalf("NewRequest error = %v", err)
	}
	req.Header.Set(ToolHeaderSupportsImageInput, "true")

	session := ToolSessionContextFromHTTP(req, "bot-1")
	if !session.SupportsImageInput {
		t.Fatalf("SupportsImageInput = false, want true")
	}
}

// Driving the middleware directly cannot see this: the SDK decides what reaches
// the wire after the middleware returns, so only a real HTTP exchange shows
// whether a sessionless client gets the resultType it requires.
func TestServeToolMCPHTTPResultTypeFollowsProtocolRevision(t *testing.T) {
	provider := &gatewayTestProvider{
		tools:      []ToolDescriptor{{Name: "echo_tool", InputSchema: map[string]any{"type": "object"}}},
		callResult: map[string]map[string]any{"echo_tool": BuildToolSuccessResult(map[string]any{"ok": true})},
		callErr:    map[string]error{},
	}
	service := NewToolGatewayService(nil, []ToolSource{provider})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		ServeToolMCPHTTP(w, req, slog.New(slog.DiscardHandler), service, nil, ToolSessionContext{BotID: "bot-1"})
	}))
	defer server.Close()

	callTool := func(t *testing.T, params string, headers map[string]string) map[string]any {
		t.Helper()
		body := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":` + params + `}`
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, server.URL, strings.NewReader(body))
		if err != nil {
			t.Fatalf("NewRequest error = %v", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		for name, value := range headers {
			req.Header.Set(name, value)
		}
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: test-only URL from the local httptest server
		if err != nil {
			t.Fatalf("tools/call request error = %v", err)
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatalf("read response error = %v", err)
		}
		var envelope struct {
			Result map[string]any `json:"result"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			t.Fatalf("decode response %s: %v", raw, err)
		}
		if resp.StatusCode != http.StatusOK || envelope.Result == nil {
			t.Fatalf("tools/call status = %d, body = %s", resp.StatusCode, raw)
		}
		if content, _ := envelope.Result["content"].([]any); len(content) == 0 {
			t.Fatalf("tools/call result lost its content: %s", raw)
		}
		return envelope.Result
	}

	t.Run("sessionless revision carries resultType", func(t *testing.T) {
		result := callTool(t,
			`{"name":"echo_tool","arguments":{},"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}}`,
			map[string]string{"Mcp-Protocol-Version": "2026-07-28", "Mcp-Method": "tools/call", "Mcp-Name": "echo_tool"},
		)
		if result["resultType"] != "complete" {
			t.Fatalf("resultType = %#v, want \"complete\"", result["resultType"])
		}
	})

	t.Run("handshake revision stays unchanged", func(t *testing.T) {
		result := callTool(t, `{"name":"echo_tool","arguments":{}}`, map[string]string{"Mcp-Protocol-Version": "2025-11-25"})
		if value, ok := result["resultType"]; ok {
			t.Fatalf("resultType = %#v, want it absent", value)
		}
	})
}

func TestConvertGatewayCallResultToSDKKeepsUpstreamResultType(t *testing.T) {
	upstream := BuildToolSuccessResult(map[string]any{"ok": true})
	upstream["resultType"] = "input_required"

	converted, err := ConvertGatewayCallResultToSDK(upstream, true)
	if err != nil {
		t.Fatalf("convert error = %v", err)
	}
	if !converted.NeedsInput() {
		t.Fatal("federated resultType was overwritten, want it preserved")
	}
}

func callToolRequest(name string) *sdkmcp.ServerRequest[*sdkmcp.CallToolParamsRaw] {
	return &sdkmcp.ServerRequest[*sdkmcp.CallToolParamsRaw]{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      name,
			Arguments: json.RawMessage(`{}`),
		},
	}
}

// A workspace tool-gateway mount outlives the turns it serves. Between turns
// its session carries no RunID; RequireActiveRun keeps that idle window to
// tools/list — a call with no owning run is refused before any tool effect.
func TestToolGatewayMiddlewareRefusesIdleMountCalls(t *testing.T) {
	source := &fenceCapturingToolSource{}
	service := NewToolGatewayService(nil, []ToolSource{source})
	idle := ToolGatewayMiddleware(service, nil, ToolSessionContext{
		BotID: "bot-1", RequireActiveRun: true,
	})(nil)
	if _, err := idle(context.Background(), "tools/call", callToolRequest("fenced_tool")); err == nil {
		t.Fatal("idle mount tools/call succeeded, want refusal")
	}
	if source.calls != 0 {
		t.Fatalf("tool source calls = %d, want zero for idle mount", source.calls)
	}

	active := ToolGatewayMiddleware(service, nil, ToolSessionContext{
		BotID: "bot-1", RunID: "run-1", RequireActiveRun: true,
	})(nil)
	if _, err := active(context.Background(), "tools/call", callToolRequest("fenced_tool")); err != nil {
		t.Fatalf("active turn tools/call error = %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("tool source calls = %d, want one for the active turn", source.calls)
	}
}
