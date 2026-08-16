package timeline

import (
	"strings"
	"testing"
)

func TestRenderMessage_ImageRefsPopulated(t *testing.T) {
	msg := &ICMessage{
		MessageID:    "msg-1",
		ReceivedAtMs: 100,
		TimestampSec: 100,
		Content:      []ContentNode{{Type: "text", Text: "photo"}},
		Attachments: []Attachment{
			{Type: "image", ContentHash: "hash-1", MimeType: "image/jpeg", FilePath: "/data/.memoh/media/bot/ab/hash-1.jpg"},
			{Type: "file", ContentHash: "hash-2", MimeType: "application/pdf", FilePath: "/data/.memoh/media/bot/cd/hash-2.pdf"},
			{Type: "image", MimeType: "image/png"},
		},
		Conversation: ConversationMeta{Channel: "telegram", ConversationType: "private"},
	}

	seg := renderMessage(msg, RenderParams{})

	if len(seg.ImageRefs) != 1 {
		t.Fatalf("expected 1 image ref (only images with ContentHash), got %d", len(seg.ImageRefs))
	}
	if seg.ImageRefs[0].ContentHash != "hash-1" {
		t.Fatalf("expected hash-1, got %q", seg.ImageRefs[0].ContentHash)
	}
	if seg.ImageRefs[0].Mime != "image/jpeg" {
		t.Fatalf("expected image/jpeg, got %q", seg.ImageRefs[0].Mime)
	}
}

func TestRenderMessage_NoImageRefs(t *testing.T) {
	msg := &ICMessage{
		MessageID:    "msg-2",
		ReceivedAtMs: 200,
		TimestampSec: 200,
		Content:      []ContentNode{{Type: "text", Text: "text only"}},
		Conversation: ConversationMeta{Channel: "telegram", ConversationType: "private"},
	}

	seg := renderMessage(msg, RenderParams{})

	if len(seg.ImageRefs) != 0 {
		t.Fatalf("expected 0 image refs, got %d", len(seg.ImageRefs))
	}
}

func TestRenderMessage_PreservesAddressingFlagsInCanonicalContent(t *testing.T) {
	msg := &ICMessage{
		MessageID:    "msg-addressed",
		ReceivedAtMs: 300,
		TimestampSec: 300,
		Content:      []ContentNode{{Type: "text", Text: "please inspect this"}},
		MentionsMe:   true,
		RepliesToMe:  true,
		Conversation: ConversationMeta{Channel: "telegram", ConversationType: "group"},
	}

	seg := renderMessage(msg, RenderParams{})
	if len(seg.Content) != 1 {
		t.Fatalf("content pieces = %d, want 1", len(seg.Content))
	}
	for _, want := range []string{`mentions_me="true"`, `replies_to_me="true"`} {
		if !strings.Contains(seg.Content[0].Text, want) {
			t.Fatalf("canonical content missing %s: %s", want, seg.Content[0].Text)
		}
	}

	replayed := renderMessage(msg, RenderParams{})
	if replayed.Content[0].Text != seg.Content[0].Text {
		t.Fatalf("canonical addressing content changed across replay:\nfirst: %s\nagain: %s", seg.Content[0].Text, replayed.Content[0].Text)
	}
}

func TestRenderMessagePopulatesSlotIdentityAndEditTime(t *testing.T) {
	msg := &ICMessage{
		MessageID:       "msg-slot",
		ReceivedAtMs:    1000,
		TimestampSec:    1,
		EditedAtSec:     7,
		LastEventCursor: 4242,
		Content:         []ContentNode{{Type: "text", Text: "edited body"}},
		Conversation:    ConversationMeta{Channel: "telegram", ConversationType: "group"},
	}

	seg := renderMessage(msg, RenderParams{})

	if seg.MessageID != "msg-slot" {
		t.Fatalf("MessageID = %q, want msg-slot (coverage matching depends on it)", seg.MessageID)
	}
	if seg.EditedAtMs != 7000 {
		t.Fatalf("EditedAtMs = %d, want 7000 (EditedAtSec converted to ms)", seg.EditedAtMs)
	}
	if seg.LastEventCursor != 4242 {
		t.Fatalf("LastEventCursor = %d, want 4242", seg.LastEventCursor)
	}
}

func TestRenderDeletedMessagePopulatesSlotIdentityAndEditTime(t *testing.T) {
	msg := &ICMessage{
		MessageID:       "msg-deleted",
		ReceivedAtMs:    2000,
		TimestampSec:    2,
		EditedAtSec:     9,
		LastEventCursor: 5150,
		Deleted:         true,
		Conversation:    ConversationMeta{Channel: "telegram", ConversationType: "group"},
	}

	seg := renderMessage(msg, RenderParams{})

	if seg.MessageID != "msg-deleted" || seg.EditedAtMs != 9000 || seg.LastEventCursor != 5150 {
		t.Fatalf("deleted segment lost slot metadata: %+v", seg)
	}
}
