package native

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestAgentGenerateRejectsNonPrefixPreservingStepSelectionWithoutMutationRecord(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var secondCallMessages []sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			if call == 1 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-guard",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			}
			secondCallMessages = append([]sdk.Message(nil), params.Messages...)
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []toolexec.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
				return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
			},
		}}},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{
				Messages: []sdk.Message{
					sdk.UserMessage("modified prefix"),
				},
				Dropped: 2,
			}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(secondCallMessages) == 0 || textOfMessage(secondCallMessages[0]) != "start" {
		t.Fatalf("second provider call prefix = %#v, want original prefix", secondCallMessages)
	}
	if hasMutationKind(ledger.Records(), contextfrag.MutationLoopStepReselection) {
		t.Fatalf("mutation records = %#v, want no loop_step_reselection when prefix guard rejects selection", ledger.Records())
	}
}

func TestAgentStreamStopsOnToolLoopAbort(t *testing.T) {
	t.Parallel()

	modelProvider := &atomicMockProvider{
		handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
			if call >= 20 {
				return sdk.ModelResult{
					Text:         "unexpected-final-step",
					FinishReason: sdk.FinishReasonStop,
				}, nil
			}
			return sdk.ModelResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-stream",
					ToolName:   "loop_tool",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"query": "same"}),
				}},
			}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{
			tools: []toolexec.Tool{{
				Name:       "loop_tool",
				Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(map[string]any{"ok": true}), nil
				},
			}},
		},
	})

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("loop stream")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		LoopDetection:    LoopDetectionConfig{Enabled: true},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if terminal.Type != EventAgentAbort {
		t.Fatalf("expected EventAgentAbort, got %q", terminal.Type)
	}
}

func TestAgentStreamMarksTerminalTextLoopAsAbort(t *testing.T) {
	t.Parallel()

	repeatedChunk := strings.Repeat("abcd", 64)
	var observedCancel atomic.Bool
	// Waits for the provider goroutines to finish deciding. The assertions
	// below read what they stored, and the run can return before a goroutine
	// has been scheduled again after the cancellation. A WaitGroup rather than
	// a channel because a retrying run calls the provider more than once.
	var providers sync.WaitGroup
	modelProvider := &atomicMockProvider{
		stream: func(ctx context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
			ch := make(chan sdk.StreamPart, 16)
			providers.Add(1)
			go func() {
				defer providers.Done()
				defer close(ch)
				send := func(part sdk.StreamPart) bool {
					select {
					case <-ctx.Done():
						observedCancel.Store(true)
						return false
					case ch <- part:
						return true
					}
				}
				if !send(&sdk.StartPart{}) {
					return
				}
				if !send(&sdk.StartStepPart{}) {
					return
				}
				if !send(&sdk.TextStartPart{ID: "mock"}) {
					return
				}
				for i := 0; i < 4; i++ {
					if !send(&sdk.TextDeltaPart{ID: "mock", Text: repeatedChunk}) {
						return
					}
				}
				select {
				case <-ctx.Done():
					observedCancel.Store(true)
					return
				case <-time.After(50 * time.Millisecond):
				}
				if !send(&sdk.TextEndPart{ID: "mock"}) {
					return
				}
				if !send(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}) {
					return
				}
				_ = send(&sdk.FinishPart{FinishReason: sdk.FinishReasonStop})
			}()
			return ch, nil
		},
	}

	a := New(Deps{})

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("loop stream text")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		LoopDetection:    LoopDetectionConfig{Enabled: true},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	providers.Wait()
	if !observedCancel.Load() {
		t.Fatal("expected stream provider to observe context cancellation from text-loop abort")
	}
	if terminal.Type != EventAgentAbort {
		t.Fatalf("expected EventAgentAbort, got %q", terminal.Type)
	}
}

