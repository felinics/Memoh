package tools

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/agent/sessionmode"
	"github.com/felinics/memoh/internal/media"
	"github.com/felinics/memoh/internal/messaging"
)

func TestChannelAttachmentsToToolAttachments_NormalizesLocalPath(t *testing.T) {
	t.Parallel()

	atts := channelAttachmentsToToolAttachments([]messaging.Attachment{
		{
			Type: messaging.AttachmentImage,
			URL:  "/data/images/demo.png",
			Mime: "IMAGE/PNG; charset=utf-8",
		},
	})
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Path != "/data/images/demo.png" {
		t.Fatalf("expected local path promoted to Path, got %q", atts[0].Path)
	}
	if atts[0].URL != "" {
		t.Fatalf("expected URL cleared for local path attachment, got %q", atts[0].URL)
	}
	if atts[0].Mime != "image/png" {
		t.Fatalf("expected normalized mime image/png, got %q", atts[0].Mime)
	}
}

func TestChannelAttachmentsToToolAttachments_PreservesRemoteURL(t *testing.T) {
	t.Parallel()

	atts := channelAttachmentsToToolAttachments([]messaging.Attachment{
		{
			Type: messaging.AttachmentFile,
			URL:  "https://example.com/demo.pdf",
			Name: "demo.pdf",
		},
	})
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].URL != "https://example.com/demo.pdf" {
		t.Fatalf("expected remote URL preserved, got %q", atts[0].URL)
	}
	if atts[0].Path != "" {
		t.Fatalf("expected empty path for remote URL, got %q", atts[0].Path)
	}
	if atts[0].Name != "demo.pdf" {
		t.Fatalf("expected name preserved, got %q", atts[0].Name)
	}
}

func TestChannelAttachmentsToToolAttachments_PreservesInlineBase64(t *testing.T) {
	t.Parallel()

	atts := channelAttachmentsToToolAttachments([]messaging.Attachment{
		{
			Type:        messaging.AttachmentImage,
			Base64:      "data:image/png;base64,AAAA",
			PlatformKey: "native-ref",
			Mime:        "image/png",
		},
	})
	if len(atts) != 1 {
		t.Fatalf("expected 1 attachment, got %d", len(atts))
	}
	if atts[0].Base64 != "data:image/png;base64,AAAA" {
		t.Fatalf("expected inline base64 preserved, got %q", atts[0].Base64)
	}
	if atts[0].PlatformKey != "native-ref" {
		t.Fatalf("expected platform key preserved, got %q", atts[0].PlatformKey)
	}
	if atts[0].URL != "" || atts[0].Path != "" {
		t.Fatalf("expected no path/url for inline attachment, got path=%q url=%q", atts[0].Path, atts[0].URL)
	}
}

func TestExecReactSameConversationRequiresMessageID(t *testing.T) {
	t.Parallel()

	provider := NewMessageProvider(nil, usageTestSender{}, usageTestReactor{}, usageTestResolver{}, nil)
	var emitted bool
	_, err := provider.execReact(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		Emitter: func(ToolStreamEvent) {
			emitted = true
		},
	}, map[string]any{
		"emoji": "👍",
	})
	if err == nil || !strings.Contains(err.Error(), "message_id is required") {
		t.Fatalf("execReact error = %v, want message_id required", err)
	}
	if emitted {
		t.Fatal("execReact should not emit a local reaction event on invalid input")
	}
}

type recordingSender struct {
	called int
	req    messaging.SendRequest
}

func (s *recordingSender) Send(_ context.Context, _ string, _ messaging.Platform, req messaging.SendRequest) error {
	s.called++
	s.req = req
	return nil
}

type messageTestAssetResolver struct{}

func (messageTestAssetResolver) Stat(context.Context, string, string) (media.Asset, error) {
	return media.Asset{}, context.Canceled
}

func (messageTestAssetResolver) GetByStorageKey(context.Context, string, string) (media.Asset, error) {
	return media.Asset{}, context.Canceled
}

func (messageTestAssetResolver) Open(context.Context, string, string) (io.ReadCloser, media.Asset, error) {
	return nil, media.Asset{}, context.Canceled
}

func (messageTestAssetResolver) Ingest(context.Context, media.IngestInput) (media.Asset, error) {
	return media.Asset{}, context.Canceled
}

func (messageTestAssetResolver) AccessPath(_ context.Context, asset media.Asset) string {
	return "/data/.memoh/media/" + asset.ContentHash
}

func (messageTestAssetResolver) IngestContainerFile(context.Context, string, string) (media.Asset, error) {
	return media.Asset{
		ContentHash: "hash_1",
		Mime:        "image/png",
		SizeBytes:   42,
		StorageKey:  "media/generated/hash_1",
	}, nil
}

