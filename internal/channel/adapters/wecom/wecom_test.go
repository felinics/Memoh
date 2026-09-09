package wecom

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestDiscoverSelf(t *testing.T) {
	adapter := NewWeComAdapter(nil)
	identity, externalID, err := adapter.DiscoverSelf(context.Background(), map[string]any{
		"botId":  "bot_123",
		"secret": "sec",
	})
	if err != nil {
		t.Fatalf("DiscoverSelf error = %v", err)
	}
	if externalID != "bot_123" {
		t.Fatalf("unexpected external id: %q", externalID)
	}
	if identity["bot_id"] != "bot_123" {
		t.Fatalf("unexpected bot_id: %v", identity["bot_id"])
	}
	if identity["aibot_id"] != "bot_123" {
		t.Fatalf("unexpected aibot_id: %v", identity["aibot_id"])
	}
	if _, ok := identity["name"]; ok {
		t.Fatalf("unexpected name field: %v", identity["name"])
	}
	if _, ok := identity["display_name"]; ok {
		t.Fatalf("unexpected display_name field: %v", identity["display_name"])
	}
}

func TestOpenStream_FallbackReplyFromSourceMessageID(t *testing.T) {
	adapter := NewWeComAdapter(nil)
	stream, err := adapter.OpenStream(context.Background(), channel.ChannelConfig{}, "chat_id:chat_1", channel.StreamOptions{
		SourceMessageID: "msg_1",
	})
	if err != nil {
		t.Fatalf("OpenStream error = %v", err)
	}
	ws, ok := stream.(*wecomOutboundStream)
	if !ok {
		t.Fatalf("unexpected stream type: %T", stream)
	}
	if ws.reply == nil || ws.reply.MessageID != "msg_1" || ws.reply.Target != "chat_id:chat_1" {
		t.Fatalf("unexpected reply fallback: %+v", ws.reply)
	}
}
