package application

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/event"
	"github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestConfigureNativeStepTracePublishesRunTraceThroughLifecycle(t *testing.T) {
	t.Parallel()

	tracker := newStepTraceTracker(nil)
	cfg := native.RunConfig{ContextLifecycle: contextfrag.NewLifecycleHolder()}
	cfg.ContextLifecycle.SetManifest(contextfrag.BuildManifest(nil))
	var previousAgentEvents, previousProviderEvents int
	cfg.OnAgentEventObserved = func(native.StreamEvent) { previousAgentEvents++ }
	cfg.OnProviderStreamEventObserved = func(native.StreamEvent) { previousProviderEvents++ }
	configureNativeStepTrace(&cfg, tracker, nil)

	cfg.OnAgentEventObserved(native.StreamEvent{Type: native.EventAgentStart})
	cfg.OnProviderStreamEventObserved(providerStepEnd(1_000, 1_100, 2_000, sdk.Usage{OutputTokens: 3}, "tool-calls"))
	cfg.OnAgentEventObserved(native.StreamEvent{Type: native.EventToolCallStart, ToolCallID: "c1"})
	cfg.OnAgentEventObserved(native.StreamEvent{Type: native.EventToolCallEnd, ToolCallID: "c1", Metadata: map[string]any{
		event.ExecutionTimingMetadataKey: event.ExecutionTiming{StartedAtMS: 2_000, EndedAtMS: 2_300},
	}})
	cfg.OnAgentEventObserved(native.StreamEvent{Type: native.EventAgentEnd})

	if previousAgentEvents != 4 || previousProviderEvents != 1 {
		t.Fatalf("previous observers = %d agent, %d provider", previousAgentEvents, previousProviderEvents)
	}
	snapshot, ok := cfg.ContextLifecycle.Snapshot()
	if !ok || snapshot.RunTrace == nil {
		t.Fatalf("snapshot run trace missing: %#v", snapshot.RunTrace)
	}
	if snapshot.RunTrace.Steps != 1 || snapshot.RunTrace.ToolCalls != 1 || snapshot.RunTrace.ToolMs != 300 || snapshot.RunTrace.LLMMs != 1_000 {
		t.Fatalf("run trace = %#v", snapshot.RunTrace)
	}
}