func TestExecSendDiscussTextUsesChannelAdapter(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Discuss,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	}, "call-test", map[string]any{
		"text": "observed reply",
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if sender.req.Target != "chat-1" || sender.req.Message.Text != "observed reply" {
		t.Fatalf("unexpected send request: %+v", sender.req)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["delivered"] != "current_conversation" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecSendDiscussExplicitTargetReportsTargetDelivery(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Discuss,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	}, "call-test", map[string]any{
		"target": "chat-2",
		"text":   "cross target",
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if sender.req.Target != "chat-2" {
		t.Fatalf("unexpected target: %q", sender.req.Target)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["delivered"] != "target" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecSendCurrentConversationWithoutEmitterUsesChannelAdapter(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
	}, "call-test", map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if sender.req.Target != "chat-1" {
		t.Fatalf("unexpected target: %q", sender.req.Target)
	}
	if got := sender.req.Message.Attachments[0].ContentHash; got != "hash_1" {
		t.Fatalf("expected resolved attachment content hash, got %q", got)
	}
	if got := sender.req.Message.Attachments[0].ContentHash; got != "hash_1" {
		t.Fatalf("expected resolved attachment content hash, got %q", got)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["delivered"] != "current_conversation" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecSendCurrentConversationCollectingEmitterUsesChannelAdapter(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	var emitted bool
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		Emitter: func(ToolStreamEvent) {
			emitted = true
		},
	}, "call-test", map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if emitted {
		t.Fatal("non-live collecting emitter should not be used for current-conversation send")
	}
	if sender.called != 1 {
		t.Fatalf("expected sender called once, got %d", sender.called)
	}
	if got := sender.req.Message.Attachments[0].ContentHash; got != "hash_1" {
		t.Fatalf("expected resolved attachment content hash, got %q", got)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["delivered"] != "current_conversation" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecSendCurrentConversationLiveStreamUsesEmitter(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	var emitted int
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		LiveStream:      true,
		Emitter: func(ToolStreamEvent) {
			emitted++
		},
	}, "call-test", map[string]any{
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if emitted != 1 {
		t.Fatalf("expected live stream emitter called once, got %d", emitted)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called for live stream shortcut, got %d", sender.called)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["delivered"] != "current_conversation" {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecSendCurrentConversationLiveStreamAttachmentWithTextEmitsAndFlagsText(t *testing.T) {
	t.Parallel()

	sender := &recordingSender{}
	provider := NewMessageProvider(nil, sender, usageTestReactor{}, usageTestResolver{}, messageTestAssetResolver{})
	var emitted int
	result, err := provider.execSend(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		LiveStream:      true,
		Emitter: func(ToolStreamEvent) {
			emitted++
		},
	}, "call-test", map[string]any{
		"text":        "这是当前 blog 的页面截图。",
		"attachments": []any{"screenshot.png"},
	})
	if err != nil {
		t.Fatalf("execSend returned error: %v", err)
	}
	if emitted != 1 {
		t.Fatalf("expected live stream emitter called once, got %d", emitted)
	}
	if sender.called != 0 {
		t.Fatalf("expected sender not called for live stream shortcut, got %d", sender.called)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["delivered"] != "current_conversation" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if resp["text_delivered"] != false {
		t.Fatalf("expected text_delivered=false in result, got %#v", result)
	}
	note, _ := resp["note"].(string)
	if !strings.Contains(note, "assistant reply") {
		t.Fatalf("expected note guiding assistant reply, got %#v", result)
	}
}

type recordingReactor struct {
	called int
	req    messaging.ReactRequest
}

func (r *recordingReactor) React(_ context.Context, _ string, _ messaging.Platform, req messaging.ReactRequest) error {
	r.called++
	r.req = req
	return nil
}

func TestExecReactSameConversationCollectingEmitterUsesReactor(t *testing.T) {
	t.Parallel()

	reactor := &recordingReactor{}
	provider := NewMessageProvider(nil, usageTestSender{}, reactor, usageTestResolver{}, nil)
	var emitted bool
	result, err := provider.execReact(context.Background(), SessionContext{
		BotID:           "bot_1",
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		Emitter: func(ToolStreamEvent) {
			emitted = true
		},
	}, map[string]any{
		"message_id": "msg-1",
		"emoji":      "👍",
	})
	if err != nil {
		t.Fatalf("execReact returned error: %v", err)
	}
	if emitted {
		t.Fatal("execReact should delegate to reactor instead of emitting a lossy local event")
	}
	if reactor.called != 1 {
		t.Fatalf("expected reactor called once, got %d", reactor.called)
	}
	if reactor.req.Target != "chat-1" || reactor.req.MessageID != "msg-1" || reactor.req.Emoji != "👍" {
		t.Fatalf("unexpected reaction request: %+v", reactor.req)
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestExecReactSameConversationLiveStreamUsesEmitter(t *testing.T) {
	t.Parallel()

	reactor := &recordingReactor{}
	provider := NewMessageProvider(nil, usageTestSender{}, reactor, usageTestResolver{}, nil)
	var emitted []ToolStreamEvent
	result, err := provider.execReact(context.Background(), SessionContext{
		BotID:           "bot_1",
		SessionType:     sessionmode.Chat,
		CurrentPlatform: "telegram",
		ReplyTarget:     "chat-1",
		LiveStream:      true,
		Emitter: func(evt ToolStreamEvent) {
			emitted = append(emitted, evt)
		},
	}, map[string]any{
		"message_id": "msg-1",
		"emoji":      "👍",
	})
	if err != nil {
		t.Fatalf("execReact returned error: %v", err)
	}
	if reactor.called != 0 {
		t.Fatalf("expected reactor not called for live stream shortcut, got %d", reactor.called)
	}
	if len(emitted) != 1 || emitted[0].Type != StreamEventReaction || len(emitted[0].Reactions) != 1 {
		t.Fatalf("expected one reaction stream event, got %#v", emitted)
	}
	if emitted[0].Reactions[0].Emoji != "👍" || emitted[0].Reactions[0].MessageID != "msg-1" {
		t.Fatalf("unexpected emitted reaction: %#v", emitted[0].Reactions[0])
	}
	resp, ok := result.(map[string]any)
	if !ok || resp["ok"] != true || resp["target"] != "chat-1" {
		t.Fatalf("unexpected result: %#v", result)
	}
}
