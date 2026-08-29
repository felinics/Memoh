package native

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
)

func TestAgentGenerateDroppedReadMediaIsNotRecordedAsProviderInput(t *testing.T) {
	t.Parallel()

	pdfBytes := []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\ntrailer\n%%EOF\n")
	var providerInputs []sdk.GenerateParams
	modelProvider := &agentReadMediaMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		providerInputs = append(providerInputs, cloneGenerateParams(params))
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-pdf",
					ToolName:   "read",
					Input:      map[string]any{"path": "/data/report.pdf"},
				}},
			}, nil
		}
		if messagesHaveFilePart(params.Messages) {
			t.Fatal("second provider call retained the file selected for eviction")
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, newAgentReadMediaBridgeProvider(t, map[string][]byte{
			"report.pdf": pdfBytes,
		}), nil, "/data"),
	})

	var committed []sdk.StepResult
	result, err := a.Generate(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("inspect the document")},
		SupportsFileInput: true,
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-1"},
		ContextMutations:  contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selected := make([]sdk.Message, 0, len(input.Messages))
			dropped := 0
			for _, message := range input.Messages {
				if messagesHaveFilePart([]sdk.Message{message}) {
					dropped++
					continue
				}
				selected = append(selected, message)
			}
			if dropped != 1 {
				t.Fatalf("file messages selected for eviction = %d, want 1", dropped)
			}
			return ContextStepSelectionResult{
				Messages:    selected,
				Dropped:     dropped,
				DropReasons: map[string]int{"native_media": dropped},
			}
		},
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, *step)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(providerInputs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(providerInputs))
	}
	for i, step := range committed {
		if messagesHaveFilePart(step.Messages) {
			t.Fatalf("committed step %d recorded a file the provider did not receive", i)
		}
	}
	if messagesHaveFilePart(result.Messages) {
		t.Fatal("terminal messages recorded a file the provider did not receive")
	}
}

func TestAgentGenerateFailedPreflightKeepsLastDispatchedHashAndFork(t *testing.T) {
	t.Parallel()

	bridgeProvider, hookService := newBeforeModelCallHook(t, "last-dispatched-hook")
	var firstProviderInput sdk.GenerateParams
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call != 1 {
			return nil, errors.New("provider called after failed step preflight")
		}
		firstProviderInput = cloneGenerateParams(params)
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls: []sdk.ToolCall{{
				ToolCallID: "call-overflow",
				ToolName:   "lookup",
				Input:      map[string]any{"q": "one"},
			}},
		}, nil
	}}
	ledger := contextfrag.NewMutationLedger()
	fork := agenttools.NewMessageSnapshot(nil)
	a := New(Deps{
		BridgeProvider: bridgeProvider,
		HookService:    hookService,
		ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
			return cfg, nil
		},
	})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return map[string]any{"answer": "ok"}, nil
		},
	}}}})

	var selectorCalls atomic.Int32
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ForkContext:      fork,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			if selectorCalls.Add(1) == 1 {
				return ContextStepSelectionResult{}
			}
			return ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow}
		},
	})
	if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Generate() error = %v, want %v", err, contextfrag.ErrProtectedContextOverflow)
	}
	wantHash := contextfrag.ProviderPayloadHash(
		firstProviderInput.System,
		firstProviderInput.Messages,
		firstProviderInput.Tools,
	)
	if got := ledger.FinalInputHash(); got != wantHash {
		t.Fatalf("final input hash = %q, want last dispatched hash %q", got, wantHash)
	}
	forkMessages, snapshotErr := fork.Messages()
	if snapshotErr != nil {
		t.Fatalf("read fork snapshot: %v", snapshotErr)
	}
	if !reflect.DeepEqual(forkMessages, firstProviderInput.Messages) {
		t.Fatalf("fork snapshot = %#v, want last dispatched messages %#v", forkMessages, firstProviderInput.Messages)
	}
}

