package native

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models/modelretry"
)

// streamScript builds a DoStream implementation that plays back one scripted
// stream per provider invocation; invocations beyond the script replay the
// last entry (useful for "fails forever" scenarios).
func streamScript(invocations *atomic.Int32, scripts ...func(chan<- sdk.StreamPart)) func(context.Context, sdk.Request) (<-chan sdk.StreamPart, error) {
	return func(_ context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
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
		return ch, nil
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

// scriptStreamError fails the step mid-stream: any partial text is poisoned
// (never committed), mirroring how a real provider 429 surfaces.
func scriptStreamError(partialText string, err error) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		if partialText != "" {
			ch <- &sdk.TextStartPart{ID: "mock"}
			ch <- &sdk.TextDeltaPart{ID: "mock", Text: partialText}
		}
		ch <- &sdk.ErrorPart{Error: err}
	}
}

// rateLimitedErr and serverErr are provider answers the loop retries;
// rejectedErr is one it does not.
func rateLimitedErr() error {
	return &sdk.APIError{Provider: "mock", StatusCode: 429, Kind: sdk.KindRateLimited, Message: "engine overloaded"}
}

func serverErr() error {
	return &sdk.APIError{Provider: "mock", StatusCode: 500, Kind: sdk.KindServerError}
}

func rejectedErr() error {
	return &sdk.APIError{Provider: "mock", StatusCode: 400, Kind: sdk.KindUnknown, Message: "bad request"}
}

func scriptToolCall(callID, toolName string) func(chan<- sdk.StreamPart) {
	return func(ch chan<- sdk.StreamPart) {
		ch <- &sdk.StartPart{}
		ch <- &sdk.StartStepPart{}
		ch <- &sdk.StreamToolCallPart{ToolCallID: callID, ToolName: toolName, Input: toolexec.ArgumentsFromValue(map[string]any{"q": "fold"})}
		ch <- &sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls}
		ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonToolCalls}
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
			if value, ok := toolexec.OutputValue(result.Result).(string); ok && strings.Contains(value, text) {
				count++
			}
		}
	}
	return count
}

// fastRetry keeps tests instant: every attempt fires with no backoff delay.
var fastRetry = modelretry.Config{MaxAttempts: 5, FastAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}

// TestAgentStreamMidStreamRetryExhaustsAttempts pins the terminal behavior of
// the retry fold: the run must stop at MaxAttempts, publish the giving-up
// error, end as aborted, and every retry attempt must resume from the
// committed boundary (the tool step committed before the failures).
func TestAgentStreamMidStreamRetryExhaustsAttempts(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	var captured []sdk.Request
	provider := &atomicMockProvider{}
	provider.stream = func(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		captured = append(captured, cloneGenerateParams(params))
		return streamScript(&invocations,
			scriptToolCall("exhaust-call-1", "lookup"),
			scriptStreamError("exhaust-partial", rateLimitedErr()),
		)(ctx, params)
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
		Retry:            modelretry.Config{MaxAttempts: 3, FastAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond},
	}) {
		events = append(events, ev)
	}

	if got := int(invocations.Load()); got != 5 {
		t.Fatalf("provider invocations = %d, want 5 (tool step, initial failure, MaxAttempts 3 retries)", got)
	}
	if got := countEventType(events, EventRetry); got != 3 {
		t.Fatalf("EventRetry count = %d, want MaxAttempts 3", got)
	}
	// A retried attempt publishes only its EventRetry; the give-up is the one
	// error event, and its cause is the last attempt's provider failure.
	if got := countEventType(events, EventError); got != 1 {
		t.Fatalf("EventError count = %d, want only the give-up", got)
	}
	var gaveUp error
	for _, ev := range events {
		if ev.Type == EventError {
			gaveUp = ev.Cause
		}
	}
	var apiErr *sdk.APIError
	if !errors.As(gaveUp, &apiErr) || apiErr.Kind != sdk.KindRateLimited || !strings.Contains(gaveUp.Error(), "model call retries exhausted") {
		t.Fatalf("give-up cause = %v, want the exhausted retries of the rate-limited call", gaveUp)
	}
	terminal := events[len(events)-1]
	if terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q after exhausting attempts", terminal.Type, EventAgentAbort)
	}
	// The committed tool step survives the abort exactly once; the poisoned
	// partial output of the failed attempts never reaches the terminal state.
	var terminalMessages []sdk.Message
	if err := json.Unmarshal(terminal.Messages, &terminalMessages); err != nil {
		t.Fatalf("decode terminal messages: %v", err)
	}
	if got := countToolResultText(terminalMessages, "large tool result"); got != 1 {
		t.Fatalf("terminal messages tool result occurrences = %d, want 1: %#v", got, terminalMessages)
	}
	if got := countRound8MessageText(terminalMessages, "exhaust-partial"); got != 0 {
		t.Fatalf("terminal messages contain poisoned partial output: %#v", terminalMessages)
	}
	// Every retry resumes from the boundary that already contains the committed
	// tool step, exactly once, and never sees the poisoned partial output.
	for i := 2; i < len(captured); i++ {
		if got := countToolResultText(captured[i].Messages, "large tool result"); got != 1 {
			t.Fatalf("retry call %d tool result occurrences = %d, want 1", i+1, got)
		}
		if got := countRound8MessageText(captured[i].Messages, "exhaust-partial"); got != 0 {
			t.Fatalf("retry call %d input contains poisoned partial output: %#v", i+1, captured[i].Messages)
		}
	}
}

