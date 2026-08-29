package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync/atomic"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/models"
)

func TestBuildGenerateOptionsSetsModelInfoAndLegacyPruneLoopSelectionModeWithoutReselector(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	model := &sdk.Model{ID: "mock-model", Provider: &usageRecordingProvider{}, Type: sdk.ModelTypeChat}
	cfg := RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		ContextMutations: ledger,
	}

	(*Agent)(nil).buildGenerateOptions(context.Background(), cfg, nil, nil, nil)

	gotModel, gotClientType := ledger.ModelInfo()
	if gotModel != "mock-model" {
		t.Fatalf("model = %q, want mock-model", gotModel)
	}
	if gotClientType != string(models.ClientTypeOpenAICompletions) {
		t.Fatalf("client type = %q, want %q", gotClientType, models.ClientTypeOpenAICompletions)
	}
	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionLegacyPrune {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionLegacyPrune)
	}
}

func TestBuildGenerateOptionsSetsSuffixOnlyLoopSelectionModeWithReselector(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	model := &sdk.Model{ID: "mock-model", Provider: &usageRecordingProvider{}, Type: sdk.ModelTypeChat}
	cfg := RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		ContextMutations: ledger,
		ContextStepReselector: func(context.Context, ContextStepSelectionInput) ContextStepSelectionResult {
			return ContextStepSelectionResult{}
		},
	}

	(*Agent)(nil).buildGenerateOptions(context.Background(), cfg, nil, nil, nil)

	if got := ledger.LoopSelectionMode(); got != contextfrag.LoopSelectionSuffixOnly {
		t.Fatalf("loop selection mode = %q, want %q", got, contextfrag.LoopSelectionSuffixOnly)
	}
}

// TestAgentGenerateStepReselectionAppliedPreservesDecoratedPrefix is Gap-G(a):
// with an Anthropic model and a cache plan so the initial prefix carries
// cache_control, a reselector that keeps the initial prefix intact and only
// shrinks the suffix must be applied, and the decorated prefix (including
// CacheControl) must survive unchanged into the next provider call.
// anthropicNameMockProvider wraps atomicMockProvider so
// models.ResolveClientType resolves it to the Anthropic Messages client
// type (decoration dispatch keys off Provider.Name() containing
// "anthropic"), while DoGenerate/DoStream still run through the
// caller-supplied mock handler instead of a real network call.
type anthropicNameMockProvider struct {
	*atomicMockProvider
}

func (anthropicNameMockProvider) Name() string { return "anthropic-mock" }

func TestAgentGenerateStepReselectionAppliedPreservesDecoratedPrefix(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()

	var firstCallMessages, secondCallMessages []sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				firstCallMessages = append([]sdk.Message(nil), params.Messages...)
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			case 2:
				secondCallMessages = append([]sdk.Message(nil), params.Messages...)
				return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			default:
				return nil, errors.New("unexpected provider call")
			}
		},
	}
	model := &sdk.Model{ID: "claude-test", Provider: anthropicNameMockProvider{atomicMockProvider: modelProvider}, Type: sdk.ModelTypeChat}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	var reselectorCalls atomic.Int32
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		PromptCacheTTL:   models.DefaultPromptCacheTTL,
		ContextCachePlan: contextfrag.CachePlan{StableMessageCount: 1},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			reselectorCalls.Add(1)
			prefix := append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...)
			suffix := input.Messages[input.InitialMessageCount:]
			reduced := suffix
			dropped := 0
			if len(suffix) > 1 {
				reduced = suffix[:len(suffix)-1]
				dropped = 1
			}
			return ContextStepSelectionResult{
				Messages: append(prefix, reduced...),
				Dropped:  dropped,
			}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if reselectorCalls.Load() != 1 {
		t.Fatalf("reselector calls = %d, want 1", reselectorCalls.Load())
	}
	if len(firstCallMessages) != 2 {
		t.Fatalf("first call messages = %d, want 2 (decorated system+user prefix)", len(firstCallMessages))
	}
	if textPartOf(t, firstCallMessages[0]).CacheControl == nil {
		t.Fatal("first call's leading (system) message should carry cache_control")
	}
	if textPartOf(t, firstCallMessages[1]).CacheControl == nil {
		t.Fatal("first call's second (user) message should carry cache_control")
	}

	steps := ledger.StepSnapshots()
	if len(steps) != 2 {
		t.Fatalf("step snapshots = %#v, want 2 (step 0 for the initial call, step 1 for the reselection prepare)", steps)
	}
	if !steps[1].ReselectionApplied {
		t.Fatalf("steps[1].ReselectionApplied = false, want true: %#v", steps[1])
	}
	if steps[1].Dropped != 1 {
		t.Fatalf("steps[1].Dropped = %d, want 1", steps[1].Dropped)
	}

	if len(secondCallMessages) < 2 {
		t.Fatalf("second call messages = %d, want at least 2", len(secondCallMessages))
	}
	if !reflect.DeepEqual(secondCallMessages[:2], firstCallMessages) {
		t.Fatalf("second call prefix = %#v, want unchanged decorated prefix %#v", secondCallMessages[:2], firstCallMessages)
	}
}