func TestAgentGenerateStepHookRemainsTransientAfterAdmissionReconciliation(t *testing.T) {
	t.Parallel()

	const marker = "transient-step-hook"
	bridgeProvider, hookService := newBeforeModelCallHook(t, marker)
	var providerInputs []sdk.GenerateParams
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		providerInputs = append(providerInputs, cloneGenerateParams(params))
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "call-hook", ToolName: "lookup"}},
			}, nil
		}
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{BridgeProvider: bridgeProvider, HookService: hookService})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return "ok", nil
		},
	}}}})

	var committed []sdk.StepResult
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: contextfrag.NewMutationLedger(),
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, *step)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(providerInputs) != 2 || !providerAttemptContainsText(providerInputs[1].Messages, marker) {
		t.Fatalf("step hook was not provider-visible: %#v", providerInputs)
	}
	for i, step := range committed {
		raw, marshalErr := json.Marshal(step.Messages)
		if marshalErr != nil {
			t.Fatalf("marshal committed step %d: %v", i, marshalErr)
		}
		if strings.Contains(string(raw), marker) {
			t.Fatalf("committed step %d made transient hook durable: %s", i, raw)
		}
	}
}

func TestAgentStreamRetryRevokesReadMediaAdmission(t *testing.T) {
	pdfBytes := []byte("%PDF-1.4\nretry admission\n%%EOF\n")
	var providerInputs []sdk.GenerateParams
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		providerInputs = append(providerInputs, cloneGenerateParams(params))
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-retry-pdf",
					ToolName:   "read",
					Input:      map[string]any{"path": "/data/retry.pdf"},
				}},
			}, nil
		case 2:
			if !messagesHaveFilePart(params.Messages) {
				t.Fatal("failed provider attempt did not receive admitted file")
			}
			return nil, errors.New("api error 500")
		default:
			if messagesHaveFilePart(params.Messages) {
				t.Fatal("retry provider attempt retained file selected for eviction")
			}
			return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
		}
	}}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, newAgentReadMediaBridgeProvider(t, map[string][]byte{
			"retry.pdf": pdfBytes,
		}), nil, "/data"),
	})

	var selectorCalls atomic.Int32
	var committed []sdk.StepResult
	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("inspect the document")},
		SupportsFileInput: true,
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-1"},
		ContextMutations:  contextfrag.NewMutationLedger(),
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			if selectorCalls.Add(1) == 1 {
				return ContextStepSelectionResult{Messages: input.Messages}
			}
			selected := make([]sdk.Message, 0, len(input.Messages))
			for _, message := range input.Messages {
				if !messagesHaveFilePart([]sdk.Message{message}) {
					selected = append(selected, message)
				}
			}
			return ContextStepSelectionResult{
				Messages:    selected,
				Dropped:     len(input.Messages) - len(selected),
				DropReasons: map[string]int{"native_media": len(input.Messages) - len(selected)},
			}
		},
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, *step)
			return nil
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if len(providerInputs) != 3 || selectorCalls.Load() != 2 {
		t.Fatalf("provider/selector calls = %d/%d, want 3/2", len(providerInputs), selectorCalls.Load())
	}
	for i, step := range committed {
		if messagesHaveFilePart(step.Messages) {
			t.Fatalf("committed retry step %d retained a file the retry provider did not receive", i)
		}
	}
	var terminalMessages []sdk.Message
	if terminal.Type != EventAgentEnd || json.Unmarshal(terminal.Messages, &terminalMessages) != nil {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, terminalMessages)
	}
	if messagesHaveFilePart(terminalMessages) {
		t.Fatal("terminal messages retained a file revoked by retry preflight")
	}
}

