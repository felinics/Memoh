package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"

	"github.com/felinics/memoh/internal/channel"
)

type fakeProcessingReactionGateway struct {
	addCalls    []struct{ messageID, reactionType string }
	removeCalls []struct{ messageID, reactionID string }
	addResponse []struct {
		reactionID string
		err        error
	}
	removeErr error
}

func (g *fakeProcessingReactionGateway) Add(_ context.Context, messageID, reactionType string) (string, error) {
	g.addCalls = append(g.addCalls, struct{ messageID, reactionType string }{
		messageID:    messageID,
		reactionType: reactionType,
	})
	if len(g.addResponse) == 0 {
		return "reaction-default", nil
	}
	resp := g.addResponse[0]
	g.addResponse = g.addResponse[1:]
	return resp.reactionID, resp.err
}

func (g *fakeProcessingReactionGateway) Remove(_ context.Context, messageID, reactionID string) error {
	g.removeCalls = append(g.removeCalls, struct{ messageID, reactionID string }{
		messageID:  messageID,
		reactionID: reactionID,
	})
	return g.removeErr
}

func TestResolveFeishuReceiveID(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw       string
		wantID    string
		wantType  string
		shouldErr bool
	}{
		{raw: "open_id:ou_123", wantID: "ou_123", wantType: "open_id"},
		{raw: "user_id:uu_123", wantID: "uu_123", wantType: "user_id"},
		{raw: "chat_id:oc_123", wantID: "oc_123", wantType: "chat_id"},
		{raw: "ou_999", wantID: "ou_999", wantType: "open_id"},
		{raw: "", shouldErr: true},
	}
	for _, tc := range cases {
		id, idType, err := resolveFeishuReceiveID(tc.raw)
		if tc.shouldErr {
			if err == nil {
				t.Fatalf("expected error for %q", tc.raw)
			}
			continue
		}
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", tc.raw, err)
		}
		if id != tc.wantID || idType != tc.wantType {
			t.Fatalf("unexpected result for %q: %s %s", tc.raw, id, idType)
		}
	}
}

func TestExtractFeishuInboundP2P(t *testing.T) {
	t.Parallel()

	text := `{"text":"hi"}`
	msgType := larkim.MsgTypeText
	chatType := "p2p"
	chatID := "oc_1"
	userID := "u_1"
	openID := "ou_1"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{
					UserId: &userID,
					OpenId: &openID,
				},
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if got.Message.PlainText() != "hi" {
		t.Fatalf("unexpected text: %s", got.Message.PlainText())
	}
	if got.ReplyTarget != "ou_1" {
		t.Fatalf("unexpected reply target: %s", got.ReplyTarget)
	}
	if got.Sender.DisplayName != "" {
		t.Fatalf("expected empty sender display name, got: %s", got.Sender.DisplayName)
	}
	if got.Sender.SubjectID != "ou_1" {
		t.Fatalf("unexpected sender subject id: %s", got.Sender.SubjectID)
	}
	if got.Sender.Attribute("open_id") != "ou_1" {
		t.Fatalf("unexpected sender open_id: %s", got.Sender.Attribute("open_id"))
	}
	if got.Sender.Attribute("user_id") != "u_1" {
		t.Fatalf("unexpected sender user_id: %s", got.Sender.Attribute("user_id"))
	}
	if mentioned, _ := got.Metadata["is_mentioned"].(bool); mentioned {
		t.Fatalf("unexpected mention flag for p2p message")
	}
}

func TestExtractFeishuInboundGroup(t *testing.T) {
	t.Parallel()

	text := `{"text":"hi"}`
	msgType := larkim.MsgTypeText
	chatType := "group"
	chatID := "oc_2"
	userID := "u_2"
	openID := "ou_2"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
			Sender: &larkim.EventSender{
				SenderId: &larkim.UserId{
					UserId: &userID,
					OpenId: &openID,
				},
			},
		},
	}
	got := extractFeishuInbound(event, "ou_bot")
	if got.ReplyTarget != "chat_id:oc_2" {
		t.Fatalf("unexpected reply target: %s", got.ReplyTarget)
	}
	if mentioned, _ := got.Metadata["is_mentioned"].(bool); mentioned {
		t.Fatalf("unexpected mention flag for group message without mentions")
	}
}

