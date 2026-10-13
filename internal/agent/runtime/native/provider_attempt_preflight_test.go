package native

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
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

func TestAgentGenerateInitialHookContextIsCheckedAgainstTheAllowance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		window           int
		wantProviderCall int32
		wantError        error
	}{
		{name: "within allowance", window: 8192, wantProviderCall: 1},
		{name: "outgrows allowance", window: 1024, wantError: contextfrag.ErrBudgetUnsatisfied},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			marker := "round8-step-zero-hook-" + strings.ReplaceAll(tc.name, " ", "-")
			bridgeProvider, hookService := newBeforeModelCallHook(t, marker+"\n"+strings.Repeat("protected ", 400))
			ledger := contextfrag.NewMutationLedger()
			plan := contextfrag.ContextBudgetPlan{Window: tc.window, OutputReserve: 256}
			var providerParams sdk.Request
			modelProvider := &atomicMockProvider{
				handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) {
					providerParams = cloneGenerateParams(params)
					return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
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
				Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
				Messages:         []sdk.Message{sdk.UserMessage("task")},
				Identity:         SessionContext{BotID: "bot-1"},
				ContextMutations: ledger,
				ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
			})
			if !errors.Is(err, tc.wantError) {
				t.Fatalf("Generate() error = %v, want %v", err, tc.wantError)
			}
			if got := modelProvider.calls.Load(); got != tc.wantProviderCall {
				t.Fatalf("provider calls = %d, want %d", got, tc.wantProviderCall)
			}

			steps := ledger.StepSnapshots()
			if len(steps) != 1 || steps[0].Attempt != 0 || steps[0].StepIndex != 0 {
				t.Fatalf("step snapshots = %#v, want exactly attempt 0 step 0", steps)
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
	var providerParams sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) {
			providerParams = cloneGenerateParams(params)
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
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
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if modelProvider.calls.Load() != 1 || !providerAttemptContainsText(providerParams.Messages, marker) {
		t.Fatalf("provider payload = %#v, want unbudgeted hook context", providerParams.Messages)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].PostPrepareInputHash == "" {
		t.Fatalf("step snapshots = %#v, want one evaluated and hashed step", steps)
	}
	if countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextBudgetFailure) != 0 {
		t.Fatalf("window-zero run recorded budget failure: %#v", ledger.Records())
	}
}

// Every tool result a request carries goes to the provider as it was
// produced, however many there are and however large each is.
func TestPrepareProviderAttemptKeepsToolResultsWhole(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{sdk.UserMessage("original task")}
	messages := append(append([]sdk.Message(nil), prefix...), round8RetryToolCycles(10, 2048)...)
	sent := cloneProviderMessages(messages)
	ledger := contextfrag.NewMutationLedger()
	cfg := RunConfig{ContextMutations: ledger}
	handoff := newProviderAttemptHandoff(cfg)
	params := prepareProviderAttempt(
		context.Background(),
		cfg,
		handoff,
		false,
		len(prefix),
		0,
		nil,
		&sdk.Request{Messages: messages},
	)
	if err := handoff.publish(*params); err != nil {
		t.Fatalf("publish provider attempt: %v", err)
	}

	if !providerMessagesEqual(params.Messages, sent) {
		t.Fatal("prepared request is not the request the loop built")
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].StepIndex != 0 {
		t.Fatalf("step snapshots = %#v", steps)
	}
	wantHash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
	if steps[0].PostPrepareInputHash != wantHash || ledger.FinalInputHash() != wantHash {
		t.Fatalf("step/final hashes = %q/%q, want %q", steps[0].PostPrepareInputHash, ledger.FinalInputHash(), wantHash)
	}
	if records := ledger.Records(); len(records) != 0 {
		t.Fatalf("mutation records = %#v, want none", records)
	}
}

func TestAgentGenerateSnapshotHashesResolvedMapToolSchema(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	var providerParams sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(_ int, params sdk.Request) (sdk.ModelResult, error) {
			providerParams = cloneGenerateParams(params)
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name: "lookup",
		Parameters: toolexec.SchemaFromValue(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
			},
			"required": []string{"query"},
		}),
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return sdk.ToolOutput{}, nil
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
	// The request carries the resolved JSON Schema document, not the caller's
	// map literal: the frozen definition is what the digest and the provider
	// both see.
	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
		Required   []string       `json:"required"`
	}
	if data, err := json.Marshal(providerParams.Tools[0].Parameters); err != nil || json.Unmarshal(data, &schema) != nil {
		t.Fatalf("provider schema parameters = %s, want a resolved JSON Schema document: %v", providerParams.Tools[0].Parameters, err)
	}
	if schema.Type != "object" || len(schema.Properties) != 1 || len(schema.Required) != 1 {
		t.Fatalf("provider schema = %s, want the resolved map schema with one property and one required entry", providerParams.Tools[0].Parameters)
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
	var callParams []sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			callParams = append(callParams, cloneGenerateParams(params))
			if call == 1 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-round8",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			}
			return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}
	a := New(Deps{
		BridgeProvider: bridgeProvider,
		HookService:    hookService,
		ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
			return cfg, nil
		},
	})
	a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
		Name:       "lookup",
		Parameters: &jsonschema.Schema{Type: "object"},
		Execute: func(_ *toolexec.ToolExecContext, _ sdk.ToolArguments) (sdk.ToolOutput, error) {
			return toolexec.OutputFromValue(map[string]any{"answer": "ok"}), nil
		},
	}}}})

	plan := contextfrag.ContextBudgetPlan{Window: 16384, OutputReserve: 256}
	_, err := a.Generate(context.Background(), RunConfig{
		Model: &sdk.Model{
			ID:       "claude-round8",
			Provider: anthropicNameMockProvider{atomicMockProvider: modelProvider},
		},
		System:           "stable system",
		Messages:         []sdk.Message{sdk.UserMessage("task")},
		PromptCacheTTL:   models.DefaultPromptCacheTTL,
		ContextCachePlan: contextfrag.CachePlan{StableMessageCount: 1},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextManifest:  contextfrag.Manifest{BudgetPlan: &plan},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(callParams) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(callParams))
	}
	for i, params := range callParams {
		// The promoted system message and the applier message lead; the
		// hook context follows them on every step.
		if len(params.Messages) < 3 || !providerAttemptContainsText(params.Messages[2:], marker) {
			t.Fatalf("call %d lost the hook context after its prefix: %#v", i+1, params.Messages)
		}
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 2 {
		t.Fatalf("step snapshots = %#v, want exactly two", steps)
	}
	for i, step := range steps {
		if step.Attempt != 0 || step.StepIndex != i {
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
