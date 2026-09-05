package native

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

// streamScript builds a DoStream implementation that plays back one scripted
// stream per provider invocation; invocations beyond the script replay the
// last entry (useful for "fails forever" scenarios).
func streamScript(invocations *atomic.Int32, scripts ...func(chan<- sdk.StreamPart)) func(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	return func(_ context.Context, _ sdk.GenerateParams) (*sdk.StreamResult, error) {
		idx := int(invocations.Add(1)) - 1
		ch := make(chan sdk.StreamPart, 16)
		go func() {
			defer close(ch)
			script := scripts[len(scripts)-1]
			if idx < len(scripts) {
				script = scripts[idx]
			}
			script(ch)
		}()
		return &sdk.StreamResult{Stream: ch}, nil
	}
}

func scriptText(text string) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.TextStartPart{ID: "mock"}
		ch <- &sdk.TextDeltaPart{ID: "mock", Text: text}
		ch <- &sdk.TextEndPart{ID: "mock"}
		ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}
		ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
	}
}

// scriptStreamError fails the step mid-stream: any partial text is poisoned by
// the SDK (never committed), mirroring how a real provider 429 surfaces.
func scriptStreamError(partialText, errMsg string) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		if partialText != "" {
			ch <- &sdk.TextStartPart{ID: "mock"}
			ch <- &sdk.TextDeltaPart{ID: "mock", Text: partialText}
		}
		ch <- &sdk.ErrorPart{Error: errors.New(errMsg)}
	}
}

func scriptToolCall(callID, toolName string) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.StreamToolCallPart{ToolCallID: callID, ToolName: toolName, Input: map[string]any{"q": "fold"}}
		ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls}
		ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonToolCalls}
	}
}

func drainStreamEvents(ch <-chan StreamEvent) []StreamEvent {
	var events []StreamEvent
	for {
		select {
		case ev := <-ch:
			events = append(events, ev)
		default:
			return events
		}
	}
}

func countEventType(events []StreamEvent, eventType StreamEventType) int {
	count := 0
	for _, ev := range events {
		if ev.Type == eventType {
			count++
		}
	}
	return count
}

// countToolResultText counts tool-result parts whose payload mentions text:
// results live in ToolResultPart.Result (any), not in TextPart, so text
// counters miss them. Counting parts (not substring occurrences) keeps the
// assertion about duplication, not payload size.
func countToolResultText(messages []sdk.Message, text string) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != sdk.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			result, ok := part.(sdk.ToolResultPart)
			if !ok {
				continue
			}
			if value, ok := result.Result.(string); ok && strings.Contains(value, text) {
				count++
			}
		}
	}
	return count
}

func retryLoopTestConfig(provider *atomicMockProvider, retry RetryConfig) RunConfig {
	return captureProviderAttemptPrefix(RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("original task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            retry,
	})
}