func TestExtractFeishuInboundNonText(t *testing.T) {
	t.Parallel()

	msgType := "image"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if got.Message.PlainText() != "" {
		t.Fatalf("expected empty text, got %s", got.Message.PlainText())
	}
}

func TestExtractFeishuInboundImageAttachmentReference(t *testing.T) {
	t.Parallel()

	content := `{"image_key":"img_1"}`
	msgType := larkim.MsgTypeImage
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &content,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if len(got.Message.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(got.Message.Attachments))
	}
	att := got.Message.Attachments[0]
	if att.Type != channel.AttachmentImage {
		t.Fatalf("unexpected attachment type: %s", att.Type)
	}
	if att.PlatformKey != "img_1" {
		t.Fatalf("unexpected platform key: %s", att.PlatformKey)
	}
	if att.SourcePlatform != Type.String() {
		t.Fatalf("unexpected source platform: %s", att.SourcePlatform)
	}
	if att.Metadata == nil || att.Metadata["message_id"] == nil {
		t.Fatal("expected message_id in attachment metadata")
	}
}

func TestExtractFeishuInboundFileAttachmentInfersVideoType(t *testing.T) {
	t.Parallel()

	content := `{"file_key":"file_1","file_name":"clip.mp4","mime_type":"video/mp4"}`
	msgType := larkim.MsgTypeFile
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &content,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if len(got.Message.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(got.Message.Attachments))
	}
	att := got.Message.Attachments[0]
	if att.Type != channel.AttachmentVideo {
		t.Fatalf("expected inferred video type, got %s", att.Type)
	}
	if att.Mime != "video/mp4" {
		t.Fatalf("expected normalized mime video/mp4, got %s", att.Mime)
	}
}

func TestFeishuDescriptorIncludesStreamingAndMedia(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	desc := adapter.Descriptor()
	caps := desc.Capabilities
	if !caps.Streaming {
		t.Fatal("expected streaming capability")
	}
	if !caps.Media {
		t.Fatal("expected media capability")
	}
	if !caps.Markdown {
		t.Fatal("expected markdown capability for lark_md card rendering")
	}
	policy := channel.NormalizeOutboundPolicy(desc.OutboundPolicy)
	if policy.RichTextChunkLimit != feishuStreamMaxRunes {
		t.Fatalf("RichTextChunkLimit = %d, want %d", policy.RichTextChunkLimit, feishuStreamMaxRunes)
	}
}

func TestFeishuResolveAttachmentRequiresPlatformKey(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	_, err := adapter.ResolveAttachment(context.Background(), channel.ChannelConfig{}, channel.Attachment{})
	if err == nil {
		t.Fatal("expected error when platform_key is missing")
	}
	if !strings.Contains(err.Error(), "platform_key") {
		t.Fatalf("expected platform_key error, got: %v", err)
	}
}

func TestFeishuResolveAttachmentRequiresMessageID(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	_, err := adapter.ResolveAttachment(context.Background(), channel.ChannelConfig{}, channel.Attachment{
		PlatformKey: "img_123",
	})
	if err == nil {
		t.Fatal("expected error when message_id is missing")
	}
	if !strings.Contains(err.Error(), "message_id") {
		t.Fatalf("expected message_id error, got: %v", err)
	}
}

func TestIsFeishuImageAttachment(t *testing.T) {
	t.Parallel()

	if !isFeishuImageAttachment(channel.Attachment{Type: channel.AttachmentImage}) {
		t.Fatal("expected image type to be identified as image")
	}
	if !isFeishuImageAttachment(channel.Attachment{Type: channel.AttachmentGIF}) {
		t.Fatal("expected gif type to be identified as image")
	}
	if !isFeishuImageAttachment(channel.Attachment{Mime: "image/jpeg"}) {
		t.Fatal("expected image/ mime to be identified as image")
	}
	if isFeishuImageAttachment(channel.Attachment{Type: channel.AttachmentFile}) {
		t.Fatal("expected file type to not be identified as image")
	}
	if isFeishuImageAttachment(channel.Attachment{Type: channel.AttachmentAudio}) {
		t.Fatal("expected audio type to not be identified as image")
	}
}

