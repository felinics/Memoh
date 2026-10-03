package line

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
)

func TestHandleWebhookInvalidCredentialsReturnsCauseWithoutLogging(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	adapter := NewAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))
	cfg := testLineConfig()
	cfg.Credentials = map[string]any{}

	err := adapter.HandleWebhook(context.Background(), cfg, nil, signedLineRequest(testLineTextCallback("event-bad-creds")), httptest.NewRecorder())

	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *echo.HTTPError", err)
	}
	if httpErr.Code != http.StatusInternalServerError || httpErr.Message != "line channel not configured" {
		t.Fatalf("HTTPError = %d %v, want 500 line channel not configured", httpErr.Code, httpErr.Message)
	}
	if httpErr.Internal == nil {
		t.Fatal("HTTPError carries no cause for the result record")
	}
	if buf.Len() != 0 {
		t.Fatalf("adapter logged the failure the result record holds: %s", buf.String())
	}
}
