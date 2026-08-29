package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/felinics/memoh/internal/apperror"
	mcpgw "github.com/felinics/memoh/internal/mcp"
)

func TestBuildToolCallPayloadFromRaw(t *testing.T) {
	params := &sdkmcp.CallToolParamsRaw{
		Name:      " tool_a ",
		Arguments: json.RawMessage(`{"x":1}`),
	}
	payload, err := buildToolCallPayloadFromRaw(params)
	if err != nil {
		t.Fatalf("valid payload should parse: %v", err)
	}
	if payload.Name != "tool_a" {
		t.Fatalf("unexpected tool name: %s", payload.Name)
	}
	if _, ok := payload.Arguments["x"]; !ok {
		t.Fatalf("expected argument x")
	}

	invalid := &sdkmcp.CallToolParamsRaw{
		Name:      "",
		Arguments: json.RawMessage(`{}`),
	}
	if _, err := buildToolCallPayloadFromRaw(invalid); err == nil {
		t.Fatalf("empty tool name should fail")
	}
}

func TestHandleMCPToolsWithoutGateway(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetPath("/bots/:bot_id/tools")
	c.SetParamNames("bot_id")
	c.SetParamValues("bot-1")

	handler := &ContainerdHandler{}
	err := handler.HandleMCPTools(c)
	if err == nil {
		t.Fatalf("expected service unavailable error")
	}
	httpErr := &echo.HTTPError{}
	ok := errors.As(err, &httpErr)
	if !ok {
		t.Fatalf("expected echo HTTP error, got %T", err)
	}
	if httpErr.Code != http.StatusServiceUnavailable {
		t.Fatalf("unexpected status code: %d", httpErr.Code)
	}
}

type mcpToolsTestExecutor struct {
	lastSession mcpgw.ToolSessionContext
}

func (e *mcpToolsTestExecutor) ListTools(_ context.Context, session mcpgw.ToolSessionContext) ([]mcpgw.ToolDescriptor, error) {
	e.lastSession = session
	return []mcpgw.ToolDescriptor{
		{
			Name:        "echo_tool",
			Description: "echo input",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string"},
				},
			},
		},
	}, nil
}

func (e *mcpToolsTestExecutor) CallTool(_ context.Context, session mcpgw.ToolSessionContext, toolName string, arguments map[string]any) (map[string]any, error) {
	e.lastSession = session
	if strings.TrimSpace(toolName) != "echo_tool" {
		return nil, mcpgw.ErrToolNotFound
	}
	return mcpgw.BuildToolSuccessResult(map[string]any{
		"ok":                  true,
		"echo":                mcpgw.StringArg(arguments, "input"),
		"chat_id":             session.ChatID,
		"channel_identity_id": session.ChannelIdentityID,
	}), nil
}

type mcpToolsRuntimeResolver struct {
	session mcpgw.ToolSessionContext
	ok      bool
}

func (r mcpToolsRuntimeResolver) ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcpgw.ToolSessionContext, bool) {
	if botID != r.session.BotID || runtimeID != r.session.RuntimeID || toolToken != r.session.RuntimeToken {
		return mcpgw.ToolSessionContext{}, false
	}
	return r.session, r.ok
}

type mismatchedMCPToolsRuntimeResolver struct{}

func (mismatchedMCPToolsRuntimeResolver) ResolveRuntimeToolContext(_, _, _ string) (mcpgw.ToolSessionContext, bool) {
	return mcpgw.ToolSessionContext{BotID: "other-bot", RuntimeID: "other-runtime"}, true
}