func TestResolveFeishuFileType(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		file string
		mime string
		want string
	}{
		{name: "video mime", file: "clip.bin", mime: "video/mp4", want: larkim.FileTypeMp4},
		{name: "pdf mime", file: "doc.bin", mime: "application/pdf", want: larkim.FileTypePdf},
		{name: "doc ext", file: "a.docx", mime: "application/octet-stream", want: larkim.FileTypeDoc},
		{name: "xls ext", file: "a.xlsx", mime: "application/octet-stream", want: larkim.FileTypeXls},
		{name: "ppt ext", file: "a.pptx", mime: "application/octet-stream", want: larkim.FileTypePpt},
		{name: "zip mime", file: "a.bin", mime: "application/zip", want: larkim.FileTypeStream},
		{name: "tar gz ext", file: "backup.tar.gz", mime: "application/octet-stream", want: larkim.FileTypeStream},
		{name: "default stream", file: "notes.txt", mime: "text/plain", want: larkim.FileTypeStream},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := resolveFeishuFileType(tc.file, tc.mime); got != tc.want {
				t.Fatalf("resolveFeishuFileType(%q,%q)=%q want=%q", tc.file, tc.mime, got, tc.want)
			}
		})
	}
}

func TestBuildFeishuStreamCardContent(t *testing.T) {
	t.Parallel()

	payload, err := buildFeishuStreamCardContent("hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("unexpected json error: %v", err)
	}
	cfg, ok := parsed["config"].(map[string]any)
	if !ok {
		t.Fatalf("missing config: %+v", parsed)
	}
	value, ok := cfg["update_multi"].(bool)
	if !ok || !value {
		t.Fatalf("expected update_multi=true, got: %#v", cfg["update_multi"])
	}
}

func TestBuildFeishuStreamCardContentWithState(t *testing.T) {
	t.Parallel()

	payload, err := buildFeishuStreamCardContent("answer body")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(payload, "answer body") {
		t.Fatalf("expected stream body content in payload: %s", payload)
	}
	if strings.Contains(payload, "Tools:**") || strings.Contains(payload, "Calling:") {
		t.Fatalf("expected no tool/process panel in payload: %s", payload)
	}
}

