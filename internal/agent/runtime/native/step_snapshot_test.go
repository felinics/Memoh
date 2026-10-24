package native

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"
	"github.com/google/jsonschema-go/jsonschema"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/step"
	agenttools "github.com/felinics/memoh/internal/agent/tool"
	"github.com/felinics/memoh/internal/agent/toolexec"
	"github.com/felinics/memoh/internal/models"
)

func TestBuildGenerateDispatchSetsModelInfo(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	model := &sdk.Model{ID: "mock-model", Provider: &usageRecordingProvider{}, Type: sdk.ModelTypeChat}
	cfg := RunConfig{
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("hi")},
		ContextMutations: ledger,
	}

	if _, err := (*Agent)(nil).buildGenerateDispatch(context.Background(), cfg, 0, nil, nil, nil); err != nil {
		t.Fatalf("buildGenerateDispatch: %v", err)
	}

	gotModel, gotClientType := ledger.ModelInfo()
	if gotModel != "mock-model" {
		t.Fatalf("model = %q, want mock-model", gotModel)
	}
	if gotClientType != string(models.ClientTypeOpenAICompletions) {
		t.Fatalf("client type = %q, want %q", gotClientType, models.ClientTypeOpenAICompletions)
	}
}

// anthropicNameMockProvider wraps atomicMockProvider so
// models.ResolveClientType resolves it to the Anthropic Messages client
// type (decoration dispatch keys off Provider.Name() containing
// "anthropic"), while DoGenerate/DoStream still run through the
// caller-supplied mock handler instead of a real network call.
type anthropicNameMockProvider struct {
	*atomicMockProvider
}

func (anthropicNameMockProvider) Name() string { return "anthropic-mock" }

func TestAgentGenerateLaterStepKeepsDecoratedPrefix(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()

	var firstCallMessages, secondCallMessages []sdk.Message
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			switch call {
			case 1:
				firstCallMessages = append([]sdk.Message(nil), params.Messages...)
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: "call-1",
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"q": "one"}),
					}},
				}, nil
			case 2:
				secondCallMessages = append([]sdk.Message(nil), params.Messages...)
				return sdk.ModelResult{Text: "ok", FinishReason: sdk.FinishReasonStop}, nil
			default:
				return sdk.ModelResult{}, errors.New("unexpected provider call")
			}
		},
	}
	model := &sdk.Model{ID: "claude-test", Provider: anthropicNameMockProvider{atomicMockProvider: modelProvider}, Type: sdk.ModelTypeChat}

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
		Model:            model,
		System:           "sys",
		Messages:         []sdk.Message{sdk.UserMessage("start")},
		PromptCacheTTL:   models.DefaultPromptCacheTTL,
		ContextCachePlan: contextfrag.CachePlan{StableMessageCount: 1},
		SupportsToolCall: true,
		Identity:         SessionContext{BotID: "bot-1"},
		ContextMutations: ledger,
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
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
		t.Fatalf("step snapshots = %#v, want 2 (step 0 for the initial call, step 1 for the tool step)", steps)
	}

	if len(secondCallMessages) < 2 {
		t.Fatalf("second call messages = %d, want at least 2", len(secondCallMessages))
	}
	if !reflect.DeepEqual(secondCallMessages[:2], firstCallMessages) {
		t.Fatalf("second call prefix = %#v, want unchanged decorated prefix %#v", secondCallMessages[:2], firstCallMessages)
	}
}

