package misskey

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

var (
	_ channel.Sender       = (*MisskeyAdapter)(nil)
	_ channel.StreamSender = (*MisskeyAdapter)(nil)
	_ channel.Reactor      = (*MisskeyAdapter)(nil)
	_ channel.Receiver     = (*MisskeyAdapter)(nil)
)

func withMisskeyHTTPStub(t *testing.T, handler http.HandlerFunc) (channel.ChannelConfig, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	cfg := channel.ChannelConfig{
		ID:    "cfg-1",
		BotID: "bot-1",
		Credentials: map[string]any{
			"instanceURL": server.URL,
			"accessToken": "tkn",
		},
	}
	return cfg, server
}

func TestSendDeliversPreparedOutboundMessage(t *testing.T) {
	t.Parallel()

	var got struct {
		path string
		body createNoteRequest
	}
	cfg, _ := withMisskeyHTTPStub(t, func(w http.ResponseWriter, r *http.Request) {
		got.path = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got.body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"createdNote":{"id":"note-1"}}`))
	})

	adapter := NewMisskeyAdapter(nil)
	err := adapter.Send(context.Background(), cfg, channel.PreparedOutboundMessage{
		Target: "note-source",
		Message: channel.PreparedMessage{Message: channel.Message{
			Text: "hello misskey",
		}},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !strings.HasSuffix(got.path, "/api/notes/create") {
		t.Fatalf("unexpected api path: %q", got.path)
	}
	if got.body.Text != "hello misskey" || got.body.ReplyID != "note-source" {
		t.Fatalf("unexpected note request: %#v", got.body)
	}
}

func TestOpenStreamReturnsPreparedOutboundStream(t *testing.T) {
	t.Parallel()

	var sent createNoteRequest
	cfg, _ := withMisskeyHTTPStub(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"createdNote":{"id":"note-2"}}`))
	})

	adapter := NewMisskeyAdapter(nil)
	stream, err := adapter.OpenStream(context.Background(), cfg, "note-source", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "hello ",
	}); err != nil {
		t.Fatalf("Push delta: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "stream",
	}); err != nil {
		t.Fatalf("Push delta: %v", err)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if sent.Text != "hello stream" {
		t.Fatalf("expected buffered text 'hello stream', got %q", sent.Text)
	}
}

func TestBuildInboundMessageReplyKeepsTextClean(t *testing.T) {
	t.Parallel()

	adapter := &MisskeyAdapter{}
	inbound, ok := adapter.buildInboundMessage(&meResponse{ID: "bot-1", Username: "bot"}, misskeyNote{
		ID:      "note-1",
		Text:    "@bot reply body",
		UserID:  "user-1",
		User:    misskeyUser{Username: "sender", Name: "Sender"},
		ReplyID: "source-note",
		Reply: &misskeyNote{
			ID:   "source-note",
			Text: "quoted text",
			User: misskeyUser{Username: "original", Name: "Original"},
		},
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if inbound.Message.Text != "reply body" {
		t.Fatalf("unexpected text: %q", inbound.Message.Text)
	}
	if inbound.Message.Reply == nil {
		t.Fatal("expected reply ref")
	}
	if inbound.Message.Reply.MessageID != "source-note" ||
		inbound.Message.Reply.Sender != "Original" ||
		inbound.Message.Reply.Preview != "quoted text" {
		t.Fatalf("unexpected reply ref: %#v", inbound.Message.Reply)
	}
}

func TestBuildInboundMessageMarksReplyToBot(t *testing.T) {
	t.Parallel()

	adapter := &MisskeyAdapter{}
	me := &meResponse{ID: "bot-1", Username: "bot"}
	// Reply to the bot's own note (no @-mention): must be flagged so
	// isDirectedAtBot lets plain-text ask_user replies through.
	inbound, ok := adapter.buildInboundMessage(me, misskeyNote{
		ID:     "note-2",
		Text:   "my answer",
		UserID: "user-1",
		User:   misskeyUser{Username: "sender"},
		Reply:  &misskeyNote{ID: "bot-note", UserID: "bot-1", Text: "question"},
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if replyToBot, _ := inbound.Metadata["is_reply_to_bot"].(bool); !replyToBot {
		t.Fatalf("metadata = %#v, want is_reply_to_bot", inbound.Metadata)
	}

	// Reply to someone else's note must NOT be flagged.
	inbound, ok = adapter.buildInboundMessage(me, misskeyNote{
		ID:     "note-3",
		Text:   "side chatter",
		UserID: "user-1",
		User:   misskeyUser{Username: "sender"},
		Reply:  &misskeyNote{ID: "other-note", UserID: "user-2", Text: "hi"},
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if replyToBot, _ := inbound.Metadata["is_reply_to_bot"].(bool); replyToBot {
		t.Fatalf("metadata = %#v, reply to another user must not be flagged", inbound.Metadata)
	}
}

func TestBuildInboundMessageRenoteMapsForward(t *testing.T) {
	t.Parallel()

	adapter := &MisskeyAdapter{}
	inbound, ok := adapter.buildInboundMessage(&meResponse{ID: "bot-1", Username: "bot"}, misskeyNote{
		ID:       "note-1",
		Text:     "@bot check this",
		UserID:   "user-1",
		User:     misskeyUser{Username: "sender", Name: "Sender"},
		RenoteID: "renote-1",
		Renote: &misskeyNote{
			ID:        "renote-1",
			UserID:    "source-user",
			User:      misskeyUser{Username: "source", Name: "Source"},
			CreatedAt: "2026-05-09T00:00:00Z",
		},
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if inbound.Message.Text != "check this" {
		t.Fatalf("unexpected text: %q", inbound.Message.Text)
	}
	if inbound.Message.Forward == nil {
		t.Fatal("expected forward ref")
	}
	if inbound.Message.Forward.MessageID != "renote-1" ||
		inbound.Message.Forward.FromUserID != "source-user" ||
		inbound.Message.Forward.Sender != "Source" ||
		inbound.Message.Forward.Date == 0 {
		t.Fatalf("unexpected forward ref: %#v", inbound.Message.Forward)
	}
}
