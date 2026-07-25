package mcp

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

type gatewayTestProvider struct {
	tools      []ToolDescriptor
	callResult map[string]map[string]any
	callErr    map[string]error
}

func (p *gatewayTestProvider) ListTools(_ context.Context, _ ToolSessionContext) ([]ToolDescriptor, error) {
	return p.tools, nil
}

type sessionAwareGatewayTestProvider struct{}

func (*sessionAwareGatewayTestProvider) ListTools(_ context.Context, session ToolSessionContext) ([]ToolDescriptor, error) {
	if session.IsSubagent {
		return []ToolDescriptor{{Name: "subagent_tool", InputSchema: map[string]any{"type": "object"}}}, nil
	}
	return []ToolDescriptor{{Name: "agent_tool", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (*sessionAwareGatewayTestProvider) CallTool(context.Context, ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return nil, ErrToolNotFound
}

type countingGatewayTestProvider struct {
	calls int
}

func (p *countingGatewayTestProvider) ListTools(_ context.Context, _ ToolSessionContext) ([]ToolDescriptor, error) {
	p.calls++
	return []ToolDescriptor{{Name: "cached_tool", InputSchema: map[string]any{"type": "object"}}}, nil
}

func (*countingGatewayTestProvider) CallTool(context.Context, ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return nil, ErrToolNotFound
}

type mutableGatewayTestProvider struct {
	tools []ToolDescriptor
}

func (p *mutableGatewayTestProvider) ListTools(_ context.Context, _ ToolSessionContext) ([]ToolDescriptor, error) {
	return append([]ToolDescriptor(nil), p.tools...), nil
}

func (*mutableGatewayTestProvider) CallTool(context.Context, ToolSessionContext, string, map[string]any) (map[string]any, error) {
	return nil, ErrToolNotFound
}

func (p *gatewayTestProvider) CallTool(_ context.Context, _ ToolSessionContext, toolName string, _ map[string]any) (map[string]any, error) {
	if err, ok := p.callErr[toolName]; ok {
		return nil, err
	}
	if result, ok := p.callResult[toolName]; ok {
		return result, nil
	}
	return nil, ErrToolNotFound
}

func TestToolGatewayServiceCacheSeparatesSessionID(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:             "bot-1",
		SessionID:         "session-1",
		SessionType:       "acp_agent",
		ChannelIdentityID: "user-1",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.SessionID = "session-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("ListTools calls = %d, want separate cache entries by session ID", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesStreamID(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:       "bot-1",
		SessionID:   "session-1",
		SessionType: "chat",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.StreamID = "stream-1"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after stream change failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("ListTools calls = %d, want separate cache entries by stream ID", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesSessionType(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:       "bot-1",
		SessionID:   "session-1",
		SessionType: "chat",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.SessionType = "schedule"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after session type change failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("ListTools calls = %d, want separate cache entries by session type", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesUserInputCapability(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:       "bot-1",
		SessionID:   "session-1",
		StreamID:    "stream-1",
		SessionType: "chat",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.CanRequestUserInput = true
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after user input capability change failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("ListTools calls = %d, want separate cache entries by user input capability", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesImageCapability(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:       "bot-1",
		SessionID:   "session-1",
		SessionType: "chat",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.SupportsImageInput = true
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after image capability change failed: %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("ListTools calls = %d, want separate cache entries by image capability", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesCurrentConversation(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:             "bot-1",
		SessionType:       "chat",
		ChannelIdentityID: "user-1",
		CurrentPlatform:   "telegram",
		ReplyTarget:       "chat-1",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.ReplyTarget = "chat-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after target change failed: %v", err)
	}
	session.CurrentPlatform = "discord"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after platform change failed: %v", err)
	}
	if provider.calls != 3 {
		t.Fatalf("ListTools calls = %d, want separate cache entries for current conversation fields", provider.calls)
	}
}

func TestToolGatewayServiceCacheSeparatesDiscoveryContext(t *testing.T) {
	provider := &countingGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{
		BotID:        "bot-1",
		ChatID:       "chat-1",
		RuntimeID:    "runtime-1",
		RuntimeToken: "runtime-token-1",
		RouteID:      "route-1",
		SessionToken: "token-1",
	}

	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	session.ChatID = "chat-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after chat change failed: %v", err)
	}
	session.RuntimeID = "runtime-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after runtime change failed: %v", err)
	}
	session.RuntimeToken = "runtime-token-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after runtime token change failed: %v", err)
	}
	session.RouteID = "route-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after route change failed: %v", err)
	}
	session.SessionToken = "token-2"
	if _, err := service.ListTools(context.Background(), session); err != nil {
		t.Fatalf("list tools after session token change failed: %v", err)
	}
	if provider.calls != 6 {
		t.Fatalf("ListTools calls = %d, want separate cache entries for discovery context fields", provider.calls)
	}
	if strings.Contains(toolRegistryCacheKey(session), session.RuntimeToken) {
		t.Fatal("cache key must not contain the raw runtime token")
	}
	if strings.Contains(toolRegistryCacheKey(session), session.SessionToken) {
		t.Fatal("cache key must not contain the raw session token")
	}
}