// TestAgentGenerateRecordsOneStepSnapshotPerModelStepWithDistinctHashes
// covers Defect A: R provider calls must produce exactly R step snapshots
// (0..R-1), each hashing that call's ACTUAL input request — not R-1 snapshots
// misattributed one call ahead of the usage they are supposed to pair with.
// The owned generate loop builds the request for every step, including step 0,
// so each snapshot pairs with the call it was taken from.
func TestAgentGenerateRecordsOneStepSnapshotPerModelStepWithDistinctHashes(t *testing.T) {
	t.Parallel()

	var callParams []sdk.Request
	modelProvider := &atomicMockProvider{
		handler: func(call int, params sdk.Request) (sdk.ModelResult, error) {
			callParams = append(callParams, sdk.Request{
				System:   params.System,
				Messages: append([]sdk.Message(nil), params.Messages...),
				Tools:    append([]sdk.ToolDefinition(nil), params.Tools...),
			})
			if call <= 3 {
				return sdk.ModelResult{
					FinishReason: sdk.FinishReasonToolCalls,
					ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("call-%d", call),
						ToolName:   "lookup",
						Input:      toolexec.ArgumentsFromValue(map[string]any{"step": call}),
					}},
				}, nil
			}
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

// The step audit a run records is keyed by durable step in both loops: a
// continuation counts on from the step its run paused before, the dispatch an
// in-run event rebuilds goes on from the step it regenerates, and each call's
// cache usage is in the ledger before its step commits, so the lifecycle the
// step persists covers the call that made it.
func TestRunStepAuditUsesDurableStepIndexes(t *testing.T) {
	t.Parallel()

	const offset = 7
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			t.Parallel()
			provider := &atomicMockProvider{handler: func(call int, _ sdk.Request) (sdk.ModelResult, error) {
				usage := sdk.Usage{InputTokens: 1000, InputTokenDetails: sdk.InputTokenDetail{CacheReadTokens: 100 * call, NoCacheTokens: 1000 - 100*call}}
				if call <= 4 {
					return sdk.ModelResult{FinishReason: sdk.FinishReasonToolCalls, Usage: usage, ToolCalls: []sdk.ToolCall{{
						ToolCallID: fmt.Sprintf("c%d", call), ToolName: "effect", Input: toolexec.ArgumentsFromValue(map[string]any{"n": call}),
					}}}, nil
				}
				return sdk.ModelResult{Text: "done", FinishReason: sdk.FinishReasonStop, Usage: usage}, nil
			}}
			a := New(Deps{})
			a.SetToolProviders([]agenttools.ToolProvider{staticToolProvider{tools: []toolexec.Tool{{
				Name: "effect", Parameters: &jsonschema.Schema{Type: "object"},
				Execute: func(*toolexec.ToolExecContext, sdk.ToolArguments) (sdk.ToolOutput, error) {
					return toolexec.OutputFromValue(strings.Repeat(" r", 2000)), nil
				},
			}}}})
			input := sdk.UserMessage("INPUT")
			ledger := contextfrag.NewMutationLedger()
			var committed, unrecorded []int
			cfg := RunConfig{
				Model: &sdk.Model{ID: "mock-model", Provider: provider}, Messages: []sdk.Message{input}, SupportsToolCall: true,
				Identity: SessionContext{BotID: "bot-1", SessionID: "sess-1"}, ContextMutations: ledger, StepIndexOffset: offset,
				ContextManifest:   contextfrag.Manifest{BudgetPlan: &contextfrag.ContextBudgetPlan{Window: 2600}},
				ContextProvenance: []MessageProvenance{{Message: input, Sources: rowSources("input"), Anchor: true}},
				OnStepCommitted: func(_ context.Context, index int, record *step.Record) (StepDirective, error) {
					committed = append(committed, index)
					if !slices.ContainsFunc(ledger.CacheUsageRecords(), func(usage contextfrag.CacheUsageRecord) bool { return usage.StepIndex == index }) {
						unrecorded = append(unrecorded, index)
					}
					provenance := make([]MessageProvenance, len(record.Messages))
					for i := range provenance {
						provenance[i].Sources = rowSources(fmt.Sprintf("s%d.%d", index, i))
					}
					return StepDirective{Provenance: provenance}, nil
				},
				SwitchContext: func(_ context.Context, sw ContextSwitch) (string, error) {
					sw.Fold(ContextFold{Artifact: "a1", Summary: sdk.UserMessage("<summary>\nsteps\n"), Covered: rowSources("s7.0", "s7.1", "s8.0", "s8.1")})
					return "trigger=in_run passes=1", nil
				},
			}
			if stream {
				for event := range a.Stream(context.Background(), cfg) {
					if event.Type == EventError {
						t.Fatalf("stream failed: %v", event.Cause)
					}
				}
			} else if _, err := a.Generate(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}

			want := []int{7, 8, 9, 10, 11}
			var stepIndexes, usageIndexes, cacheReads []int
			for _, snapshot := range ledger.StepSnapshots() {
				stepIndexes = append(stepIndexes, snapshot.StepIndex)
			}
			for _, usage := range ledger.CacheUsageRecords() {
				usageIndexes = append(usageIndexes, usage.StepIndex)
				cacheReads = append(cacheReads, usage.CacheReadTokens)
			}
			if !slices.Equal(committed, want) || !slices.Equal(stepIndexes, want) || !slices.Equal(usageIndexes, want) {
				t.Fatalf("committed=%v step audits=%v cache usage=%v, want every one at durable steps %v", committed, stepIndexes, usageIndexes, want)
			}
			if !slices.Equal(cacheReads, []int{100, 200, 300, 400, 500}) || len(unrecorded) != 0 {
				t.Fatalf("cache reads=%v, steps committed before their call's usage was recorded=%v", cacheReads, unrecorded)
			}
			if switches := countProviderAttemptMutations(ledger.Records(), contextfrag.MutationContextSwitch); switches != 1 {
				t.Fatalf("context switches = %d, want the event that rebuilt the dispatch", switches)
			}
		})
	}
}

func TestPrepareMidStreamRetryConfigWithMessagesAdvancesAttemptForSubsequentRecords(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{StepIndex: 0})

	cfg := RunConfig{
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		ContextMutations: ledger,
	}
	_ = prepareMidStreamRetryConfigWithMessages(cfg, cfg.Messages, nil, 0, "timeout")
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