// TestAgentStreamMidStreamRetryStopsOnNonRetryableError pins that a
// non-retryable error inside a retry attempt stays terminal: no extra
// attempts, no loop.
func TestAgentStreamMidStreamRetryStopsOnNonRetryableError(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", rateLimitedErr()),
		scriptStreamError("", rejectedErr()),
	)
	a := New(Deps{})

	var events []StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            fastRetry,
	}) {
		events = append(events, ev)
	}

	if got := int(invocations.Load()); got != 2 {
		t.Fatalf("provider invocations = %d, want 2 (no looping on 400)", got)
	}
	if got := countEventType(events, EventRetry); got != 1 {
		t.Fatalf("EventRetry count = %d, want 1 (only the 429 was retried)", got)
	}
	if terminal := events[len(events)-1]; terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q on a non-retryable error", terminal.Type, EventAgentAbort)
	}
}

// TestAgentStreamMidStreamRetryBackoffHonorsContextCancel pins that the
// backoff sleep between attempts stays interruptible instead of pinning the
// run.
func TestAgentStreamMidStreamRetryBackoffHonorsContextCancel(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptStreamError("", rateLimitedErr()),
	)
	a := New(Deps{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	events := a.Stream(ctx, RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            modelretry.Config{MaxAttempts: 5, FastAttempts: 1, BaseDelay: time.Hour, MaxDelay: time.Hour},
	})

	done := make(chan StreamEvent, 1)
	go func() {
		var terminal StreamEvent
		for ev := range events {
			if ev.IsTerminal() {
				terminal = ev
			}
		}
		done <- terminal
	}()

	// Attempt 1 is a fast retry; attempt 2 enters the one-hour backoff.
	for invocations.Load() < 2 {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case terminal := <-done:
		if terminal.Type != EventAgentAbort {
			t.Fatalf("terminal event = %q, want %q after cancel during backoff", terminal.Type, EventAgentAbort)
		}
		if got := int(invocations.Load()); got != 2 {
			t.Fatalf("provider invocations = %d, want 2 (cancelled during backoff before the next attempt)", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stream stuck in backoff after context cancel")
	}
}

// TestAgentStreamMidStreamRetryChainRecovers drives the full Stream path:
// initial step commits a tool call, the next provider call 429s, the first
// retry 429s again, and only the second retry succeeds — a run a
// single-attempt retry cap would have aborted. The errored steps never
// commit, so the durable step indices stay contiguous across the fold.
func TestAgentStreamMidStreamRetryChainRecovers(t *testing.T) {
	t.Parallel()

	var callParams []sdk.Request
	provider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			callParams = append(callParams, cloneGenerateParams(params))
			switch call {
			case 1:
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "chain-call-1",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"query": "one"}),
					}},
				}, nil
			case 2, 3:
				return sdk.ModelResult{}, rateLimitedErr()
			default:
				return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}
		},
	}
	a := New(Deps{})
	a.SetToolProviders(mockToolLoopTools())

	var committed []int
	var events []StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            fastRetry,
		OnStepCommitted: func(_ context.Context, stepIndex int, _ *step.Record) (StepDirective, error) {
			committed = append(committed, stepIndex)
			return StepDirective{}, nil
		},
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
	if len(committed) != 2 || committed[0] != 0 || committed[1] != 1 {
		t.Fatalf("committed step indices = %#v, want [0 1] (errored steps never commit or shift the index)", committed)
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

// A failed attempt the loop retries is recorded once, as an event: a WARN
// record with the failure's attribution and text. The stream carries only the
// retry and the failure's class, so a run that recovers publishes no error.
func TestMidStreamRetryRecordsTheFailedAttemptAsAnEvent(t *testing.T) {
	t.Parallel()

	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations, scriptStreamError("", serverErr()), scriptText("recovered"))
	handler := &lifecycleRecordingHandler{}
	a := New(Deps{Logger: slog.New(handler)})

	var events []StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		Retry:            fastRetry,
		RunID:            "run-retry",
	}) {
		events = append(events, ev)
	}

	if got := countEventType(events, EventError); got != 0 {
		t.Fatalf("EventError count = %d, want none for a recovered run", got)
	}
	if terminal := events[len(events)-1]; terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}
	var retries []StreamEvent
	for _, ev := range events {
		if ev.Type == EventRetry {
			retries = append(retries, ev)
		}
	}
	if len(retries) != 1 || retries[0].Attempt != 1 || retries[0].MaxAttempt != fastRetry.MaxAttempts ||
		retries[0].RetryReason != string(modelretry.ReasonServerError) ||
		retries[0].Error != "" || retries[0].Code != "" || retries[0].Cause != nil {
		t.Fatalf("retry events = %#v, want one carrying its counters and the failure's class alone", retries)
	}

	handler.mu.Lock()
	defer handler.mu.Unlock()
	var recorded []slog.Record
	for _, record := range handler.records {
		if record.Level >= slog.LevelError {
			t.Fatalf("record %q at %v, want no ERROR for a recovered run", record.Message, record.Level)
		}
		if record.Message == "model call failed, retrying" {
			recorded = append(recorded, record)
		}
	}
	if len(recorded) != 1 || recorded[0].Level != slog.LevelWarn {
		t.Fatalf("retry records = %v, want one WARN event", recorded)
	}
	attrs := map[string]string{}
	recorded[0].Attrs(func(attr slog.Attr) bool {
		attrs[attr.Key] = attr.Value.String()
		return true
	})
	if attrs["run_id"] != "run-retry" || attrs["attempt"] != "1" || attrs["max_attempts"] != "5" || attrs["fault"] != "dependency" ||
		!strings.Contains(attrs["error"], "model stream: mock: 500") || attrs["error_source"] == "" {
		t.Fatalf("retry record attrs = %v, want the attempt with the provider failure's attribution", attrs)
	}
}
