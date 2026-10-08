package dingtalk

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestOutboundStreamSnapshotBuffersVisibleTextAndAttachments(t *testing.T) {
	reply := &channel.ReplyRef{MessageID: "msg-1", Target: "user:alice"}
	stream := &dingtalkOutboundStream{
		target: "user:alice",
		reply:  reply,
	}

	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "Thinking...",
		Phase: channel.StreamPhaseReasoning,
	}); err != nil {
		t.Fatalf("reasoning Push error = %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "Hello ",
	}); err != nil {
		t.Fatalf("first delta Push error = %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "world",
	}); err != nil {
		t.Fatalf("second delta Push error = %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventAttachment,
		Attachments: []channel.PreparedAttachment{{
			Kind:    channel.PreparedAttachmentPublicURL,
			Logical: channel.Attachment{Type: channel.AttachmentImage, URL: "https://example.com/a.png"},
		}},
	}); err != nil {
		t.Fatalf("attachment Push error = %v", err)
	}

	got := stream.snapshotPrepared()
	if got.Message.Text != "Hello world" {
		t.Fatalf("snapshot text = %q, want Hello world", got.Message.Text)
	}
	if len(got.Attachments) != 1 || len(got.Message.Attachments) != 1 {
		t.Fatalf("expected prepared and logical attachments, got prepared=%d logical=%d", len(got.Attachments), len(got.Message.Attachments))
	}
	if got.Message.Reply != reply {
		t.Fatalf("reply was not applied: %#v", got.Message.Reply)
	}
}

func TestOutboundStreamSnapshotFinalMessageWinsOverBufferedText(t *testing.T) {
	stream := &dingtalkOutboundStream{}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "partial",
	}); err != nil {
		t.Fatalf("delta Push error = %v", err)
	}
	stream.final = &channel.PreparedMessage{
		Message: channel.Message{Text: "final"},
	}

	got := stream.snapshotPrepared()
	if got.Message.Text != "final" {
		t.Fatalf("snapshot text = %q, want final", got.Message.Text)
	}
}

func TestOutboundStreamRejectsPushAfterClose(t *testing.T) {
	stream := &dingtalkOutboundStream{}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close error = %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "late"}); err == nil {
		t.Fatal("expected push after close error")
	}
}

func TestSessionWebhookCacheExpiryAndValidity(t *testing.T) {
	cache := newSessionWebhookCache(time.Hour)
	cache.put("config-a", "msg-1", sessionWebhookContext{
		SessionWebhook: "https://example.com/hook",
		ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
	})
	got, ok := cache.get("config-a", "msg-1")
	if !ok {
		t.Fatal("expected cached webhook")
	}
	if !got.isValid() {
		t.Fatal("expected webhook to be valid")
	}

	cache.put("config-a", "msg-2", sessionWebhookContext{
		SessionWebhook: "https://example.com/old",
		ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
		CreatedAt:      time.Now().Add(-2 * time.Hour),
	})
	if _, ok := cache.get("config-a", "msg-2"); ok {
		t.Fatal("expected stale webhook to be evicted")
	}

	if (sessionWebhookContext{SessionWebhook: "https://example.com/expired", ExpiredTime: time.Now().Add(-time.Second).UnixMilli()}).isValid() {
		t.Fatal("expected expired webhook to be invalid")
	}
}

func TestSessionWebhookCacheIsScopedByConfig(t *testing.T) {
	cache := newSessionWebhookCache(time.Hour)
	cache.put("config-a", "shared-message", sessionWebhookContext{
		SessionWebhook: "https://example.com/config-a",
		ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
	})
	cache.put("config-b", "shared-message", sessionWebhookContext{
		SessionWebhook: "https://example.com/config-b",
		ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
	})

	first, ok := cache.get("config-a", "shared-message")
	if !ok || first.SessionWebhook != "https://example.com/config-a" {
		t.Fatalf("config-a webhook = %#v, found=%v", first, ok)
	}
	second, ok := cache.get("config-b", "shared-message")
	if !ok || second.SessionWebhook != "https://example.com/config-b" {
		t.Fatalf("config-b webhook = %#v, found=%v", second, ok)
	}
}