func TestBuildFeishuOutboundContentPlainTextKeepsInteractiveCard(t *testing.T) {
	t.Parallel()

	msgType, content, err := buildFeishuOutboundContent(channel.Message{Text: "你好 **bold**"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg type = %q, want %q", msgType, larkim.MsgTypeInteractive)
	}
	if !strings.Contains(content, "你好 **bold**") {
		t.Fatalf("expected interactive card content to preserve plain text, got %s", content)
	}
}

func TestBuildFeishuOutboundContentPlainFormatUsesPlainTextCard(t *testing.T) {
	t.Parallel()

	raw := `literal **bold** [x](https://example.com) <at user_id="ou_123"></at>`
	msgType, content, err := buildFeishuOutboundContent(channel.Message{
		Text:   raw,
		Format: channel.MessageFormatPlain,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg type = %q, want %q", msgType, larkim.MsgTypeInteractive)
	}
	text := decodeFeishuCardTextObject(t, content)
	if text["tag"] != "plain_text" {
		t.Fatalf("plain format should use plain_text, got %#v", text)
	}
	if text["content"] != raw {
		t.Fatalf("plain card content = %q, want %q", text["content"], raw)
	}
}

func TestBuildFeishuOutboundContentRichPartsUsesInteractiveCard(t *testing.T) {
	t.Parallel()

	msgType, content, err := buildFeishuOutboundContent(channel.Message{
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: "bold", Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != larkim.MsgTypeInteractive {
		t.Fatalf("msg type = %q, want %q", msgType, larkim.MsgTypeInteractive)
	}
	if !strings.Contains(content, `"tag":"lark_md"`) || !strings.Contains(content, "**bold**") {
		t.Fatalf("expected interactive card lark_md content, got %s", content)
	}
}

func decodeFeishuCardTextObject(t *testing.T, content string) map[string]any {
	t.Helper()

	var card struct {
		Elements []struct {
			Fields []struct {
				Text map[string]any `json:"text"`
			} `json:"fields"`
		} `json:"elements"`
	}
	if err := json.Unmarshal([]byte(content), &card); err != nil {
		t.Fatalf("content is not json: %v", err)
	}
	if len(card.Elements) == 0 || len(card.Elements[0].Fields) == 0 || card.Elements[0].Fields[0].Text == nil {
		t.Fatalf("missing card text object: %s", content)
	}
	return card.Elements[0].Fields[0].Text
}

func TestBuildFeishuOutboundContentLongRichPartsFallsBackToText(t *testing.T) {
	t.Parallel()

	msgType, content, err := buildFeishuOutboundContent(channel.Message{
		Parts: []channel.MessagePart{
			{Type: channel.MessagePartText, Text: strings.Repeat("你", feishuStreamMaxRunes+100), Styles: []channel.MessageTextStyle{channel.MessageStyleBold}},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msgType != larkim.MsgTypeText {
		t.Fatalf("msg type = %q, want %q", msgType, larkim.MsgTypeText)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(content), &got); err != nil {
		t.Fatalf("content is not json: %v", err)
	}
	if len([]rune(got["text"])) > feishuStreamMaxRunes+4 {
		t.Fatalf("text content should be truncated, got len=%d", len([]rune(got["text"])))
	}
	if strings.Contains(got["text"], "**") {
		t.Fatalf("long rich fallback should be plain text, got %q", got["text"][:20])
	}
}

func TestNormalizeFeishuStreamText(t *testing.T) {
	t.Parallel()

	if got := normalizeFeishuStreamText("   "); got != feishuStreamThinkingText {
		t.Fatalf("unexpected thinking text: %s", got)
	}
	long := strings.Repeat("a", feishuStreamMaxRunes+100)
	got := normalizeFeishuStreamText(long)
	if len([]rune(got)) > feishuStreamMaxRunes+4 {
		t.Fatalf("expected truncated text, got len=%d", len([]rune(got)))
	}
	if !strings.HasPrefix(got, "...\n") {
		t.Fatalf("expected truncation prefix, got: %s", got[:4])
	}
}

func TestProcessFeishuCardMarkdown(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"literal newline", "a\\nb", "a\nb"},
		{"atx h1", "# Title", "**Title**"},
		{"atx h2", "## Section", "**Section**"},
		{"atx h6", "###### Small", "**Small**"},
		{"heading with newline", "# Hi\n\nBody", "**Hi**\n\nBody"},
		{"no heading", "plain **bold**", "plain **bold**"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := processFeishuCardMarkdown(tt.in)
			if got != tt.want {
				t.Errorf("processFeishuCardMarkdown() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractFeishuInboundMentionFallbackNoBotID(t *testing.T) {
	t.Parallel()

	text := `{"text":"@bot hi","mentions":[{"key":"@bot"}]}`
	msgType := larkim.MsgTypeText
	chatType := "group"
	chatID := "oc_3"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	mentioned, ok := got.Metadata["is_mentioned"].(bool)
	if !ok || !mentioned {
		t.Fatalf("expected mention flag to be true (fallback)")
	}
}

func TestExtractFeishuInboundMentionBotMatched(t *testing.T) {
	t.Parallel()

	text := `{"text":"hello"}`
	msgType := larkim.MsgTypeText
	chatType := "group"
	chatID := "oc_mention_event"
	botOpenID := "ou_bot_123"
	mention := larkim.NewMentionEventBuilder().
		Key("@_user_1").
		Id(larkim.NewUserIdBuilder().OpenId(botOpenID).Build()).
		Build()
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
				Mentions:    []*larkim.MentionEvent{mention},
			},
		},
	}
	got := extractFeishuInbound(event, botOpenID)
	mentioned, ok := got.Metadata["is_mentioned"].(bool)
	if !ok || !mentioned {
		t.Fatalf("expected mention flag when bot is mentioned")
	}
	if alias, _ := got.Metadata["bot_alias"].(string); alias != "@open_id:"+botOpenID {
		t.Fatalf("bot_alias = %q, want current bot mention alias", alias)
	}
}

func TestExtractFeishuInboundMentionKeyRewriteAndTargets(t *testing.T) {
	t.Parallel()

	text := `{"text":"@_user_1 hello @_user_2"}`
	msgType := larkim.MsgTypeText
	chatType := "group"
	chatID := "oc_mention_rewrite"

	openID1 := "ou_user_1"
	name1 := "Alice"
	mention1 := larkim.NewMentionEventBuilder().
		Key("@_user_1").
		Name(name1).
		Id(larkim.NewUserIdBuilder().OpenId(openID1).Build()).
		Build()

	userID2 := "u_user_2"
	name2 := "Bob"
	mention2 := larkim.NewMentionEventBuilder().
		Key("@_user_2").
		Name(name2).
		Id(larkim.NewUserIdBuilder().UserId(userID2).Build()).
		Build()

	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
				Mentions:    []*larkim.MentionEvent{mention1, mention2},
			},
		},
	}

	got := extractFeishuInbound(event, "ou_bot_123")
	if got.Message.PlainText() != "@Alice hello @Bob" {
		t.Fatalf("unexpected rewritten text: %q", got.Message.PlainText())
	}

	targets, ok := got.Metadata["mentioned_targets"].([]string)
	if !ok {
		t.Fatalf("expected mentioned_targets to be []string, got %#v", got.Metadata["mentioned_targets"])
	}
	if len(targets) != 2 || targets[0] != "open_id:ou_user_1" || targets[1] != "user_id:u_user_2" {
		t.Fatalf("unexpected mentioned_targets: %#v", targets)
	}

	mentions, ok := got.Metadata["mentions"].([]map[string]any)
	if !ok || len(mentions) != 2 {
		t.Fatalf("expected mentions metadata with 2 entries, got %#v", got.Metadata["mentions"])
	}
	if mentions[0]["target"] != "open_id:ou_user_1" || mentions[1]["target"] != "user_id:u_user_2" {
		t.Fatalf("unexpected mention targets in metadata: %#v", mentions)
	}
}

func TestExtractFeishuInboundMentionOtherUserIgnored(t *testing.T) {
	t.Parallel()

	text := `{"text":"hello"}`
	msgType := larkim.MsgTypeText
	chatType := "group"
	chatID := "oc_mention_other"
	otherOpenID := "ou_other_user"
	mention := larkim.NewMentionEventBuilder().
		Key("@_user_1").
		Id(larkim.NewUserIdBuilder().OpenId(otherOpenID).Build()).
		Build()
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &text,
				ChatType:    &chatType,
				ChatId:      &chatID,
				Mentions:    []*larkim.MentionEvent{mention},
			},
		},
	}
	got := extractFeishuInbound(event, "ou_bot_123")
	if mentioned, _ := got.Metadata["is_mentioned"].(bool); mentioned {
		t.Fatalf("expected no mention flag when another user is mentioned")
	}
}

