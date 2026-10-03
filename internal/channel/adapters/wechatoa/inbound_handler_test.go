package wechatoa

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/felinics/memoh/internal/channel"
)

const testWechatToken = "wechat-token"

func plainWechatConfig() channel.ChannelConfig {
	return channel.ChannelConfig{
		ID:          "cfg-1",
		BotID:       "bot-1",
		ChannelType: Type,
		Credentials: map[string]any{
			"appId":          "app",
			"appSecret":      "secret",
			"token":          testWechatToken,
			"encryptionMode": encryptionModePlain,
		},
	}
}

func signedPlainRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	verifier, err := newSecurityVerifier(testWechatToken, "", "app")
	if err != nil {
		t.Fatalf("newSecurityVerifier: %v", err)
	}
	q := url.Values{}
	q.Set("timestamp", "1700000000")
	q.Set("nonce", "nonce-1")
	q.Set("signature", verifier.sign("1700000000", "nonce-1"))
	return httptest.NewRequest(http.MethodPost, "/channels/wechatoa/webhook/cfg-1?"+q.Encode(), strings.NewReader(body))
}

const testWechatTextXML = `<xml><ToUserName>gh_app</ToUserName><FromUserName>open-1</FromUserName><CreateTime>1700000000</CreateTime><MsgType>text</MsgType><Content>hello</Content><MsgId>1</MsgId></xml>`

func TestHandleWebhookRecordsDroppedInboundAsEvent(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	adapter := NewWeChatOAAdapter(slog.New(slog.NewJSONHandler(&buf, nil)))
	rec := httptest.NewRecorder()
	handler := func(context.Context, channel.ChannelConfig, channel.InboundMessage) error {
		return channel.ErrInboundQueueFull
	}

	if err := adapter.HandleWebhook(context.Background(), plainWechatConfig(), handler, signedPlainRequest(t, testWechatTextXML), rec); err != nil {
		t.Fatalf("HandleWebhook() error = %v", err)
	}
	if rec.Code != http.StatusOK || rec.Body.String() != "success" {
		t.Fatalf("response = %d %q, want 200 success", rec.Code, rec.Body.String())
	}
	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("want one record, got %q: %v", buf.String(), err)
	}
	attrs, _ := record["error_attrs"].(map[string]any)
	text, _ := record["error"].(string)
	if record["msg"] != "wechatoa inbound dropped" || record["level"] != "WARN" || record["fault"] != "server" ||
		!strings.Contains(text, channel.ErrInboundQueueFull.Error()) || attrs["config_id"] != "cfg-1" || attrs["bot_id"] != "bot-1" {
		t.Fatalf("record = %#v, want the dropped-inbound event with its cause", record)
	}
}

func TestHandleWebhookInvalidXMLKeepsCause(t *testing.T) {
	t.Parallel()

	adapter := NewWeChatOAAdapter(slog.New(slog.DiscardHandler))
	err := adapter.HandleWebhook(context.Background(), plainWechatConfig(), nil, signedPlainRequest(t, `<xml><unclosed>`), httptest.NewRecorder())

	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *echo.HTTPError", err)
	}
	if httpErr.Code != http.StatusBadRequest || httpErr.Message != "invalid xml payload" || httpErr.Internal == nil {
		t.Fatalf("HTTPError = %d %v internal=%v, want 400 invalid xml payload with its cause", httpErr.Code, httpErr.Message, httpErr.Internal)
	}
}
