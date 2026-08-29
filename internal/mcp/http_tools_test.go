package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

func callToolRequest(name string) *sdkmcp.ServerRequest[*sdkmcp.CallToolParamsRaw] {
	return &sdkmcp.ServerRequest[*sdkmcp.CallToolParamsRaw]{
		Params: &sdkmcp.CallToolParamsRaw{
			Name:      name,
			Arguments: json.RawMessage(`{}`),
		},
	}
}
