package dingtalk

import (
	"testing"
	"time"

	"github.com/felinics/dingtalk-stream-sdk-go/chatbot"

	"github.com/felinics/memoh/internal/channel"
)

func TestBuildInboundMessageTextGroup(t *testing.T) {
	createdAt := time.Date(2026, 5, 16, 12, 0, 0, 0, time.UTC).UnixMilli()
	msg, ok := buildInboundMessage(&chatbot.BotCallbackDataModel{
		MsgId:             "msg-1",
		Msgtype:           "text",
		Text:              chatbot.BotCallbackDataTextModel{Content: " hello "},
		ConversationType:  "2",
		ConversationId:    "cid-1",
		ConversationTitle: "Project Room",
		SenderId:          "union-1",
		SenderStaffId:     "staff-1",
		SenderNick:        "Alice",
		SenderCorpId:      "corp-1",
		ChatbotCorpId:     "bot-corp",
		ChatbotUserId:     "bot-user",
		CreateAt:          createdAt,
		IsAdmin:           true,
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.Channel != Type {
		t.Fatalf("channel = %q, want %q", msg.Channel, Type)
	}
	if msg.Message.ID != "msg-1" || msg.Message.Text != "hello" || msg.Message.Format != channel.MessageFormatPlain {
		t.Fatalf("unexpected message: %#v", msg.Message)
	}
	if msg.ReplyTarget != "group:cid-1" {
		t.Fatalf("reply target = %q, want group:cid-1", msg.ReplyTarget)
	}
	if msg.Sender.SubjectID != "staff-1" || msg.Sender.Attributes["user_id"] != "union-1" {
		t.Fatalf("unexpected sender: %#v", msg.Sender)
	}
	if msg.Conversation.ID != "cid-1" || msg.Conversation.Type != channel.ConversationTypeGroup || msg.Conversation.Name != "Project Room" {
		t.Fatalf("unexpected conversation: %#v", msg.Conversation)
	}
	if got := msg.ReceivedAt; !got.Equal(time.UnixMilli(createdAt).UTC()) {
		t.Fatalf("received at = %s, want %s", got, time.UnixMilli(createdAt).UTC())
	}
	if msg.Metadata["is_admin"] != true || msg.Metadata["conversation_id"] != "cid-1" {
		t.Fatalf("unexpected metadata: %#v", msg.Metadata)
	}
}

func TestBuildInboundMessagePrivatePrefersStaffIDReplyTarget(t *testing.T) {
	msg, ok := buildInboundMessage(&chatbot.BotCallbackDataModel{
		MsgId:            "msg-2",
		Msgtype:          "text",
		Text:             chatbot.BotCallbackDataTextModel{Content: "ping"},
		ConversationType: "1",
		ConversationId:   "private-conv",
		SenderId:         "union-2",
		SenderStaffId:    "staff-2",
	})
	if !ok {
		t.Fatal("expected inbound message")
	}
	if msg.ReplyTarget != "user:staff-2" {
		t.Fatalf("reply target = %q, want user:staff-2", msg.ReplyTarget)
	}
	if msg.Conversation.Type != channel.ConversationTypePrivate {
		t.Fatalf("conversation type = %q, want private", msg.Conversation.Type)
	}
}

func TestExtractContentAttachments(t *testing.T) {
	tests := []struct {
		name     string
		data     *chatbot.BotCallbackDataModel
		wantType channel.AttachmentType
		wantKey  string
	}{
		{
			name: "picture",
			data: &chatbot.BotCallbackDataModel{
				Msgtype: "picture",
				Content: map[string]any{"downloadCode": "dl-img", "width": float64(640), "height": float64(480)},
			},
			wantType: channel.AttachmentImage,
			wantKey:  "dl-img",
		},
		{
			name: "file",
			data: &chatbot.BotCallbackDataModel{
				Msgtype: "file",
				Content: map[string]any{"downloadCode": "dl-file", "fileName": "report.pdf"},
			},
			wantType: channel.AttachmentFile,
			wantKey:  "dl-file",
		},
		{
			name: "video",
			data: &chatbot.BotCallbackDataModel{
				Msgtype: "video",
				Content: map[string]any{"videoMediaId": "video-1", "duration": float64(12)},
			},
			wantType: channel.AttachmentVideo,
			wantKey:  "video-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, _, attachments := extractContent(tt.data)
			if text != "" {
				t.Fatalf("text = %q, want empty", text)
			}
			if len(attachments) != 1 {
				t.Fatalf("attachments len = %d, want 1", len(attachments))
			}
			if attachments[0].Type != tt.wantType || attachments[0].PlatformKey != tt.wantKey || attachments[0].SourcePlatform != Type.String() {
				t.Fatalf("unexpected attachment: %#v", attachments[0])
			}
		})
	}
}

func TestExtractContentAudioRecognitionAndRichText(t *testing.T) {
	text, format, attachments := extractContent(&chatbot.BotCallbackDataModel{
		Msgtype: "audio",
		Content: map[string]any{"recognition": "voice transcript", "downloadCode": "voice-code"},
	})
	if text != "voice transcript" || format != channel.MessageFormatPlain || len(attachments) != 0 {
		t.Fatalf("unexpected audio recognition result: text=%q format=%q attachments=%#v", text, format, attachments)
	}

	text, format, attachments = extractRichText(map[string]any{
		"richText": []map[string]any{
			{"type": "text", "text": "hello"},
			{"type": "picture", "downloadCode": "pic-1", "width": 320, "height": 200},
		},
	})
	if text != "hello" || format != channel.MessageFormatPlain || len(attachments) != 1 {
		t.Fatalf("unexpected richtext result: text=%q format=%q attachments=%#v", text, format, attachments)
	}
	if attachments[0].Type != channel.AttachmentImage || attachments[0].PlatformKey != "pic-1" {
		t.Fatalf("unexpected richtext attachment: %#v", attachments[0])
	}
}

func TestBuildInboundMessageIgnoresEmptyContent(t *testing.T) {
	if _, ok := buildInboundMessage(&chatbot.BotCallbackDataModel{Msgtype: "unknown"}); ok {
		t.Fatal("expected empty callback to be ignored")
	}
}
