package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/apperror"
	"github.com/felinics/memoh/internal/config"
	apphandlers "github.com/felinics/memoh/internal/handlers"
	mcpgw "github.com/felinics/memoh/internal/mcp"
)

func TestShouldSkipJWT_ChannelWebhookPaths(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{path: "/channels/feishu/webhook/cfg-1", want: true},
		{path: "/channels/wechatoa/webhook/cfg-1", want: true},
		{path: "/channels/line/webhook/cfg-1", want: true},
		{path: "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg", want: true},
		{path: "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/metadata", want: false},
		{path: "/channels/telegram/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg", want: true},
		{path: "/channels/feishu/webhook", want: false},
		{path: "/api/channels/feishu/webhook", want: false},
		{path: "/webhook-tunnel/status", want: false},
	}

	for _, tc := range cases {
		got := shouldSkipJWT(tc.path)
		if got != tc.want {
			t.Fatalf("path=%q want=%v got=%v", tc.path, tc.want, got)
		}
	}
}

func TestShouldLimitPublicRequestBody(t *testing.T) {
	t.Parallel()

	cases := []struct {
		path string
		want bool
	}{
		{path: "/channels/line/webhook/cfg-1", want: true},
		{path: "/channels/telegram/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg", want: false},
		{path: "/api/fs/upload", want: false},
		{path: "/api/bots/backup/import", want: false},
	}

	for _, tc := range cases {
		got := shouldLimitPublicRequestBody(tc.path)
		if got != tc.want {
			t.Fatalf("path=%q want=%v got=%v", tc.path, tc.want, got)
		}
	}
}

func TestShouldLimitRuntimeToolsRequestBody(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", nil)
	req.Header.Set(mcpgw.ToolHeaderRuntimeID, "runtime-1")
	req.Header.Set(mcpgw.ToolHeaderRuntimeToken, "token-1")
	if !shouldLimitRequestBody(req) {
		t.Fatal("complete runtime tool credential must enable the public request body limit")
	}

	req.Header.Del(mcpgw.ToolHeaderRuntimeToken)
	if shouldLimitRequestBody(req) {
		t.Fatal("ordinary authenticated tools request must preserve its existing body-limit behavior")
	}
}

