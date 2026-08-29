package weixin

import (
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestBuildInboundMessage_TextOnly(t *testing.T) {
	msg := WeixinMessage{
		MessageID:    12345,
		Seq:          1,
		FromUserID:   "user1@im.wechat",
		CreateTimeMs: 1700000000000,
		ContextToken: "ctx-tok-1",
		ItemList: []MessageItem{
			{Type: ItemTypeText, TextItem: &TextItem{Text: "hello world"}},
		},
	}

	inbound, ok := buildInboundMessage(msg)
	if !ok {
		t.Fatal("expected valid inbound message")
	}
	if inbound.Channel != Type {
		t.Errorf("channel = %v, want %v", inbound.Channel, Type)
	}
	if inbound.Message.Text != "hello world" {
		t.Errorf("text = %q, want %q", inbound.Message.Text, "hello world")
	}
	if inbound.ReplyTarget != "user1@im.wechat" {
		t.Errorf("reply_target = %q", inbound.ReplyTarget)
	}
	if inbound.Sender.SubjectID != "user1@im.wechat" {
		t.Errorf("sender = %q", inbound.Sender.SubjectID)
	}
	if inbound.Conversation.Type != channel.ConversationTypePrivate {
		t.Errorf("conv_type = %q", inbound.Conversation.Type)
	}
	if inbound.Message.ID != "12345:1" {
		t.Errorf("message_id = %q", inbound.Message.ID)
	}
	meta := inbound.Metadata
	if meta == nil {
		t.Fatal("metadata is nil")
	}
	if meta["context_token"] != "ctx-tok-1" {
		t.Errorf("context_token = %v", meta["context_token"])
	}
}

func TestBuildInboundMessage_Empty(t *testing.T) {
	msg := WeixinMessage{
		MessageID:  1,
		FromUserID: "u1",
		ItemList:   []MessageItem{},
	}
	_, ok := buildInboundMessage(msg)
	if ok {
		t.Error("expected false for empty message")
	}
}

func TestBuildInboundMessage_NoFrom(t *testing.T) {
	msg := WeixinMessage{
		MessageID: 1,
		ItemList:  []MessageItem{{Type: ItemTypeText, TextItem: &TextItem{Text: "hi"}}},
	}
	_, ok := buildInboundMessage(msg)
	if ok {
		t.Error("expected false for message without from_user_id")
	}
}

func TestBuildInboundMessage_ImageAttachment(t *testing.T) {
	msg := WeixinMessage{
		MessageID:  1,
		FromUserID: "u1",
		ItemList: []MessageItem{
			{
				Type: ItemTypeImage,
				ImageItem: &ImageItem{
					Media: &CDNMedia{
						EncryptQueryParam: "enc-param-1",
						AESKey:            "QUJDREVGR0hJSktMTU5PUA==", // base64 of 16 bytes
					},
				},
			},
		},
	}

	inbound, ok := buildInboundMessage(msg)
	if !ok {
		t.Fatal("expected valid inbound message")
	}
	if len(inbound.Message.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(inbound.Message.Attachments))
	}
	att := inbound.Message.Attachments[0]
	if att.Type != channel.AttachmentImage {
		t.Errorf("attachment type = %v", att.Type)
	}
	if att.PlatformKey != "enc-param-1" {
		t.Errorf("platform_key = %q", att.PlatformKey)
	}
	if att.SourcePlatform != "weixin" {
		t.Errorf("source_platform = %q", att.SourcePlatform)
	}
}

func TestBuildInboundMessage_VoiceWithText(t *testing.T) {
	msg := WeixinMessage{
		MessageID:  1,
		FromUserID: "u1",
		ItemList: []MessageItem{
			{
				Type: ItemTypeVoice,
				VoiceItem: &VoiceItem{
					Text: "transcribed voice text",
				},
			},
		},
	}

	inbound, ok := buildInboundMessage(msg)
	if !ok {
		t.Fatal("expected valid inbound message")
	}
	if !strings.Contains(inbound.Message.Text, "transcribed voice text") {
		t.Errorf("text = %q, expected voice transcription", inbound.Message.Text)
	}
	if len(inbound.Message.Attachments) != 0 {
		t.Errorf("attachments = %d, want 0 (voice with text should be text only)", len(inbound.Message.Attachments))
	}
}

func TestBuildInboundMessage_QuotedText(t *testing.T) {
	msg := WeixinMessage{
		MessageID:  1,
		FromUserID: "u1",
		ItemList: []MessageItem{
			{
				Type:     ItemTypeText,
				TextItem: &TextItem{Text: "my reply"},
				RefMsg: &RefMessage{
					Title: "Original",
					MessageItem: &MessageItem{
						MsgID:    "source-msg",
						Type:     ItemTypeText,
						TextItem: &TextItem{Text: "original text"},
					},
				},
			},
		},
	}

	inbound, ok := buildInboundMessage(msg)
	if !ok {
		t.Fatal("expected valid inbound message")
	}
	if inbound.Message.Text != "my reply" {
		t.Errorf("text = %q, want original reply body only", inbound.Message.Text)
	}
	if inbound.Message.Reply == nil {
		t.Fatal("expected reply ref")
	}
	if inbound.Message.Reply.MessageID != "source-msg" ||
		inbound.Message.Reply.Sender != "Original" ||
		inbound.Message.Reply.Preview != "original text" {
		t.Fatalf("unexpected reply ref: %#v", inbound.Message.Reply)
	}
}

func TestBuildInboundMessage_FileAttachment(t *testing.T) {
	msg := WeixinMessage{
		MessageID:  1,
		FromUserID: "u1",
		ItemList: []MessageItem{
			{
				Type: ItemTypeFile,
				FileItem: &FileItem{
					Media: &CDNMedia{
						EncryptQueryParam: "file-enc-1",
						AESKey:            "QUJDREVGR0hJSktMTU5PUA==",
					},
					FileName: "report.pdf",
				},
			},
		},
	}

	inbound, ok := buildInboundMessage(msg)
	if !ok {
		t.Fatal("expected valid inbound message")
	}
	if len(inbound.Message.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(inbound.Message.Attachments))
	}
	att := inbound.Message.Attachments[0]
	if att.Type != channel.AttachmentFile {
		t.Errorf("type = %v", att.Type)
	}
	if att.Name != "report.pdf" {
		t.Errorf("name = %q", att.Name)
	}
}