func TestHandleMCPToolsWithGatewayAcceptCompatibility(t *testing.T) {
	e := echo.New()
	executor := &mcpToolsTestExecutor{}
	toolGateway := mcpgw.NewToolGatewayService(slog.Default(), []mcpgw.ToolSource{executor})
	handler := &ContainerdHandler{
		logger:      slog.Default(),
		toolGateway: toolGateway,
	}

	listReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
	listReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	listReq.Header.Set("Accept", "application/json")
	listReq.Header.Set("X-Memoh-Channel-Identity-Id", "user-1")
	listRec := httptest.NewRecorder()
	listCtx := e.NewContext(listReq, listRec)

	if err := handler.handleMCPToolsWithBotID(listCtx, "bot-1"); err != nil {
		t.Fatalf("list tools should succeed: %v", err)
	}
	if listRec.Code != http.StatusOK {
		t.Fatalf("unexpected list status: %d body=%s", listRec.Code, listRec.Body.String())
	}
	if !strings.Contains(strings.ToLower(listReq.Header.Get("Accept")), "text/event-stream") {
		t.Fatalf("accept header should include text/event-stream: %s", listReq.Header.Get("Accept"))
	}

	var listPayload map[string]any
	if err := json.Unmarshal(listRec.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("decode list payload failed: %v", err)
	}
	result, _ := listPayload["result"].(map[string]any)
	if cacheScope, _ := result["cacheScope"].(string); cacheScope != "private" {
		t.Fatalf("tools/list cacheScope = %#v, want private", result["cacheScope"])
	}
	if ttlMs, ok := result["ttlMs"].(float64); !ok || ttlMs != 0 {
		t.Fatalf("tools/list ttlMs = %#v, want 0", result["ttlMs"])
	}
	tools, _ := result["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("expected one tool, got: %#v", result["tools"])
	}

	callReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"echo_tool","arguments":{"input":"hello"}}}`))
	callReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	callReq.Header.Set("Accept", "application/json")
	callReq.Header.Set("X-Memoh-Channel-Identity-Id", "user-1")
	callRec := httptest.NewRecorder()
	callCtx := e.NewContext(callReq, callRec)

	if err := handler.handleMCPToolsWithBotID(callCtx, "bot-1"); err != nil {
		t.Fatalf("call tool should succeed: %v", err)
	}
	if callRec.Code != http.StatusOK {
		t.Fatalf("unexpected call status: %d body=%s", callRec.Code, callRec.Body.String())
	}

	var callPayload map[string]any
	if err := json.Unmarshal(callRec.Body.Bytes(), &callPayload); err != nil {
		t.Fatalf("decode call payload failed: %v", err)
	}
	callResult, _ := callPayload["result"].(map[string]any)
	structured, _ := callResult["structuredContent"].(map[string]any)
	if echoValue := strings.TrimSpace(mcpgw.StringArg(structured, "echo")); echoValue != "hello" {
		t.Fatalf("unexpected echo value: %#v", structured["echo"])
	}
	if strings.TrimSpace(mcpgw.StringArg(structured, "chat_id")) != "bot-1" {
		t.Fatalf("unexpected chat id: %#v", structured["chat_id"])
	}
	if strings.TrimSpace(mcpgw.StringArg(structured, "channel_identity_id")) != "" {
		t.Fatalf("public header should not set channel identity id: %#v", structured["channel_identity_id"])
	}
}

func TestHandleMCPToolsRuntimeIDUsesTrustedRuntimeContext(t *testing.T) {
	e := echo.New()
	executor := &mcpToolsTestExecutor{}
	trusted := mcpgw.ToolSessionContext{
		BotID:              "bot-1",
		ChatID:             "trusted-chat",
		RuntimeID:          "runtime-1",
		RuntimeToken:       "runtime-token-1",
		SessionID:          "trusted-session",
		RunID:              "trusted-run",
		ChannelIdentityID:  "trusted-user",
		SupportsImageInput: true,
		RuntimeActive:      true,
	}
	handler := &ContainerdHandler{
		logger:      slog.Default(),
		toolGateway: mcpgw.NewToolGatewayService(slog.Default(), []mcpgw.ToolSource{executor}),
		acpRuntimes: mcpToolsRuntimeResolver{session: trusted, ok: true},
	}

	callReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"echo_tool","arguments":{"input":"hello"}}}`))
	callReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	callReq.Header.Set("Accept", "application/json")
	callReq.Header.Set(mcpgw.ToolHeaderRuntimeID, "runtime-1")
	callReq.Header.Set(mcpgw.ToolHeaderRuntimeToken, "runtime-token-1")
	callReq.Header.Set(headerChatID, "untrusted-chat")
	callReq.Header.Set(headerSessionID, "untrusted-session")
	callReq.Header.Set(mcpgw.ToolHeaderSupportsImageInput, "false")
	callRec := httptest.NewRecorder()
	callCtx := e.NewContext(callReq, callRec)

	if err := handler.handleMCPToolsWithBotID(callCtx, "bot-1"); err != nil {
		t.Fatalf("call tool should succeed: %v", err)
	}
	if callRec.Code != http.StatusOK {
		t.Fatalf("unexpected call status: %d body=%s", callRec.Code, callRec.Body.String())
	}
	if executor.lastSession.ChatID != trusted.ChatID ||
		executor.lastSession.SessionID != trusted.SessionID ||
		executor.lastSession.RunID != trusted.RunID ||
		executor.lastSession.ChannelIdentityID != trusted.ChannelIdentityID ||
		!executor.lastSession.SupportsImageInput ||
		!executor.lastSession.RuntimeActive {
		t.Fatalf("runtime request did not use trusted resolver context: %#v", executor.lastSession)
	}
}