func TestOpenStreamUsesSourceMessageIDAsReply(t *testing.T) {
	adapter := NewDingTalkAdapter(nil)
	stream, err := adapter.OpenStream(context.Background(), channel.ChannelConfig{}, "user:alice", channel.StreamOptions{
		SourceMessageID: "source-1",
	})
	if err != nil {
		t.Fatalf("OpenStream error = %v", err)
	}
	dtStream, ok := stream.(*dingtalkOutboundStream)
	if !ok {
		t.Fatalf("stream type = %T, want *dingtalkOutboundStream", stream)
	}
	if dtStream.reply == nil || dtStream.reply.MessageID != "source-1" || dtStream.reply.Target != "user:alice" {
		t.Fatalf("unexpected reply: %#v", dtStream.reply)
	}
}

func TestOutboundStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const secret = "dingtalk-secret-value-123456"
	redact.SetSecrets("dingtalk-stream-test", secret)

	cases := []struct {
		name  string
		event channel.PreparedStreamEvent
		want  []string
	}{
		{
			name:  "coded error shows the copy as it is",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
			want:  []string{"The workspace is unreachable."},
		},
		{
			name:  "uncoded error is redacted and labelled",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "request failed with token " + secret},
			want:  []string{"Error: request failed with token " + strings.Repeat("*", len(secret))},
		},
		{
			name:  "blank error sends nothing",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "  "},
			want:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				sent []string
			)
			server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				var body struct {
					Text struct {
						Content string `json:"content"`
					} `json:"text"`
				}
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &body)
				mu.Lock()
				sent = append(sent, body.Text.Content)
				mu.Unlock()
			}))
			t.Cleanup(server.Close)

			adapter := NewDingTalkAdapter(nil)
			cfg := channel.ChannelConfig{ID: "cfg-1"}
			adapter.rememberWebhook(cfg.ID, "source-1", sessionWebhookContext{
				SessionWebhook: server.URL,
				ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
			})
			stream, err := adapter.OpenStream(context.Background(), cfg, "user:alice", channel.StreamOptions{SourceMessageID: "source-1"})
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "draft"}); err != nil {
				t.Fatalf("Push delta: %v", err)
			}
			if err := stream.Push(context.Background(), tc.event); err != nil {
				t.Fatalf("Push error: %v", err)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(sent) != len(tc.want) || strings.Join(sent, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("sent messages = %q, want %q", sent, tc.want)
			}
		})
	}
}

// An error replaces the answer: the attachments buffered for it are not sent
// with the error, nor by a later Close.
func TestOutboundStreamErrorDropsBufferedAnswer(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(raw))
		mu.Unlock()
	}))
	t.Cleanup(server.Close)

	adapter := NewDingTalkAdapter(nil)
	cfg := channel.ChannelConfig{ID: "cfg-1"}
	adapter.rememberWebhook(cfg.ID, "source-1", sessionWebhookContext{
		SessionWebhook: server.URL,
		ExpiredTime:    time.Now().Add(time.Minute).UnixMilli(),
	})
	stream, err := adapter.OpenStream(context.Background(), cfg, "user:alice", channel.StreamOptions{SourceMessageID: "source-1"})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	events := []channel.PreparedStreamEvent{
		{Type: channel.StreamEventDelta, Delta: "draft answer"},
		{Type: channel.StreamEventAttachment, Attachments: []channel.PreparedAttachment{{
			Kind:    channel.PreparedAttachmentPublicURL,
			Logical: channel.Attachment{Type: channel.AttachmentImage, URL: "https://example.com/previous.png"},
		}}},
		{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
	}
	var pushErr error
	for _, event := range events {
		pushErr = stream.Push(context.Background(), event)
	}
	if got := stream.(*dingtalkOutboundStream).snapshotPrepared(); len(got.Attachments) != 0 || len(got.Message.Attachments) != 0 {
		t.Fatalf("error reply carries the buffered attachments: %+v", got)
	}
	if pushErr != nil {
		t.Fatalf("Push error: %v", pushErr)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 1 || !strings.Contains(bodies[0], "The workspace is unreachable.") || strings.Contains(bodies[0], "previous.png") {
		t.Fatalf("sent = %q, want only the error reply", bodies)
	}
}
