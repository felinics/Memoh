package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/partmeta"
	"github.com/felinics/memoh/internal/agent/step"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func round8RetryToolCycles(cycles, resultBytes int) []sdk.Message {
	messages := make([]sdk.Message, 0, cycles*2)
	for i := 0; i < cycles; i++ {
		callID := fmt.Sprintf("round8-retry-%02d", i)
		messages = append(messages,
			sdk.Message{
				Role: sdk.MessageRoleAssistant,
				Content: []sdk.MessagePart{sdk.ToolCallPart{
					ToolCallID: callID,
					ToolName:   "lookup",
					Input:      toolexec.ArgumentsFromValue(map[string]any{"step": i}),
				}},
			},
			sdk.ToolMessage(sdk.ToolResultPart{
				ToolCallID: callID,
				ToolName:   "lookup",
				Result:     toolexec.OutputFromValue(strings.Repeat("x", resultBytes)),
			}),
		)
	}
	return messages
}

func pruneRound8RetryOldToolResults(messages []sdk.Message, keepRecent int) ([]sdk.Message, int) {
	out := append([]sdk.Message(nil), messages...)
	toolResults := 0
	for _, msg := range out {
		if msg.Role == sdk.MessageRoleTool {
			toolResults++
		}
	}
	toPrune := toolResults - keepRecent
	if toPrune < 1 {
		return out, 0
	}
	pruned := 0
	for i, msg := range out {
		if msg.Role != sdk.MessageRoleTool || pruned >= toPrune || len(msg.Content) == 0 {
			continue
		}
		result, ok := msg.Content[0].(sdk.ToolResultPart)
		if !ok {
			continue
		}
		result.Result = sdk.TextOutput("[tool result pruned for round8 retry]")
		out[i] = sdk.ToolMessage(result)
		pruned++
	}
	return out, pruned
}

func countRound8PrunedToolResults(messages []sdk.Message) int {
	count := 0
	for _, msg := range messages {
		if msg.Role != sdk.MessageRoleTool || len(msg.Content) == 0 {
			continue
		}
		result, ok := msg.Content[0].(sdk.ToolResultPart)
		if !ok {
			continue
		}
		text, _ := toolexec.OutputValue(result.Result).(string)
		if strings.Contains(text, "pruned for round8 retry") {
			count++
		}
	}
	return count
}

func countRound8MessageText(messages []sdk.Message, text string) int {
	count := 0
	for _, msg := range messages {
		for _, part := range msg.Content {
			if value, ok := part.(sdk.TextPart); ok {
				count += strings.Count(value.Text, text)
			}
		}
	}
	return count
}