func TestExtractFeishuInboundPostMentionFallback(t *testing.T) {
	t.Parallel()

	content := `{"title":"","content":[[{"tag":"at","user_name":"bot"},{"tag":"text","text":" hi"}]]}`
	msgType := larkim.MsgTypePost
	chatType := "group"
	chatID := "oc_post_1"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &content,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if got.Message.PlainText() == "" {
		t.Fatalf("expected post message to be converted into text")
	}
	mentioned, ok := got.Metadata["is_mentioned"].(bool)
	if !ok || !mentioned {
		t.Fatalf("expected mention flag for post message (fallback)")
	}
}

func TestExtractFeishuInboundPostMentionBotMatched(t *testing.T) {
	t.Parallel()

	botOpenID := "ou_bot_123"
	content := `{"title":"","content":[[{"tag":"at","user_id":"ou_bot_123"},{"tag":"text","text":" hi"}]]}`
	msgType := larkim.MsgTypePost
	chatType := "group"
	chatID := "oc_post_bot"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &content,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
		},
	}
	got := extractFeishuInbound(event, botOpenID)
	mentioned, ok := got.Metadata["is_mentioned"].(bool)
	if !ok || !mentioned {
		t.Fatalf("expected mention flag for post with bot user_id")
	}
}

func TestExtractFeishuInboundPostRootContent(t *testing.T) {
	t.Parallel()

	// Feishu event payload uses root-level content
	content := `{"title":"","content":[[{"tag":"img","image_key":"img_v3_02uv_81bc4785-24d6-4fe2-b841-c3c4c691fc0g","width":1438,"height":810}],[{"tag":"text","text":"这是什么作品","style":[]}]]}`
	msgType := larkim.MsgTypePost
	chatType := "p2p"
	chatID := "oc_eb2b5e623f3a21e288fce40878564f8e"
	msgID := "om_x100b5606f7fc6ca4b376e5432634210"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageId:   &msgID,
				MessageType: &msgType,
				Content:     &content,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
		},
	}
	got := extractFeishuInbound(event, "")
	if got.Message.PlainText() != "这是什么作品" {
		t.Fatalf("expected text 这是什么作品, got %q", got.Message.PlainText())
	}
	if len(got.Message.Attachments) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(got.Message.Attachments))
	}
	if got.Message.Attachments[0].PlatformKey != "img_v3_02uv_81bc4785-24d6-4fe2-b841-c3c4c691fc0g" {
		t.Fatalf("unexpected platform_key: %q", got.Message.Attachments[0].PlatformKey)
	}
}

