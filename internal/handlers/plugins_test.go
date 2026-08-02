package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
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
