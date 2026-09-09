package sessionruntime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
	chatview "github.com/felinics/memoh/internal/agent/view"
)

func TestHandleAgentEventPublishesStepTracesAnchoredToBlocks(t *testing.T) {
	t.Parallel()

	fixture := newAdmitFixture(t)
	admission, err := fixture.manager.Admit(context.Background(), fixture.input("invocation-step-trace", `{"text":"trace me"}`))
	if err != nil {
		t.Fatalf("admit run: %v", err)
	}
	handle := admission.Handle
	sub, err := fixture.manager.Subscribe(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer sub.Close()

	usage, _ := json.Marshal(map[string]any{"inputTokens": 40, "outputTokens": 2})
	events := []native.StreamEvent{
		{Type: native.EventAgentStart},
		{Type: native.EventStepStart, StepIndex: 0},
		{Type: native.EventTextDelta, Delta: "hello"},
		{Type: native.EventStepEnd, StepIndex: 0, FinishReason: "tool-calls", Usage: usage, Timing: &event.StepTiming{StartedAtMS: 1_000, FirstTokenAtMS: 1_100, EndedAtMS: 1_500}},
		{Type: native.EventStepStart, StepIndex: 1},
		{Type: native.EventToolCallStart, ToolName: "exec", ToolCallID: "call-1"},
		{Type: native.EventToolCallEnd, ToolName: "exec", ToolCallID: "call-1", Result: "ok", Metadata: map[string]any{
			event.ExecutionTimingMetadataKey: event.ExecutionTiming{StartedAtMS: 1_600, EndedAtMS: 1_900},
		}},
		{Type: native.EventStepEnd, StepIndex: 1, FinishReason: "stop", Timing: &event.StepTiming{StartedAtMS: 1_550, EndedAtMS: 1_590}},
		{Type: native.EventStepStart, StepIndex: 2},
		{Type: native.EventStepEnd, StepIndex: 2, Timing: &event.StepTiming{StartedAtMS: 2_000, EndedAtMS: 2_100}},
	}
	for _, ev := range events {
		if _, err := fixture.manager.HandleAgentEvent(context.Background(), handle, ev); err != nil {
			t.Fatalf("publish %s: %v", ev.Type, err)
		}
	}

	snapshot, err := fixture.manager.Snapshot(context.Background(), testBotID, testSessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	run := snapshot.CurrentRunView
	if run == nil || len(run.StepTraces) != 2 {
		t.Fatalf("run step traces = %#v", run)
	}
	if run.StepTraces[0].FirstMessageID != run.Messages[0].ID || run.StepTraces[0].FinishReason != "tool-calls" || run.StepTraces[0].Usage == nil || run.StepTraces[0].Usage.InputTokens != 40 {
		t.Fatalf("first trace = %#v", run.StepTraces[0])
	}
	if run.StepTraces[1].FirstMessageID != run.Messages[1].ID || run.StepTraces[1].StepIndex != 1 {
		t.Fatalf("second trace = %#v", run.StepTraces[1])
	}
	if tool := run.Messages[1]; tool.Type != chatview.UIMessageTool || tool.ExecutionTiming == nil || tool.ExecutionTiming.EndedAtMS != 1_900 {
		t.Fatalf("tool block = %#v", tool)
	}

	var appended []chatview.UIStepTrace
	deadline := time.After(2 * time.Second)
	for len(appended) < 2 {
		select {
		case ev, ok := <-sub.C:
			if !ok {
				t.Fatalf("subscription closed early")
			}
			if ev.Delta != nil {
				appended = append(appended, ev.Delta.StepTraceAppends...)
			}
		case <-deadline:
			t.Fatalf("step trace deltas = %#v, want two", appended)
		}
	}
	if appended[0].StepIndex != 0 || appended[1].StepIndex != 1 {
		t.Fatalf("appended = %#v", appended)
	}
}

func TestCloneSnapshotKeepsStepTraces(t *testing.T) {
	t.Parallel()

	snapshot := Snapshot{CurrentRunView: &CurrentRunView{
		RunID:      "run-1",
		TurnID:     "turn-1",
		Messages:   []chatview.UIMessage{{ID: 0, Type: chatview.UIMessageText, Content: "hi"}},
		StepTraces: []chatview.UIStepTrace{{FirstMessageID: 0, LastMessageID: 0, StepIndex: 0, StartedAtMS: 1, EndedAtMS: 2}},
	}}
	cloned, err := cloneSnapshot(snapshot)
	if err != nil {
		t.Fatalf("clone: %v", err)
	}
	if len(cloned.CurrentRunView.StepTraces) != 1 || cloned.CurrentRunView.StepTraces[0].EndedAtMS != 2 {
		t.Fatalf("cloned step traces = %#v", cloned.CurrentRunView.StepTraces)
	}
	cloned.CurrentRunView.StepTraces[0].EndedAtMS = 9
	if snapshot.CurrentRunView.StepTraces[0].EndedAtMS != 2 {
		t.Fatalf("clone shares the step trace backing array")
	}
}
