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
