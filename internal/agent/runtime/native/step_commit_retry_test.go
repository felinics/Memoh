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

func TestStepCommitFailureMustNotReplayExecutedTool(t *testing.T) {
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
		Retry:            RetryConfig{MaxAttempts: 1, FastAttempts: 1},
		OnStepCommitted: func(context.Context, int, *step.Record) (StepDirective, error) {
			commits.Add(1)
			return StepDirective{}, io.EOF
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
	var retries, errorsSeen int
	for _, event := range events {
		switch event.Type {
		case EventRetry:
			retries++
		case EventError:
			errorsSeen++
			if !strings.Contains(event.Error, "commit step") {
				t.Fatalf("commit error = %q, want commit step context", event.Error)
			}
		}
	}
	if retries != 0 {
		t.Fatalf("retry events = %d, want 0", retries)
	}
	if errorsSeen != 1 {
		t.Fatalf("error events = %d, want 1", errorsSeen)
	}
	if len(events) == 0 || events[len(events)-1].Type != EventAgentAbort {
		t.Fatalf("stream termination = %v, want %q", events, EventAgentAbort)
	}
}

func TestRetryableStreamErrorRejectsTaggedCommitErrors(t *testing.T) {
	t.Parallel()

	tagged := tagStepCommitError(io.EOF)
	if isRetryableStreamError(tagged) {
		t.Fatal("tagged commit error wrapping EOF is retryable")
	}
	rewrapped := fmt.Errorf("twilightai: commit step 0: %w", tagged)
	if isRetryableStreamError(rewrapped) {
		t.Fatal("re-wrapped tagged commit error is retryable")
	}
	if !isRetryableStreamError(io.EOF) {
		t.Fatal("plain EOF is not retryable")
	}
}

func TestStepCommitErrorPreservesCauseAndMessage(t *testing.T) {
	t.Parallel()

	original := errors.New("commit failed")
	tagged := tagStepCommitError(original)
	if tagged.Error() != original.Error() {
		t.Fatalf("tagged error = %q, want %q", tagged.Error(), original.Error())
	}
	if !errors.Is(tagged, original) {
		t.Fatal("tagged error does not preserve original cause")
	}
}