// fastRetry keeps tests instant: every attempt fires with no backoff delay.
var fastRetry = RetryConfig{MaxAttempts: 5, FastAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

// TestRunMidStreamRetryLoopsOnRetryableStreamError is the regression test for
// the single-attempt cap: a retryable in-stream error during the retry itself
// must fold and take the next attempt instead of aborting the run.
func TestRunMidStreamRetryLoopsOnRetryableStreamError(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	var captured []sdk.GenerateParams
	provider := &atomicMockProvider{}
	provider.stream = func(ctx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
		captured = append(captured, cloneGenerateParams(params))
		return streamScript(&invocations,
			scriptStreamError("partial-", "api error 429: engine overloaded"),
			scriptText("recovered"),
		)(ctx, params)
	}
	cfg := retryLoopTestConfig(provider, fastRetry)
	streamCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	installContextStepFailureHandler(&cfg, cancel)

	ch := make(chan StreamEvent, 256)
	result, aborted := New(Deps{}).runMidStreamRetry(
		context.Background(), streamCtx, cancel, newToolAbortRegistry(), ch,
		cfg, nil, nil, nil,
		&sdk.StreamResult{}, &stepMessageCapture{}, nil,
		&interruptedStepCapture{}, nil, nil, 0, "api error 429: initial failure",
		&strings.Builder{}, nil,
	)
	events := drainStreamEvents(ch)

	if aborted {
		t.Fatal("runMidStreamRetry() aborted, want recovery on the second attempt")
	}
	if got := int(invocations.Load()); got != 2 {
		t.Fatalf("provider invocations = %d, want 2 (failed attempt plus recovery)", got)
	}
	if got := countEventType(events, EventRetry); got != 2 {
		t.Fatalf("EventRetry count = %d, want 2", got)
	}
	if got := countEventType(events, EventError); got != 1 {
		t.Fatalf("EventError count = %d, want the retry stream's single 429", got)
	}
	if len(captured) != 2 {
		t.Fatalf("captured provider params = %d, want 2", len(captured))
	}
	// The poisoned partial text must not leak into the next attempt's input.
	if countRound8MessageText(captured[1].Messages, "partial-") != 0 {
		t.Fatalf("second attempt input contains poisoned partial output: %#v", captured[1].Messages)
	}
	if len(captured[1].Messages) != len(captured[0].Messages) {
		t.Fatalf("second attempt input length = %d, want same committed boundary as first (%d)",
			len(captured[1].Messages), len(captured[0].Messages))
	}
	if result == nil || countRound8MessageText(result.Messages, "recovered") != 1 {
		t.Fatalf("final result = %#v, want the recovered assistant message", result)
	}
}

// TestRunMidStreamRetryExhaustsAttemptsOnPersistent429 pins the terminal
// behavior: the loop must stop at MaxAttempts, publish the giving-up error,
// and preserve everything committed along the way.
func TestRunMidStreamRetryExhaustsAttemptsOnPersistent429(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", "api error 429: engine overloaded"),
	)
	cfg := retryLoopTestConfig(provider, RetryConfig{MaxAttempts: 3, FastAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond})
	streamCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	installContextStepFailureHandler(&cfg, cancel)

	prevResult := &sdk.StreamResult{Messages: []sdk.Message{sdk.AssistantMessage("committed before the failure")}}
	ch := make(chan StreamEvent, 256)
	result, aborted := New(Deps{}).runMidStreamRetry(
		context.Background(), streamCtx, cancel, newToolAbortRegistry(), ch,
		cfg, nil, nil, nil,
		prevResult, &stepMessageCapture{}, nil,
		&interruptedStepCapture{}, nil, nil, 0, "api error 429: initial failure",
		&strings.Builder{}, nil,
	)
	events := drainStreamEvents(ch)

	if !aborted {
		t.Fatal("runMidStreamRetry() did not abort after exhausting attempts")
	}
	if got := int(invocations.Load()); got != 3 {
		t.Fatalf("provider invocations = %d, want MaxAttempts 3", got)
	}
	if got := countEventType(events, EventRetry); got != 3 {
		t.Fatalf("EventRetry count = %d, want 3", got)
	}
	last := events[len(events)-1]
	if last.Type != EventError || !strings.Contains(last.Error, "all 3 attempts failed") {
		t.Fatalf("terminal event = %#v, want the giving-up error", last)
	}
	if result == nil || countRound8MessageText(result.Messages, "committed before the failure") != 1 {
		t.Fatalf("final result = %#v, want previously committed messages preserved", result)
	}
}

// TestRunMidStreamRetryStopsOnNonRetryableStreamError pins that a non-retryable
// error inside the retry stream stays terminal: no extra attempts, no loop.
func TestRunMidStreamRetryStopsOnNonRetryableStreamError(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", "api error 400: bad request"),
	)
	cfg := retryLoopTestConfig(provider, fastRetry)
	streamCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	installContextStepFailureHandler(&cfg, cancel)

	ch := make(chan StreamEvent, 256)
	_, aborted := New(Deps{}).runMidStreamRetry(
		context.Background(), streamCtx, cancel, newToolAbortRegistry(), ch,
		cfg, nil, nil, nil,
		&sdk.StreamResult{}, &stepMessageCapture{}, nil,
		&interruptedStepCapture{}, nil, nil, 0, "api error 429: initial failure",
		&strings.Builder{}, nil,
	)
	events := drainStreamEvents(ch)

	if !aborted {
		t.Fatal("runMidStreamRetry() did not abort on a non-retryable error")
	}
	if got := int(invocations.Load()); got != 1 {
		t.Fatalf("provider invocations = %d, want exactly 1 (no looping on 400)", got)
	}
	if got := countEventType(events, EventRetry); got != 1 {
		t.Fatalf("EventRetry count = %d, want 1 (only the attempt that ran)", got)
	}
}