func TestToolGatewayServiceListTools(t *testing.T) {
	providerA := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "tool_a", InputSchema: map[string]any{"type": "object"}},
			{Name: "dup_tool", InputSchema: map[string]any{"type": "object"}},
		},
	}
	providerB := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "tool_b", InputSchema: map[string]any{"type": "object"}},
			{Name: "dup_tool", InputSchema: map[string]any{"type": "object"}},
		},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{providerA, providerB})

	tools, err := service.ListTools(context.Background(), ToolSessionContext{BotID: "bot-1"})
	if err != nil {
		t.Fatalf("list tools failed: %v", err)
	}
	if len(tools) != 3 {
		t.Fatalf("expected 3 tools after dedupe, got %d", len(tools))
	}
}

func TestToolGatewayServiceLookupTool(t *testing.T) {
	provider := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "lookup_tool", Description: "Lookup", InputSchema: map[string]any{"type": "object"}},
		},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})

	desc, ok, err := service.LookupTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, "lookup_tool")
	if err != nil {
		t.Fatalf("LookupTool error = %v", err)
	}
	if !ok || desc.Name != "lookup_tool" {
		t.Fatalf("LookupTool = (%#v, %v), want lookup_tool", desc, ok)
	}
	if _, ok, err := service.LookupTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, "missing_tool"); err != nil || ok {
		t.Fatalf("LookupTool missing = ok %v err %v, want not found without error", ok, err)
	}
}

func TestToolGatewayServiceLookupToolRefreshesAfterCachedMiss(t *testing.T) {
	provider := &mutableGatewayTestProvider{}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})
	session := ToolSessionContext{BotID: "bot-1"}

	if _, ok, err := service.LookupTool(context.Background(), session, "late_tool"); err != nil || ok {
		t.Fatalf("LookupTool before provider update = ok %v err %v, want cached miss", ok, err)
	}

	provider.tools = []ToolDescriptor{{Name: "late_tool", Description: "Late", InputSchema: map[string]any{"type": "object"}}}
	desc, ok, err := service.LookupTool(context.Background(), session, "late_tool")
	if err != nil {
		t.Fatalf("LookupTool after provider update error = %v", err)
	}
	if !ok || desc.Name != "late_tool" {
		t.Fatalf("LookupTool after provider update = (%#v, %v), want late_tool", desc, ok)
	}
}

func TestToolGatewayServiceCacheSeparatesSessionToolScopes(t *testing.T) {
	service := NewToolGatewayService(slog.Default(), []ToolSource{&sessionAwareGatewayTestProvider{}})

	agentTools, err := service.ListTools(context.Background(), ToolSessionContext{BotID: "bot-1", SessionID: "session-1"})
	if err != nil {
		t.Fatalf("list agent tools failed: %v", err)
	}
	if len(agentTools) != 1 || agentTools[0].Name != "agent_tool" {
		t.Fatalf("agent tools = %#v", agentTools)
	}

	subagentTools, err := service.ListTools(context.Background(), ToolSessionContext{BotID: "bot-1", SessionID: "session-1", IsSubagent: true})
	if err != nil {
		t.Fatalf("list subagent tools failed: %v", err)
	}
	if len(subagentTools) != 1 || subagentTools[0].Name != "subagent_tool" {
		t.Fatalf("subagent tools = %#v", subagentTools)
	}
}

