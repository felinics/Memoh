package wecom

import (
	"context"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
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

// The stream has no live connection here, so the test reads the message the
// error event queued for sending instead of a delivered frame.
func TestOutboundStreamErrorReply(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)
	const secret = "wecom-secret-value-123456"
	redact.SetSecrets("wecom-stream-test", secret)

	cases := []struct {
		name     string
		event    channel.PreparedStreamEvent
		want     string
		wantSend bool
	}{
		{
			name:     "coded error shows the copy as it is",
			event:    channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"},
			want:     "The workspace is unreachable.",
			wantSend: true,
		},
		{
			name:     "uncoded error is redacted and labelled",
			event:    channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "request failed with token " + secret},
			want:     "Error: request failed with token " + strings.Repeat("*", len(secret)),
			wantSend: true,
		},
		{
			name:  "blank error sends nothing",
			event: channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "  "},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stream, err := NewWeComAdapter(nil).OpenStream(context.Background(), channel.ChannelConfig{ID: "cfg-1"}, "chat_id:chat_1", channel.StreamOptions{})
			if err != nil {
				t.Fatalf("OpenStream: %v", err)
			}
			ws := stream.(*wecomOutboundStream)
			if err := ws.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "draft"}); err != nil {
				t.Fatalf("Push delta: %v", err)
			}
			pushErr := ws.Push(context.Background(), tc.event)
			if !tc.wantSend {
				if pushErr != nil || ws.final != nil {
					t.Fatalf("blank error queued %#v (err %v), want nothing", ws.final, pushErr)
				}
				return
			}
			if pushErr == nil {
				t.Fatal("error reply did not try to send")
			}
			if ws.final == nil || ws.final.Message.Text != tc.want {
				t.Fatalf("queued error reply = %#v, want %q", ws.final, tc.want)
			}
		})
	}
}

func TestOutboundStreamErrorDropsBufferedAttachments(t *testing.T) {
	stream, err := NewWeComAdapter(nil).OpenStream(context.Background(), channel.ChannelConfig{ID: "cfg-1"}, "chat_id:chat_1", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	ws := stream.(*wecomOutboundStream)
	if err := ws.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventAttachment, Attachments: []channel.PreparedAttachment{{
		Kind:    channel.PreparedAttachmentPublicURL,
		Logical: channel.Attachment{Type: channel.AttachmentImage, URL: "https://example.com/previous.png"},
	}}}); err != nil {
		t.Fatalf("Push attachment: %v", err)
	}
	// No live connection, so the send fails; only the queued message matters.
	_ = ws.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventError, Error: "The workspace is unreachable.", ErrorCode: "workspace.unreachable"})
	msg, _ := ws.snapshotMessage(true)
	if len(msg.Attachments) != 0 || len(msg.Message.Attachments) != 0 {
		t.Fatalf("error reply carries attachments: %#v", msg)
	}
}
