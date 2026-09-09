package channelmessaging

import (
	"context"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/messaging"
)

type fakeRuntime struct {
	send channel.SendRequest
}

func (f *fakeRuntime) Send(_ context.Context, _ string, _ channel.ChannelType, req channel.SendRequest) error {
	f.send = req
	return nil
}

func (*fakeRuntime) React(context.Context, string, channel.ChannelType, channel.ReactRequest) error {
	return nil
}

type fakeResolver struct{}

func (fakeResolver) ParseChannelType(raw string) (channel.ChannelType, error) {
	return channel.ChannelType(raw), nil
}

func TestAdapterPreservesStructuredMessage(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{}
	adapter := New(runtime, fakeResolver{}, nil)
	err := adapter.Send(context.Background(), "bot-1", "telegram", messaging.SendRequest{
		Target: "chat-1",
		Message: messaging.Message{
			Format: messaging.MessageFormatRich,
			Parts: []messaging.MessagePart{{
				Type: messaging.MessagePartText, Text: "hello",
				Styles: []messaging.MessageTextStyle{messaging.MessageStyleBold},
			}},
			Attachments: []messaging.Attachment{{Type: messaging.AttachmentImage, URL: "https://example.com/a.png"}},
			Reply:       &messaging.ReplyRef{MessageID: "message-1"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if runtime.send.Target != "chat-1" || runtime.send.Message.Format != channel.MessageFormatRich {
		t.Fatalf("unexpected request: %#v", runtime.send)
	}
	if len(runtime.send.Message.Parts) != 1 || runtime.send.Message.Parts[0].Styles[0] != channel.MessageStyleBold {
		t.Fatalf("structured parts not preserved: %#v", runtime.send.Message.Parts)
	}
	if runtime.send.Message.Reply == nil || runtime.send.Message.Reply.MessageID != "message-1" {
		t.Fatalf("reply not preserved: %#v", runtime.send.Message.Reply)
	}
}
