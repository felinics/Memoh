package native

import (
	"context"
	"encoding/json"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func stepBoundaryEvents(events []StreamEvent) (starts, ends []StreamEvent) {
	for _, event := range events {
		switch event.Type {
		case EventStepStart:
			starts = append(starts, event)
		case EventStepEnd:
			ends = append(ends, event)
		}
	}
	return starts, ends
}

func TestAgentStreamEmitsStepBoundariesPerModelRequest(t *testing.T) {
	t.Parallel()

	calls := 0
	provider := agentStreamTestProvider(func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
		calls++
		if calls == 1 {
			return closedAgentTestStream(
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{ToolCallID: "call-1", ToolName: "echo", Input: map[string]any{"text": "x"}},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls, Usage: sdk.Usage{InputTokens: 10, OutputTokens: 2}},
			), nil
		}
		return closedAgentTestStream(
			&sdk.StartStepPart{},
			&sdk.TextDeltaPart{ID: "text-1", Text: "done"},
			&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop, Usage: sdk.Usage{InputTokens: 20, OutputTokens: 4}},
		), nil
	})
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "echo",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return "ok", nil
		},
	}}}})

	var events []StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		SupportsToolCall: true,
	}) {
		events = append(events, event)
	}

	starts, ends := stepBoundaryEvents(events)
	if len(starts) != 2 || len(ends) != 2 {
		t.Fatalf("step boundaries = %d starts, %d ends: %#v", len(starts), len(ends), events)
	}
	for i := range 2 {
		if starts[i].StepIndex != i || ends[i].StepIndex != i {
			t.Fatalf("step index mismatch: start=%#v end=%#v", starts[i], ends[i])
		}
		if ends[i].Timing == nil || ends[i].Timing.StartedAtMS == 0 || ends[i].Timing.EndedAtMS < ends[i].Timing.StartedAtMS {
			t.Fatalf("step %d timing = %#v", i, ends[i].Timing)
		}
	}
	if ends[0].FinishReason != string(sdk.FinishReasonToolCalls) || ends[1].FinishReason != string(sdk.FinishReasonStop) {
		t.Fatalf("finish reasons = %q, %q", ends[0].FinishReason, ends[1].FinishReason)
	}
	var usage sdk.Usage
	if err := json.Unmarshal(ends[1].Usage, &usage); err != nil || usage.InputTokens != 20 {
		t.Fatalf("second step usage = %s (%v)", ends[1].Usage, err)
	}

	position := func(match func(StreamEvent) bool) int {
		for i, event := range events {
			if match(event) {
				return i
			}
		}
		return -1
	}
	firstStart := position(func(e StreamEvent) bool { return e.Type == EventStepStart && e.StepIndex == 0 })
	firstEnd := position(func(e StreamEvent) bool { return e.Type == EventStepEnd && e.StepIndex == 0 })
	secondStart := position(func(e StreamEvent) bool { return e.Type == EventStepStart && e.StepIndex == 1 })
	toolStart := position(func(e StreamEvent) bool { return e.Type == EventToolCallStart })
	toolEnd := position(func(e StreamEvent) bool { return e.Type == EventToolCallEnd })
	// The model request ends before its tool executes; the execution belongs
	// to the step until the next request opens.
	if firstStart >= toolStart || toolStart >= firstEnd || firstEnd >= toolEnd || toolEnd >= secondStart {
		t.Fatalf("unexpected step ordering: start0=%d tool_start=%d end0=%d tool_end=%d start1=%d", firstStart, toolStart, firstEnd, toolEnd, secondStart)
	}
	if last := events[len(events)-1]; last.Type != EventAgentEnd {
		t.Fatalf("last event = %#v", last)
	}
}

func TestStepBoundaryEmitterResetReopensTheRetriedRequest(t *testing.T) {
	t.Parallel()

	emitter := &stepBoundaryEmitter{clock: newStepClock(nil), index: 2}
	if _, ok := emitter.observe(&sdk.StartStepPart{}); !ok {
		t.Fatal("first start not emitted")
	}
	if _, ok := emitter.observe(&sdk.StartStepPart{}); ok {
		t.Fatal("duplicate start emitted while the request is open")
	}
	emitter.reset(2)
	ev, ok := emitter.observe(&sdk.StartStepPart{})
	if !ok || ev.Type != EventStepStart || ev.StepIndex != 2 {
		t.Fatalf("retried start = %#v (ok=%v), want step_start at index 2", ev, ok)
	}
}
