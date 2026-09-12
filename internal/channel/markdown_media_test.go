package channel

import (
	"context"
	"encoding/base64"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel/channeltest"
	"github.com/felinics/memoh/internal/markdownmedia"
)

func TestMarkdownMediaAcrossChannelPreparation(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a2ioAAAAASUVORK5CYII=")
	for _, platform := range []string{"discord", "telegram", "feishu", "dingtalk", "qq", "slack", "matrix", "misskey", "line", "wecom", "weixin", "wechatoa", "web", "cli"} {
		t.Run(platform, func(t *testing.T) {
			ctx := context.Background()
			store := channeltest.NewMemoryAttachmentStore()
			store.SeedContainerFile("bot-1", "/data/截图.png", png, "image/png", "截图.png")
			store.SeedContainerFile("bot-1", "/data/report.txt", []byte("report bytes"), "text/plain", "report.txt")
			cfg := ChannelConfig{BotID: "bot-1", ChannelType: ChannelType(platform)}
			msg := resolveMarkdownMessage(ctx, store, cfg, Message{Text: "before\n\n![截图](/data/截图.png)\n\nmiddle\n\n[report](/data/report.txt)\n\nafter", Format: MessageFormatMarkdown})
			parts, err := buildOutboundMessages(OutboundMessage{Message: msg}, OutboundPolicy{})
			if err != nil || len(parts) != 5 {
				t.Fatalf("parts=%+v err=%v", parts, err)
			}
			for i, part := range parts {
				prepared, err := PrepareOutboundMessage(ctx, store, cfg, part)
				if err != nil {
					t.Fatal(err)
				}
				if i != 1 && i != 3 {
					continue
				}
				if len(prepared.Message.Attachments) != 1 {
					t.Fatalf("attachments=%+v", prepared)
				}
				reader, err := prepared.Message.Attachments[0].Open(ctx)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(reader)
				_ = reader.Close()
				want := string(png)
				if i == 3 {
					want = "report bytes"
				}
				if string(data) != want {
					t.Fatal("uploaded bytes changed")
				}
			}
		})
	}
}

func TestMarkdownMediaBuffersIncompleteReferenceUntilFinal(t *testing.T) {
	ctx := context.Background()
	stream, reo, sent := newDeltaSplitTestStream(t, 2000, 1)
	store := stream.manager.attachmentStore.(*channeltest.MemoryAttachmentStore)
	store.SeedContainerFile("bot-1", "/data/report.txt", []byte("snapshot"), "text/plain", "report.txt")
	for _, delta := range []string{"before\n\n", "[report](/data/", "report.txt)\n\nafter"} {
		if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: delta}); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 0 {
		t.Fatal("media sent before final")
	}
	for _, event := range reo.streams[0].events {
		if strings.Contains(event.Delta, "/data/") {
			t.Fatal("workspace reference leaked while streaming")
		}
	}
	msg := Message{Format: MessageFormatMarkdown, Text: "before\n\n[report](/data/report.txt)\n\nafter"}
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: msg}}); err != nil {
		t.Fatal(err)
	}
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: msg}}); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 2 || len((*sent)[0].Message.Attachments) != 1 || strings.TrimSpace((*sent)[1].Message.Text) != "after" {
		t.Fatalf("sent=%+v", *sent)
	}
}

func TestFailedMediaPreservesRemainingContent(t *testing.T) {
	msg := Message{Format: MessageFormatMarkdown, Text: "before ![missing](/data/missing.png) after"}
	msg = resolveMarkdownMessage(context.Background(), nil, ChannelConfig{BotID: "bot"}, msg)
	parts, ok := expandMarkdownMessage(OutboundMessage{Message: msg})
	if !ok || len(parts) != 3 || !strings.Contains(parts[1].Message.Text, "could not be shared") {
		t.Fatalf("parts=%+v", parts)
	}
	if len(markdownmedia.Bindings(parts[1].Message.Metadata)) != 0 {
		t.Fatal("expanded references could be replayed")
	}
}

func TestMediaOnlyFinalUsesAttachmentSender(t *testing.T) {
	ctx := context.Background()
	stream, _, sent := newDeltaSplitTestStream(t, 2000, 1)
	store := stream.manager.attachmentStore.(*channeltest.MemoryAttachmentStore)
	store.SeedContainerFile("bot-1", "/data/report.txt", []byte("snapshot"), "text/plain", "report.txt")
	final := StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "[report](/data/report.txt)", Format: MessageFormatMarkdown}}}
	if err := stream.Push(ctx, final); err != nil {
		t.Fatal(err)
	}
	if err := stream.Push(ctx, final); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 || len((*sent)[0].Message.Attachments) != 1 {
		t.Fatalf("sent=%+v", *sent)
	}
}