func TestHandleMCPToolsRuntimeIDRequiresRuntimeToolToken(t *testing.T) {
	e := echo.New()
	executor := &mcpToolsTestExecutor{}
	trusted := mcpgw.ToolSessionContext{
		BotID:         "bot-1",
		RuntimeID:     "runtime-1",
		RuntimeToken:  "runtime-token-1",
		RuntimeActive: true,
	}
	handler := &ContainerdHandler{
		logger:      slog.Default(),
		toolGateway: mcpgw.NewToolGatewayService(slog.Default(), []mcpgw.ToolSource{executor}),
		acpRuntimes: mcpToolsRuntimeResolver{session: trusted, ok: true},
	}

	callReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"2","method":"tools/list"}`))
	callReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	callReq.Header.Set("Accept", "application/json")
	callReq.Header.Set(mcpgw.ToolHeaderRuntimeID, "runtime-1")
	callRec := httptest.NewRecorder()
	callCtx := e.NewContext(callReq, callRec)

	err := handler.handleMCPToolsWithBotID(callCtx, "bot-1")
	if err == nil {
		t.Fatal("runtime tool request without token should fail")
	}
	if got := apperror.CodeOf(err); got != apperror.CodeACPRuntimeNotFound {
		t.Fatalf("runtime tool request without token error = %v, code = %q", err, got)
	}
}

func TestHandleMCPToolsRuntimeCredentialFailsBeforeGatewayAvailability(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", nil)
	req.Header.Set(mcpgw.ToolHeaderRuntimeID, "runtime-1")
	req.Header.Set(mcpgw.ToolHeaderRuntimeToken, "wrong-token")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("bot_id")
	c.SetParamValues("bot-1")

	err := (&ContainerdHandler{}).HandleMCPTools(c)
	if got := apperror.CodeOf(err); got != apperror.CodeACPRuntimeNotFound {
		t.Fatalf("invalid runtime credential error = %v, code = %q", err, got)
	}
}

func TestHandleMCPToolsRejectsMismatchedResolvedRuntimeIdentity(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", nil)
	req.Header.Set(mcpgw.ToolHeaderRuntimeID, "runtime-1")
	req.Header.Set(mcpgw.ToolHeaderRuntimeToken, "runtime-token-1")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)
	c.SetParamNames("bot_id")
	c.SetParamValues("bot-1")

	handler := &ContainerdHandler{acpRuntimes: mismatchedMCPToolsRuntimeResolver{}}
	err := handler.HandleMCPTools(c)
	if got := apperror.CodeOf(err); got != apperror.CodeACPRuntimeNotFound {
		t.Fatalf("mismatched runtime identity error = %v, code = %q", err, got)
	}
}

func TestHandleMCPToolsCallRoutesPublicEventHeadersWithoutTrustingIdentity(t *testing.T) {
	e := echo.New()
	executor := &mcpToolsTestExecutor{}
	toolGateway := mcpgw.NewToolGatewayService(slog.Default(), []mcpgw.ToolSource{executor})
	toolContexts := mcpgw.NewToolSessionContextStore()
	var delivered []mcpgw.ToolStreamEvent
	unregister := toolContexts.RegisterToolEventSink(mcpgw.ToolSessionContext{
		BotID:     "bot-1",
		SessionID: "session-1",
		RunID:     "run-1",
	}, func(event mcpgw.ToolStreamEvent) {
		delivered = append(delivered, event)
	})
	defer unregister()
	handler := &ContainerdHandler{
		logger:       slog.Default(),
		toolGateway:  toolGateway,
		toolContexts: toolContexts,
	}

	callReq := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/bots/bot-1/tools", strings.NewReader(`{"jsonrpc":"2.0","id":"2","method":"tools/call","params":{"name":"echo_tool","arguments":{"input":"hello"}}}`))
	callReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	callReq.Header.Set("Accept", "application/json")
	callReq.Header.Set(headerBotID, "bot-1")
	callReq.Header.Set(headerChatID, "chat-1")
	callReq.Header.Set(headerSessionID, "session-1")
	callReq.Header.Set(headerRunID, "run-1")
	callReq.Header.Set(headerSessionType, "acp_agent")
	callReq.Header.Set(headerChannelIdentityID, "user-1")
	callRec := httptest.NewRecorder()
	callCtx := e.NewContext(callReq, callRec)

	if err := handler.handleMCPToolsWithBotID(callCtx, "bot-1"); err != nil {
		t.Fatalf("call tool should succeed: %v", err)
	}
	if callRec.Code != http.StatusOK {
		t.Fatalf("unexpected call status: %d body=%s", callRec.Code, callRec.Body.String())
	}

	if len(delivered) != 2 {
		t.Fatalf("delivered events = %#v, want start/end", delivered)
	}
	if delivered[0].Type != "tool_call_start" || delivered[1].Type != "tool_call_end" {
		t.Fatalf("unexpected delivered events: %#v", delivered)
	}
	if executor.lastSession.ChannelIdentityID != "" || executor.lastSession.SessionToken != "" {
		t.Fatalf("public identity/credential headers were trusted: %#v", executor.lastSession)
	}
}

func TestBuildToolSessionContextPreservesRoutingHeadersAndIgnoresIdentityHeaders(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", nil)
	req.Header.Set(headerBotID, "spoofed-bot")
	req.Header.Set(headerChatID, "chat-1")
	req.Header.Set(mcpgw.ToolHeaderRuntimeToken, "runtime-token-1")
	req.Header.Set(headerSessionID, "session-1")
	req.Header.Set(headerRunID, "run-1")
	req.Header.Set(headerSessionType, "acp_agent")
	req.Header.Set(headerRouteID, "route-1")
	req.Header.Set(headerChannelIdentityID, "user-1")
	req.Header.Set(headerSessionToken, "token-1")
	req.Header.Set(headerCurrentPlatform, "web")
	req.Header.Set(headerReplyTarget, "reply-1")
	req.Header.Set(headerConversationType, "private")
	req.Header.Set(headerIsSubagent, "true")
	req.Header.Set(mcpgw.ToolHeaderSupportsImageInput, "true")
	rec := httptest.NewRecorder()
	c := e.NewContext(req, rec)

	session := (&ContainerdHandler{}).buildToolSessionContext(c, "fallback-bot")
	if session.BotID != "fallback-bot" || session.ChatID != "chat-1" {
		t.Fatalf("unexpected base ids: %#v", session)
	}
	if session.SessionID != "session-1" ||
		session.RunID != "run-1" ||
		session.SessionType != "acp_agent" ||
		session.RouteID != "route-1" ||
		session.CurrentPlatform != "web" ||
		session.ReplyTarget != "reply-1" ||
		session.ConversationType != "private" ||
		!session.IsSubagent {
		t.Fatalf("routing headers were not preserved: %#v", session)
	}
	if session.RuntimeToken != "" || session.ChannelIdentityID != "" || session.SessionToken != "" {
		t.Fatalf("public identity/credential headers should be ignored: %#v", session)
	}
	if session.SupportsImageInput {
		t.Fatalf("public endpoint should not trust image capability header: %#v", session)
	}
	if session.CanRequestUserInput {
		t.Fatalf("public endpoint should not trust stream id as user-input capability: %#v", session)
	}
}

func TestBuildToolSessionContextUsesAuthenticatedIdentity(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", nil)
	req.Header.Set(headerChannelIdentityID, "spoofed-user")
	rec := httptest.NewRecorder()
	c := testAuthContext(e, req, rec, "user-1")

	session := (&ContainerdHandler{}).buildToolSessionContext(c, "bot-1")
	if session.ChannelIdentityID != "user-1" {
		t.Fatalf("channel identity = %q, want authenticated user", session.ChannelIdentityID)
	}
}
