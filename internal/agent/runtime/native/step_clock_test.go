package native

import (
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

// A wall-clock step between two readings must never produce a first token
// before its request or an end before its first token: every later mark is
// the start anchor plus monotonic elapsed time.
func TestStepClockDerivesMarksFromElapsedTime(t *testing.T) {
	t.Parallel()

	wall := time.UnixMilli(10_000)
	elapsed := 300 * time.Millisecond
	clock := newStepClock(func() time.Time { return wall })
	clock.since = func(time.Time) time.Duration { return elapsed }

	if got := clock.begin(); got != 10_000 {
		t.Fatalf("begin = %d, want 10000", got)
	}
	wall = time.UnixMilli(9_000)
	clock.firstTokenText("hi")
	elapsed = 900 * time.Millisecond
	completed, ok := clock.finish(sdk.Usage{}, sdk.FinishReasonStop)
	if !ok {
		t.Fatal("finish reported no active attempt")
	}
	want := event.StepTiming{StartedAtMS: 10_000, FirstTokenAtMS: 10_300, EndedAtMS: 10_900}
	if completed.Timing != want {
		t.Fatalf("timing = %+v, want %+v", completed.Timing, want)
	}
}

func TestToolExecutionRegistryClocksWithElapsedTime(t *testing.T) {
	t.Parallel()

	registry := newToolExecutionMetadataRegistry(nil)
	wall := time.UnixMilli(5_000)
	registry.now = func() time.Time { return wall }
	registry.since = func(time.Time) time.Duration { return 250 * time.Millisecond }
	tools := registry.wrapExecute([]sdk.Tool{{Name: "exec", Execute: func(*sdk.ToolExecContext, any) (any, error) {
		wall = time.UnixMilli(4_000)
		return "ok", nil
	}}})
	if _, err := tools[0].Execute(&sdk.ToolExecContext{ToolCallID: "c1"}, nil); err != nil {
		t.Fatalf("execute: %v", err)
	}
	timing, ok := registry.metadata("c1")[event.ExecutionTimingMetadataKey].(event.ExecutionTiming)
	if !ok || timing != (event.ExecutionTiming{StartedAtMS: 5_000, EndedAtMS: 5_250}) {
		t.Fatalf("timing = %#v", registry.metadata("c1"))
	}
}

func TestStepBoundaryEmitterPairsFinishPartsWithCompletionsInOrder(t *testing.T) {
	t.Parallel()

	current := time.UnixMilli(1_000)
	now := func() time.Time { return current }
	clock := newStepClock(now)
	clock.since = func(startedAt time.Time) time.Duration { return now().Sub(startedAt) }
	emitter := &stepBoundaryEmitter{clock: clock}

	// The seam finishes two requests before the event loop publishes either.
	clock.begin()
	current = current.Add(40 * time.Millisecond)
	clock.finish(sdk.Usage{OutputTokens: 1}, sdk.FinishReasonToolCalls)
	current = current.Add(10 * time.Millisecond)
	clock.begin()
	current = current.Add(70 * time.Millisecond)
	clock.finish(sdk.Usage{OutputTokens: 2}, sdk.FinishReasonStop)

	first, ok := emitter.observe(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls})
	if !ok || first.Timing == nil || first.Timing.StartedAtMS != 1_000 || first.Timing.EndedAtMS != 1_040 {
		t.Fatalf("first step_end = %#v, want the first request's clock", first.Timing)
	}
	second, ok := emitter.observe(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop})
	if !ok || second.Timing == nil || second.Timing.StartedAtMS != 1_050 || second.Timing.EndedAtMS != 1_120 || second.StepIndex != 1 {
		t.Fatalf("second step_end = %#v (index %d), want the second request's clock", second.Timing, second.StepIndex)
	}
	if third, _ := emitter.observe(&sdk.FinishStepPart{}); third.Timing != nil {
		t.Fatalf("a finish without a completed request carries no timing: %#v", third.Timing)
	}
}
