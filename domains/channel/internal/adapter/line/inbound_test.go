package line

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/memohai/memoh/domains/channel/gateway"
	"github.com/memohai/memoh/internal/apperror"
)

const testLineSecret = "line-secret"

func TestHandleWebhookTextMessageAndSuccessDedup(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	cfg := testLineConfig()
	body := testLineTextCallback("event-1")
	var delivered []gateway.InboundMessage
	handler := func(_ context.Context, _ gateway.ChannelConfig, msg gateway.InboundMessage) error {
		delivered = append(delivered, msg)
		return nil
	}

	rec := httptest.NewRecorder()
	err := adapter.HandleWebhook(context.Background(), cfg, handler, signedLineRequest(body), rec)
	if err != nil {
		t.Fatalf("HandleWebhook returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(delivered) != 1 {
		t.Fatalf("delivered = %d, want 1", len(delivered))
	}
	msg := delivered[0]
	if msg.Channel != Type || msg.BotID != cfg.BotID || msg.ReplyTarget != "Uuser" {
		t.Fatalf("unexpected route fields: channel=%s bot=%s target=%s", msg.Channel, msg.BotID, msg.ReplyTarget)
	}
	if msg.Conversation.ID != "Uuser" || msg.Conversation.Type != gateway.ConversationTypePrivate {
		t.Fatalf("unexpected conversation: %+v", msg.Conversation)
	}
	if msg.Message.ID != "message-1" || msg.Message.Text != "hello line" {
		t.Fatalf("unexpected message: %+v", msg.Message)
	}

	rec = httptest.NewRecorder()
	err = adapter.HandleWebhook(context.Background(), cfg, handler, signedLineRequest(body), rec)
	if err != nil {
		t.Fatalf("dedup HandleWebhook returned error: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("dedup status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(delivered) != 1 {
		t.Fatalf("dedup delivered = %d, want 1", len(delivered))
	}
}

func TestHandleWebhookQueueFullReturnsRetryableError(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	cfg := testLineConfig()
	body := testLineTextCallback("event-queue-full")
	calls := 0
	handler := func(_ context.Context, _ gateway.ChannelConfig, _ gateway.InboundMessage) error {
		calls++
		return gateway.ErrInboundQueueFull
	}

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		err := adapter.HandleWebhook(context.Background(), cfg, handler, signedLineRequest(body), rec)
		if apperror.KindOf(err) != apperror.KindUnavailable {
			t.Fatalf("kind on attempt %d = %v, want %v", i+1, apperror.KindOf(err), apperror.KindUnavailable)
		}
	}
	if calls != 2 {
		t.Fatalf("handler calls = %d, want 2", calls)
	}
}

func TestHandleWebhookMissingSelfIdentityReturnsError(t *testing.T) {
	t.Parallel()

	cfg := testLineConfig()
	cfg.SelfIdentity = nil

	err := NewAdapter(nil).HandleWebhook(context.Background(), cfg, nil, signedLineRequest(testLineTextCallback("event-no-self")), httptest.NewRecorder())
	if apperror.KindOf(err) != apperror.KindInternal {
		t.Fatalf("kind = %v, want %v", apperror.KindOf(err), apperror.KindInternal)
	}
}

func TestHandleWebhookRejectsInvalidSignature(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/channels/line/webhook/cfg", strings.NewReader(testLineTextCallback("event-invalid")))
	req.Header.Set("x-line-signature", "invalid")

	err := NewAdapter(nil).HandleWebhook(context.Background(), testLineConfig(), nil, req, httptest.NewRecorder())
	if apperror.KindOf(err) != apperror.KindForbidden {
		t.Fatalf("kind = %v, want %v", apperror.KindOf(err), apperror.KindForbidden)
	}
}

func TestHandleWebhookNilBodyFollowsSignatureValidation(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/channels/line/webhook/cfg", nil)
	req.Body = nil

	err := NewAdapter(nil).HandleWebhook(context.Background(), testLineConfig(), nil, req, httptest.NewRecorder())
	if apperror.KindOf(err) != apperror.KindForbidden {
		t.Fatalf("kind = %v, want %v", apperror.KindOf(err), apperror.KindForbidden)
	}
}

func testLineConfig() gateway.ChannelConfig {
	return gateway.ChannelConfig{
		ID:          "line-config",
		BotID:       "bot-1",
		ChannelType: Type,
		Credentials: map[string]any{
			configKeyChannelSecret:      testLineSecret,
			configKeyChannelAccessToken: "line-token",
		},
		SelfIdentity: map[string]any{
			"bot_user_id": "Ubot",
		},
	}
}

func signedLineRequest(body string) *http.Request {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/channels/line/webhook/cfg", strings.NewReader(body))
	req.Header.Set("x-line-signature", lineSignature(testLineSecret, body))
	return req
}

func lineSignature(secret string, body string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(body))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func testLineTextCallback(eventID string) string {
	return `{
  "destination": "Ubot",
  "events": [
    {
      "type": "message",
      "mode": "active",
      "timestamp": 1710000000000,
      "source": { "type": "user", "userId": "Uuser" },
      "webhookEventId": "` + eventID + `",
      "deliveryContext": { "isRedelivery": false },
      "replyToken": "reply-token",
      "message": {
        "type": "text",
        "id": "message-1",
        "text": " hello line ",
        "quoteToken": "quote-token"
      }
    }
  ]
}`
}