// TestAgentStreamMidStreamRetryAppliesAccumulatedSuffixHygiene pins that the
// rebuilt dispatch of a mid-stream retry runs the same provider-attempt
// preflight as every other call: with a zero budget window the step
// reselector still sees the accumulated suffix (original prefix intact),
// receives the hygiene knobs, and its applied selection is recorded on the
// retry attempt's step snapshot.
func TestAgentStreamMidStreamRetryAppliesAccumulatedSuffixHygiene(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var invocations atomic.Int32
	var captured []sdk.Request
	provider := &atomicMockProvider{}
	provider.stream = func(ctx context.Context, params sdk.Request) (<-chan sdk.StreamPart, error) {
		captured = append(captured, cloneGenerateParams(params))
		return streamScript(&invocations,
			scriptToolCall("hygiene-call-1", "lookup"),
			scriptStreamError("hygiene-partial", "api error 429: engine overloaded"),
			scriptText("recovered"),
		)(ctx, params)
	}
	a := New(Deps{})
	a.SetToolProviders(mockToolLoopTools())

	var selectorCalls atomic.Int32
	var terminal StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:                  &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:               []sdk.Message{sdk.UserMessage("original task")},
		SupportsToolCall:       true,
		Identity:               SessionContext{BotID: "bot-1"},
		ContextMutations:       ledger,
		ContextBudgetMaxTokens: 0,
		Retry:                  fastRetry,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls.Add(1)
			if input.InitialMessageCount != 1 {
				t.Errorf("InitialMessageCount = %d, want original turn prefix 1", input.InitialMessageCount)
			}
			if input.BudgetMaxTokens != 0 {
				t.Errorf("BudgetMaxTokens = %d, want 0 for a window-zero run", input.BudgetMaxTokens)
			}
			if input.KeepRecentToolResults != stepReselectKeepRecentToolResults ||
				input.MinMessages != stepReselectMinMessages {
				t.Errorf("hygiene settings = keep %d/min %d", input.KeepRecentToolResults, input.MinMessages)
			}
			selected, pruned := pruneRound8RetryOldToolResults(input.Messages, 0)
			return ContextStepSelectionResult{Messages: selected, Truncated: pruned}
		},
	}) {
		if ev.IsTerminal() {
			terminal = ev
		}
	}

	if terminal.Type != EventAgentEnd {
		t.Fatalf("terminal event = %q, want %q", terminal.Type, EventAgentEnd)
	}
	if got := int(invocations.Load()); got != 3 {
		t.Fatalf("provider invocations = %d, want 3 (tool step, failure, retry)", got)
	}
	if got := selectorCalls.Load(); got != 2 {
		t.Fatalf("selector calls = %d, want 2 (failing step preflight plus retry rebuild)", got)
	}

	retryParams := captured[len(captured)-1]
	if got := countRound8PrunedToolResults(retryParams.Messages); got != 1 {
		t.Fatalf("pruned retry tool results = %d, want 1", got)
	}
	if textOfMessage(retryParams.Messages[0]) != "original task" {
		t.Fatalf("retry prefix = %#v, want original task", retryParams.Messages[0])
	}
	if got := countRound8MessageText(retryParams.Messages, "hygiene-partial"); got != 0 {
		t.Fatalf("retry input contains poisoned partial output: %#v", retryParams.Messages)
	}

	steps := ledger.StepSnapshots()
	var retrySnapshot *contextfrag.StepSnapshot
	for i := range steps {
		if steps[i].Attempt == 1 {
			retrySnapshot = &steps[i]
		}
	}
	if retrySnapshot == nil {
		t.Fatalf("step snapshots = %#v, want one recorded for the retry attempt", steps)
	}
	if retrySnapshot.StepIndex != 0 ||
		retrySnapshot.ReselectionOutcome != contextfrag.ReselectionOutcomeApplied ||
		retrySnapshot.Truncated != 1 {
		t.Fatalf("retry step snapshot = %#v", retrySnapshot)
	}
	if countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure) != 0 {
		t.Fatalf("window-zero retry recorded budget failure: %#v", ledger.Records())
	}
}

