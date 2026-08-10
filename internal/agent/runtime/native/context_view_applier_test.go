package native

import (
	"context"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	tools "github.com/memohai/memoh/internal/agent/tool"
)

func TestGenerateAppliesContextViewBeforeProviderOptions(t *testing.T) {
	t.Parallel()
	modelProvider := &usageRecordingProvider{}
	ledger := contextfrag.NewMutationLedger()
	called := 0
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) RunConfig {
		called++
		if !strings.Contains(cfg.ContextToolUsage, usageMarker) {
			t.Fatalf("applier tool usage = %q, want marker", cfg.ContextToolUsage)
		}
		if len(cfg.ContextToolDefs) != 1 || cfg.ContextToolDefs[0].Name != "fake_tool" {
			t.Fatalf("applier tool definitions = %#v", cfg.ContextToolDefs)
		}
		cfg.System = "compiled system"
		cfg.Messages = []sdk.Message{sdk.UserMessage("compiled message")}
		cfg.ContextMutations = ledger
		return cfg
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
	wantHash, _ := contextfrag.ProviderPayloadHashAndBytes(params.System, params.Messages, params.Tools)
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
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) RunConfig {
		cfg.ContextMutations = ledger
		return cfg
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
	wantHash, _ := contextfrag.ProviderPayloadHashAndBytes(lastParams.System, lastParams.Messages, lastParams.Tools)
	if ledger.FinalInputHash() != wantHash {
		t.Fatalf("final input hash = %q, want last provider step %q", ledger.FinalInputHash(), wantHash)
	}
}

func TestStreamAppliesContextViewOnce(t *testing.T) {
	t.Parallel()
	modelProvider := &usageStreamRecordingProvider{}
	called := make(chan RunConfig, 1)
	a := New(Deps{ContextViewApplier: func(_ context.Context, cfg RunConfig) RunConfig {
		called <- cfg
		cfg.System = "compiled stream system"
		cfg.Messages = []sdk.Message{sdk.UserMessage("compiled stream message")}
		cfg.ContextMutations = contextfrag.NewMutationLedger()
		return cfg
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
	got := New(Deps{}).applyContextView(context.Background(), cfg)
	if len(got.ContextFrags) == 0 || got.ContextManifest.Counts.Messages != 1 {
		t.Fatalf("legacy context refresh = %#v", got.ContextManifest)
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
