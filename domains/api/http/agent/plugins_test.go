package agent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	pluginspkg "github.com/memohai/memoh/domains/agent/extension/plugins"
)

func TestPluginsHandlerRegisterDoesNotExposeManifestInstallRoute(t *testing.T) {
	e := echo.New()
	(&PluginsHandler{}).Register(e)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/bots/bot-1/plugins", nil)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /bots/:bot_id/plugins status = %d, want 405", rec.Code)
	}
}

func TestPluginServiceError(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantStatus  int
		wantMessage string
	}{
		{
			name:        "not found",
			err:         fmt.Errorf("get installation: %w", pluginspkg.ErrNotFound),
			wantStatus:  http.StatusNotFound,
			wantMessage: "plugin installation not found",
		},
		{
			name:        "bad request",
			err:         errors.New("plugin is not ready"),
			wantStatus:  http.StatusBadRequest,
			wantMessage: "plugin is not ready",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var httpErr *echo.HTTPError
			if !errors.As(pluginServiceError(test.err), &httpErr) {
				t.Fatal("pluginServiceError() did not return *echo.HTTPError")
			}
			if httpErr.Code != test.wantStatus || httpErr.Message != test.wantMessage {
				t.Fatalf("pluginServiceError() = (%d, %v), want (%d, %q)", httpErr.Code, httpErr.Message, test.wantStatus, test.wantMessage)
			}
		})
	}
}
