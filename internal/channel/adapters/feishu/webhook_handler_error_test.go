package feishu

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"

	"github.com/felinics/memoh/internal/channel"
)

// requireHTTPErrorWithCause checks that err answers status, that the answer
// does not carry the cause, and that the cause, naming what failed, is kept
// for the result record.
func requireHTTPErrorWithCause(t *testing.T, err error, status int, cause string) {
	t.Helper()
	var he *echo.HTTPError
	if !errors.As(err, &he) {
		t.Fatalf("error = %T, want *echo.HTTPError", err)
	}
	if he.Code != status {
		t.Fatalf("HTTPError = %d, want %d", he.Code, status)
	}
	if he.Internal == nil || !strings.Contains(he.Internal.Error(), cause) {
		t.Fatalf("HTTPError internal = %v, want a cause naming %q", he.Internal, cause)
	}
	if message, _ := he.Message.(string); message != http.StatusText(status) && strings.Contains(he.Internal.Error(), message) {
		t.Fatalf("HTTPError message %q carries the cause", message)
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

const testFeishuEventJSON = `{"schema":"2.0","header":{"event_id":"evt_1","event_type":"im.message.receive_v1","token":"verify-token"},"event":{"sender":{"sender_id":{"open_id":"ou_user_1"}},"message":{"message_id":"om_1","chat_id":"oc_1","chat_type":"p2p","message_type":"text","content":"{\"text\":\"hello\"}"}},"type":"event_callback"}`

func encryptedFeishuEventRequest(t *testing.T, sign func(body string) string) *http.Request {
	t.Helper()
	encrypt, err := larkcore.EncryptedEventMsg(context.Background(), testFeishuEventJSON, "encrypt-key")
	if err != nil {
		t.Fatalf("encrypt event payload: %v", err)
	}
	body := `{"encrypt":"` + encrypt + `"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/feishu/webhook/"+testWebhookConfigID, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	req.Header.Set(larkevent.EventRequestTimestamp, "1700000000")
	req.Header.Set(larkevent.EventRequestNonce, "nonce-1")
	req.Header.Set(larkevent.EventSignature, sign(body))
	return req
}

func encryptedWebhookConfig() channel.ChannelConfig {
	cfg := newWebhookConfig(map[string]any{
		"app_id":             "app",
		"app_secret":         "secret",
		"encrypt_key":        "encrypt-key",
		"verification_token": "verify-token",
		"inbound_mode":       "webhook",
	})
	cfg.SelfIdentity = map[string]any{"open_id": "ou_bot"}
	return cfg
}

func TestHandleWebhook_ForgedSignatureIsUnauthorizedWithCause(t *testing.T) {
	t.Parallel()

	manager := &fakeWebhookManager{}
	req := encryptedFeishuEventRequest(t, func(string) string { return "forged" })
	rec := httptest.NewRecorder()

	err := NewFeishuAdapter(nil).HandleWebhook(context.Background(), encryptedWebhookConfig(), manager.HandleInbound, req, rec)

	requireHTTPErrorWithCause(t, err, http.StatusUnauthorized, "signature verification failed")
	if len(manager.calls) != 0 || rec.Body.Len() != 0 {
		t.Fatalf("forged callback dispatched %d messages and wrote %q", len(manager.calls), rec.Body.String())
	}
}

func TestHandleWebhook_HandlerErrorIsReturnedNotWritten(t *testing.T) {
	t.Parallel()

	manager := &fakeWebhookManager{err: channel.ErrInboundQueueFull}
	req := encryptedFeishuEventRequest(t, func(body string) string {
		return larkevent.Signature("1700000000", "nonce-1", "encrypt-key", body)
	})
	rec := httptest.NewRecorder()

	err := NewFeishuAdapter(nil).HandleWebhook(context.Background(), encryptedWebhookConfig(), manager.HandleInbound, req, rec)

	if !errors.Is(err, channel.ErrInboundQueueFull) {
		t.Fatalf("error = %v, want the handler's error", err)
	}
	if len(manager.calls) != 1 {
		t.Fatalf("inbound calls = %d, want 1", len(manager.calls))
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want nothing written so the cause stays out of it", rec.Body.String())
	}
}

func TestHandleWebhook_DispatchFailureIsReturnedNotWritten(t *testing.T) {
	t.Parallel()

	cfg := newWebhookConfig(map[string]any{
		"app_id":             "app",
		"app_secret":         "secret",
		"verification_token": "verify-token",
		"inbound_mode":       "webhook",
	})
	// The message field does not decode into the SDK's event type, so the SDK
	// fails before any handler runs.
	body := `{"schema":"2.0","header":{"event_id":"evt_1","event_type":"im.message.receive_v1","token":"verify-token"},"event":{"message":"not an object"},"type":"event_callback"}`
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/feishu/webhook/"+testWebhookConfigID, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()

	err := NewFeishuAdapter(nil).HandleWebhook(context.Background(), cfg, (&fakeWebhookManager{}).HandleInbound, req, rec)

	if err == nil || err.Error() != "feishu event dispatch failed" {
		t.Fatalf("error = %v, want the dispatch failure", err)
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("response body = %q, want the SDK's error body withheld", rec.Body.String())
	}
}
