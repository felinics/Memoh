package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	httpx "github.com/memohai/memoh/domains/api/http/httpx"
	"github.com/memohai/memoh/internal/apperror"
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
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

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
	rec := httptest.NewRecorder()
	server.echo.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if rec.Body.String() != "{\"message\":\"legacy message\"}\n" {
		t.Fatalf("legacy body changed: %s", rec.Body.String())
	}
}

type sseTestHandler struct{}

func (sseTestHandler) Register(e *echo.Echo) {
	e.GET("/health", func(c echo.Context) error {
		if err := httpx.PrepareSSE(c); err != nil {
			return err
		}
		return c.NoContent(http.StatusOK)
	})
}

// PrepareSSE reaches the real connection only if every middleware in the
// production stack keeps http.ResponseController's unwrap chain intact. Adding
// one that does not (a compression middleware, say) would truncate every stream
// at WriteTimeout in production while handler-level tests stayed green.
func TestSSEDeadlineLiftSurvivesProductionMiddlewareStack(t *testing.T) {
	server := NewServer(slog.New(slog.DiscardHandler), ":0", "test-secret", sseTestHandler{})
	ts := httptest.NewServer(server.echo)
	defer ts.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/health", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want %d: %s", resp.StatusCode, http.StatusOK, body)
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
