package inbound

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	userinput "github.com/felinics/memoh/internal/agent/decision/input"
	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
)

func TestContinuationForwardsReplyAndInteractiveFollowUp(t *testing.T) {
	processor := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, nil, nil, nil)
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

func TestContinuationHiddenToolCallEndsIMTextSegment(t *testing.T) {
	for _, tc := range []struct {
		channel        channel.ChannelType
		wantSegmentEnd bool
	}{
		{channel: channel.ChannelType("telegram"), wantSegmentEnd: true},
		{channel: channel.ChannelType("web"), wantSegmentEnd: false},
	} {
		processor := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, nil, nil, nil)
		sender := &fakeReplySender{}
		msg := channel.InboundMessage{Channel: tc.channel, ReplyTarget: "test-chat", Message: channel.Message{ID: "approve"}}
		err := processor.streamContinuationCommand(context.Background(), msg, sender, InboundIdentity{BotID: "bot"}, "", func(_ context.Context, ch chan<- json.RawMessage) error {
			ch <- json.RawMessage(`{"type":"text_delta","delta":"Let me check."}`)
			ch <- json.RawMessage(`{"type":"tool_call_start","toolName":"read","toolCallId":"call-1"}`)
			ch <- json.RawMessage(`{"type":"text_delta","delta":"Here is the answer."}`)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		var types []channel.StreamEventType
		for _, event := range sender.events {
			if event.Type == channel.StreamEventToolCallStart {
				t.Fatalf("%s: ordinary tool call start must stay hidden, events=%+v", tc.channel, sender.events)
			}
			if event.Type == channel.StreamEventDelta || event.Type == channel.StreamEventSegmentEnd {
				types = append(types, event.Type)
			}
		}
		want := []channel.StreamEventType{channel.StreamEventDelta, channel.StreamEventDelta}
		if tc.wantSegmentEnd {
			want = []channel.StreamEventType{channel.StreamEventDelta, channel.StreamEventSegmentEnd, channel.StreamEventDelta}
		}
		if !reflect.DeepEqual(types, want) {
			t.Fatalf("%s: text events = %v, want %v", tc.channel, types, want)
		}
	}
}

func TestAcceptanceReceiptPrecedesFailedContinuation(t *testing.T) {
	p := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, nil, nil, nil)
	sink := &fakeReplySender{}
	accepted := false
	sender := decisionReplySender{StreamReplySender: sink, accepted: func(context.Context, string) { accepted = true }}
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat"}
	err := p.streamContinuationCommand(context.Background(), msg, sender, InboundIdentity{BotID: "bot"}, "", func(_ context.Context, ch chan<- json.RawMessage) error {
		ch <- json.RawMessage(`{"type":"decision_accepted","decision_id":"question"}`)
		return errors.New("SECRET transport failure after acceptance")
	})
	if err != nil || !accepted {
		t.Fatalf("accepted=%v err=%v", accepted, err)
	}
	found := false
	for _, event := range sink.events {
		if event.Type == channel.StreamEventError {
			found = true
			if strings.Contains(event.Error, "SECRET") {
				t.Fatal("private cause leaked")
			}
			if event.ErrorCode != "agent.response_interrupted" || event.Error != "The model response was interrupted. Please try again." {
				t.Fatalf("failure feedback = %+v, want the response_interrupted copy", event)
			}
		}
	}
	if !found {
		t.Fatal("missing output failure feedback")
	}
}

type statelessApprovalRunner struct {
	calls        []turn.ToolApprovalResponse
	releaseCalls []turn.ToolApprovalResponse
	released     bool
	busy         bool
	events       []json.RawMessage
}

func (r *statelessApprovalRunner) RespondToolApproval(_ context.Context, input turn.ToolApprovalResponse, eventCh chan<- json.RawMessage) error {
	r.calls = append(r.calls, input)
	if r.busy {
		return turn.ErrDecisionOutputBusy
	}
	if r.released {
		return nil
	}
	eventCh <- json.RawMessage(`{"type":"decision_accepted","decision_id":"approval-defer-1"}`)
	for _, ev := range r.events {
		eventCh <- ev
	}
	if !input.DeferRelease {
		r.released = true
	}
	return nil
}

func (r *statelessApprovalRunner) ReleaseToolApprovalOutput(_ context.Context, input turn.ToolApprovalResponse) error {
	r.releaseCalls = append(r.releaseCalls, input)
	r.released = true
	return nil
}

type failFinalOnceReplySender struct {
	fakeReplySender
	streamCount int
}

