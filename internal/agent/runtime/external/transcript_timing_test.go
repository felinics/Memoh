package external

import (
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/event"
)

func TestTranscriptRecorderStampsToolExecutionTimingFromArrival(t *testing.T) {
	t.Parallel()

	now := time.UnixMilli(10_000)
	recorder := NewTranscriptRecorder()
	recorder.now = func() time.Time { return now }
	recorder.since = func(time.Time) time.Duration { return 700 * time.Millisecond }
	recorder.Add(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "call-1", ToolName: "exec", Input: map[string]any{}})
	// The wall clock steps backwards mid-call; the end mark must still be
	// start plus elapsed.
	now = time.UnixMilli(9_000)
	recorder.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "call-1", ToolName: "exec", Result: "ok"})

	messages := recorder.Messages("")
	var call sdk.ToolCallPart
	for _, message := range messages {
		for _, part := range message.Content {
			if candidate, ok := part.(sdk.ToolCallPart); ok {
				call = candidate
			}
		}
	}
	timing, ok := call.ProviderMetadata[event.ExecutionTimingMetadataKey].(event.ExecutionTiming)
	if !ok || timing.StartedAtMS != 10_000 || timing.EndedAtMS != 10_700 {
		t.Fatalf("execution timing = %#v", call.ProviderMetadata)
	}
}

func TestTranscriptRecorderKeepsRuntimeProvidedExecutionTiming(t *testing.T) {
	t.Parallel()

	recorder := NewTranscriptRecorder()
	recorder.Add(event.StreamEvent{Type: event.ToolCallStart, ToolCallID: "call-1", ToolName: "exec"})
	provided := event.ExecutionTiming{StartedAtMS: 1, EndedAtMS: 2}
	recorder.Add(event.StreamEvent{Type: event.ToolCallEnd, ToolCallID: "call-1", ToolName: "exec", Result: "ok", Metadata: map[string]any{event.ExecutionTimingMetadataKey: provided}})

	call := recorder.Messages("")[0].Content[0].(sdk.ToolCallPart)
	if call.ProviderMetadata[event.ExecutionTimingMetadataKey] != provided {
		t.Fatalf("provided timing overwritten: %#v", call.ProviderMetadata)
	}
}
