package discuss

import (
	"context"
	"testing"

	agentevent "github.com/memohai/memoh/internal/agent/event"
	"github.com/memohai/memoh/internal/channel"
	"github.com/memohai/memoh/internal/delivery"
)

func TestPartialTopLevelJSONStringStreamsOnlyDecodedText(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		raw   string
		want  string
		found bool
	}{
		{raw: `{"platform":"telegram","text":"hello`, want: "hello", found: true},
		{raw: `{"text":"line\nnext`, want: "line\nnext", found: true},
		{raw: `{"text":"你好\u4e1`, want: "你好", found: true},
		{raw: `{"reasoning":"private"}`, found: false},
	} {
		got, found := partialTopLevelJSONString(test.raw, "text")
		if found != test.found || got != test.want {
			t.Fatalf("partialTopLevelJSONString(%q) = %q,%v want %q,%v", test.raw, got, found, test.want, test.found)
		}
	}
}

func TestSameDiscussConversationDefaultsOnlyToCurrentTarget(t *testing.T) {
	t.Parallel()
	cfg := DiscussSessionConfig{CurrentPlatform: "telegram", ReplyTarget: "chat-1"}
	if !delivery.IsSameConversation(cfg.CurrentPlatform, cfg.ReplyTarget, "", "") {
		t.Fatal("omitted target should mean the current conversation")
	}
	if delivery.IsSameConversation(cfg.CurrentPlatform, cfg.ReplyTarget, "telegram", "chat-2") ||
		delivery.IsSameConversation(cfg.CurrentPlatform, cfg.ReplyTarget, "discord", "chat-1") {
		t.Fatal("cross-conversation send was accepted for preview")
	}
}

func TestDiscussSendPreviewWaitsForCompleteCurrentRoute(t *testing.T) {
	t.Parallel()

	sender := &fakeDiscussReplySender{}
	preview := newDiscussSendPreview(DiscussSessionConfig{
		BotID: "bot-1", CurrentPlatform: "telegram", ReplyTarget: "chat-1", ReplySender: sender,
	}, channel.NewSendToolStreamCoordinator(), nil)
	ctx := context.Background()
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallInputStart, ToolName: "send", ToolCallID: "call-1",
	})
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallInputDelta, ToolCallID: "call-1",
		Delta: `{"text":"must not leak","target":"chat-2"}`,
	})
	if len(sender.streamEvents) != 0 {
		t.Fatalf("preview opened before complete routing was validated: %#v", sender.streamEvents)
	}
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallStart, ToolName: "send", ToolCallID: "call-1",
		// Exercise the defensive merge from the completed streamed JSON: some
		// runtimes may omit routing keys from the decoded event payload.
		Input: map[string]any{"text": "must not leak"},
	})
	if len(sender.streamEvents) != 0 {
		t.Fatalf("cross-target send leaked into current preview: %#v", sender.streamEvents)
	}
}

func TestCompleteSendArgumentsRejectsConflictingRoute(t *testing.T) {
	t.Parallel()

	if _, complete := completeSendArguments(
		map[string]any{"text": "must not leak", "target": "chat-1"},
		`{"text":"must not leak","target":"chat-2"}`,
	); complete {
		t.Fatal("conflicting final and streamed routes were accepted")
	}
	args, complete := completeSendArguments(
		map[string]any{"text": "safe", "target": ""},
		`{"text":"safe","target":"chat-2"}`,
	)
	if !complete || args["target"] != "chat-2" {
		t.Fatalf("explicit streamed route was lost: %#v, complete=%v", args, complete)
	}
}

func TestDiscussSendPreviewOpensAfterRouteValidationAndAbortsSilently(t *testing.T) {
	t.Parallel()

	sender := &fakeDiscussReplySender{}
	preview := newDiscussSendPreview(DiscussSessionConfig{
		BotID: "bot-1", CurrentPlatform: "telegram", ReplyTarget: "chat-1", ReplySender: sender,
	}, channel.NewSendToolStreamCoordinator(), nil)
	ctx := context.Background()
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallInputStart, ToolName: "send", ToolCallID: "call-1",
	})
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallInputDelta, ToolCallID: "call-1", Delta: `{"text":"hello"}`,
	})
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallStart, ToolName: "send", ToolCallID: "call-1",
		Input: map[string]any{"text": "hello"},
	})
	if len(sender.streamEvents) != 2 || sender.streamEvents[0].Type != channel.StreamEventStatus ||
		sender.streamEvents[1].Type != channel.StreamEventDelta || sender.streamEvents[1].Delta != "hello" {
		t.Fatalf("validated preview events = %#v", sender.streamEvents)
	}
	preview.Handle(ctx, agentevent.StreamEvent{
		Type: agentevent.ToolCallEnd, ToolName: "send", ToolCallID: "call-1", Error: "send failed",
	})
	for _, event := range sender.streamEvents {
		if event.Type == channel.StreamEventError {
			t.Fatalf("abort exposed a permanent audience error: %#v", sender.streamEvents)
		}
	}
}

func TestDiscussSendPreviewReplyIsOptIn(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		raw       string
		input     map[string]any
		wantReply string
	}{
		{
			name:  "omitted reply",
			raw:   `{"text":"ordinary message"}`,
			input: map[string]any{"text": "ordinary message"},
		},
		{
			name:      "top-level reply_to",
			raw:       `{"text":"quoted message","reply_to":"message-42"}`,
			input:     map[string]any{"text": "quoted message", "reply_to": "message-42"},
			wantReply: "message-42",
		},
		{
			name: "structured message reply",
			raw:  `{"text":"quoted message","message":{"text":"quoted message","reply":{"message_id":"message-84"}}}`,
			input: map[string]any{
				"text": "quoted message",
				"message": map[string]any{
					"text":  "quoted message",
					"reply": map[string]any{"message_id": "message-84"},
				},
			},
			wantReply: "message-84",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			sender := &fakeDiscussReplySender{}
			preview := newDiscussSendPreview(DiscussSessionConfig{
				BotID: "bot-1", CurrentPlatform: "telegram", ReplyTarget: "chat-1",
				SourceMessageID: "trigger-message", ReplySender: sender,
			}, channel.NewSendToolStreamCoordinator(), nil)
			ctx := context.Background()
			preview.Handle(ctx, agentevent.StreamEvent{
				Type: agentevent.ToolCallInputStart, ToolName: "send", ToolCallID: "call-1",
			})
			preview.Handle(ctx, agentevent.StreamEvent{
				Type: agentevent.ToolCallInputDelta, ToolCallID: "call-1", Delta: test.raw,
			})
			preview.Handle(ctx, agentevent.StreamEvent{
				Type: agentevent.ToolCallStart, ToolName: "send", ToolCallID: "call-1", Input: test.input,
			})

			if len(sender.streamOptions) != 1 {
				t.Fatalf("opened streams = %d, want 1", len(sender.streamOptions))
			}
			options := sender.streamOptions[0]
			if options.SourceMessageID != "trigger-message" {
				t.Fatalf("SourceMessageID = %q, want internal trigger ID", options.SourceMessageID)
			}
			if test.wantReply == "" {
				if options.Reply != nil {
					t.Fatalf("omitted reply_to produced Telegram reply %#v", options.Reply)
				}
				return
			}
			if options.Reply == nil || options.Reply.MessageID != test.wantReply || options.Reply.Target != "chat-1" {
				t.Fatalf("Telegram reply = %#v, want target chat-1 message %s", options.Reply, test.wantReply)
			}
		})
	}
}