func TestShouldSkipJWTRequestForExactRuntimeToolsCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		method string
		path   string
		id     string
		token  string
		want   bool
	}{
		{name: "exact", method: http.MethodPost, path: "/bots/bot-1/tools", id: "runtime-1", token: "token-1", want: true},
		{name: "missing token", method: http.MethodPost, path: "/bots/bot-1/tools", id: "runtime-1"},
		{name: "missing runtime", method: http.MethodPost, path: "/bots/bot-1/tools", token: "token-1"},
		{name: "get", method: http.MethodGet, path: "/bots/bot-1/tools", id: "runtime-1", token: "token-1"},
		{name: "trailing slash", method: http.MethodPost, path: "/bots/bot-1/tools/", id: "runtime-1", token: "token-1"},
		{name: "nested", method: http.MethodPost, path: "/api/bots/bot-1/tools", id: "runtime-1", token: "token-1"},
		{name: "empty bot", method: http.MethodPost, path: "/bots//tools", id: "runtime-1", token: "token-1"},
		{name: "ordinary tools request", method: http.MethodPost, path: "/bots/bot-1/tools"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set(mcpgw.ToolHeaderRuntimeID, tt.id)
			req.Header.Set(mcpgw.ToolHeaderRuntimeToken, tt.token)
			if got := shouldSkipJWTRequest(req); got != tt.want {
				t.Fatalf("shouldSkipJWTRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSafeRequestLogURIStripsPublicMediaQuery(t *testing.T) {
	t.Parallel()

	u, err := neturl.Parse("/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg?exp=123&sig=secret")
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	got := safeRequestLogURI(u, u.RequestURI())
	want := "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg"
	if got != want {
		t.Fatalf("safeRequestLogURI = %q, want %q", got, want)
	}
}

func TestShouldSkipJWT_MCPOAuthCallbackPaths(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"/oauth/mcp/callback", "/api/oauth/mcp/callback"} {
		if !shouldSkipJWT(path) {
			t.Fatalf("path=%q should skip jwt", path)
		}
	}
}

type errorTestHandler struct {
	err error
}

func (h errorTestHandler) Register(e *echo.Echo) {
	e.GET("/health", func(echo.Context) error { return h.err })
}

func TestServerRendersAppErrorAsProblemWithRequestID(t *testing.T) {
	cause := errors.New("dial unix /private/runtime.sock: connection refused")
	server := NewServer(
		slog.New(slog.DiscardHandler),
		":0",
		"test-secret",
		errorTestHandler{err: apperror.Wrap(apperror.CodeWorkspaceUnreachable, cause, nil)},
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "application/problem+json" {
		t.Fatalf("content-type = %q", got)
	}
	requestID := rec.Header().Get(echo.HeaderXRequestID)
	if requestID == "" {
		t.Fatal("response request ID is empty")
	}

	var problem apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode problem: %v", err)
	}
	if problem.Code != string(apperror.CodeWorkspaceUnreachable) {
		t.Fatalf("code = %q", problem.Code)
	}
	if problem.RequestID != requestID {
		t.Fatalf("body request_id = %q, header = %q", problem.RequestID, requestID)
	}
	if got := rec.Body.String(); strings.Contains(got, cause.Error()) {
		t.Fatal("private cause was exposed in response")
	}
}

func TestServerLogsFinalProblemStatus(t *testing.T) {
	var logs bytes.Buffer
	server := NewServer(
		slog.New(slog.NewJSONHandler(&logs, nil)),
		":0",
		"test-secret",
		errorTestHandler{err: apperror.Wrap(apperror.CodeWorkspaceUnreachable, errors.New("private cause"), nil)},
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
	if got := logs.String(); !strings.Contains(got, `"msg":"request"`) || !strings.Contains(got, `"status":503`) {
		t.Fatalf("request log did not capture final status: %s", got)
	}
}

func TestServerKeepsLegacyHTTPErrorBehavior(t *testing.T) {
	server := NewServer(
		slog.New(slog.DiscardHandler),
		":0",
		"test-secret",
		errorTestHandler{err: echo.NewHTTPError(http.StatusBadRequest, "legacy message")},
	)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rec.Body.String() != "{\"message\":\"legacy message\"}\n" {
		t.Fatalf("legacy body changed: %s", rec.Body.String())
	}
}

func TestShouldSkipJWTOnlyForRuntimeConnectEndpoint(t *testing.T) {
	t.Parallel()
	if !shouldSkipJWT("/runtimes/connect") {
		t.Fatal("Runtime key endpoint must authenticate before JWT middleware")
	}
	for _, path := range []string{"/runtimes", "/runtimes/connect/extra", "/users/me/runtimes"} {
		if shouldSkipJWT(path) {
			t.Fatalf("path=%q unexpectedly skips JWT", path)
		}
	}
}

type runtimeMCPRouteOnly struct {
	handler *apphandlers.ContainerdHandler
}

func (h runtimeMCPRouteOnly) Register(e *echo.Echo) {
	e.POST("/bots/:bot_id/tools", h.handler.HandleMCPTools)
}

type runtimeMCPResolver struct {
	session mcpgw.ToolSessionContext
	calls   int
}

func (r *runtimeMCPResolver) ResolveRuntimeToolContext(botID, runtimeID, toolToken string) (mcpgw.ToolSessionContext, bool) {
	r.calls++
	if botID != r.session.BotID || runtimeID != r.session.RuntimeID || toolToken != r.session.RuntimeToken {
		return mcpgw.ToolSessionContext{}, false
	}
	return r.session, true
}

type runtimeMCPToolSource struct {
	lastSession mcpgw.ToolSessionContext
}

func (s *runtimeMCPToolSource) ListTools(_ context.Context, session mcpgw.ToolSessionContext) ([]mcpgw.ToolDescriptor, error) {
	s.lastSession = session
	return []mcpgw.ToolDescriptor{{
		Name:        "runtime_probe",
		Description: "runtime authentication integration probe",
		InputSchema: map[string]any{"type": "object"},
	}}, nil
}

func (s *runtimeMCPToolSource) CallTool(_ context.Context, session mcpgw.ToolSessionContext, _ string, _ map[string]any) (map[string]any, error) {
	s.lastSession = session
	return mcpgw.BuildToolSuccessResult(map[string]any{"ok": true}), nil
}

func TestRuntimeCredentialAuthenticatesExactMCPRouteWithoutUserJWT(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	trusted := mcpgw.ToolSessionContext{
		BotID:         "bot-1",
		ChatID:        "trusted-chat",
		RuntimeID:     "runtime-1",
		RuntimeToken:  "runtime-token-1",
		SessionID:     "trusted-session",
		RuntimeActive: true,
	}
	resolver := &runtimeMCPResolver{session: trusted}
	source := &runtimeMCPToolSource{}
	handler := apphandlers.NewContainerdHandler(log, nil, config.WorkspaceConfig{}, "", nil, nil, nil)
	handler.SetToolGatewayService(mcpgw.NewToolGatewayService(log, []mcpgw.ToolSource{source}))
	handler.SetACPRuntimeResolver(resolver)
	server := NewServer(log, ":0", "test-secret", runtimeMCPRouteOnly{handler: handler})

	request := func(method, path, runtimeID, runtimeToken string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(`{"jsonrpc":"2.0","id":"1","method":"tools/list"}`))
		req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
		req.Header.Set(echo.HeaderAccept, echo.MIMEApplicationJSON)
		req.Header.Set(mcpgw.ToolHeaderRuntimeID, runtimeID)
		req.Header.Set(mcpgw.ToolHeaderRuntimeToken, runtimeToken)
		rec := httptest.NewRecorder()
		server.echo.ServeHTTP(rec, req)
		return rec
	}

	rec := request(http.MethodPost, "/bots/bot-1/tools", trusted.RuntimeID, trusted.RuntimeToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid runtime MCP status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if resolver.calls != 1 {
		t.Fatalf("runtime resolver calls = %d, want 1", resolver.calls)
	}
	if source.lastSession.BotID != trusted.BotID || source.lastSession.RuntimeID != trusted.RuntimeID ||
		source.lastSession.SessionID != trusted.SessionID || !source.lastSession.RuntimeActive {
		t.Fatalf("MCP source received untrusted context: %#v", source.lastSession)
	}

	rec = request(http.MethodPost, "/bots/bot-1/tools", trusted.RuntimeID, "wrong-token")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid runtime MCP status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get(echo.HeaderContentType); got != "application/problem+json" {
		t.Fatalf("invalid runtime MCP content type = %q", got)
	}
	var problem apperror.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
		t.Fatalf("decode invalid runtime MCP problem: %v", err)
	}
	if problem.Code != string(apperror.CodeACPRuntimeNotFound) || problem.RequestID == "" {
		t.Fatalf("invalid runtime MCP problem = %#v", problem)
	}

	resolverCalls := resolver.calls
	oversizedReq := httptest.NewRequest(http.MethodPost, "/bots/bot-1/tools", strings.NewReader(strings.Repeat("x", 2<<20)))
	oversizedReq.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	oversizedReq.Header.Set(mcpgw.ToolHeaderRuntimeID, trusted.RuntimeID)
	oversizedReq.Header.Set(mcpgw.ToolHeaderRuntimeToken, trusted.RuntimeToken)
	oversizedRec := httptest.NewRecorder()
	server.echo.ServeHTTP(oversizedRec, oversizedReq)
	if oversizedRec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized runtime MCP status = %d, want %d", oversizedRec.Code, http.StatusRequestEntityTooLarge)
	}
	if resolver.calls != resolverCalls {
		t.Fatalf("oversized runtime request reached resolver: calls = %d, want %d", resolver.calls, resolverCalls)
	}

	for _, test := range []struct {
		name   string
		method string
		path   string
		id     string
		token  string
	}{
		{name: "ordinary request", method: http.MethodPost, path: "/bots/bot-1/tools"},
		{name: "partial credential", method: http.MethodPost, path: "/bots/bot-1/tools", id: trusted.RuntimeID},
		{name: "wrong method", method: http.MethodGet, path: "/bots/bot-1/tools", id: trusted.RuntimeID, token: trusted.RuntimeToken},
		{name: "non-exact path", method: http.MethodPost, path: "/bots/bot-1/tools/extra", id: trusted.RuntimeID, token: trusted.RuntimeToken},
	} {
		t.Run(test.name, func(t *testing.T) {
			unauthorized := request(test.method, test.path, test.id, test.token)
			if unauthorized.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s; request bypassed user JWT", unauthorized.Code, unauthorized.Body.String())
			}
		})
	}
	if resolver.calls != resolverCalls {
		t.Fatalf("non-runtime requests reached runtime resolver: calls = %d, want %d", resolver.calls, resolverCalls)
	}
}

func TestShouldSkipJWTOnlyForDigestAddressedSupermarketSkillIcons(t *testing.T) {
	t.Parallel()
	digest := strings.Repeat("a", 64)
	if !shouldSkipJWT("/supermarket/artifacts/icon/" + digest) {
		t.Fatal("digest-addressed Skill icon must be readable by img elements")
	}
	for _, path := range []string{
		"/supermarket/artifacts/icon/", "/supermarket/artifacts/icon/short",
		"/supermarket/artifacts/icon/" + strings.ToUpper(digest),
		"/supermarket/artifacts/icon/" + digest + "/extra",
	} {
		if shouldSkipJWT(path) {
			t.Fatalf("path=%q unexpectedly skips JWT", path)
		}
	}
}