// TestRunMidStreamRetryFoldsCommittedStepIntoNextAttempt drives the deepest
// invariant: attempt one commits a tool step and then fails retryably, so
// attempt two must resume from a boundary that includes that committed step,
// must commit at the next global index, and must not see the poisoned partial.
func TestRunMidStreamRetryFoldsCommittedStepIntoNextAttempt(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptToolCall("fold-call-1", "lookup"),
		scriptStreamError("fold-partial", "api error 429: engine overloaded"),
		scriptText("recovered"),
	)
	cfg := retryLoopTestConfig(provider, fastRetry)
	cfg.SupportsToolCall = true
	streamCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	installContextStepFailureHandler(&cfg, cancel)

	tools := []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return "fold-tool-result", nil
		},
	}}
	var committed []int
	onStepCommitted := func(_ context.Context, stepIndex int, _ *sdk.StepResult) error {
		committed = append(committed, stepIndex)
		return nil
	}

	ch := make(chan StreamEvent, 256)
	result, aborted := New(Deps{}).runMidStreamRetry(
		context.Background(), streamCtx, cancel, newToolAbortRegistry(), ch,
		cfg, tools, nil, nil,
		&sdk.StreamResult{}, &stepMessageCapture{}, onStepCommitted,
		&interruptedStepCapture{}, nil, nil, 0, "api error 429: initial failure",
		&strings.Builder{}, nil,
	)
	events := drainStreamEvents(ch)

	if aborted {
		t.Fatal("runMidStreamRetry() aborted, want recovery after folding the committed step")
	}
	if got := int(invocations.Load()); got != 3 {
		t.Fatalf("provider invocations = %d, want 3 (tool step, failed step, recovery)", got)
	}
	if len(committed) != 2 || committed[0] != 0 || committed[1] != 1 {
		t.Fatalf("committed step indices = %#v, want [0 1] (folded offset, no collision)", committed)
	}
	if result == nil {
		t.Fatal("runMidStreamRetry() result is nil")
	}
	if got := countToolResultText(result.Messages, "fold-tool-result"); got != 1 {
		t.Fatalf("tool result occurrences in final messages = %d, want exactly 1 (folded, not duplicated)", got)
	}
	if got := countRound8MessageText(result.Messages, "fold-partial"); got != 0 {
		t.Fatalf("poisoned partial output leaked into final messages %d times", got)
	}
	if got := countRound8MessageText(result.Messages, "recovered"); got != 1 {
		t.Fatalf("recovered text occurrences in final messages = %d, want 1", got)
	}
	if got := countEventType(events, EventRetry); got != 2 {
		t.Fatalf("EventRetry count = %d, want 2", got)
	}
}

// TestRunMidStreamRetryBackoffHonorsContextCancel pins that the backoff sleep
// between attempts stays interruptible instead of pinning the run.
func TestRunMidStreamRetryBackoffHonorsContextCancel(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", "api error 429: engine overloaded"),
	)
	cfg := retryLoopTestConfig(provider, RetryConfig{MaxAttempts: 5, FastAttempts: 1, BaseDelay: time.Hour, MaxDelay: time.Hour})
	streamCtx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	installContextStepFailureHandler(&cfg, cancel)

	type outcome struct{ aborted bool }
	done := make(chan outcome, 1)
	go func() {
		ch := make(chan StreamEvent, 256)
		_, aborted := New(Deps{}).runMidStreamRetry(
			context.Background(), streamCtx, cancel, newToolAbortRegistry(), ch,
			cfg, nil, nil, nil,
			&sdk.StreamResult{}, &stepMessageCapture{}, nil,
			&interruptedStepCapture{}, nil, nil, 0, "api error 429: initial failure",
			&strings.Builder{}, nil,
		)
		done <- outcome{aborted: aborted}
	}()

	for invocations.Load() < 1 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel(context.Canceled)

	select {
	case got := <-done:
		if !got.aborted {
			t.Fatal("runMidStreamRetry() did not abort after cancel during backoff")
		}
		if got := int(invocations.Load()); got != 1 {
			t.Fatalf("provider invocations = %d, want 1 (cancelled before the second attempt)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runMidStreamRetry() stuck in backoff after context cancel")
	}
}

// TestAgentStreamMidStreamRetryChainRecovers drives the full Stream path:
// initial step commits a tool call, the next provider call 429s, the first
// retry 429s again, and only the second retry succeeds — a run the old
// single-attempt loop would have aborted.
func TestAgentStreamMidStreamRetryChainRecovers(t *testing.T) {
	t.Parallel()

	var callParams []sdk.GenerateParams
	provider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			callParams = append(callParams, cloneGenerateParams(params))
			switch call {
			case 1:
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "chain-call-1",
						ToolName:   "lookup",
						Input:      map[string]any{"query": "one"},
					}},
				}, nil
			case 2, 3:
				return nil, errors.New("api error 429: engine overloaded")
			default:
				return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}
		},
	}
	a := New(Deps{})
	a.SetToolProviders(mockToolLoopTools())

	var events []StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            fastRetry,
	}) {
		events = append(events, ev)
	}

	if got := int(provider.calls.Load()); got != 4 {
		t.Fatalf("provider calls = %d, want 4 (tool step, two 429s, recovery)", got)
	}
	if got := countEventType(events, EventRetry); got != 2 {
		t.Fatalf("EventRetry count = %d, want 2", got)
	}
	if got := countEventType(events, EventAgentAbort); got != 0 {
		t.Fatalf("run aborted with %d abort events, want a clean EventAgentEnd", got)
	}
	if got := countEventType(events, EventAgentEnd); got != 1 {
		t.Fatalf("EventAgentEnd count = %d, want 1", got)
	}
	if len(callParams) != 4 {
		t.Fatalf("captured provider params = %d, want 4", len(callParams))
	}
	// Both retry attempts resume from the boundary that already contains the
	// committed tool step, exactly once (no duplication, no partial leak).
	for _, idx := range []int{2, 3} {
		if got := countToolResultText(callParams[idx].Messages, "large tool result"); got != 1 {
			t.Fatalf("provider call %d tool result occurrences = %d, want 1", idx+1, got)
		}
	}
}
