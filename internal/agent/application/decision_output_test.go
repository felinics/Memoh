package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/agent/runtime/native"
	sessionruntime "github.com/felinics/memoh/internal/agent/runtime/session"
)

type decisionOutputBackend struct {
	sessionruntime.Backend
	output []struct {
		Type   string
		Output json.RawMessage
	}
}

func (b *decisionOutputBackend) Publish(ctx context.Context, event sessionruntime.Event) error {
	if event.Delta != nil && event.Delta.DecisionOutput != nil {
		checkpoint := event.Delta.DecisionOutput
		for _, raw := range checkpoint.Events {
			b.output = append(b.output, struct {
				Type   string
				Output json.RawMessage
			}{Output: raw})
		}
		if checkpoint.Done {
			b.output = append(b.output, struct {
				Type   string
				Output json.RawMessage
			}{Type: "decision_output_end"})
		}
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

type failedEndCheckpointBackend struct{ sessionruntime.Backend }

func (b failedEndCheckpointBackend) Update(ctx context.Context, key sessionruntime.Key, update sessionruntime.SnapshotUpdate) (sessionruntime.Snapshot, bool, error) {
	return b.Backend.Update(ctx, key, func(s sessionruntime.Snapshot, exists bool) (sessionruntime.Snapshot, bool, error) {
		next, changed, err := update(s, exists)
		if err == nil && next.DecisionOutput != nil && next.DecisionOutput.Done {
			return s, false, errors.New("checkpoint write unavailable")
		}
		return next, changed, err
	})
}

func TestContinuationClosesRunWhenEndCheckpointCannotPersist(t *testing.T) {
	backend := failedEndCheckpointBackend{sessionruntime.NewMemoryBackend()}
	manager, handle := newWaitingDecisionRuntime(t, backend)
	service := &Service{decisionRuntime: manager}
	service.continueRuntimeDecision(context.Background(), sessionruntime.Command{ID: "answer", BotID: handle.BotID, SessionID: handle.SessionID, RunID: handle.RunID, Generation: handle.Generation}, func(_ context.Context, _ *continuationLifecycleResult, ch chan<- WSStreamEvent) error {
		ch <- runtimeDecisionEvent(t, native.StreamEvent{Type: native.EventUserInputRequest, UserInputID: "next", Status: "pending"})
		ch <- runtimeDecisionEvent(t, native.StreamEvent{Type: native.EventAgentEnd, UserInputID: "next", Status: "pending"})
		return nil
	})
	snapshot, err := manager.Snapshot(context.Background(), handle.BotID, handle.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CurrentRunView != nil && snapshot.CurrentRunView.Status != sessionruntime.RunStatusErrored {
		t.Fatalf("failed end write left run parked: %+v", snapshot.CurrentRunView)
	}
}
