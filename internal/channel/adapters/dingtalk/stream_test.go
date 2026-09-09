package dingtalk

import (
	"context"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/channel"
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