// TestAgentStreamMidStreamRetryProtectedOverflowFencesProvider pins that a
// fatal reselection error raised while rebuilding the retry dispatch fences
// the provider: no further provider call happens, the run aborts with the
// budget cause, and the failed retry attempt is recorded on the ledger.
func TestAgentStreamMidStreamRetryProtectedOverflowFencesProvider(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var invocations atomic.Int32
	provider := &atomicMockProvider{}
	provider.stream = streamScript(&invocations,
		scriptToolCall("fence-call-1", "lookup"),
		scriptStreamError("", "api error 429: engine overloaded"),
	)
	a := New(Deps{})
	a.SetToolProviders(mockToolLoopTools())

	var selectorCalls atomic.Int32
	var terminal StreamEvent
	for ev := range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: provider},
		Messages:         []sdk.Message{sdk.UserMessage("original task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		Retry:            fastRetry,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			if selectorCalls.Add(1) == 1 {
				// The failing step's own preflight passes untouched.
				return ContextStepSelectionResult{}
			}
			// The retry rebuild's preflight overflows fatally.
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
	}) {
		if ev.IsTerminal() {
			terminal = ev
		}
	}

	if terminal.Type != EventAgentAbort {
		t.Fatalf("terminal event = %q, want %q on protected overflow", terminal.Type, EventAgentAbort)
	}
	if got := int(invocations.Load()); got != 2 {
		t.Fatalf("provider invocations = %d, want 2 (the fenced retry never reaches the provider)", got)
	}
	if got := selectorCalls.Load(); got != 2 {
		t.Fatalf("selector calls = %d, want 2", got)
	}

	steps := ledger.StepSnapshots()
	if len(steps) == 0 {
		t.Fatal("no step snapshots recorded")
	}
	failed := steps[len(steps)-1]
	if failed.Attempt != 1 || failed.StepIndex != 0 ||
		failed.ReselectionOutcome != contextfrag.ReselectionOutcomeFailed ||
		failed.PostPrepareInputHash != "" {
		t.Fatalf("failed retry snapshot = %#v", failed)
	}
	if got := countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure); got != 1 {
		t.Fatalf("budget failure mutations = %d, want 1", got)
	}
	if got := countProviderAttemptMutations(ledger.Records(), contextfrag.MutationMidStreamRetry); got != 1 {
		t.Fatalf("mid-stream retry mutations = %d, want 1", got)
	}
}

func TestAgentStreamRetryPreservesStepDynamicHookContext(t *testing.T) {
	t.Parallel()

	marker := "round8-step-dynamic-retry"
	bridgeProvider, hookService := newBeforeModelCallHook(t, marker)
	var callParams []sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			callParams = append(callParams, cloneGenerateParams(params))
			switch call {
			case 1:
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "round8-dynamic-retry-call",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"query": "one"}),
					}},
				}, nil
			case 2:
				return sdk.ModelResult{}, errors.New("api error 500")
			default:
				return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			}
		},
	}
	a := New(Deps{
		BridgeProvider: bridgeProvider,
		HookService:    hookService,
	})
	a.SetToolProviders(mockToolLoopTools())

	for range a.Stream(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
	}) {
	}

	if len(callParams) != 3 {
		t.Fatalf("provider calls = %d, want initial, failed step, and retry", len(callParams))
	}
	beforeRetry := countRound8MessageText(callParams[1].Messages, marker)
	afterRetry := countRound8MessageText(callParams[2].Messages, marker)
	if beforeRetry != 2 {
		t.Fatalf("failed-step hook markers = %d, want initial plus step hook", beforeRetry)
	}
	if afterRetry != beforeRetry {
		t.Fatalf("retry hook markers = %d, want all %d dynamic inputs preserved", afterRetry, beforeRetry)
	}
}

