package messaging

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/media"
)

type testSender struct {
	called int
	req    SendRequest
}

func (s *testSender) Send(_ context.Context, _ string, _ Platform, req SendRequest) error {
	s.called++
	s.req = req
	return nil
}

type testResolver struct{}

func (testResolver) ParseChannelType(raw string) (Platform, error) {
	return Platform(raw), nil
}

type testAssetResolver struct {
	ingestCalled int
	lastPath     string
}

func (*testAssetResolver) Stat(_ context.Context, _, _ string) (AssetMeta, error) {
	return AssetMeta{}, context.Canceled
}

func (*testAssetResolver) GetByStorageKey(_ context.Context, _, _ string) (AssetMeta, error) {
	return AssetMeta{}, context.Canceled
}

func (*testAssetResolver) Open(_ context.Context, _, _ string) (io.ReadCloser, media.Asset, error) {
	return nil, media.Asset{}, context.Canceled
}

func (*testAssetResolver) Ingest(_ context.Context, _ media.IngestInput) (media.Asset, error) {
	return media.Asset{}, context.Canceled
}

func (*testAssetResolver) AccessPath(_ context.Context, asset media.Asset) string {
	return "https://example.com/media/" + asset.ContentHash
}

func (r *testAssetResolver) IngestContainerFile(_ context.Context, _, containerPath string) (AssetMeta, error) {
	r.ingestCalled++
	r.lastPath = containerPath
	return AssetMeta{
		ContentHash: "hash_1",
		Mime:        "image/png",
		SizeBytes:   42,
		StorageKey:  "media/generated/hash_1",
	}, nil
}

func TestSendDirectSameConversationWithAttachmentsResolvesAssets(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:        sender,
		Resolver:      testResolver{},
		AssetResolver: &testAssetResolver{},
	}

	session := SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}

	result, err := exec.SendDirect(context.Background(), session, "", map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("SendDirect returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if len(sender.req.Message.Attachments) != 1 || sender.req.Message.Attachments[0].ContentHash != "hash_1" {
		t.Fatalf("unexpected attachments: %+v", sender.req.Message.Attachments)
	}
}

func TestSendSameConversationWithAttachmentsUsesLocalResult(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:        sender,
		Resolver:      testResolver{},
		AssetResolver: &testAssetResolver{},
	}

	session := SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}

	result, err := exec.Send(context.Background(), session, map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
		return
	}
	if !result.Local {
		t.Fatal("expected local result for same-conversation send")
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called for local result, got %d", sender.called)
	}
	if len(result.LocalAttachments) != 1 {
		t.Fatalf("expected 1 local attachment, got %d", len(result.LocalAttachments))
	}
	att := result.LocalAttachments[0]
	if att.Path != "/data/screenshot.png" {
		t.Fatalf("unexpected local attachment path: %q", att.Path)
	}
	if att.Type != AttachmentImage {
		t.Fatalf("unexpected local attachment type: %q", att.Type)
	}
}