func (s *failFinalOnceReplySender) OpenStream(_ context.Context, target string, _ channel.StreamOptions) (channel.OutboundStream, error) {
	s.streamCount++
	return &failFinalOnceStream{fakeOutboundStream: fakeOutboundStream{sender: &s.fakeReplySender, target: strings.TrimSpace(target)}, failFinal: s.streamCount == 1}, nil
}

type failFinalOnceStream struct {
	fakeOutboundStream
	failFinal bool
}

func (s *failFinalOnceStream) Push(ctx context.Context, ev channel.StreamEvent) error {
	if s.failFinal && ev.Type == channel.StreamEventFinal {
		return errors.New("telegram draft commit 502 bad gateway")
	}
	return s.fakeOutboundStream.Push(ctx, ev)
}

func TestContinuationDeferReleaseFullReplayBusyAndReceipts(t *testing.T) {
	processor := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, nil, nil, nil)
	runner := &statelessApprovalRunner{events: []json.RawMessage{
		json.RawMessage(`{"type":"text_delta","delta":"Final answer survives!"}`),
		json.RawMessage(`{"type":"agent_end","messages":[{"role":"assistant","content":"Final answer survives!"}]}`),
	}}
	rawSender := &failFinalOnceReplySender{}
	var finalizeCount atomic.Int32
	sender := decisionReplySender{StreamReplySender: rawSender, finalized: func(context.Context) { finalizeCount.Add(1) }}
	msg := channel.InboundMessage{Channel: channel.ChannelType("telegram"), ReplyTarget: "chat-final-fail", Message: channel.Message{ID: "msg-1"}}
	input := turn.ToolApprovalResponse{BotID: "bot-1", ThreadID: "sess-final-fail", Decision: "approve"}

	if err := processor.streamToolApprovalCommand(context.Background(), msg, sender, InboundIdentity{BotID: "bot-1"}, "route-1", runner, input); err == nil {
		t.Fatal("expected StreamEventFinal failure on attempt 1")
	}
	if len(runner.releaseCalls) != 0 || runner.released || finalizeCount.Load() != 0 {
		t.Fatalf("premature release/finalize: releases=%d released=%v finalized=%d", len(runner.releaseCalls), runner.released, finalizeCount.Load())
	}

	runner.busy = true
	busySink := &fakeReplySender{}
	if err := processor.streamToolApprovalCommand(context.Background(), msg, busySink, InboundIdentity{BotID: "bot-1"}, "route-1", runner, input); !errors.Is(err, turn.ErrDecisionOutputBusy) {
		t.Fatalf("busy err = %v, want turn.ErrDecisionOutputBusy", err)
	}
	for _, ev := range busySink.events {
		if ev.Type == channel.StreamEventError {
			t.Fatalf("busy duplicate click pushed error bubble: %+v", ev)
		}
	}
	runner.busy = false

	msg.Message.ID = "msg-2"
	if err := processor.streamToolApprovalCommand(context.Background(), msg, sender, InboundIdentity{BotID: "bot-1"}, "route-1", runner, input); err != nil {
		t.Fatalf("retry err = %v, want nil", err)
	}
	if len(rawSender.sent) != 1 || rawSender.sent[0].Message.PlainText() != "Final answer survives!" {
		t.Fatalf("sent = %+v", rawSender.sent)
	}
	if finalizeCount.Load() != 1 || len(runner.releaseCalls) != 1 || runner.releaseCalls[0].ApprovalID != "approval-defer-1" {
		t.Fatalf("finalized=%d releaseCalls=%+v", finalizeCount.Load(), runner.releaseCalls)
	}

	registry := channel.NewRegistry()
	adapter := &nativeUserInputTestAdapter{typ: channel.ChannelType("telegram"), cardPresent: true}
	registry.MustRegister(adapter)
	cfg := channel.ChannelConfig{BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	receiptProc := NewChannelInboundProcessor(slog.Default(), registry, nil, nil, nil, nil, nil)
	if err := receiptProc.streamContinuationCommand(context.Background(), msg, receiptProc.withDecisionReceipts(&fakeReplySender{}, cfg), InboundIdentity{BotID: "bot-1"}, "sess-1", func(_ context.Context, ch chan<- json.RawMessage) error {
		ch <- json.RawMessage(`{"type":"decision_accepted","decision_id":"req-card-1"}`)
		return turn.ErrDecisionOutputInterrupted
	}); !errors.Is(err, turn.ErrDecisionOutputInterrupted) || len(adapter.cardUpdates) != 0 {
		t.Fatalf("interrupted err=%v cardUpdates=%v", err, adapter.cardUpdates)
	}
	if err := receiptProc.streamContinuationCommand(context.Background(), msg, receiptProc.withDecisionReceipts(&fakeReplySender{}, cfg), InboundIdentity{BotID: "bot-1"}, "sess-1", func(_ context.Context, ch chan<- json.RawMessage) error {
		ch <- json.RawMessage(`{"type":"decision_accepted","decision_id":"req-card-1"}`)
		ch <- json.RawMessage(`{"type":"agent_end","messages":[{"role":"assistant","content":"complete"}]}`)
		return nil
	}); err != nil || !reflect.DeepEqual(adapter.cardUpdates, []string{"req-card-1"}) {
		t.Fatalf("completed err=%v cardUpdates=%v", err, adapter.cardUpdates)
	}
}