func TestProviderAttemptStateBuildsRawRetryMessages(t *testing.T) {
	t.Parallel()

	cacheControl := &sdk.CacheControl{Type: "ephemeral"}
	// The SDK keeps tool JSON in RFC 8785 form, so numbers have binary64
	// semantics and 2^53 is the largest integer that stays exact. The replay
	// guard below is byte identity with the stored attempt; the decoded value
	// is checked as well so a lossy re-encoding through float64 would show.
	const exactLargeInteger = int64(9007199254740992)
	toolCall := sdk.Message{
		Role: sdk.MessageRoleAssistant,
		Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Input:      toolexec.ArgumentsFromValue(map[string]any{"id": exactLargeInteger}),
		}},
	}
	toolResult := sdk.Message{
		Role: sdk.MessageRoleTool,
		Content: []sdk.MessagePart{sdk.ToolResultPart{
			ToolCallID: "call-1",
			ToolName:   "lookup",
			Result:     toolexec.OutputFromValue(map[string]any{"id": exactLargeInteger}),
		}},
	}
	state := &providerAttemptState{}
	state.store(&sdk.Request{Messages: []sdk.Message{
		{
			Role: sdk.MessageRoleSystem,
			Content: []sdk.MessagePart{sdk.TextPart{
				Text:         "stable system",
				CacheControl: cacheControl,
			}},
		},
		{
			Role: sdk.MessageRoleUser,
			Content: []sdk.MessagePart{sdk.TextPart{
				Text:             "task",
				CacheControl:     cacheControl,
				ProviderMetadata: partmeta.Fold(map[string]any{"trace": "keep"}),
			}},
		},
		toolCall,
		toolResult,
		sdk.UserMessage("dynamic hook"),
	}}, 1, true, []dynamicSourceRef{{recordID: 0, index: 4}})
	previousSteps := []step.Record{
		{Messages: []sdk.Message{toolCall, toolResult}},
		{Messages: []sdk.Message{sdk.AssistantMessage("partial retry tail")}},
	}

	messages, ok := state.retryMessages(previousSteps)
	if !ok || len(messages) != 5 {
		t.Fatalf("retry messages = %#v, %v; want raw input plus current tail", messages, ok)
	}
	if messages[0].Role != sdk.MessageRoleUser || textOfMessage(messages[0]) != "task" {
		t.Fatalf("promoted system was not removed: %#v", messages)
	}
	taskPart, ok := messages[0].Content[0].(sdk.TextPart)
	if !ok || taskPart.CacheControl != nil || partmeta.Unfold(taskPart.ProviderMetadata)["trace"] != "keep" {
		t.Fatalf("raw task part = %#v, want cache control cleared and provider metadata retained", messages[0].Content)
	}
	retryCall, ok := messages[1].Content[0].(sdk.ToolCallPart)
	if !ok {
		t.Fatalf("retry tool call = %#v, want sdk.ToolCallPart", messages[1].Content)
	}
	storedCall := toolCall.Content[0].(sdk.ToolCallPart)
	if string(retryCall.Input.JSON) != string(storedCall.Input.JSON) {
		t.Fatalf("retry tool input = %s, want the stored attempt's bytes %s", retryCall.Input.JSON, storedCall.Input.JSON)
	}
	var replayInput struct {
		ID int64 `json:"id"`
	}
	if err := retryCall.Input.Unmarshal(&replayInput); err != nil || replayInput.ID != exactLargeInteger {
		t.Fatalf("retry tool input id = %#v, want exact int64 %d", retryCall.Input, exactLargeInteger)
	}
	retryResult, ok := messages[2].Content[0].(sdk.ToolResultPart)
	if !ok {
		t.Fatalf("retry tool result = %#v, want sdk.ToolResultPart", messages[2].Content)
	}
	storedResult := toolResult.Content[0].(sdk.ToolResultPart)
	if string(retryResult.Result.JSON) != string(storedResult.Result.JSON) {
		t.Fatalf("retry tool result = %s, want the stored attempt's bytes %s", retryResult.Result.JSON, storedResult.Result.JSON)
	}
	var replayOutput struct {
		ID int64 `json:"id"`
	}
	if err := json.Unmarshal(retryResult.Result.JSON, &replayOutput); err != nil || replayOutput.ID != exactLargeInteger {
		t.Fatalf("retry tool result id = %#v, want exact int64 %d", retryResult.Result, exactLargeInteger)
	}
	if textOfMessage(messages[len(messages)-1]) != "partial retry tail" {
		t.Fatalf("current partial output was not appended: %#v", messages)
	}
	retryInput, ok := state.retryInput(previousSteps)
	if !ok {
		t.Fatal("retry input was not available")
	}
	// The promoted system message was removed, so the dynamic ref stored at
	// payload index 4 shifts to index 3 of the retry input.
	wantRefs := []dynamicSourceRef{{recordID: 0, index: 3}}
	if !reflect.DeepEqual(retryInput.dynamicRefs, wantRefs) {
		t.Fatalf("retry dynamic refs = %#v, want %#v", retryInput.dynamicRefs, wantRefs)
	}
}