func TestToolGatewayServiceCallToolSuccess(t *testing.T) {
	provider := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "echo_tool", InputSchema: map[string]any{"type": "object"}},
		},
		callResult: map[string]map[string]any{
			"echo_tool": {
				"content": []map[string]any{
					{"type": "text", "text": "ok"},
				},
			},
		},
		callErr: map[string]error{},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})

	result, err := service.CallTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, ToolCallPayload{
		Name:      "echo_tool",
		Arguments: map[string]any{"value": "hello"},
	})
	if err != nil {
		t.Fatalf("call tool should not fail: %v", err)
	}
	if _, ok := result["content"]; !ok {
		t.Fatalf("expected content in tool result")
	}
}

func TestToolGatewayServiceCallToolNotFound(t *testing.T) {
	provider := &gatewayTestProvider{
		tools:      []ToolDescriptor{},
		callResult: map[string]map[string]any{},
		callErr:    map[string]error{},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})

	result, err := service.CallTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, ToolCallPayload{
		Name:      "missing_tool",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call should return mcp error result instead of failing: %v", err)
	}
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Fatalf("expected isError=true for missing tool")
	}
}

func TestToolGatewayServiceCallToolProviderError(t *testing.T) {
	provider := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "broken_tool", InputSchema: map[string]any{"type": "object"}},
		},
		callResult: map[string]map[string]any{},
		callErr: map[string]error{
			"broken_tool": errors.New("boom"),
		},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider})

	result, err := service.CallTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, ToolCallPayload{
		Name:      "broken_tool",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("call should not return hard error: %v", err)
	}
	isErr, _ := result["isError"].(bool)
	if !isErr {
		t.Fatalf("expected isError=true for provider failure")
	}
}

func TestToolGatewayServiceLimitsProviderResult(t *testing.T) {
	provider := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "big_tool", InputSchema: map[string]any{"type": "object"}},
		},
		callResult: map[string]map[string]any{
			"big_tool": BuildToolSuccessResult(map[string]any{
				"content": "HEAD\n" + strings.Repeat("0123456789", 200) + "\nTAIL",
			}),
		},
		callErr: map[string]error{},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider}, WithToolOutputLimit(ToolOutputLimit{MaxBytes: 512, MaxLines: 80}))

	result, err := service.CallTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, ToolCallPayload{
		Name:      "big_tool",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	assertJSONBytesAtMost(t, result, 512)
	if !strings.Contains(toolResultText(result), "[memoh pruned]") {
		t.Fatalf("limited gateway result missing prune marker: %#v", result)
	}
}

func TestToolGatewayServiceLimitsProviderError(t *testing.T) {
	provider := &gatewayTestProvider{
		tools: []ToolDescriptor{
			{Name: "broken_tool", InputSchema: map[string]any{"type": "object"}},
		},
		callResult: map[string]map[string]any{},
		callErr: map[string]error{
			"broken_tool": errors.New("HEAD\n" + strings.Repeat("error detail ", 300) + "\nTAIL"),
		},
	}
	service := NewToolGatewayService(slog.Default(), []ToolSource{provider}, WithToolOutputLimit(ToolOutputLimit{MaxBytes: 512, MaxLines: 80}))

	result, err := service.CallTool(context.Background(), ToolSessionContext{BotID: "bot-1"}, ToolCallPayload{
		Name:      "broken_tool",
		Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	assertJSONBytesAtMost(t, result, 512)
	if isErr, _ := result["isError"].(bool); !isErr {
		t.Fatalf("limited provider error lost isError: %#v", result)
	}
	if !strings.Contains(toolResultText(result), "[memoh pruned]") {
		t.Fatalf("limited provider error missing prune marker: %#v", result)
	}
}