type statelessPlainTextGateway struct {
	fakeChatGateway
	released, failFirst bool
	respondCalls        []turn.UserInputResponse
}

func (g *statelessPlainTextGateway) AdvancePlainTextUserInput(_ context.Context, input userinput.AdvanceTextInput) (userinput.AdvanceTextResult, error) {
	if g.released {
		return userinput.AdvanceTextResult{Handled: false}, nil
	}
	return userinput.AdvanceTextResult{Handled: true, Request: userinput.Request{
		ID: "req-plain-1", BotID: input.BotID, Status: userinput.StatusSubmitted,
		UIPayload:   userinput.UIPayload{Version: 2, Questions: []userinput.UIQuestion{{ID: "q1", Kind: userinput.QuestionKindText, Text: "When?"}}},
		Interaction: userinput.TextInteractionState{Completed: true, Answers: []userinput.QuestionAnswer{{QuestionID: "q1", Text: input.Text}}},
	}}, nil
}

func (g *statelessPlainTextGateway) RespondUserInput(_ context.Context, input turn.UserInputResponse, eventCh chan<- json.RawMessage) error {
	g.respondCalls = append(g.respondCalls, input)
	eventCh <- json.RawMessage(`{"type":"decision_accepted","decision_id":"req-plain-1"}`)
	eventCh <- json.RawMessage(`{"type":"text_delta","delta":"Full answer delivered."}`)
	if g.failFirst && len(g.respondCalls) == 1 {
		return turn.ErrDecisionOutputInterrupted
	}
	eventCh <- json.RawMessage(`{"type":"agent_end","messages":[{"role":"assistant","content":"Full answer delivered."}]}`)
	return nil
}

func (g *statelessPlainTextGateway) ReleaseUserInputOutput(_ context.Context, _ turn.UserInputResponse) error {
	g.released = true
	return nil
}

func TestPlainTextUserInputStatelessRetryAndPostReleaseNormalChat(t *testing.T) {
	gw := &statelessPlainTextGateway{failFirst: true}
	processor := NewChannelInboundProcessor(slog.Default(), nil, nil, nil, gw, nil, nil)
	cfg := channel.ChannelConfig{ID: "cfg-1", BotID: "bot-1", ChannelType: channel.ChannelType("telegram")}
	id := InboundIdentity{BotID: "bot-1", UserID: "u-1"}
	msg := channel.InboundMessage{
		Channel: channel.ChannelType("telegram"), ReplyTarget: "chat-pt",
		Conversation: channel.Conversation{ID: "chat-pt", Type: channel.ConversationTypePrivate},
		Message:      channel.Message{ID: "m-1", Text: "tomorrow morning"},
	}
	if handled, err := processor.handlePlainTextUserInput(context.Background(), cfg, msg, &fakeReplySender{}, id, "route-1", "sess-pt", "tomorrow morning"); !handled || !errors.Is(err, turn.ErrDecisionOutputInterrupted) || gw.released {
		t.Fatalf("attempt 1 = (%v, %v), released=%v", handled, err, gw.released)
	}
	msg.Message.ID = "m-2"
	sink2 := &fakeReplySender{}
	if handled, err := processor.handlePlainTextUserInput(context.Background(), cfg, msg, sink2, id, "route-1", "sess-pt", "tomorrow morning"); !handled || err != nil || !gw.released || len(sink2.sent) != 1 || sink2.sent[0].Message.PlainText() != "Full answer delivered." {
		t.Fatalf("stateless resend = (%v, %v), released=%v sent=%+v", handled, err, gw.released, sink2.sent)
	}
	msg.Message.ID = "m-3"
	if handled, err := processor.handlePlainTextUserInput(context.Background(), cfg, msg, &fakeReplySender{}, id, "route-1", "sess-pt", "tomorrow morning"); handled || err != nil || len(gw.respondCalls) != 2 {
		t.Fatalf("post-release normal chat = (%v, %v), calls=%d", handled, err, len(gw.respondCalls))
	}
}
