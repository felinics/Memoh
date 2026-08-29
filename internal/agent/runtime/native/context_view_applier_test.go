package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestGenerateAppliesContextViewBeforeProviderOptions(t *testing.T) {
	t.Parallel()
	modelProvider := &usageRecordingProvider{}
	ledger := contextfrag.NewMutationLedger()
	called := 0
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		called++
		if !strings.Contains(cfg.ContextToolUsage, usageMarker) {
			t.Fatalf("applier tool usage = %q, want marker", cfg.ContextToolUsage)
		}
		if len(cfg.ContextToolDefs) != 1 || cfg.ContextToolDefs[0].Name != "fake_tool" {
			t.Fatalf("applier tool definitions = %#v", cfg.ContextToolDefs)
		}
		if len(cfg.ContextToolUsageFrags) != 2 ||
			cfg.ContextToolUsageFrags[0].ID != "system.tool_usage.header" ||
			cfg.ContextToolUsageFrags[1].ID != "system.tool_usage.fake_tool" {
			t.Fatalf("applier structured tool usage = %#v, want header and provider item", cfg.ContextToolUsageFrags)
		}
		if !cfg.ContextToolDefsResolved {
			t.Fatal("applier must see an authoritative tool capability roster")
		}
		cfg.System = "compiled system"
		cfg.Messages = []sdk.Message{sdk.UserMessage("compiled message")}
		cfg.ContextMutations = ledger
		return cfg, nil
	}})
	a.SetToolProviders([]tools.ToolProvider{&usageTestProvider{emitTool: true, usage: usageMarker}})

	if _, err := a.Generate(context.Background(), RunConfig{
		Model:  &sdk.Model{ID: "view-model", Provider: modelProvider, Type: sdk.ModelTypeChat},
		System: "legacy system", Messages: []sdk.Message{sdk.UserMessage("legacy message")}, SupportsToolCall: true,
	}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	if called != 1 {
		t.Fatalf("applier calls = %d, want 1", called)
	}
	params := modelProvider.lastParams()
	if params.System != "compiled system" || !reflect.DeepEqual(params.Messages, []sdk.Message{sdk.UserMessage("compiled message")}) {
		t.Fatalf("provider payload = system %q messages %#v", params.System, params.Messages)
	}
	wantHash := contextfrag.ProviderPayloadHash(params.System, params.Messages, params.Tools)
	if ledger.FinalInputHash() != wantHash {
		t.Fatalf("final input hash = %q, want %q", ledger.FinalInputHash(), wantHash)
	}
}

func TestGenerateFinalInputHashTracksLastProviderStep(t *testing.T) {
	t.Parallel()
	ledger := contextfrag.NewMutationLedger()
	var lastParams sdk.GenerateParams
	modelProvider := &atomicMockProvider{handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
		if call == 1 {
			return &sdk.GenerateResult{
				FinishReason: sdk.FinishReasonToolCalls,
				ToolCalls:    []sdk.ToolCall{{ToolCallID: "hash-call", ToolName: "hash_tool"}},
			}, nil
		}
		lastParams = params
		return &sdk.GenerateResult{Text: "done", FinishReason: sdk.FinishReasonStop}, nil
	}}
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		cfg.ContextMutations = ledger
		return cfg, nil
	}})
	a.SetToolProviders([]tools.ToolProvider{staticToolProvider{tools: []sdk.Tool{{
		Name: "hash_tool",
		Execute: func(*sdk.ToolExecContext, any) (any, error) {
			return "ok", nil
		},
	}}}})

	if _, err := a.Generate(context.Background(), RunConfig{
		Model:    &sdk.Model{ID: "hash-model", Provider: modelProvider, Type: sdk.ModelTypeChat},
		Messages: []sdk.Message{sdk.UserMessage("run the tool")}, SupportsToolCall: true,
	}); err != nil {
		t.Fatalf("Generate error: %v", err)
	}
	wantHash := contextfrag.ProviderPayloadHash(lastParams.System, lastParams.Messages, lastParams.Tools)
	if ledger.FinalInputHash() != wantHash {
		t.Fatalf("final input hash = %q, want last provider step %q", ledger.FinalInputHash(), wantHash)
	}
}

