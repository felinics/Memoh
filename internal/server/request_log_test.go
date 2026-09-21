package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/auth"
	"github.com/felinics/memoh/internal/logger"
)

type requestLogTestHandler struct {
	handle echo.HandlerFunc
}

func (h requestLogTestHandler) Register(e *echo.Echo) {
	e.GET("/*", h.handle)
}

func TestServerRequestLogOmitsQuery(t *testing.T) {
	token, _, err := auth.GenerateToken("request-log-user", "test-secret", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name       string
		path       string
		query      string
		authed     bool
		nilURL     bool
		requestURI string
		clearURI   bool
		wantPath   string
	}{
		{name: "query token websocket", path: "/chat/ws", query: "token=" + token, authed: true, wantPath: "/chat/ws"},
		{name: "authenticated media", path: "/bots/bot-1/media/file-1", query: "token=" + token, authed: true, wantPath: "/bots/bot-1/media/file-1"},
		{name: "public media", path: "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg", query: "exp=123&sig=synthetic-signature", wantPath: "/channels/line/public/media/bot-1/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa/preview.jpg"},
		{name: "OAuth code and state", path: "/oauth/mcp/callback", query: "code=synthetic-code&state=synthetic-state", wantPath: "/oauth/mcp/callback"},
		{name: "provider OAuth", path: "/providers/oauth/callback", query: "code=synthetic-code&state=synthetic-state", wantPath: "/providers/oauth/callback"},
		{name: "ordinary query", path: "/health", query: "search=synthetic-search&page=2", wantPath: "/health"},
		{name: "encoded and repeated keys", path: "/health", query: "%74oken=synthetic-first&token=synthetic-second&%63ode=synthetic-code", wantPath: "/health"},
		{name: "escaped path", path: "/assets/a%2Fb%20c", query: "value=synthetic-value", wantPath: "/assets/a%2Fb%20c"},
		{name: "no query", path: "/health", wantPath: "/health"},
		{name: "URL takes precedence", path: "/health", query: "value=synthetic-value", requestURI: "/other?value=synthetic-fallback", wantPath: "/health"},
		{name: "URL missing at log time", path: "/health", query: "value=synthetic-value", nilURL: true, wantPath: "/health"},
		{name: "escaped fallback", path: "/assets/a%2Fb", query: "value=synthetic-value", nilURL: true, wantPath: "/assets/a%2Fb"},
		{name: "absolute fallback", path: "/health", nilURL: true, requestURI: "https://example.test/assets/a%2Fb?value=synthetic-fallback", wantPath: "/assets/a%2Fb"},
		{name: "invalid fallback", path: "/health", nilURL: true, requestURI: "/%zz?value=synthetic-fallback", wantPath: ""},
		{name: "empty fallback", path: "/health", nilURL: true, clearURI: true, wantPath: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			handled := false
			// logger.New, not a bare JSON handler: request_id reaches the
			// record through the correlation handler rather than being
			// written by this middleware, so a logger assembled any other
			// way would drop it — which is what this case would then be
			// asserting about.
			srv := NewServer(logger.New(&logs, "info", "json"), ":0", "test-secret", requestLogTestHandler{
				handle: func(c echo.Context) error {
					handled = true
					if c.Request().URL.RawQuery != tc.query {
						t.Error("request query changed before reaching handler")
					}
					if tc.authed {
						userID, err := auth.UserIDFromContext(c)
						if err != nil || userID != "request-log-user" {
							t.Errorf("query token did not authenticate: user=%q err=%v", userID, err)
						}
					}
					if tc.nilURL {
						c.Request().URL = nil
					}
					return c.NoContent(http.StatusNoContent)
				},
			})
			target := tc.path
			if tc.query != "" {
				target += "?" + tc.query
			}
			req := httptest.NewRequest(http.MethodGet, target, nil)
			if tc.requestURI != "" {
				req.RequestURI = tc.requestURI
			}
			if tc.clearURI {
				req.RequestURI = ""
			}
			rec := httptest.NewRecorder()
			srv.echo.ServeHTTP(rec, req)
			if !handled || rec.Code != http.StatusNoContent {
				t.Fatalf("handler=%v status=%d", handled, rec.Code)
			}
			if strings.Contains(logs.String(), token) || strings.Contains(logs.String(), "synthetic-") {
				t.Fatal("request log exposed query values")
			}
			var entry struct {
				URI       string `json:"uri"`
				Method    string `json:"method"`
				Status    int    `json:"status"`
				RequestID string `json:"request_id"`
			}
			if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
				t.Fatal(err)
			}
			if entry.URI != tc.wantPath || entry.Method != http.MethodGet || entry.Status != http.StatusNoContent || entry.RequestID == "" || entry.RequestID != rec.Header().Get(echo.HeaderXRequestID) {
				t.Fatalf("request observability lost: %+v", entry)
			}
		})
	}
}
