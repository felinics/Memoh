package view

import (
	"strings"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/turn"
)

func TestNewRequestUserTurnNilWithoutPersistedUserMessage(t *testing.T) {
	t.Parallel()

	cases := map[string]turn.StartTurnCommand{
		// Discuss-shaped commands carry composed context, not a user message.
		"empty command": {},
		// An attachment-only message persists no user turn
		// (prependTurnUserMessage), so nothing may project either.
		"attachment only": {
			Attachments: []turn.Attachment{{Type: "image", ContentHash: "hash-1"}},
		},
		// Whitespace is not a message.
		"blank query": {Query: "  \n "},
	}
	for name, cmd := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := NewRequestUserTurn(cmd, "turn-1"); got != nil {
				t.Fatalf("NewRequestUserTurn() = %#v, want nil", got)
			}
		})
	}
}

func TestNewRequestUserTurnTextMessage(t *testing.T) {
	t.Parallel()

	got := NewRequestUserTurn(turn.StartTurnCommand{
		BotID:                   "bot-1",
		Query:                   "  hello bot  ",
		ExternalMessageID:       "ext-1",
		CurrentChannel:          "Telegram",
		DisplayName:             "  Alice  ",
		SourceChannelIdentityID: "ci-1",
	}, "turn-1")
	if got == nil {
		t.Fatal("NewRequestUserTurn() = nil, want a projection")
	}
	if got.TurnID != "turn-1" || got.Role != "user" {
		t.Fatalf("identity = (%q, %q), want (turn-1, user)", got.TurnID, got.Role)
	}
	if got.Text != "hello bot" {
		t.Fatalf("Text = %q, want the trimmed query", got.Text)
	}
	if got.Platform != "telegram" {
		t.Fatalf("Platform = %q, want lowercased telegram", got.Platform)
	}
	if got.SenderDisplayName != "Alice" || got.SenderUserID != "ci-1" {
		t.Fatalf("sender = (%q, %q), want (Alice, ci-1)", got.SenderDisplayName, got.SenderUserID)
	}
	if got.ExternalMessageID != "ext-1" {
		t.Fatalf("ExternalMessageID = %q, want ext-1", got.ExternalMessageID)
	}
	if got.Timestamp.IsZero() {
		t.Fatal("Timestamp is zero, want admission time")
	}
}

func TestNewRequestUserTurnDisplayTextMirrorsPersistence(t *testing.T) {
	t.Parallel()

	// The projected text must equal the display text the persistence layer
	// stores (service_store.go), or the bubble visibly changes at handover.
	wrapped := turn.FormatUserHeaderFromMeta(turn.UserMessageMeta{DisplayName: "Alice"}, "real text")
	cases := map[string]struct {
		cmd  turn.StartTurnCommand
		want string
	}{
		"visible text wins": {
			cmd:  turn.StartTurnCommand{Query: "model facing", UserVisibleText: "user facing"},
			want: "user facing",
		},
		"envelope unwrapped": {
			cmd:  turn.StartTurnCommand{Query: wrapped},
			want: "real text",
		},
		"skill activation keeps visible text": {
			cmd: turn.StartTurnCommand{
				Query:           "summarize this",
				UserVisibleText: "summarize this",
				UserMessageKind: turn.UserMessageKindSkillActivation,
			},
			want: "summarize this",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := NewRequestUserTurn(tc.cmd, "turn-1")
			if got == nil {
				t.Fatal("NewRequestUserTurn() = nil, want a projection")
			}
			if got.Text != tc.want {
				t.Fatalf("Text = %q, want %q", got.Text, tc.want)
			}
		})
	}
}

func TestNewRequestUserTurnBareSkillActivation(t *testing.T) {
	t.Parallel()

	got := NewRequestUserTurn(turn.StartTurnCommand{
		UserMessageKind: turn.UserMessageKindSkillActivation,
		SkillActivation: &turn.SkillActivation{Prompt: "run it"},
	}, "turn-1")
	if got == nil {
		t.Fatal("NewRequestUserTurn() = nil, want a projection for a bare activation")
	}
	if got.Text != "" {
		t.Fatalf("Text = %q, want empty for a bare activation", got.Text)
	}
	if got.SkillActivation == nil || got.SkillActivation.Prompt != "run it" {
		t.Fatalf("SkillActivation = %#v, want the carried activation", got.SkillActivation)
	}
	if got.UserMessageKind != turn.UserMessageKindSkillActivation {
		t.Fatalf("UserMessageKind = %q, want %q", got.UserMessageKind, turn.UserMessageKindSkillActivation)
	}
}

