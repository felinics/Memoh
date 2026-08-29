package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/hooks"
	"github.com/felinics/memoh/internal/models"
	"github.com/felinics/memoh/internal/workspace/bridge"
	pb "github.com/felinics/memoh/internal/workspace/bridgepb"
)

func (s *mockExecContainerService) ReadRaw(req *pb.ReadRawRequest, stream pb.ContainerService_ReadRawServer) error {
	s.mu.Lock()
	data, ok := s.written[req.GetPath()]
	data = append([]byte(nil), data...)
	s.mu.Unlock()
	if !ok {
		return status.Error(codes.NotFound, "file not found")
	}
	if len(data) == 0 {
		return nil
	}
	return stream.Send(&pb.DataChunk{Data: data})
}

func newBeforeModelCallHook(t *testing.T, appendContext string) (bridge.Provider, *hooks.Service) {
	t.Helper()

	output, err := json.Marshal(map[string]any{
		"decision":       hooks.DecisionAppendContext,
		"append_context": appendContext,
	})
	if err != nil {
		t.Fatalf("marshal hook output: %v", err)
	}
	svc := newMockExecContainerService()
	svc.setBehavior("round8-before-model", execBehavior{stdout: string(output)})
	svc.written[hooks.DefaultConfigPath] = []byte(`{
		"version": 1,
		"enabled": true,
		"hooks": [{
			"name": "round8 before model",
			"event": "BeforeModelCall",
			"actions": [{
				"type": "command",
				"command": "round8-before-model"
			}]
		}]
	}`)
	provider, cleanup := setupExecTestInfra(t, svc)
	t.Cleanup(cleanup)
	return provider, hooks.NewService(nil, provider)
}

func providerAttemptContainsText(messages []sdk.Message, text string) bool {
	for _, msg := range messages {
		for _, part := range msg.Content {
			if value, ok := part.(sdk.TextPart); ok && strings.Contains(value.Text, text) {
				return true
			}
		}
	}
	return false
}

func countProviderAttemptMutations(records []contextfrag.MutationRecord, kind contextfrag.MutationKind) int {
	count := 0
	for _, record := range records {
		if record.Kind == kind {
			count++
		}
	}
	return count
}

func TestAgentGenerateInitialHookProviderAttemptModes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		mode             LoopReselectMode
		selection        ContextStepSelectionResult
		wantOutcome      string
		wantProviderCall int32
		wantSelectorCall int32
		wantError        error
	}{
		{
			name:             "active within allowance",
			mode:             LoopReselectActive,
			wantOutcome:      contextfrag.ReselectionOutcomeUnchanged,
			wantProviderCall: 1,
			wantSelectorCall: 1,
		},
		{
			name:             "active protected overflow",
			mode:             LoopReselectActive,
			selection:        ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow},
			wantOutcome:      contextfrag.ReselectionOutcomeFailed,
			wantSelectorCall: 1,
			wantError:        contextfrag.ErrProtectedContextOverflow,
		},
		{
			name:             "shadow would fail",
			mode:             LoopReselectShadow,
			selection:        ContextStepSelectionResult{FatalError: contextfrag.ErrProtectedContextOverflow},
			wantOutcome:      contextfrag.ReselectionOutcomeWouldFail,
			wantProviderCall: 1,
			wantSelectorCall: 1,
		},
		{
			name:             "off stays legacy",
			mode:             LoopReselectOff,
			wantProviderCall: 1,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			marker := "round8-step-zero-hook-" + strings.ReplaceAll(tc.name, " ", "-")
			bridgeProvider, hookService := newBeforeModelCallHook(t, marker+"\n"+strings.Repeat("protected ", 400))
			ledger := contextfrag.NewMutationLedger()
			plan := contextfrag.ContextBudgetPlan{Window: 8192, OutputReserve: 256}
			var selectorCalls atomic.Int32
			var providerParams sdk.GenerateParams
			modelProvider := &atomicMockProvider{
				handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
					providerParams = cloneGenerateParams(params)
					return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
				},
			}
			applierMessageCount := 0
			a := New(Deps{
				BridgeProvider:   bridgeProvider,
				HookService:      hookService,
				LoopReselectMode: tc.mode,
				ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
					applierMessageCount = len(cfg.Messages)
					return cfg, nil
				},
			})

			_, err := a.Generate(context.Background(), RunConfig{
				Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
				Messages:         []sdk.Message{sdk.UserMessage("task")},
				Identity:         SessionContext{BotID: "bot-1"},
				ContextMutations: ledger,
				ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
				ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
					selectorCalls.Add(1)
					if input.InitialMessageCount != applierMessageCount {
						t.Fatalf("InitialMessageCount = %d, want applier count %d", input.InitialMessageCount, applierMessageCount)
					}
					if !providerAttemptContainsText(input.Messages[input.InitialMessageCount:], marker) {
						t.Fatalf("hook marker is not in governed suffix: %#v", input.Messages)
					}
					if input.BudgetMaxTokens <= 0 {
						t.Fatalf("BudgetMaxTokens = %d, want active allowance", input.BudgetMaxTokens)
					}
					return tc.selection
				},
			})
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Generate() error = %v, want %v", err, tc.wantError)
			}
			if got := modelProvider.calls.Load(); got != tc.wantProviderCall {
				t.Fatalf("provider calls = %d, want %d", got, tc.wantProviderCall)
			}
			if got := selectorCalls.Load(); got != tc.wantSelectorCall {
				t.Fatalf("selector calls = %d, want %d", got, tc.wantSelectorCall)
			}

			steps := ledger.StepSnapshots()
			if len(steps) != 1 || steps[0].Attempt != 0 || steps[0].StepIndex != 0 {
				t.Fatalf("step snapshots = %#v, want exactly attempt 0 step 0", steps)
			}
			if steps[0].ReselectionOutcome != tc.wantOutcome {
				t.Fatalf("reselection outcome = %q, want %q", steps[0].ReselectionOutcome, tc.wantOutcome)
			}
			if tc.wantError != nil {
				if steps[0].PostPrepareInputHash != "" {
					t.Fatalf("failed preflight hash = %q, want empty", steps[0].PostPrepareInputHash)
				}
				if got := countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure); got != 1 {
					t.Fatalf("budget failure mutations = %d, want 1", got)
				}
				return
			}

			wantHash := contextfrag.ProviderPayloadHash(providerParams.System, providerParams.Messages, providerParams.Tools)
			if steps[0].PostPrepareInputHash != wantHash {
				t.Fatalf("step hash = %q, want actual provider hash %q", steps[0].PostPrepareInputHash, wantHash)
			}
			if !providerAttemptContainsText(providerParams.Messages, marker) {
				t.Fatalf("provider payload lost hook marker: %#v", providerParams.Messages)
			}
			if got := countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure); got != 0 {
				t.Fatalf("budget failure mutations = %d, want 0", got)
			}
		})
	}
}

