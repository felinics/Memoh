package inbound

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/felinics/memoh/internal/channel"
)

func TestContinuationForwardsReplyAndInteractiveFollowUp(t *testing.T) {
	processor := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, nil, nil, nil, "", 0)
	sender := &fakeReplySender{}
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "test-chat", Message: channel.Message{ID: "answer"}}
	err := processor.streamContinuationCommand(context.Background(), msg, sender, InboundIdentity{BotID: "bot"}, "", func(_ context.Context, ch chan<- json.RawMessage) error {
		ch <- json.RawMessage(`{"type":"text_delta","delta":"收到你的答案"}`)
		ch <- json.RawMessage(`{"type":"user_input_request","toolName":"ask_user","toolCallId":"second-call","userInputId":"second-question","status":"pending","metadata":{"ui_payload":{"version":2,"questions":[{"id":"q1","kind":"text","text":"接下来做什么？"}]}}}`)
		ch <- json.RawMessage(`{"type":"agent_end","userInputId":"second-question","status":"pending"}`)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var text, question bool
	for _, event := range sender.events {
		if event.Type == channel.StreamEventDelta {
			text = true
		}
		if isUserInputEvent(&event) && event.ToolCall.CallID == "second-call" {
			question = true
		}
	}
	if !text || !question {
		t.Fatalf("reply=%v follow-up=%v events=%+v", text, question, sender.events)
	}
}