func TestStreamAppliesContextViewOnce(t *testing.T) {
	t.Parallel()
	modelProvider := &usageStreamRecordingProvider{}
	called := make(chan RunConfig, 1)
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		called <- cfg
		cfg.System = "compiled stream system"
		cfg.Messages = []sdk.Message{sdk.UserMessage("compiled stream message")}
		cfg.ContextMutations = contextfrag.NewMutationLedger()
		return cfg, nil
	}})

	for range a.Stream(context.Background(), RunConfig{
		Model:  &sdk.Model{ID: "view-stream-model", Provider: modelProvider, Type: sdk.ModelTypeChat},
		System: "legacy stream system", Messages: []sdk.Message{sdk.UserMessage("legacy stream message")},
	}) {
	}
	select {
	case <-called:
	default:
		t.Fatal("context view applier was not called")
	}
	select {
	case <-called:
		t.Fatal("context view applier was called more than once")
	default:
	}
	params := modelProvider.lastParams()
	if params.System != "compiled stream system" || !reflect.DeepEqual(params.Messages, []sdk.Message{sdk.UserMessage("compiled stream message")}) {
		t.Fatalf("provider payload = system %q messages %#v", params.System, params.Messages)
	}
}

func TestApplyContextViewFallsBackToLegacyRefresh(t *testing.T) {
	t.Parallel()
	cfg := RunConfig{System: "system", Messages: []sdk.Message{sdk.UserMessage("history")}}
	got, err := New(Deps{}).applyContextView(context.Background(), cfg)
	if err != nil {
		t.Fatalf("applyContextView error = %v", err)
	}
	if len(got.ContextFrags) == 0 || got.ContextManifest.Counts.Messages != 1 {
		t.Fatalf("legacy context refresh = %#v", got.ContextManifest)
	}
}

type preflightCountingProvider struct {
	mu            sync.Mutex
	generateCalls int
	streamCalls   int
}

func (*preflightCountingProvider) Name() string { return "preflight-counting" }

func (*preflightCountingProvider) ListModels(context.Context) ([]sdk.Model, error) { return nil, nil }

func (*preflightCountingProvider) Test(context.Context) *sdk.ProviderTestResult {
	return &sdk.ProviderTestResult{Status: sdk.ProviderStatusOK}
}

func (*preflightCountingProvider) TestModel(context.Context, string) (*sdk.ModelTestResult, error) {
	return &sdk.ModelTestResult{Supported: true}, nil
}

func (p *preflightCountingProvider) DoGenerate(context.Context, sdk.GenerateParams) (*sdk.GenerateResult, error) {
	p.mu.Lock()
	p.generateCalls++
	p.mu.Unlock()
	return &sdk.GenerateResult{Text: "unexpected", FinishReason: sdk.FinishReasonStop}, nil
}

func (p *preflightCountingProvider) DoStream(context.Context, sdk.GenerateParams) (*sdk.StreamResult, error) {
	p.mu.Lock()
	p.streamCalls++
	p.mu.Unlock()
	return nil, errors.New("unexpected provider stream call")
}

func (p *preflightCountingProvider) calls() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.generateCalls, p.streamCalls
}

func budgetErrorApplier(err error) ContextViewApplier {
	return func(_ context.Context, cfg RunConfig) (RunConfig, error) { return cfg, err }
}