func TestAgentGenerateInitialHookWindowZeroRunsPreflightWithoutBudgetEnforcement(t *testing.T) {
	t.Parallel()

	marker := "round8-window-zero-hook"
	bridgeProvider, hookService := newBeforeModelCallHook(t, marker+"\n"+strings.Repeat("large ", 1000))
	ledger := contextfrag.NewMutationLedger()
	var selectorCalls atomic.Int32
	var providerParams sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			providerParams = cloneGenerateParams(params)
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{
		BridgeProvider: bridgeProvider,
		HookService:    hookService,
		ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
			return cfg, nil
		},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:                  &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:               []sdk.Message{sdk.UserMessage("task")},
		Identity:               SessionContext{BotID: "bot-1"},
		ContextMutations:       ledger,
		ContextBudgetMaxTokens: 0,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls.Add(1)
			if input.BudgetMaxTokens != 0 {
				t.Fatalf("BudgetMaxTokens = %d, want 0", input.BudgetMaxTokens)
			}
			if input.KeepRecentToolResults != stepReselectKeepRecentToolResults || input.MinMessages != stepReselectMinMessages {
				t.Fatalf("hygiene settings = keep %d/min %d", input.KeepRecentToolResults, input.MinMessages)
			}
			return ContextStepSelectionResult{}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if selectorCalls.Load() != 1 {
		t.Fatalf("selector calls = %d, want 1", selectorCalls.Load())
	}
	if modelProvider.calls.Load() != 1 || !providerAttemptContainsText(providerParams.Messages, marker) {
		t.Fatalf("provider payload = %#v, want unbudgeted hook context", providerParams.Messages)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].ReselectionOutcome != contextfrag.ReselectionOutcomeUnchanged ||
		steps[0].PostPrepareInputHash == "" {
		t.Fatalf("step snapshots = %#v, want one evaluated and hashed step", steps)
	}
	if countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure) != 0 {
		t.Fatalf("window-zero run recorded budget failure: %#v", ledger.Records())
	}
}