// TestAgentGenerateStepReselectionRejectedKeepsDecoratedPrefixUnchanged is
// Gap-G(b): a reselector that strips cache_control from a prefix message
// must be rejected by the prefix guard (fallback prune path), leaving the
// decorated prefix byte-for-byte unchanged in the next provider call.
func TestAgentGenerateStepReselectionRejectedKeepsDecoratedPrefixUnchanged(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()

	var firstCallMessages, secondCallMessages []sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			switch call {
			case 1:
				firstCallMessages = append([]sdk.Message(nil), params.Messages...)
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      map[string]any{"q": "one"},
					}},
				}, nil
			case 2:
				secondCallMessages = append([]sdk.Message(nil), params.Messages...)
				return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			default:
				return nil, errors.New("unexpected provider call")
			}
		},
	}
	model := &sdk.Model{ID: "claude-test", Provider: anthropicNameMockProvider{atomicMockProvider: modelProvider}, Type: sdk.ModelTypeChat}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	_, err := a.Generate(context.Background(), RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		PromptCacheTTL:   models.DefaultPromptCacheTTL,
		ContextCachePlan: contextfrag.CachePlan{StableMessageCount: 1},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
		ContextStepReselector: func(_ context.Context, input ContextStepSelectionInput) ContextStepSelectionResult {
			prefix := append([]sdk.Message(nil), input.Messages[:input.InitialMessageCount]...)
			stripCacheControlFromLastPart(&prefix[len(prefix)-1])
			return ContextStepSelectionResult{
				Messages: append(prefix, input.Messages[input.InitialMessageCount:]...),
			}
		},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	steps := ledger.StepSnapshots()
	if len(steps) != 2 {
		t.Fatalf("step snapshots = %#v, want 2 (step 0 for the initial call, step 1 for the rejected reselection prepare)", steps)
	}
	if steps[1].ReselectionApplied {
		t.Fatalf("steps[1].ReselectionApplied = true, want false (cache_control-stripped prefix must be rejected): %#v", steps[1])
	}

	if len(secondCallMessages) < 2 {
		t.Fatalf("second call messages = %d, want at least 2", len(secondCallMessages))
	}
	if !reflect.DeepEqual(secondCallMessages[:2], firstCallMessages) {
		t.Fatalf("second call prefix = %#v, want unchanged decorated prefix %#v", secondCallMessages[:2], firstCallMessages)
	}
}