func TestContextViewStreamErrorUsesStablePublicContract(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		err      error
		wantCode string
		wantText string
	}{
		{fmt.Errorf("%w: private cost", contextfrag.ErrProtectedContextOverflow), "context.protected_overflow", "Required context exceeds the model context budget."},
		{fmt.Errorf("%w: private math", contextfrag.ErrBudgetUnsatisfied), "context.budget_unsatisfied", "The model context window is too small for this request."},
		{errors.New("private collector failure"), "", publicContextPreparationError},
	} {
		event := contextViewStreamError(tt.err)
		if event.Type != EventError || event.Code != tt.wantCode || event.Error != tt.wantText {
			t.Fatalf("contextViewStreamError(%v) = %#v", tt.err, event)
		}
		if strings.Contains(event.Error, tt.err.Error()) {
			t.Fatalf("public event leaked private error: %#v", event)
		}
	}
}

func TestAgentInstallsLifecycleHolderBeforeContextPreflight(t *testing.T) {
	t.Parallel()

	seen := make(chan *contextfrag.LifecycleHolder, 1)
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) (RunConfig, error) {
		seen <- cfg.ContextLifecycle
		return cfg, contextfrag.ErrBudgetUnsatisfied
	}})
	_, _ = a.Generate(context.Background(), RunConfig{})
	if holder := <-seen; holder == nil {
		t.Fatal("context preflight received a nil lifecycle holder")
	}
}

func TestGenerateContextBudgetErrorStopsBeforeProvider(t *testing.T) {
	t.Parallel()

	provider := &preflightCountingProvider{}
	wantErr := fmt.Errorf("%w: private cost", contextfrag.ErrProtectedContextOverflow)
	result, err := New(Deps{ContextViewApplier: budgetErrorApplier(wantErr)}).Generate(context.Background(), RunConfig{
		Model: &sdk.Model{ID: "budget-generate", Provider: provider, Type: sdk.ModelTypeChat},
	})
	if result != nil || !errors.Is(err, wantErr) || !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
	if generate, stream := provider.calls(); generate != 0 || stream != 0 {
		t.Fatalf("provider calls = %d/%d, want zero", generate, stream)
	}
}

func TestStreamContextBudgetErrorIsPublicAndStopsBeforeProvider(t *testing.T) {
	t.Parallel()

	provider := &preflightCountingProvider{}
	wantErr := fmt.Errorf("%w: window=31 private", contextfrag.ErrBudgetUnsatisfied)
	var events []StreamEvent
	for event := range New(Deps{ContextViewApplier: budgetErrorApplier(wantErr)}).Stream(context.Background(), RunConfig{
		Model: &sdk.Model{ID: "budget-stream", Provider: provider, Type: sdk.ModelTypeChat},
	}) {
		if event.Type == EventError {
			events = append(events, event)
		}
	}
	if len(events) != 1 || events[0].Code != "context.budget_unsatisfied" ||
		events[0].Error != "The model context window is too small for this request." {
		t.Fatalf("error events = %#v", events)
	}
	if strings.Contains(events[0].Error, "window=31") {
		t.Fatalf("public event leaked private error: %#v", events[0])
	}
	if generate, stream := provider.calls(); generate != 0 || stream != 0 {
		t.Fatalf("provider calls = %d/%d, want zero", generate, stream)
	}
}

func TestBeforeModelCallAppendRecordsPostViewMutation(t *testing.T) {
	t.Parallel()
	ledger := contextfrag.NewMutationLedger()
	source := []contextfrag.ContextFrag{{ID: "authoritative"}}
	cfg := RunConfig{Messages: []sdk.Message{sdk.UserMessage("before")}, ContextSourceFrags: source, ContextMutations: ledger}

	got := applyBeforeModelCallAppendContext(cfg, "hook bytes")
	if len(got.Messages) != 2 || !reflect.DeepEqual(got.ContextSourceFrags, source) {
		t.Fatalf("hook append changed authoritative source: %#v", got)
	}
	records := ledger.Records()
	if len(records) != 1 || records[0].Kind != contextfrag.MutationBeforeModelCallHook {
		t.Fatalf("mutation records = %#v", records)
	}
}
