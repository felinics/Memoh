package line

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/line/line-bot-sdk-go/v8/linebot/messaging_api"

	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/redact"
)

func TestOpenStreamRequiresTarget(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(nil)
	if _, err := adapter.OpenStream(context.Background(), testLineConfig(), "  ", channel.StreamOptions{}); err == nil {
		t.Fatal("expected empty target to fail")
	}
}

func TestLineStreamAccumulatesDeltasAndFiltersReasoning(t *testing.T) {
	t.Parallel()

	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	stream, err := adapter.OpenStream(context.Background(), testLineConfig(), "Uuser", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "hello"}); err != nil {
		t.Fatalf("Push delta returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Phase: channel.StreamPhaseReasoning, Delta: " secret reasoning "}); err != nil {
		t.Fatalf("Push reasoning returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: " world"}); err != nil {
		t.Fatalf("Push delta returned error: %v", err)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if got := pushedTextMessages(client); len(got) != 1 || got[0] != "hello world" {
		t.Fatalf("pushed text messages = %#v, want [hello world]", got)
	}
}

func TestLineStreamFinalUsesFinalMessage(t *testing.T) {
	t.Parallel()

	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	stream, err := adapter.OpenStream(context.Background(), testLineConfig(), "Uuser", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "draft"}); err != nil {
		t.Fatalf("Push delta returned error: %v", err)
	}
	err = stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Format: channel.MessageFormatPlain,
					Text:   "final",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("Push final returned error: %v", err)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	if got := pushedTextMessages(client); len(got) != 1 || got[0] != "final" {
		t.Fatalf("pushed text messages = %#v, want [final]", got)
	}
}

func TestLineStreamSuppressesPartialSendFailure(t *testing.T) {
	t.Parallel()

	client := &testMessagingClient{pushErrOnCall: 2, pushErr: errors.New("network failure")}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	stream, err := adapter.OpenStream(context.Background(), testLineConfig(), "Uuser", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream returned error: %v", err)
	}
	err = stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type: channel.StreamEventFinal,
		Final: &channel.PreparedStreamFinalizePayload{
			Message: channel.PreparedMessage{
				Message: channel.Message{
					Format: channel.MessageFormatPlain,
					Text:   "hello",
				},
				Attachments: []channel.PreparedAttachment{{
					Kind:      channel.PreparedAttachmentPublicURL,
					PublicURL: "https://cdn.example.com/image.png",
					Mime:      "image/png",
					Size:      1024,
					Logical: channel.Attachment{
						Type: channel.AttachmentImage,
						URL:  "https://cdn.example.com/image.png",
						Mime: "image/png",
						Size: 1024,
					},
				}},
			},
		},
	})
	if err != nil {
		t.Fatalf("Push final returned error after partial send: %v", err)
	}
	if client.pushCalls != 2 {
		t.Fatalf("push calls = %d, want 2", client.pushCalls)
	}
}

func TestLineStreamErrorRedactsSecretsAndClearsBuffer(t *testing.T) {
	redact.ResetForTest()
	t.Cleanup(redact.ResetForTest)

	const secret = "line-secret-value-123456"
	redact.SetSecrets("line-stream-test", secret)

	client := &testMessagingClient{}
	adapter := NewAdapter(nil)
	adapter.client = testLineClientFactory{messaging: client}

	stream, err := adapter.OpenStream(context.Background(), testLineConfig(), "Uuser", channel.StreamOptions{})
	if err != nil {
		t.Fatalf("OpenStream returned error: %v", err)
	}
	if err := stream.Push(context.Background(), channel.PreparedStreamEvent{Type: channel.StreamEventDelta, Delta: "draft"}); err != nil {
		t.Fatalf("Push delta returned error: %v", err)
	}
	err = stream.Push(context.Background(), channel.PreparedStreamEvent{
		Type:  channel.StreamEventError,
		Error: "request failed with token " + secret,
	})
	if err != nil {
		t.Fatalf("Push error returned error: %v", err)
	}
	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("Close returned error: %v", err)
	}

	got := pushedTextMessages(client)
	if len(got) != 1 {
		t.Fatalf("pushed text messages = %#v, want exactly one error message", got)
	}
	if strings.Contains(got[0], secret) {
		t.Fatalf("pushed error leaked secret: %q", got[0])
	}
	if strings.Contains(got[0], "draft") {
		t.Fatalf("pushed error retained buffered draft: %q", got[0])
	}
	if !strings.Contains(got[0], "Error: ") || !strings.Contains(got[0], strings.Repeat("*", len(secret))) {
		t.Fatalf("pushed error did not contain expected redacted error text: %q", got[0])
	}
}

func pushedTextMessages(client *testMessagingClient) []string {
	if client == nil {
		return nil
	}
	var texts []string
	for _, req := range client.pushRequests {
		if req == nil {
			continue
		}
		for _, msg := range req.Messages {
			switch text := msg.(type) {
			case messaging_api.TextMessage:
				texts = append(texts, text.Text)
			case *messaging_api.TextMessage:
				if text != nil {
					texts = append(texts, text.Text)
				}
			}
		}
	}
	return texts
}
