package qq

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func preparedQQEvent(event channel.StreamEvent) channel.PreparedStreamEvent {
	prepared := channel.PreparedStreamEvent{
		Type:   event.Type,
		Delta:  event.Delta,
		Error:  event.Error,
		Status: event.Status,
		Phase:  event.Phase,
	}
	if len(event.Attachments) > 0 {
		prepared.Attachments = make([]channel.PreparedAttachment, 0, len(event.Attachments))
		for _, att := range event.Attachments {
			prepared.Attachments = append(prepared.Attachments, channel.PreparedAttachment{
				Logical:   att,
				Kind:      channel.PreparedAttachmentUpload,
				Name:      att.Name,
				Mime:      att.Mime,
				PublicURL: att.URL,
				Open: func(context.Context) (io.ReadCloser, error) {
					return io.NopCloser(strings.NewReader("test")), nil
				},
			})
		}
	}
	if event.Final != nil {
		prepared.Final = &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{Message: event.Final.Message},
		}
	}
	return prepared
}

func TestQQOutboundStreamFlushesBufferedTextOnFinal(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventStatus, Status: channel.StreamStatusStarted})); err != nil {
		t.Fatalf("push status: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "Hi "})); err != nil {
		t.Fatalf("push delta1: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "there"})); err != nil {
		t.Fatalf("push delta2: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if sent[0].Target != "c2c:user-openid" {
		t.Fatalf("unexpected target: %s", sent[0].Target)
	}
	if sent[0].Message.PlainText() != "Hi there" {
		t.Fatalf("unexpected text: %q", sent[0].Message.PlainText())
	}
}

func TestQQOutboundStreamFinalUsesExplicitMessageAndBufferedAttachments(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "group:group-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type:        channel.StreamEventAttachment,
		Attachments: []channel.Attachment{{Type: channel.AttachmentImage, URL: "https://example.com/a.png"}},
	})); err != nil {
		t.Fatalf("push attachment: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "done",
		}},
	})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if sent[0].Message.PlainText() != "done" {
		t.Fatalf("unexpected text: %q", sent[0].Message.PlainText())
	}
	if len(sent[0].Message.Attachments) != 1 {
		t.Fatalf("unexpected attachments: %d", len(sent[0].Message.Attachments))
	}
}

func TestQQOutboundStreamFinalPrefersBufferedVisibleText(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "visible "})); err != nil {
		t.Fatalf("push delta1: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "answer"})); err != nil {
		t.Fatalf("push delta2: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "internal trace\nvisible answer",
		}},
	})); err != nil {
		t.Fatalf("push final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected one send, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); got != "visible answer" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestQQOutboundStreamIgnoresLaterTextOnlyFinalAfterBufferedReply(t *testing.T) {
	t.Parallel()

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	ctx := context.Background()
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventDelta, Delta: "visible answer"})); err != nil {
		t.Fatalf("push delta: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventFinal, Final: &channel.StreamFinalizePayload{}})); err != nil {
		t.Fatalf("push first final: %v", err)
	}
	if err := stream.Push(ctx, preparedQQEvent(channel.StreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.StreamFinalizePayload{Message: channel.Message{
			Text: "我需要按照用户的要求，在工具调用后完整复述。",
		}},
	})); err != nil {
		t.Fatalf("push second final: %v", err)
	}

	if len(sent) != 1 {
		t.Fatalf("expected 1 outbound message, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); got != "visible answer" {
		t.Fatalf("unexpected text: %q", got)
	}
}

func TestQQOutboundStreamRejectsAfterClose(t *testing.T) {
	t.Parallel()

	stream := &qqOutboundStream{}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := stream.Push(context.Background(), preparedQQEvent(channel.StreamEvent{
		Type:  channel.StreamEventDelta,
		Delta: "x",
	})); err == nil {
		t.Fatal("expected closed error")
	}
}

func TestQQOutboundStreamErrorRedactsRegisteredTokenFragments(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const token = "qq-token-ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	redact.SetSecrets("test", token)
	prefixHalf := token[:len(token)/2]

	var sent []channel.OutboundMessage
	stream := &qqOutboundStream{
		target: "c2c:user-openid",
		send: func(_ context.Context, msg channel.PreparedOutboundMessage) error {
			sent = append(sent, msg.LogicalMessage())
			return nil
		},
	}

	err := stream.Push(context.Background(), preparedQQEvent(channel.StreamEvent{Type: channel.StreamEventError, Error: "failed: " + prefixHalf}))
	if err != nil {
		t.Fatalf("push error: %v", err)
	}
	if len(sent) != 1 {
		t.Fatalf("expected one outbound message, got %d", len(sent))
	}
	if got := sent[0].Message.PlainText(); strings.Contains(got, prefixHalf) {
		t.Fatalf("expected redacted token fragment, got %q", got)
	}
}