func TestNewRequestUserTurnLocalPlatformHasNoSender(t *testing.T) {
	t.Parallel()

	got := NewRequestUserTurn(turn.StartTurnCommand{
		Query:          "hi",
		CurrentChannel: "local",
		DisplayName:    "Alice",
	}, "turn-1")
	if got == nil {
		t.Fatal("NewRequestUserTurn() = nil, want a projection")
	}
	if got.Platform != "" {
		t.Fatalf("Platform = %q, want empty for local", got.Platform)
	}
	if got.SenderDisplayName != "" || got.SenderUserID != "" {
		t.Fatalf("sender = (%q, %q), want empty without a platform", got.SenderDisplayName, got.SenderUserID)
	}
}

func TestNewRequestUserTurnReplyAndForward(t *testing.T) {
	t.Parallel()

	longPreview := strings.Repeat("长", uiReplyPreviewMaxRunes+10)
	got := NewRequestUserTurn(turn.StartTurnCommand{
		Query:                  "replying",
		SourceReplyToMessageID: "msg-9",
		ReplySender:            "Bob",
		ReplyPreview:           longPreview,
		ReplyAttachments:       []turn.Attachment{{Type: "image", ContentHash: "rh-1", Mime: "image/png"}},
		ForwardMessageID:       "fwd-1",
		ForwardFromUserID:      "user-2",
		ForwardSender:          "Carol",
		ForwardDate:            123,
	}, "turn-1")
	if got == nil {
		t.Fatal("NewRequestUserTurn() = nil, want a projection")
	}
	if got.Reply == nil {
		t.Fatal("Reply = nil, want the reply ref")
	}
	if got.Reply.MessageID != "msg-9" || got.Reply.Sender != "Bob" {
		t.Fatalf("Reply = %#v, want msg-9 from Bob", got.Reply)
	}
	if len([]rune(got.Reply.Preview)) > uiReplyPreviewMaxRunes {
		t.Fatalf("Reply.Preview = %d runes, want at most %d", len([]rune(got.Reply.Preview)), uiReplyPreviewMaxRunes)
	}
	if len(got.Reply.Attachments) != 1 || got.Reply.Attachments[0].ContentHash != "rh-1" {
		t.Fatalf("Reply.Attachments = %#v, want the single reply attachment", got.Reply.Attachments)
	}
	if got.Forward == nil {
		t.Fatal("Forward = nil, want the forward ref")
	}
	if got.Forward.MessageID != "fwd-1" || got.Forward.FromUserID != "user-2" || got.Forward.Sender != "Carol" || got.Forward.Date != 123 {
		t.Fatalf("Forward = %#v, want the carried forward fields", got.Forward)
	}
}

func TestUIAttachmentsFromTurnAttachments(t *testing.T) {
	t.Parallel()

	got := UIAttachmentsFromTurnAttachments("bot-1", []turn.Attachment{
		{
			Type:        "",
			Mime:        "image/png",
			ContentHash: "hash-1",
			Name:        "photo.png",
			Size:        42,
			Base64:      "AAAA",
			Metadata:    map[string]any{"storage_key": "key-1"},
		},
		{Type: "FILE", ContentHash: "hash-2"},
	})
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	first := got[0]
	if first.Type != "image" {
		t.Fatalf("Type = %q, want inferred image", first.Type)
	}
	if first.ID != "hash-1" || first.ContentHash != "hash-1" || first.BotID != "bot-1" {
		t.Fatalf("identity = (%q, %q, %q), want hash-1/hash-1/bot-1", first.ID, first.ContentHash, first.BotID)
	}
	if first.Base64 != "" {
		t.Fatal("Base64 copied into runtime state, want references only")
	}
	if first.StorageKey != "key-1" {
		t.Fatalf("StorageKey = %q, want key-1 from metadata", first.StorageKey)
	}
	if got[1].Type != "file" {
		t.Fatalf("second Type = %q, want lowercased file", got[1].Type)
	}
}

func TestNewRequestUserTurnTimestampRecent(t *testing.T) {
	t.Parallel()

	before := time.Now().UTC()
	got := NewRequestUserTurn(turn.StartTurnCommand{Query: "hi"}, "turn-1")
	if got == nil {
		t.Fatal("NewRequestUserTurn() = nil, want a projection")
	}
	if got.Timestamp.Before(before) || time.Since(got.Timestamp) > time.Minute {
		t.Fatalf("Timestamp = %v, want admission time", got.Timestamp)
	}
}
