package agent

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"

	pluginspersistence "github.com/memohai/memoh/domains/agent/extension/plugins/persistence"
	"github.com/memohai/memoh/internal/apperror"
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
		name     string
		err      error
		wantKind apperror.Kind
	}{
		{
			name:     "not found",
			err:      fmt.Errorf("get installation: %w", pluginspersistence.ErrNotFound),
			wantKind: apperror.KindNotFound,
		},
		{
			name:     "bad request",
			err:      errors.New("plugin is not ready"),
			wantKind: apperror.KindInvalid,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pluginServiceError(test.err)
			if kind := apperror.KindOf(got); kind != test.wantKind {
				t.Fatalf("pluginServiceError() kind = %s, want %s", kind, test.wantKind)
			}
		})
	}
}