func TestAgentStreamMarksRetryTextLoopAsAbort(t *testing.T) {
	t.Parallel()

	repeatedChunk := strings.Repeat("abcd", 64)
	var streamCalls atomic.Int32
	var observedCancel atomic.Bool
	// Waits for the provider goroutines to finish deciding. The assertions
	// below read what they stored, and the run can return before a goroutine
	// has been scheduled again after the cancellation. A WaitGroup rather than
	// a channel because a retrying run calls the provider more than once.
	var providers sync.WaitGroup
	modelProvider := &atomicMockProvider{
		stream: func(ctx context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
			call := streamCalls.Add(1)
			ch := make(chan sdk.StreamPart, 16)
			providers.Add(1)
			go func() {
				defer providers.Done()
				defer close(ch)
				send := func(part sdk.StreamPart) bool {
					select {
					case <-ctx.Done():
						observedCancel.Store(true)
						return false
					case ch <- part:
						return true
					}
				}

				if !send(&sdk.StartPart{}) {
					return
				}
				if !send(&sdk.StartStepPart{}) {
					return
				}

				if call == 1 {
					_ = send(&sdk.ErrorPart{Error: errors.New("api error 500")})
					return
				}

				if !send(&sdk.TextStartPart{ID: "mock-retry"}) {
					return
				}
				for i := 0; i < 4; i++ {
					if !send(&sdk.TextDeltaPart{ID: "mock-retry", Text: repeatedChunk}) {
						return
					}
				}
				select {
				case <-ctx.Done():
					observedCancel.Store(true)
					return
				case <-time.After(50 * time.Millisecond):
				}
				if !send(&sdk.TextEndPart{ID: "mock-retry"}) {
					return
				}
				if !send(&sdk.FinishStepPart{FinishReason: sdk.FinishReasonStop}) {
					return
				}
				_ = send(&sdk.FinishPart{FinishReason: sdk.FinishReasonStop})
			}()
			return ch, nil
		},
	}

	a := New(Deps{})

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("loop stream retry text")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		LoopDetection:    LoopDetectionConfig{Enabled: true},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if streamCalls.Load() != 2 {
		t.Fatalf("expected one retry stream attempt, got %d stream calls", streamCalls.Load())
	}
	providers.Wait()
	if !observedCancel.Load() {
		t.Fatal("expected retry stream provider to observe context cancellation from text-loop abort")
	}
	if terminal.Type != EventAgentAbort {
		t.Fatalf("expected EventAgentAbort after retry text-loop abort, got %q", terminal.Type)
	}
}

// TestAgentStreamMidStreamRetryRecordsCacheUsageForRetryAttempt pins that a
// step produced by a mid-stream retry attempt still records its cache usage
// and its MutationMidStreamRetry ledger entry under the advanced attempt
// counter (the legacy retry loop once dropped both).
func TestAgentStreamMidStreamRetryRecordsCacheUsageForRetryAttempt(t *testing.T) {
	t.Parallel()

	var streamCalls atomic.Int32
	modelProvider := &atomicMockProvider{
		stream: func(_ context.Context, _ sdk.Request) (<-chan sdk.StreamPart, error) {
			call := streamCalls.Add(1)
			ch := make(chan sdk.StreamPart, 16)
			go func() {
				defer close(ch)
				ch <- &sdk.StartPart{}
				ch <- &sdk.StartStepPart{}
				if call == 1 {
					ch <- &sdk.ErrorPart{Error: errors.New("api error 500")}
					return
				}
				ch <- &sdk.TextStartPart{ID: "mock-retry-usage"}
				ch <- &sdk.TextDeltaPart{ID: "mock-retry-usage", Text: "ok"}
				ch <- &sdk.TextEndPart{ID: "mock-retry-usage"}
				ch <- &sdk.FinishStepPart{
					FinishReason: sdk.FinishReasonStop,
					Usage: sdk.Usage{
						InputTokenDetails: sdk.InputTokenDetail{CacheReadTokens: 50},
					},
				}
				ch <- &sdk.FinishPart{FinishReason: sdk.FinishReasonStop}
			}()
			return ch, nil
		},
	}

	a := New(Deps{})
	ledger := contextfrag.NewMutationLedger()

	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("retry cache usage")},
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if streamCalls.Load() != 2 {
		t.Fatalf("stream calls = %d, want 2 (initial + one retry)", streamCalls.Load())
	}
	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}

	records := ledger.CacheUsageRecords()
	found := false
	for _, r := range records {
		if r.Attempt == 1 && r.CacheReadTokens == 50 {
			found = true
		}
	}
	if !found {
		t.Fatalf("cache usage records = %#v, want a record with attempt=1 cache_read_tokens=50 from the retry step", records)
	}

	retryFound := false
	for _, r := range ledger.Records() {
		if r.Kind == contextfrag.MutationMidStreamRetry && strings.Contains(r.Detail, "attempt=1") {
			retryFound = true
		}
	}
	if !retryFound {
		t.Fatalf("mutation records = %#v, want a MutationMidStreamRetry record with attempt=1", ledger.Records())
	}
}

func textOfMessage(msg sdk.Message) string {
	if len(msg.Content) == 0 {
		return ""
	}
	text, _ := msg.Content[0].(sdk.TextPart)
	return text.Text
}

func hasMutationKind(records []contextfrag.MutationRecord, kind contextfrag.MutationKind) bool {
	for _, record := range records {
		if record.Kind == kind {
			return true
		}
	}
	return false
}
