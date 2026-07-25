package messaging

import (
	"context"
	"testing"

	"github.com/memohai/memoh/domains/channel/delivery"
	"github.com/memohai/memoh/domains/channel/gateway"
)

type fakeRuntime struct {
	send gateway.SendRequest
}

func (f *fakeRuntime) Send(_ context.Context, _ string, _ gateway.ChannelType, req gateway.SendRequest) error {
	f.send = req
	return nil
}

func (*fakeRuntime) React(context.Context, string, gateway.ChannelType, gateway.ReactRequest) error {
	return nil
}

type fakeResolver struct{}

func (fakeResolver) ParseChannelType(raw string) (gateway.ChannelType, error) {
	return gateway.ChannelType(raw), nil
}

func TestAdapterPreservesStructuredMessage(t *testing.T) {
	t.Parallel()

	runtime := &fakeRuntime{}
	adapter := New(runtime, fakeResolver{}, nil)
	err := adapter.Send(context.Background(), "bot-1", "telegram", delivery.SendRequest{
		Target: "chat-1",
		Message: delivery.Message{
			Format: delivery.MessageFormatRich,
			Parts: []delivery.MessagePart{{
				Type: delivery.MessagePartText, Text: "hello",
				Styles: []delivery.MessageTextStyle{delivery.MessageStyleBold},
			}},
			Attachments: []delivery.Attachment{{Type: delivery.AttachmentImage, URL: "https://example.com/a.png"}},
			Reply:       &delivery.ReplyRef{MessageID: "message-1"},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if runtime.send.Target != "chat-1" || runtime.send.Message.Format != gateway.MessageFormatRich {
		t.Fatalf("unexpected request: %#v", runtime.send)
	}
	if len(runtime.send.Message.Parts) != 1 || runtime.send.Message.Parts[0].Styles[0] != gateway.MessageStyleBold {
		t.Fatalf("structured parts not preserved: %#v", runtime.send.Message.Parts)
	}
	if runtime.send.Message.Reply == nil || runtime.send.Message.Reply.MessageID != "message-1" {
		t.Fatalf("reply not preserved: %#v", runtime.send.Message.Reply)
	}
}
