package feishu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
)

// requireHTTPErrorWithCause checks that err answers status with a message
// that does not carry the cause, and that the cause is kept for the result
// record.
func requireHTTPErrorWithCause(t *testing.T, err error, status int, message string) {
	t.Helper()
	var he *echo.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error = %T, want *echo.HTTPError", err)
	}
	if he.Code != status || he.Message != message {
		t.Fatalf("HTTPError = %d %v, want %d %q", he.Code, he.Message, status, message)
	}
	if he.Internal == nil {
		t.Fatal("HTTPError carries no cause for the result record")
	}
}

func TestHandleWebhook_InvalidPayloadKeepsCauseOutOfMessage(t *testing.T) {
	t.Parallel()

	cfg := newWebhookConfig(map[string]any{
		"app_id":             "app",
		"app_secret":         "secret",
		"verification_token": "verify-token",
		"inbound_mode":       "webhook",
	})
	cfg.SelfIdentity = map[string]any{"open_id": "ou_bot"}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/feishu/webhook/"+testWebhookConfigID, strings.NewReader(`not json`))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)

	err := NewFeishuAdapter(nil).HandleWebhook(context.Background(), cfg, (&fakeWebhookManager{}).HandleInbound, req, httptest.NewRecorder())

	requireHTTPErrorWithCause(t, err, http.StatusBadRequest, "invalid feishu webhook payload")
}

func TestHandleWebhook_InvalidConfigKeepsCauseOutOfMessage(t *testing.T) {
	t.Parallel()

	cfg := newWebhookConfig(map[string]any{"inbound_mode": "webhook"})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/feishu/webhook/"+testWebhookConfigID, strings.NewReader(`{}`))

	err := NewFeishuAdapter(nil).HandleWebhook(context.Background(), cfg, (&fakeWebhookManager{}).HandleInbound, req, httptest.NewRecorder())

	requireHTTPErrorWithCause(t, err, http.StatusBadRequest, "invalid feishu channel config")
}
