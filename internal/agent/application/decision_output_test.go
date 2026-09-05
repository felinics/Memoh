package application

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

type decisionOutputBackend struct {
	sessionruntime.Backend
	output []sessionruntime.Event
}

func (b *decisionOutputBackend) Publish(ctx context.Context, event sessionruntime.Event) error {
	if len(event.Output) > 0 || event.Type == "decision_output_end" {
		b.output = append(b.output, event)
	}
	return b.Backend.Publish(ctx, event)
}

func TestContinuationPublishesTextAndNextQuestionToChannel(t *testing.T) {
	for _, nextQuestion := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary reply", true: "second question"}[nextQuestion], func(t *testing.T) {
			backend := &decisionOutputBackend{Backend: sessionruntime.NewMemoryBackend()}
			manager, handle := newWaitingDecisionRuntime(t, backend)
			service := &Service{decisionRuntime: manager}
			events := []native.StreamEvent{{Type: native.EventAgentStart}, {Type: native.EventTextDelta, Delta: "收到你的答案"}}
			terminal := native.StreamEvent{Type: native.EventAgentEnd}
			if nextQuestion {
				events = append(events, native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "next-question", Status: "pending"})
				terminal.UserInputID = "next-question"
				terminal.Status = "pending"
			}
			events = append(events, terminal)
			service.continueRuntimeDecision(context.Background(), sessionruntime.Command{ID: "answer-command", BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, Generation: handle.Generation}, func(_ context.Context, _ *continuationLifecycleResult, ch chan<- WSStreamEvent) error {
				for _, event := range events {
					ch <- runtimeDecisionEvent(t, event)
				}
				return nil
			})
			if len(backend.output) != len(events)+1 {
				t.Fatalf("channel output count=%d, want %d", len(backend.output), len(events)+1)
			}
			for i, want := range events {
				var got native.StreamEvent
				if err := json.Unmarshal(backend.output[i].Output, &got); err != nil {
					t.Fatal(err)
				}
				if got.Type != want.Type || got.Delta != want.Delta || got.UserInputID != want.UserInputID {
					t.Fatalf("event %d=%+v want %+v", i, got, want)
				}
			}
			if backend.output[len(events)].Type != "decision_output_end" {
				t.Fatal("channel stream did not close")
			}
		})
	}
}