func TestAgentStreamProviderStartFailureDoesNotPersistReadMedia(t *testing.T) {
	t.Parallel()

	pdfBytes := []byte("%PDF-1.4\nfailed provider start\n%%EOF\n")
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		switch call {
		case 1:
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls: []sdk.ToolCall{{
					ToolCallID: "call-failed-start-pdf",
					ToolName:   "read",
					Input:      map[string]any{"path": "/data/failed-start.pdf"},
				}},
			}, nil
		case 2:
			if !messagesHaveFilePart(params.Messages) {
				t.Fatal("failed provider start did not receive admitted file")
			}
			return nil, errors.New("invalid provider request")
		default:
			t.Fatalf("provider call %d crossed the nonretryable start failure", call)
			return nil, nil
		}
	}}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, newAgentReadMediaBridgeProvider(t, map[string][]byte{
			"failed-start.pdf": pdfBytes,
		}), nil, "/data"),
	})

	var committed []sdk.StepResult
	var terminal StreamEvent
	for event := range a.Stream(context.Background(), RunConfig{
		Model:             &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("inspect the document")},
		SupportsFileInput: true,
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-1"},
		ContextMutations:  contextfrag.NewMutationLedger(),
		OnStepCommitted: func(_ context.Context, _ int, step *sdk.StepResult) error {
			committed = append(committed, *step)
			return nil
		},
	}) {
		if event.IsTerminal() {
			terminal = event
		}
	}

	if modelProvider.calls.Load() != 2 {
		t.Fatalf("provider calls = %d, want 2", modelProvider.calls.Load())
	}
	if len(committed) != 1 || messagesHaveFilePart(committed[0].Messages) {
		t.Fatalf("committed steps = %#v, want only completed step zero without file", committed)
	}
	var terminalMessages []sdk.Message
	if !terminal.IsTerminal() || json.Unmarshal(terminal.Messages, &terminalMessages) != nil {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, terminalMessages)
	}
	if messagesHaveFilePart(terminalMessages) {
		t.Fatal("terminal messages persisted read_media from a failed provider start")
	}
}

func TestAgentStreamInterruptedReadMediaIsNotDuplicatedInTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pdfBytes := []byte("%PDF-1.4\ninterrupted admission\n%%EOF\n")
	var calls atomic.Int32
	modelProvider := &atomicMockProvider{stream: func(streamCtx context.Context, params sdk.GenerateParams) (*sdk.StreamResult, error) {
		switch calls.Add(1) {
		case 1:
			return closedAgentTestStream(
				&sdk.StartPart{},
				&sdk.StartStepPart{},
				&sdk.StreamToolCallPart{
					ToolCallID: "call-interrupted-pdf",
					ToolName:   "read",
					Input:      map[string]any{"path": "/data/interrupted.pdf"},
				},
				&sdk.FinishStepPart{FinishReason: sdk.FinishReasonToolCalls},
				&sdk.FinishPart{FinishReason: sdk.FinishReasonToolCalls},
			), nil
		default:
			if !messagesHaveFilePart(params.Messages) {
				t.Fatal("interrupted provider step did not receive admitted file")
			}
			parts := make(chan sdk.StreamPart, 4)
			parts <- &sdk.StartPart{}
			parts <- &sdk.StartStepPart{}
			parts <- &sdk.TextStartPart{ID: "partial"}
			parts <- &sdk.TextDeltaPart{ID: "partial", Text: "partially read"}
			go func() {
				<-streamCtx.Done()
				close(parts)
			}()
			return &sdk.StreamResult{Stream: parts}, nil
		}
	}}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		agenttools.NewContainerProvider(nil, newAgentReadMediaBridgeProvider(t, map[string][]byte{
			"interrupted.pdf": pdfBytes,
		}), nil, "/data"),
	})

	var interrupted *sdk.StepResult
	events := a.Stream(ctx, RunConfig{
		Model:             &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:          []sdk.Message{sdk.UserMessage("inspect the document")},
		SupportsFileInput: true,
		SupportsToolCall:  true,
		Identity:          SessionContext{BotID: "bot-1"},
		ContextMutations:  contextfrag.NewMutationLedger(),
		OnStepInterrupted: func(_ context.Context, stepIndex int, step *sdk.StepResult) error {
			if stepIndex != 1 {
				t.Errorf("interrupted step index = %d, want 1", stepIndex)
			}
			interrupted = step
			return nil
		},
	})

	var terminal StreamEvent
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case event, ok := <-events:
			if !ok {
				goto streamClosed
			}
			if event.Type == EventTextDelta {
				cancel()
			}
			if event.IsTerminal() {
				terminal = event
			}
		case <-timer.C:
			t.Fatal("timed out waiting for interrupted media stream")
		}
	}

