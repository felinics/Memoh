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
	if httpErr.Code != http.StatusBadRequest || httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), "invalid xml payload") {
		t.Fatalf("HTTPError = %d internal=%v, want 400 with the invalid xml payload cause", httpErr.Code, httpErr.Internal)
	}
}

func TestHandleWebhookUndecodableInboundReturnsForbiddenWithCause(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/channels/wechatoa/webhook/cfg-1?timestamp=1700000000&nonce=nonce-1&signature=forged", strings.NewReader(testWechatTextXML))
	rec := httptest.NewRecorder()
	err := NewWeChatOAAdapter(slog.New(slog.DiscardHandler)).HandleWebhook(context.Background(), plainWechatConfig(), nil, req, rec)

	if rec.Code != http.StatusForbidden || rec.Body.String() != "forbidden" {
		t.Fatalf("response = %d %q, want 403 forbidden", rec.Code, rec.Body.String())
	}
	var httpErr *echo.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %T, want *echo.HTTPError", err)
	}
	if httpErr.Code != http.StatusForbidden || httpErr.Internal == nil || !strings.Contains(httpErr.Internal.Error(), "invalid url signature") {
		t.Fatalf("HTTPError = %d internal=%v, want 403 with the decode cause", httpErr.Code, httpErr.Internal)
	}
}

func TestHandleWebhookVerifyRejectionReturnsTheAnswerItWrote(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		query  string
		status int
		body   string
	}{
		{name: "missing query", query: "timestamp=1700000000", status: http.StatusBadRequest, body: "invalid verify query"},
		{name: "forged signature", query: "timestamp=1700000000&nonce=nonce-1&signature=forged&echostr=echo", status: http.StatusForbidden, body: "invalid signature"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/channels/wechatoa/webhook/cfg-1?"+tc.query, nil)
			rec := httptest.NewRecorder()
			err := NewWeChatOAAdapter(slog.New(slog.DiscardHandler)).HandleWebhook(context.Background(), plainWechatConfig(), nil, req, rec)

			if rec.Code != tc.status || rec.Body.String() != tc.body {
				t.Fatalf("response = %d %q, want %d %q", rec.Code, rec.Body.String(), tc.status, tc.body)
			}
			var httpErr *echo.HTTPError
			if !errors.As(err, &httpErr) || httpErr.Code != tc.status || httpErr.Message != tc.body {
				t.Fatalf("error = %#v, want HTTPError %d %q", err, tc.status, tc.body)
			}
		})
	}
}
