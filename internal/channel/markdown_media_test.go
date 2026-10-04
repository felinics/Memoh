package channel

import (
	"context"
	"encoding/base64"
	"errors"
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

func TestSplitMarkdownFinalPreservesWithheldProseWithoutRepeatingChunks(t *testing.T) {
	for _, prefixChunks := range []int{1, 3} {
		ctx := context.Background()
		stream, reo, sent := newDeltaSplitTestStream(t, 4096, prefixChunks+2)
		store := stream.manager.attachmentStore.(*channeltest.MemoryAttachmentStore)
		store.SeedContainerFile("bot-1", "/data/report.txt", []byte("snapshot"), "text/plain", "report.txt")
		prefixDelta := strings.Repeat("a", 3072) + "\n"
		prefix := strings.Repeat(prefixDelta, prefixChunks)
		tail := "Notice! retained tail\n\n[report](/data/report.txt)\n\nafter"
		for range prefixChunks {
			if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: prefixDelta}); err != nil {
				t.Fatal(err)
			}
		}
		if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: tail}); err != nil {
			t.Fatal(err)
		}
		final := StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Format: MessageFormatMarkdown, Text: prefix + tail}}}
		if err := stream.Push(ctx, final); err != nil {
			t.Fatal(err)
		}
		if err := stream.Push(ctx, final); err != nil {
			t.Fatal(err)
		}
		var delivered strings.Builder
		for _, recording := range reo.streams {
			for _, event := range recording.events {
				delivered.WriteString(event.Delta)
				if event.Final != nil {
					delivered.WriteString(event.Final.Message.PlainText())
				}
			}
		}
		attachments := 0
		for _, part := range *sent {
			delivered.WriteString(part.Message.PlainText())
			attachments += len(part.Message.Attachments)
		}
		if !strings.Contains(delivered.String(), "Notice! retained tail") || strings.Count(delivered.String(), prefixDelta) != prefixChunks || attachments != 1 || strings.Count(delivered.String(), "after") != 1 {
			t.Fatalf("chunks=%d delivered=%q attachments=%d", prefixChunks, delivered.String(), attachments)
		}
	}
}

func TestMarkdownImageAndFileKeepDistinctDeliveryTypes(t *testing.T) {
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+a2ioAAAAASUVORK5CYII=")
	for _, source := range []string{
		"![preview](/data/a.png)\n\n[original](/data/a.png)\n\n[again](/data/a.png)",
		"[original](/data/a.png)\n\n![preview](/data/a.png)\n\n![again](/data/a.png)",
	} {
		store := channeltest.NewMemoryAttachmentStore()
		store.SeedContainerFile("bot-1", "/data/a.png", png, "image/png", "a.png")
		cfg := ChannelConfig{BotID: "bot-1", ChannelType: ChannelTypeTelegram}
		msg := resolveMarkdownMessage(context.Background(), store, cfg, Message{Text: source, Format: MessageFormatMarkdown})
		parts, _ := expandMarkdownMessage(OutboundMessage{Message: msg})
		types := map[AttachmentType]int{}
		for _, part := range parts {
			prepared, err := PrepareOutboundMessage(context.Background(), store, cfg, part)
			if err != nil {
				t.Fatal(err)
			}
			for _, attachment := range prepared.Message.Attachments {
				types[attachment.Logical.Type]++
			}
		}
		if types[AttachmentImage] != 1 || types[AttachmentFile] != 1 {
			t.Fatalf("source=%q types=%v", source, types)
		}
	}
}

func TestMarkdownFinalRetryClearsPendingText(t *testing.T) {
	ctx := context.Background()
	stream, reo, sent := newDeltaSplitTestStream(t, 4096, 2)
	store := stream.manager.attachmentStore.(*channeltest.MemoryAttachmentStore)
	store.SeedContainerFile("bot-1", "/data/report.txt", []byte("snapshot"), "text/plain", "report.txt")
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: "before [report](/data/report.txt) after"}); err != nil {
		t.Fatal(err)
	}
	send := stream.send
	fail := true
	stream.send = func(ctx context.Context, msg OutboundMessage) error {
		if fail {
			fail = false
			return errors.New("temporary transport failure")
		}
		return send(ctx, msg)
	}
	final := StreamEvent{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "before [report](/data/report.txt) after", Format: MessageFormatMarkdown}}}
	if err := stream.Push(ctx, final); err == nil {
		t.Fatal("expected transport failure")
	}
	if err := stream.Push(ctx, final); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 2 {
		t.Fatalf("sent=%+v", *sent)
	}
	if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: "next reply"}); err != nil {
		t.Fatal(err)
	}
	events := reo.streams[0].events
	if events[len(events)-1].Delta != "next reply" {
		t.Fatalf("new reply stayed buffered: events=%+v", events)
	}
}

func TestSplitMarkdownFinalWithFullyStreamedProse(t *testing.T) {
	ctx := context.Background()
	for _, attachmentsSupported := range []bool{false, true} {
		stream, _, sent := newDeltaSplitTestStream(t, 4096, 2)
		store := stream.manager.attachmentStore.(*channeltest.MemoryAttachmentStore)
		store.SeedContainerFile("bot-1", "/data/report.txt", []byte("snapshot"), "text/plain", "report.txt")
		prefix := strings.Repeat("a", 3072) + "\n"
		if err := stream.Push(ctx, StreamEvent{Type: StreamEventDelta, Delta: prefix}); err != nil {
			t.Fatal(err)
		}
		msg := resolveMarkdownMessage(ctx, store, stream.config, Message{Text: prefix + "[report](/data/report.txt)", Format: MessageFormatMarkdown})
		parts, err := stream.prepareMarkdownFinal(msg, ChannelCapabilities{Attachments: attachmentsSupported, Media: attachmentsSupported}, true)
		if err != nil {
			t.Fatal(err)
		}
		stream.markdownFinalParts = parts
		if err := stream.deliverMarkdownFinal(ctx); err != nil {
			t.Fatal(err)
		}
		if len(*sent) != 1 {
			t.Fatalf("attachmentsSupported=%v sent=%+v", attachmentsSupported, *sent)
		}
		got := (*sent)[0].Message
		if attachmentsSupported && len(got.Attachments) != 1 {
			t.Fatalf("missing attachment: %+v", got)
		}
		if !attachmentsSupported && (len(got.Attachments) != 0 || !strings.Contains(got.Text, "could not be shared")) {
			t.Fatalf("unsupported attachments did not degrade: %+v", got)
		}
	}
}