streamClosed:
	if interrupted == nil {
		t.Fatal("interrupted step was not persisted")
	}
	if countFileParts(interrupted.Messages) != 1 {
		t.Fatalf("interrupted step file parts = %d, want 1", countFileParts(interrupted.Messages))
	}
	var terminalMessages []sdk.Message
	if terminal.Type != EventAgentAbort || json.Unmarshal(terminal.Messages, &terminalMessages) != nil {
		t.Fatalf("terminal event/messages = %#v / %#v", terminal, terminalMessages)
	}
	if got := countFileParts(terminalMessages); got != 1 {
		t.Fatalf("terminal file parts = %d, want exactly one provider-observed file", got)
	}
}

func TestAgentGenerateCanceledPreflightKeepsLastDispatchedHashAndFork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var firstProviderInput sdk.GenerateParams
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call != 1 {
			return nil, errors.New("provider called after canceled step preflight")
		}
		firstProviderInput = cloneGenerateParams(params)
		return &sdk.GenerateResult{
			FinishReason: sdk.FinishReasonToolCalls,
			ToolCalls: []sdk.ToolCall{{
				ToolCallID: "call-cancel",
				ToolName:   "lookup",
				Input:      map[string]any{"q": "one"},
			}},
		}, nil
	}}
	ledger := contextfrag.NewMutationLedger()
	fork := agenttools.NewMessageSnapshot(nil)
	attemptState := &providerAttemptState{}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return map[string]any{"answer": "ok"}, nil
		},
	}}}})

	_, err := a.Generate(ctx, RunConfig{
		Model:                &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:             []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall:     true,
		Identity:             SessionContext{BotID: "bot-1"},
		ContextMutations:     ledger,
		ForkContext:          fork,
		providerAttemptState: attemptState,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			cancel()
			return ContextStepSelectionResult{}
		},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() error = %v, want context canceled", err)
	}
	if modelProvider.calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", modelProvider.calls.Load())
	}
	wantHash := contextfrag.ProviderPayloadHash(
		firstProviderInput.System,
		firstProviderInput.Messages,
		firstProviderInput.Tools,
	)
	if got := ledger.FinalInputHash(); got != wantHash {
		t.Fatalf("final input hash = %q, want last dispatched hash %q", got, wantHash)
	}
	forkMessages, snapshotErr := fork.Messages()
	if snapshotErr != nil {
		t.Fatalf("read fork snapshot: %v", snapshotErr)
	}
	if !reflect.DeepEqual(forkMessages, firstProviderInput.Messages) {
		t.Fatalf("fork snapshot = %#v, want last dispatched messages %#v", forkMessages, firstProviderInput.Messages)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].StepIndex != 0 || steps[0].PostPrepareInputHash != wantHash {
		t.Fatalf("step snapshots = %#v, want only the dispatched step-zero attempt", steps)
	}
	retryMessages, ok := attemptState.retryMessages(nil)
	if !ok || !reflect.DeepEqual(retryMessages, firstProviderInput.Messages) {
		t.Fatalf("retry messages = %#v, %t; want last dispatched messages %#v", retryMessages, ok, firstProviderInput.Messages)
	}
}

func messagesHaveFilePart(messages []sdk.Message) bool {
	for _, message := range messages {
		for _, part := range message.Content {
			if _, ok := part.(sdk.FilePart); ok {
				return true
			}
		}
	}
	return false
}

func countFileParts(messages []sdk.Message) int {
	count := 0
	for _, message := range messages {
		for _, part := range message.Content {
			if _, ok := part.(sdk.FilePart); ok {
				count++
			}
		}
	}
	return count
}