func TestSendSameConversationWithLocalShortcutDisabledUsesSender(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:        sender,
		Resolver:      testResolver{},
		AssetResolver: &testAssetResolver{},
	}

	result, err := exec.Send(context.Background(), SessionContext{
		BotID:           "bot_1",
		CanOmitTarget:   false,
		CurrentPlatform: "feishu",
		ReplyTarget:     "chat_id:oc_group_1",
	}, map[string]any{
		"platform":    "feishu",
		"target":      "chat_id:oc_group_1",
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if result.Local {
		t.Fatal("expected non-local result when local shortcut is disabled")
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if sender.req.Target != "chat_id:oc_group_1" {
		t.Fatalf("unexpected target: %q", sender.req.Target)
	}
	if len(sender.req.Message.Attachments) != 1 || sender.req.Message.Attachments[0].ContentHash != "hash_1" {
		t.Fatalf("unexpected attachments: %+v", sender.req.Message.Attachments)
	}
}

func TestSendSameConversationStructuredMessageAttachmentsAreNormalized(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	result, err := exec.Send(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}, map[string]any{
		"message": map[string]any{
			"attachments": []any{
				map[string]any{"path": "screenshot.png"},
			},
		},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result == nil || !result.Local {
		t.Fatalf("expected local result, got %#v", result)
	}
	if len(result.LocalAttachments) != 1 {
		t.Fatalf("expected 1 local attachment, got %d", len(result.LocalAttachments))
	}
	att := result.LocalAttachments[0]
	if att.Path != "/data/screenshot.png" {
		t.Fatalf("unexpected local attachment path: %q", att.Path)
	}
	if att.Type != AttachmentImage {
		t.Fatalf("unexpected local attachment type: %q", att.Type)
	}
}

func TestSendSameConversationStructuredMessageAttachmentShorthandIsNormalized(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	result, err := exec.Send(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}, map[string]any{
		"message": map[string]any{
			"attachments": []any{"screenshot.png"},
		},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if result == nil || !result.Local {
		t.Fatalf("expected local result, got %#v", result)
	}
	if len(result.LocalAttachments) != 1 {
		t.Fatalf("expected 1 local attachment, got %d", len(result.LocalAttachments))
	}
	if result.LocalAttachments[0].Path != "/data/screenshot.png" {
		t.Fatalf("unexpected local attachment path: %q", result.LocalAttachments[0].Path)
	}
}

func TestSendDirectInvalidStructuredMessageWithAttachmentsReturnsParseError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args map[string]any
	}{
		{
			name: "top level attachments",
			args: map[string]any{
				"message": map[string]any{
					"text": "see attachment",
					"typo": "should not be dropped",
				},
				"attachments": []any{"https://example.com/file.png"},
			},
		},
		{
			name: "nested attachments",
			args: map[string]any{
				"message": map[string]any{
					"text": "see attachment",
					"typo": "should not be dropped",
					"attachments": []any{
						map[string]any{"url": "https://example.com/file.png"},
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &testSender{}
			exec := &Executor{
				Sender:   sender,
				Resolver: testResolver{},
			}

			_, err := exec.SendDirect(context.Background(), SessionContext{
				BotID:           "bot_1",
				CurrentPlatform: "telegram",
			}, "chat-1", tc.args)
			if err == nil || !strings.Contains(err.Error(), "unknown message field") {
				t.Fatalf("SendDirect error = %v, want unknown message field", err)
			}
			if sender.called != 0 {
				t.Fatalf("expected sender not called, got %d", sender.called)
			}
		})
	}
}

func TestSendDirectInvalidAttachmentObjectReturnsError(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{
			name: "top level attachment typo",
			args: map[string]any{
				"message": map[string]any{"text": "see attachment"},
				"attachments": []any{
					map[string]any{"href": "https://example.com/file.png"},
				},
			},
			want: "unknown attachment field",
		},
		{
			name: "nested attachment typo",
			args: map[string]any{
				"message": map[string]any{
					"text": "see attachment",
					"attachments": []any{
						map[string]any{"href": "https://example.com/file.png"},
					},
				},
			},
			want: "unknown attachment field",
		},
		{
			name: "top level attachment unknown type",
			args: map[string]any{
				"message": map[string]any{"text": "see attachment"},
				"attachments": []any{
					map[string]any{"type": "sticker", "url": "https://example.com/file.png"},
				},
			},
			want: "unsupported attachment type",
		},
		{
			name: "top level attachment url path",
			args: map[string]any{
				"message": map[string]any{"text": "see attachment"},
				"attachments": []any{
					map[string]any{"url": "/data/file.png"},
				},
			},
			want: "attachment url must be http(s) or data URL",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sender := &testSender{}
			exec := &Executor{
				Sender:   sender,
				Resolver: testResolver{},
			}

			_, err := exec.SendDirect(context.Background(), SessionContext{
				BotID:           "bot_1",
				CurrentPlatform: "telegram",
			}, "chat-1", tc.args)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("SendDirect error = %v, want %q", err, tc.want)
			}
			if sender.called != 0 {
				t.Fatalf("expected sender not called, got %d", sender.called)
			}
		})
	}
}

func TestSendDirectInvalidReplyToReturnsError(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.SendDirect(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
	}, "chat-1", map[string]any{
		"text":     "reply",
		"reply_to": 123,
	})
	if err == nil || !strings.Contains(err.Error(), "reply_to must be string") {
		t.Fatalf("SendDirect error = %v, want reply_to type error", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendDirectUnknownTopLevelFieldReturnsError(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.SendDirect(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
	}, "chat-1", map[string]any{
		"message":    map[string]any{"text": "see attachment"},
		"attachment": []any{"https://example.com/file.png"},
	})
	if err == nil || !strings.Contains(err.Error(), "unknown send field") {
		t.Fatalf("SendDirect error = %v, want unknown send field", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestParseOutboundMessageRichPartsValidation(t *testing.T) {
	t.Parallel()

	msg, err := ParseOutboundMessage(map[string]any{
		"message": map[string]any{
			"format": "rich",
			"parts": []any{
				map[string]any{
					"type":   " text ",
					"text":   "bold",
					"styles": []any{" bold "},
				},
				map[string]any{
					"type":     "code_block",
					"text":     "go test ./...",
					"language": "go",
				},
				map[string]any{
					"type": "heading",
					"text": "Summary",
				},
				map[string]any{
					"type": "blockquote",
					"text": "quoted",
				},
				map[string]any{
					"type": "list_item",
					"text": "first item",
				},
				map[string]any{
					"type":   "text",
					"text":   "hidden detail",
					"styles": []any{" underline ", " spoiler "},
				},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("ParseOutboundMessage returned error: %v", err)
	}
	if msg.Format != MessageFormatRich || len(msg.Parts) != 6 {
		t.Fatalf("unexpected parsed message: %#v", msg)
	}
	if got := msg.Parts[0]; got.Type != MessagePartText || len(got.Styles) != 1 || got.Styles[0] != MessageStyleBold {
		t.Fatalf("unexpected first part: %#v", got)
	}
	if got := msg.Parts[5]; got.Type != MessagePartText || len(got.Styles) != 2 || got.Styles[0] != MessageStyleUnderline || got.Styles[1] != MessageStyleSpoiler {
		t.Fatalf("unexpected styled part: %#v", got)
	}

	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "unknown message field",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "typo": "dropped"}},
			want: "unknown message field",
		},
		{
			name: "schema external message id",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "id": "msg-1"}},
			want: "unknown message field",
		},
		{
			name: "schema external message thread",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "thread": map[string]any{"id": "thread-1"}}},
			want: "unknown message field",
		},
		{
			name: "schema external message forward",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "forward": map[string]any{"message_id": "msg-1"}}},
			want: "unknown message field",
		},
		{
			name: "schema external message metadata",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "metadata": map[string]any{"x": "y"}}},
			want: "unknown message field",
		},
		{
			name: "unknown format",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "format": "html"}},
			want: "unsupported message format",
		},
		{
			name: "unknown nested attachment field",
			raw: map[string]any{"message": map[string]any{
				"text":        "see attachment",
				"attachments": []any{map[string]any{"href": "https://example.com/file.png"}},
			}},
			want: "unknown attachment field",
		},
		{
			name: "unknown nested attachment type",
			raw: map[string]any{"message": map[string]any{
				"text":        "see attachment",
				"attachments": []any{map[string]any{"type": "sticker", "url": "https://example.com/file.png"}},
			}},
			want: "unsupported attachment type",
		},
		{
			name: "nested attachment url path",
			raw: map[string]any{"message": map[string]any{
				"text":        "see attachment",
				"attachments": []any{map[string]any{"url": "/data/file.png"}},
			}},
			want: "attachment url must be http(s) or data URL",
		},
		{
			name: "nested attachment without reference",
			raw: map[string]any{"message": map[string]any{
				"text":        "see attachment",
				"attachments": []any{map[string]any{"name": "file.png"}},
			}},
			want: "attachment reference is required",
		},
		{
			name: "non-object reply",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "reply": "msg-1"}},
			want: "message reply must be object",
		},
		{
			name: "unknown reply field",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "reply": map[string]any{"id": "msg-1"}}},
			want: "unknown message reply field",
		},
		{
			name: "reply without message id",
			raw:  map[string]any{"message": map[string]any{"text": "hi", "reply": map[string]any{"message_id": "   "}}},
			want: "message reply message_id is required",
		},
		{
			name: "null part",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				nil,
			}}},
			want: "message part must be object",
		},
		{
			name: "non-object part",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				"hi",
			}}},
			want: "message part must be object",
		},
		{
			name: "unknown part field",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "text", "text": "hi", "typo": "dropped"},
			}}},
			want: "unknown message part field",
		},
		{
			name: "schema external part metadata",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "text", "text": "hi", "metadata": map[string]any{"x": "y"}},
			}}},
			want: "unknown message part field",
		},
		{
			name: "unknown part type",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "table", "text": "Title"},
			}}},
			want: "unsupported message part type",
		},
		{
			name: "unknown style",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "text", "text": "hi", "styles": []any{"rainbow"}},
			}}},
			want: "unsupported message part style",
		},
		{
			name: "empty text part",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "text", "text": "   "},
			}}},
			want: "message part content is required",
		},
		{
			name: "link without url",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "link", "text": "docs"},
			}}},
			want: "message link part url is required",
		},
		{
			name: "code block without text",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "code_block", "language": "go"},
			}}},
			want: "message part content is required",
		},
		{
			name: "mention without text or identity",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "mention"},
			}}},
			want: "message mention part text is required",
		},
		{
			name: "mention without visible text",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "mention", "channel_identity_id": "123"},
			}}},
			want: "message mention part text is required",
		},
		{
			name: "emoji without text or emoji",
			raw: map[string]any{"message": map[string]any{"parts": []any{
				map[string]any{"type": "emoji"},
			}}},
			want: "message emoji part text or emoji is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOutboundMessage(tt.raw, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseOutboundMessage error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseOutboundMessageAllowsURLOnlyLinkPart(t *testing.T) {
	t.Parallel()

	msg, err := ParseOutboundMessage(map[string]any{
		"message": map[string]any{
			"parts": []any{
				map[string]any{"type": "link", "url": "https://example.com"},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("ParseOutboundMessage returned error: %v", err)
	}
	if len(msg.Parts) != 1 || msg.Parts[0].Type != MessagePartLink || msg.Parts[0].URL != "https://example.com" || msg.Parts[0].Text != "" {
		t.Fatalf("unexpected parsed parts: %#v", msg.Parts)
	}
}

func TestParseOutboundMessageActionsValidation(t *testing.T) {
	t.Parallel()

	msg, err := ParseOutboundMessage(map[string]any{
		"message": map[string]any{
			"text": "choose",
			"actions": []any{
				map[string]any{"label": "Open", "url": "https://example.com"},
			},
		},
	}, "")
	if err != nil {
		t.Fatalf("ParseOutboundMessage returned error: %v", err)
	}
	if len(msg.Actions) != 1 || msg.Actions[0].URL != "https://example.com" || msg.Actions[0].Value != "" {
		t.Fatalf("unexpected actions: %#v", msg.Actions)
	}

	tests := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{
			name: "null action",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{nil},
			}},
			want: "message action must be object",
		},
		{
			name: "non-object action",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{"open"},
			}},
			want: "message action must be object",
		},
		{
			name: "unknown action field",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{map[string]any{"label": "Open", "href": "https://example.com"}},
			}},
			want: "unknown message action field",
		},
		{
			name: "callback action value is not model constructible",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{map[string]any{"label": "Approve", "value": "approve:1"}},
			}},
			want: "unknown message action field",
		},
		{
			name: "missing action url",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{map[string]any{"label": "Open"}},
			}},
			want: "message action url is required",
		},
		{
			name: "unsafe url",
			raw: map[string]any{"message": map[string]any{
				"text":    "choose",
				"actions": []any{map[string]any{"label": "Open", "url": "javascript:alert(1)"}},
			}},
			want: "message action url must be http",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseOutboundMessage(tt.raw, "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseOutboundMessage error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSendSameConversationTextOnlyFails(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.Send(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}, map[string]any{
		"text": "ordinary reply",
	})
	if err == nil || !strings.Contains(err.Error(), "use assistant text") {
		t.Fatalf("Send error = %v, want assistant text guidance", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendSameConversationAttachmentWithTextDeliversAttachments(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	result, err := exec.Send(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}, map[string]any{
		"text":        "caption",
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !result.Local {
		t.Fatal("expected local shortcut result")
	}
	if len(result.LocalAttachments) != 1 {
		t.Fatalf("expected 1 local attachment, got %d", len(result.LocalAttachments))
	}
	if !result.LocalTextOmitted {
		t.Fatal("expected LocalTextOmitted to be true")
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendSameConversationAttachmentWithActionsFails(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.Send(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "feishu",
		ReplyTarget:        "chat_id:oc_group_1",
	}, map[string]any{
		"message": map[string]any{
			"attachments": []any{"screenshot.png"},
			"actions":     []any{map[string]any{"label": "Open", "url": "https://example.com"}},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "standalone files or attachments") {
		t.Fatalf("Send error = %v, want standalone attachment guidance", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendWithoutTargetAndNoCurrentConversationFails(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.Send(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "feishu",
	}, map[string]any{
		"text": "notify",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("Send error = %v, want target is required", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendWithDifferentPlatformDoesNotReuseCurrentTarget(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.Send(context.Background(), SessionContext{
		BotID:           "bot_1",
		CanOmitTarget:   true,
		CurrentPlatform: "telegram",
		ReplyTarget:     "telegram-chat-1",
	}, map[string]any{
		"platform": "discord",
		"text":     "notify",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("Send error = %v, want target is required", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

func TestSendCannotDefaultTargetWhenSessionDisallowsOmission(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	exec := &Executor{
		Sender:   sender,
		Resolver: testResolver{},
	}

	_, err := exec.Send(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
		ReplyTarget:     "telegram-chat-1",
	}, map[string]any{
		"text": "notify",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("Send error = %v, want target is required", err)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called, got %d", sender.called)
	}
}

type testReactor struct {
	called int
	req    ReactRequest
}

func (r *testReactor) React(_ context.Context, _ string, _ Platform, req ReactRequest) error {
	r.called++
	r.req = req
	return nil
}

func TestReactWithDifferentPlatformDoesNotReuseCurrentTarget(t *testing.T) {
	t.Parallel()

	reactor := &testReactor{}
	exec := &Executor{
		Reactor:  reactor,
		Resolver: testResolver{},
	}

	_, err := exec.React(context.Background(), SessionContext{
		BotID:           "bot_1",
		CanOmitTarget:   true,
		CurrentPlatform: "telegram",
		ReplyTarget:     "telegram-chat-1",
	}, map[string]any{
		"platform":   "discord",
		"message_id": "msg-1",
		"emoji":      "👍",
	})
	if err == nil || !strings.Contains(err.Error(), "target is required") {
		t.Fatalf("React error = %v, want target is required", err)
	}
	if reactor.called != 0 {
		t.Fatalf("expected reactor not called, got %d", reactor.called)
	}
}

func TestReactSameConversationLocalShortcut(t *testing.T) {
	t.Parallel()

	reactor := &testReactor{}
	exec := &Executor{
		Reactor:  reactor,
		Resolver: testResolver{},
	}

	result, err := exec.React(context.Background(), SessionContext{
		BotID:              "bot_1",
		CanOmitTarget:      true,
		AllowLocalShortcut: true,
		CurrentPlatform:    "web",
		ReplyTarget:        "bot_1",
	}, map[string]any{
		"message_id": "msg-1",
		"emoji":      "👍",
	})
	if err != nil {
		t.Fatalf("React returned error: %v", err)
	}
	if reactor.called != 0 {
		t.Fatalf("expected reactor not called for local shortcut, got %d", reactor.called)
	}
	if result == nil || !result.Local || result.Target != "bot_1" || result.MessageID != "msg-1" || result.Emoji != "👍" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestSendDirectPromotesDataPathAttachmentToContentHash(t *testing.T) {
	t.Parallel()

	sender := &testSender{}
	assets := &testAssetResolver{}
	exec := &Executor{
		Sender:        sender,
		Resolver:      testResolver{},
		AssetResolver: assets,
	}

	session := SessionContext{
		BotID:           "bot_1",
		CanOmitTarget:   true,
		CurrentPlatform: "feishu",
		ReplyTarget:     "chat_id:oc_group_1",
	}

	_, err := exec.SendDirect(context.Background(), session, "", map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("SendDirect returned error: %v", err)
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if assets.ingestCalled != 1 || assets.lastPath != "/data/screenshot.png" {
		t.Fatalf("expected ingest called with /data path, got called=%d path=%q", assets.ingestCalled, assets.lastPath)
	}
	if len(sender.req.Message.Attachments) != 1 {
		t.Fatalf("expected one attachment, got %d", len(sender.req.Message.Attachments))
	}
	att := sender.req.Message.Attachments[0]
	if att.ContentHash != "hash_1" {
		t.Fatalf("expected promoted content hash, got %q", att.ContentHash)
	}
}
