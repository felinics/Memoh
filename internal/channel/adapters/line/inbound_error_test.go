package line

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestHandleWebhookParseFailureRecordsOneEventWithCause(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	adapter := NewAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))
	body := "{not json"
	req := httptest.NewRequest(http.MethodPost, "/channels/line/webhook/cfg", strings.NewReader(body))
	req.Header.Set("x-line-signature", lineSignature(testLineSecret, body))
	rec := httptest.NewRecorder()

	if err := adapter.HandleWebhook(context.Background(), testLineConfig(), nil, req, rec); err != nil {
		t.Fatalf("HandleWebhook error = %v, want nil: the webhook is acknowledged", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 1 {
		t.Fatalf("records = %d, want 1: %s", len(lines), buf.String())
	}
	var record map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &record); err != nil {
		t.Fatalf("record is not JSON: %v", err)
	}
	if record["level"] != "WARN" || record["fault"] == nil || record["error"] == nil {
		t.Fatalf("record = %v, want a WARN event with fault and error", record)
	}
}
