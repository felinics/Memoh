package system

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestI18nHTTPErrorPreservesLocalizedPayload(t *testing.T) {
	e := echo.New()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/", nil)
	rec := httptest.NewRecorder()
	ctx := e.NewContext(req, rec)

	httpErr := newI18nHTTPError(
		http.StatusInternalServerError,
		"workspace_start_failed",
		"bots.container.startFailed",
		"failed to start container: connection refused",
	)
	e.DefaultHTTPErrorHandler(httpErr, ctx)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d: %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got.Code != "workspace_start_failed" {
		t.Fatalf("code = %q", got.Code)
	}
	if got.I18nKey != "bots.container.startFailed" {
		t.Fatalf("i18n_key = %q", got.I18nKey)
	}
	if got.Message != "failed to start container: connection refused" {
		t.Fatalf("message = %q", got.Message)
	}
}