func TestExtractFeishuInboundPostMentionOtherIgnored(t *testing.T) {
	t.Parallel()

	content := `{"title":"","content":[[{"tag":"at","user_id":"ou_someone_else"},{"tag":"text","text":" hi"}]]}`
	msgType := larkim.MsgTypePost
	chatType := "group"
	chatID := "oc_post_other"
	event := &larkim.P2MessageReceiveV1{
		Event: &larkim.P2MessageReceiveV1Data{
			Message: &larkim.EventMessage{
				MessageType: &msgType,
				Content:     &content,
				ChatType:    &chatType,
				ChatId:      &chatID,
			},
		},
	}
	got := extractFeishuInbound(event, "ou_bot_123")
	if mentioned, _ := got.Metadata["is_mentioned"].(bool); mentioned {
		t.Fatalf("expected no mention for post mentioning other user")
	}
}

func TestResolveConfiguredBotOpenIDPrefersSelfIdentity(t *testing.T) {
	t.Parallel()

	cfg := channel.ChannelConfig{
		SelfIdentity: map[string]any{
			"open_id": "ou_self_1",
		},
		ExternalIdentity: "open_id:ou_external_1",
	}
	if got := resolveConfiguredBotOpenID(cfg); got != "ou_self_1" {
		t.Fatalf("expected self identity open_id, got %q", got)
	}
}

func TestResolveConfiguredBotOpenIDFromExternalIdentity(t *testing.T) {
	t.Parallel()

	cfg := channel.ChannelConfig{ExternalIdentity: "open_id:ou_external_2"}
	if got := resolveConfiguredBotOpenID(cfg); got != "ou_external_2" {
		t.Fatalf("expected external identity open_id, got %q", got)
	}
}

func TestResolveConfiguredBotOpenIDIgnoresNonOpenIDExternalIdentity(t *testing.T) {
	t.Parallel()

	cfg := channel.ChannelConfig{ExternalIdentity: "chat_id:oc_group_1"}
	if got := resolveConfiguredBotOpenID(cfg); got != "" {
		t.Fatalf("expected empty open_id for non-open external identity, got %q", got)
	}
}