func TestPrepareProviderAttemptStepZeroWindowZeroAppliesSuffixHygiene(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("original task")}
	messages := append(append([]sdk.Message(nil), prefix...), round8RetryToolCycles(10, 2048)...)
	ledger := contextfrag.NewMutationLedger()
	var selectorCalls atomic.Int32
	cfg := RunConfig{
		ContextMutations:       ledger,
		ContextBudgetMaxTokens: 0,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls.Add(1)
			if input.InitialMessageCount != len(prefix) {
				t.Fatalf("InitialMessageCount = %d, want %d", input.InitialMessageCount, len(prefix))
			}
			if input.BudgetMaxTokens != 0 {
				t.Fatalf("BudgetMaxTokens = %d, want disabled budget", input.BudgetMaxTokens)
			}
			if input.KeepRecentToolResults != stepReselectKeepRecentToolResults ||
				input.MinMessages != stepReselectMinMessages {
				t.Fatalf("hygiene settings = keep %d/min %d", input.KeepRecentToolResults, input.MinMessages)
			}
			selected, pruned := pruneRound8RetryOldToolResults(input.Messages, input.KeepRecentToolResults)
			return ContextStepSelectionResult{Messages: selected, Truncated: pruned}
		},
	}
	handoff := newProviderAttemptHandoff(cfg)
	params := prepareProviderAttempt(
		context.Background(),
		cfg,
		handoff,
		LoopReselectActive,
		false,
		len(prefix),
		0,
		preparedMessageProvenance{},
		&sdk.GenerateParams{Messages: messages},
	)
	if err := handoff.publish(*params); err != nil {
		t.Fatalf("publish provider attempt: %v", err)
	}

	if selectorCalls.Load() != 1 {
		t.Fatalf("selector calls = %d, want 1", selectorCalls.Load())
	}
	if got := countRound8PrunedToolResults(params.Messages); got != 6 {
		t.Fatalf("pruned step-zero tool results = %d, want 6", got)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].StepIndex != 0 ||
		steps[0].ReselectionOutcome != contextfrag.ReselectionOutcomeApplied ||
		steps[0].Truncated != 6 {
		t.Fatalf("step snapshots = %#v", steps)
	}
	wantHash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
	if steps[0].PostPrepareInputHash != wantHash || ledger.FinalInputHash() != wantHash {
		t.Fatalf("step/final hashes = %q/%q, want %q", steps[0].PostPrepareInputHash, ledger.FinalInputHash(), wantHash)
	}
}

func TestAgentGenerateSnapshotHashesResolvedMapToolSchema(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var providerParams sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(_ int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			providerParams = cloneGenerateParams(params)
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name: "lookup",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		},
		Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
			return nil, nil
		},
	}}}})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(providerParams.Tools) != 1 {
		t.Fatalf("provider tools = %#v, want one", providerParams.Tools)
	}
	if _, ok := providerParams.Tools[0].Parameters.(*jsonschema.Schema); !ok {
		t.Fatalf("provider schema type = %T, want resolved *jsonschema.Schema", providerParams.Tools[0].Parameters)
	}

	steps := ledger.StepSnapshots()
	if len(steps) != 1 {
		t.Fatalf("step snapshots = %#v, want one", steps)
	}
	wantHash := contextfrag.ProviderPayloadHash(
		providerParams.System,
		providerParams.Messages,
		providerParams.Tools,
	)
	if steps[0].PostPrepareInputHash != wantHash || ledger.FinalInputHash() != wantHash {
		t.Fatalf(
			"step/final hashes = %q/%q, want resolved provider payload hash %q",
			steps[0].PostPrepareInputHash,
			ledger.FinalInputHash(),
			wantHash,
		)
	}
}

func TestAgentGenerateHookStaysGovernedAcrossAnthropicProviderSteps(t *testing.T) {
	t.Parallel()

	marker := "round8-multistep-hook"
	bridgeProvider, hookService := newBeforeModelCallHook(t, marker)
	ledger := contextfrag.NewMutationLedger()
	var callParams []sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			callParams = append(callParams, cloneGenerateParams(params))
			if call == 1 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-round8",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
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
	plan := contextfrag.ContextBudgetPlan{Window: 16384, OutputReserve: 256}
	_, err := a.Generate(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "claude-round8",
			Provider: anthropicNameMockProvider{atomicMockProvider: modelProvider},
			Type:     sdk.ModelTypeChat,
		},
		System:           "stable system",
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		PromptCacheTTL:   models.DefaultPromptCacheTTL,
		ContextCachePlan: contextfrag.CachePlan{StableMessageCount: 1},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			selectorCalls.Add(1)
			if input.InitialMessageCount != 2 {
				t.Fatalf("InitialMessageCount = %d, want promoted system + applier message", input.InitialMessageCount)
			}
			if !providerAttemptContainsText(input.Messages[input.InitialMessageCount:], marker) {
				t.Fatalf("hook marker left governed suffix at call %d: %#v", selectorCalls.Load(), input.Messages)
			}
			return ContextStepSelectionResult{}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if selectorCalls.Load() != 2 || len(callParams) != 2 {
		t.Fatalf("selector/provider calls = %d/%d, want 2/2", selectorCalls.Load(), len(callParams))
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 2 {
		t.Fatalf("step snapshots = %#v, want exactly two", steps)
	}
	for i, step := range steps {
		if step.Attempt != 0 || step.StepIndex != i ||
			step.ReselectionOutcome != contextfrag.ReselectionOutcomeUnchanged {
			t.Fatalf("steps[%d] = %#v", i, step)
		}
		wantHash := contextfrag.ProviderPayloadHash(
			callParams[i].System, callParams[i].Messages, callParams[i].Tools,
		)
		if step.PostPrepareInputHash != wantHash {
			t.Fatalf("steps[%d] hash = %q, want %q", i, step.PostPrepareInputHash, wantHash)
		}
	}
}
