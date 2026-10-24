package native

import (
	"context"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
)

// internalFailures drains a stream and returns its error events.
func internalFailures(t *testing.T, events <-chan StreamEvent) []StreamEvent {
	t.Helper()
	var failures []StreamEvent
	for event := range events {
		if event.Type == EventError {
			failures = append(failures, event)
		}
	}
	return failures
}

func requireInternalFailure(t *testing.T, failures []StreamEvent) {
	t.Helper()
	if len(failures) != 1 || failures[0].Code != "internal" || failures[0].Cause == nil {
		t.Fatalf("error events = %#v, want one with code internal and a private cause", failures)
	}
	if failures[0].Error != "" {
		t.Fatalf("error event text = %q, want none: the panic value must not be published", failures[0].Error)
	}
}

// A panic while the stream is being set up ends that stream with an internal
// failure; the process survives and the channel closes.
func TestAgentStreamContainsAPanicInTheStreamGoroutine(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	agent := New(Deps{ContextViewApplier: func(context.Context, RunConfig) (RunConfig, error) {
		panic("view boom")
	}})
	requireInternalFailure(t, internalFailures(t, agent.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: agentStreamTestProvider(streamScript(&invocations, scriptText("answer")))},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		ContextMutations: contextfrag.NewMutationLedger(),
	})))
}

// The step loop runs on its own goroutine, out of reach of the stream
// goroutine's recover. A panic in it, here in the commit barrier, also ends
// only the run.
func TestAgentStreamContainsAPanicInTheStepLoop(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	requireInternalFailure(t, internalFailures(t, New(Deps{}).Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: agentStreamTestProvider(streamScript(&invocations, scriptText("answer")))},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		ContextMutations: contextfrag.NewMutationLedger(),
		OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
			panic("commit boom")
		},
	})))
}