// TestAgentGenerateRecordsOneStepSnapshotPerModelStepWithDistinctHashes
// covers Defect A: the twilight SDK only invokes PrepareStep for model steps
// > 0 (step 0's input is the initial decorated payload, never re-prepared),
// while every model step 0..R-1 gets a StepResult via WithOnStep. So R
// provider calls must produce exactly R step snapshots (0..R-1), each
// hashing that call's ACTUAL input params — not R-1 snapshots misattributed
// one call ahead of the usage they're supposed to pair with.
func TestAgentGenerateRecordsOneStepSnapshotPerModelStepWithDistinctHashes(t *testing.T) {
	t.Parallel()

	var callParams []sdk.GenerateParams
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.GenerateParams) (*sdk.GenerateResult, error) {
			callParams = append(callParams, sdk.GenerateParams{
				System:   params.System,
				Messages: append([]sdk.Message(nil), params.Messages...),
				Tools:    append([]sdk.Tool(nil), params.Tools...),
			})
			if call <= 3 {
				return &sdk.GenerateResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("call-%d", call),
						ToolName:   "lookup",
						Input:      map[string]any{"step": call},
					}},
				}, nil
			}
			return &sdk.GenerateResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
		},
	}

	a := New(Deps{})
	a.SetToolProviders([]agenttools.ToolProvider{
		staticToolProvider{tools: []sdk.Tool{{
			Name:       "lookup",
			Parameters: &jsonschema.Schema{Type: "object"},
			Execute: func(_ *sdk.ToolExecContext, _ any) (any, error) {
				return map[string]any{"answer": "ok"}, nil
			},
		}}},
	})

	ledger := contextfrag.NewMutationLedger()
	_, err := a.Generate(context.Background(), RunConfig{
		Model:            &sdk.Model{ID: "mock-model", Provider: modelProvider},
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	steps := ledger.StepSnapshots()
	if len(steps) != len(callParams) {
		t.Fatalf("step snapshots = %d, want %d (one per model step, matching provider calls)", len(steps), len(callParams))
	}
	seen := map[string]bool{}
	for i, step := range steps {
		if step.StepIndex != i {
			t.Fatalf("steps[%d].StepIndex = %d, want %d", i, step.StepIndex, i)
		}
		if step.PostPrepareInputHash == "" {
			t.Fatalf("steps[%d].PostPrepareInputHash empty", i)
		}
		wantHash := contextfrag.ProviderPayloadHash(callParams[i].System, callParams[i].Messages, callParams[i].Tools)
		if step.PostPrepareInputHash != wantHash {
			t.Fatalf("steps[%d].PostPrepareInputHash = %q, want hash of call %d's actual input %q", i, step.PostPrepareInputHash, i, wantHash)
		}
		if seen[step.PostPrepareInputHash] {
			t.Fatalf("steps[%d] hash %q duplicates an earlier step; messages differ across steps so hashes must too", i, step.PostPrepareInputHash)
		}
		seen[step.PostPrepareInputHash] = true
	}
	if got, want := ledger.FinalInputHash(), steps[len(steps)-1].PostPrepareInputHash; got != want {
		t.Fatalf("FinalInputHash() = %q, want last step hash %q", got, want)
	}
}

func TestPrepareMidStreamRetryConfigAdvancesAttemptForSubsequentRecords(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{StepIndex: 0})

	cfg := RunConfig{
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		ContextMutations: ledger,
	}
	_ = prepareMidStreamRetryConfig(cfg, nil, "timeout")
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{StepIndex: 0})
	ledger.AppendStepSnapshot(contextfrag.StepSnapshot{StepIndex: 0})

	records := ledger.CacheUsageRecords()
	if len(records) != 2 || records[0].Attempt != 0 || records[1].Attempt != 1 {
		t.Fatalf("cache usage attempts = %#v, want [0, 1]", records)
	}
	steps := ledger.StepSnapshots()
	if len(steps) != 1 || steps[0].Attempt != 1 {
		t.Fatalf("step snapshot attempt = %#v, want 1", steps)
	}
}

func textPartOf(t *testing.T, msg sdk.Message) sdk.TextPart {
	t.Helper()
	if len(msg.Content) == 0 {
		t.Fatalf("message has no content parts: %#v", msg)
	}
	part, ok := msg.Content[len(msg.Content)-1].(sdk.TextPart)
	if !ok {
		t.Fatalf("last content part is not a TextPart: %#v", msg.Content[len(msg.Content)-1])
	}
	return part
}

func stripCacheControlFromLastPart(msg *sdk.Message) {
	if len(msg.Content) == 0 {
		return
	}
	parts := append([]sdk.MessagePart(nil), msg.Content...)
	last := len(parts) - 1
	if p, ok := parts[last].(sdk.TextPart); ok {
		p.CacheControl = nil
		parts[last] = p
	}
	msg.Content = parts
}
