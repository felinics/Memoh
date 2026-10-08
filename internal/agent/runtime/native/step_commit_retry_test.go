package native

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

// localFailureCauses are failures a retry would take if the provider call
// reported them. Reported by the loop's own work they end the run instead.
func localFailureCauses() []struct {
	name string
	err  error
} {
	return []struct {
		name string
		err  error
	}{
		{"unexpected EOF", fmt.Errorf("write step: %w", io.ErrUnexpectedEOF)},
		{"provider 503", &sdk.APIError{StatusCode: 503, Kind: sdk.KindServerError}},
		{"refused connection", refusedConnection()},
	}
}

// assertLocalFailureEndedRun requires the run to end with one error event
// whose cause holds want and is not a model call failure, and no retry.
func assertLocalFailureEndedRun(t *testing.T, events []StreamEvent, want error, stage string) {
	t.Helper()
	var retries int
	var failures []StreamEvent
	for _, event := range events {
		switch event.Type {
		case EventRetry:
			retries++
		case EventError:
			failures = append(failures, event)
		}
	}
	if retries != 0 {
		t.Fatalf("retry events = %d, want 0", retries)
	}
	if len(failures) != 1 {
		t.Fatalf("error events = %#v, want exactly one", failures)
	}
	cause := failures[0].Cause
	if !errors.Is(cause, want) {
		t.Fatalf("error cause = %v, want it to hold %v", cause, want)
	}
	if !strings.Contains(cause.Error(), stage) || failures[0].Error != "" || failures[0].Code != codeRunFailed {
		t.Fatalf("error event = %#v, want a %s event with a cause naming %q", failures[0], codeRunFailed, stage)
	}
	if IsModelCallFailure(cause) {
		t.Fatalf("IsModelCallFailure(%v) = true, want false for the loop's own work", cause)
	}
	if len(events) == 0 || events[len(events)-1].Type != EventAgentAbort {
		t.Fatalf("stream termination = %v, want %q", events, EventAgentAbort)
	}
}

func TestStepCommitFailureMustNotReplayExecutedTool(t *testing.T) {
	for _, tc := range localFailureCauses() {
		t.Run(tc.name, func(t *testing.T) {
			var effects, commits, providerCalls atomic.Int32
			provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
				providerCalls.Add(1)
				return closedAgentTestStream(
					&sdk.StartStepPart{},
					&sdk.StreamToolCallPart{ToolCallID: "side-effect-1", ToolName: "record_effect", Input: toolexec.ArgumentsFromValue(map[string]any{})},
					&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
				), nil
			})
			a := New(Deps{})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
				Name: "record_effect",
				Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					effects.Add(1)
					return toolexec.OutputFromValue("effect recorded"), nil
				},
			}}}})
			var events []StreamEvent
			for event := range a.Stream(context.Background(), RunConfig{
				Model:            &sdk.Model{ID: "qc-fixture", Provider: provider},
				Messages:         []sdk.Message{sdk.UserMessage("record one effect")},
				SupportsToolCall: true,
				Identity:         SessionContext{BotID: "qc-bot"},
				Retry:            fastRetry,
				OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
					commits.Add(1)
					return StepDirective{}, tc.err
				},
			}) {
				events = append(events, event)
			}

			if got := effects.Load(); got != 1 {
				t.Fatalf("executed side effects = %d, want 1", got)
			}
			if got := providerCalls.Load(); got != 1 {
				t.Fatalf("provider calls = %d, want 1", got)
			}
			if got := commits.Load(); got != 1 {
				t.Fatalf("commit attempts = %d, want 1", got)
			}
			assertLocalFailureEndedRun(t, events, tc.err, "commit step")
		})
	}
}

// A tool batch that fails (here its approval handler) ends the run: a retry
// would call the model again and run the batch a second time.
func TestToolBatchFailureIsNotRetried(t *testing.T) {
	for _, tc := range localFailureCauses() {
		t.Run(tc.name, func(t *testing.T) {
			var executions, providerCalls atomic.Int32
			provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
				providerCalls.Add(1)
				return closedAgentTestStream(
					&sdk.StartStepPart{},
					&sdk.StreamToolCallPart{ToolCallID: "guarded-1", ToolName: "guarded", Input: toolexec.ArgumentsFromValue(map[string]any{})},
					&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
				), nil
			})
			a := New(Deps{})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
				Name:            "guarded",
				RequireApproval: true,
				Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					executions.Add(1)
					return toolexec.OutputFromValue("ran"), nil
				},
			}}}})
			var events []StreamEvent
			for event := range a.Stream(context.Background(), RunConfig{
				Model:            &sdk.Model{ID: "tool-failure", Provider: provider},
				Messages:         []sdk.Message{sdk.UserMessage("run the guarded tool")},
				SupportsToolCall: true,
				Identity:         SessionContext{BotID: "bot-1"},
				Retry:            fastRetry,
				ToolApprovalHandler: func(context.Context, sdk.ToolCall) (toolexec.ToolApprovalResult, error) {
					return toolexec.ToolApprovalResult{}, tc.err
				},
			}) {
				events = append(events, event)
			}

			if got := providerCalls.Load(); got != 1 {
				t.Fatalf("provider calls = %d, want 1", got)
			}
			if got := executions.Load(); got != 0 {
				t.Fatalf("tool executions = %d, want 0", got)
			}
			assertLocalFailureEndedRun(t, events, tc.err, "approval handler")
		})
	}
}

// A provider attempt the loop cannot hand off ends the run before the model
// is called, without a retry.
func TestProviderAttemptHandoffFailureIsNotRetried(t *testing.T) {
	t.Parallel()

	var providerCalls atomic.Int32
	provider := agentStreamTestProvider(func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
		providerCalls.Add(1)
		return closedAgentTestStream(&sdk.StartStepPart{}, &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}), nil
	})
	cfg := RunConfig{Model: &sdk.Model{ID: "handoff-failure", Provider: provider}, Retry: fastRetry}
	eng := &streamEngine{
		agent:     New(Deps{}),
		baseCfg:   cfg,
		cfg:       cfg,
		streamCtx: context.Background(),
		events:    make(chan StreamEvent, 8),
		done:      make(chan struct{}),
	}
	// No staged attempt: publishing it fails.
	eng.reset(generateDispatch{handoff: newProviderAttemptHandoff(cfg)})
	eng.run()

	var events []StreamEvent
	for event := range eng.events {
		events = append(events, event)
	}
	if got := providerCalls.Load(); got != 0 {
		t.Fatalf("provider calls = %d, want 0", got)
	}
	if len(events) != 1 || events[0].Type != EventError || !errors.Is(events[0].Cause, errProviderAttemptNotPrepared) {
		t.Fatalf("events = %#v, want one error event for the failed handoff", events)
	}
	if !eng.aborted {
		t.Fatal("engine did not end after the failed handoff")
	}
}