func TestAddProcessingReactionFirstSuccess(t *testing.T) {
	t.Parallel()

	gateway := &fakeProcessingReactionGateway{
		addResponse: []struct {
			reactionID string
			err        error
		}{
			{reactionID: "reaction-1"},
		},
	}
	token, err := addProcessingReaction(context.Background(), gateway, "om_1", "Typing")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "reaction-1" {
		t.Fatalf("expected token reaction-1, got %q", token)
	}
	if len(gateway.addCalls) != 1 {
		t.Fatalf("expected one add call, got %d", len(gateway.addCalls))
	}
	if gateway.addCalls[0].messageID != "om_1" || gateway.addCalls[0].reactionType != "Typing" {
		t.Fatalf("unexpected add params: %+v", gateway.addCalls[0])
	}
}

func TestAddProcessingReactionReturnsError(t *testing.T) {
	t.Parallel()

	gateway := &fakeProcessingReactionGateway{
		addResponse: []struct {
			reactionID string
			err        error
		}{
			{err: errors.New("invalid reaction type")},
		},
	}
	token, err := addProcessingReaction(context.Background(), gateway, "om_2", "INVALID")
	if err == nil {
		t.Fatal("expected error")
	}
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
	if len(gateway.addCalls) != 1 {
		t.Fatalf("expected one add call, got %d", len(gateway.addCalls))
	}
	if gateway.addCalls[0].reactionType != "INVALID" {
		t.Fatalf("unexpected add call sequence: %+v", gateway.addCalls)
	}
}

func TestAddProcessingReactionNoMessageID(t *testing.T) {
	t.Parallel()

	gateway := &fakeProcessingReactionGateway{}
	token, err := addProcessingReaction(context.Background(), gateway, "", "Typing")
	if err != nil {
		t.Fatalf("expected no error for empty message id, got: %v", err)
	}
	if token != "" {
		t.Fatalf("expected empty token, got %q", token)
	}
	if len(gateway.addCalls) != 0 {
		t.Fatalf("expected no add calls, got %+v", gateway.addCalls)
	}
}

func TestRemoveProcessingReaction(t *testing.T) {
	t.Parallel()

	gateway := &fakeProcessingReactionGateway{}
	if err := removeProcessingReaction(context.Background(), gateway, "om_3", "reaction-3"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gateway.removeCalls) != 1 {
		t.Fatalf("expected one remove call, got %d", len(gateway.removeCalls))
	}
	if gateway.removeCalls[0].messageID != "om_3" || gateway.removeCalls[0].reactionID != "reaction-3" {
		t.Fatalf("unexpected remove params: %+v", gateway.removeCalls[0])
	}
}

func TestRemoveProcessingReactionNoopForEmptyToken(t *testing.T) {
	t.Parallel()

	gateway := &fakeProcessingReactionGateway{}
	if err := removeProcessingReaction(context.Background(), gateway, "om_4", ""); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gateway.removeCalls) != 0 {
		t.Fatalf("expected no remove calls, got %+v", gateway.removeCalls)
	}
}

func TestFeishuProcessingStartedNoSourceMessageID(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	handle, err := adapter.ProcessingStarted(
		context.Background(),
		channel.ChannelConfig{},
		channel.InboundMessage{},
		channel.ProcessingStatusInfo{},
	)
	if err != nil {
		t.Fatalf("expected no error for empty source message id, got: %v", err)
	}
	if handle.Token != "" {
		t.Fatalf("expected empty token, got %q", handle.Token)
	}
}

func TestFeishuProcessingStartedRequiresConfigWhenSourceMessageExists(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	_, err := adapter.ProcessingStarted(
		context.Background(),
		channel.ChannelConfig{},
		channel.InboundMessage{},
		channel.ProcessingStatusInfo{SourceMessageID: "om_5"},
	)
	if err == nil {
		t.Fatal("expected error when credentials are missing")
	}
}

func TestFeishuProcessingCompletedNoopWithoutToken(t *testing.T) {
	t.Parallel()

	adapter := NewFeishuAdapter(nil)
	err := adapter.ProcessingCompleted(
		context.Background(),
		channel.ChannelConfig{},
		channel.InboundMessage{},
		channel.ProcessingStatusInfo{SourceMessageID: "om_6"},
		channel.ProcessingStatusHandle{},
	)
	if err != nil {
		t.Fatalf("expected no error for empty token, got: %v", err)
	}
}
