package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

// The step reselection envelope resolves the same recent-protect window as
// the provider view, passed through from the RunConfig override.
func TestProviderStepEnvelopeCarriesRecentProtectWindow(t *testing.T) {
	t.Parallel()

	got := providerStepBudgetEnvelope(agentpkg.ContextStepSelectionInput{BudgetMaxTokens: 100})
	if got.MaxTokens != 100 || got.RecentProtectTokens != DefaultRecentProtectTokens {
		t.Fatalf("default envelope = %#v, want max 100 with the default window", got)
	}
	zero := 0
	if got := providerStepBudgetEnvelope(agentpkg.ContextStepSelectionInput{BudgetMaxTokens: 100, RecentProtectTokens: &zero}); got.RecentProtectTokens != 0 {
		t.Fatalf("zero override envelope = %#v, want disabled window", got)
	}
	window := 40
	if got := providerStepBudgetEnvelope(agentpkg.ContextStepSelectionInput{BudgetMaxTokens: 100, RecentProtectTokens: &window}); got.RecentProtectTokens != 40 {
		t.Fatalf("override envelope = %#v, want window 40", got)
	}
}

func TestProviderStepReselectorPreservesPrefixAndDropsLoopSpan(t *testing.T) {
	t.Parallel()

	prefix := []sdk.Message{
		sdk.UserMessage("initial request"),
		sdk.AssistantMessage("initial answer"),
	}
	messages := append(append([]sdk.Message(nil), prefix...),
		sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "old-call",
			ToolName:   "search",
			Input:      map[string]any{"q": "old"},
		}}},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "old-call",
			ToolName:   "search",
			Result:     strings.Repeat("old ", 2048),
		}),
		sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
			ToolCallID: "new-call",
			ToolName:   "search",
			Input:      map[string]any{"q": "new"},
		}}},
		sdk.ToolMessage(sdk.ToolResultPart{
			ToolCallID: "new-call",
			ToolName:   "search",
			Result:     "new",
		}),
	)

	result := SelectProviderStepMessages(context.Background(), agentpkg.ContextStepSelectionInput{
		Scope:               contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"},
		InitialMessageCount: len(prefix),
		Messages:            messages,
		// Leave room for the newest protected tool closure and the required
		// trim notice while forcing the bulky older closure out.
		BudgetMaxTokens: 200,
	})

	if result.Dropped != 2 {
		t.Fatalf("Dropped = %d, want 2", result.Dropped)
	}
	if got := len(result.Messages); got != 5 {
		t.Fatalf("Messages len = %d, want 5", got)
	}
	for i := range prefix {
		if result.Messages[i].Role != prefix[i].Role {
			t.Fatalf("prefix role %d = %q, want %q", i, result.Messages[i].Role, prefix[i].Role)
		}
	}
	notice, ok := result.Messages[2].Content[0].(sdk.TextPart)
	if !ok || notice.Text != HistoryTrimNotice {
		t.Fatalf("trim notice = %#v, want history trim notice", result.Messages[2].Content[0])
	}
	call, ok := result.Messages[3].Content[0].(sdk.ToolCallPart)
	if !ok || call.ToolCallID != "new-call" {
		t.Fatalf("first loop message after trim notice = %#v, want new tool call", result.Messages[3].Content[0])
	}
	// The whole loop span sits inside the default recent window, so the
	// drops report the window yielding rather than the windowless tier.
	if result.DropReasons[budgetDropReasonRecentWindow] != 2 {
		t.Fatalf("DropReasons = %#v, want the droppable cause budget:recent_window:2", result.DropReasons)
	}
}

func TestApplyProviderRunConfigProducesManifestLedgerAndCachePlan(t *testing.T) {
	t.Parallel()
	ledger := contextfrag.NewMutationLedger()
	cfg := agentpkg.RunConfig{
		System: "system", Messages: []sdk.Message{sdk.UserMessage("history")}, Query: "current",
		ContextMutations:       ledger,
		ContextToolDefs:        []contextfrag.ToolDefAccounting{{Provider: "native", Name: "read", TokenEstimate: 7}},
		ContextDynamicMutators: []contextfrag.DynamicMutator{contextfrag.DynamicMutatorPromptCache},
	}
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if got.ContextMutations != ledger || got.ContextManifest.Mutations != ledger {
		t.Fatal("mutation ledger pointer was not preserved")
	}
	if got.ContextManifest.CachePlan == nil || *got.ContextManifest.CachePlan != got.ContextCachePlan {
		t.Fatalf("cache plan = %#v, field = %#v", got.ContextManifest.CachePlan, got.ContextCachePlan)
	}
	if len(got.ContextManifest.ToolDefs) != 1 || got.ContextManifest.ToolDefs[0].Name != "read" {
		t.Fatalf("tool definitions = %#v", got.ContextManifest.ToolDefs)
	}
	if !reflect.DeepEqual(got.ContextManifest.DynamicMutators, cfg.ContextDynamicMutators) {
		t.Fatalf("dynamic mutators = %#v", got.ContextManifest.DynamicMutators)
	}
	if got.ContextCachePlan.StableMessageCount != 1 {
		t.Fatalf("stable messages = %d, want history only", got.ContextCachePlan.StableMessageCount)
	}
	if got.ContextCachePlan.StablePrefixTokenEstimate <= 7 {
		t.Fatalf("stable prefix estimate = %d, want tool defs plus fragments", got.ContextCachePlan.StablePrefixTokenEstimate)
	}
}

func TestApplyProviderRunConfigMergesOnlyMatchingHistoryAuditMetadata(t *testing.T) {
	t.Parallel()
	message := sdk.UserMessage("summary bytes")
	ref := contextfrag.ContextRef{
		Namespace: "compaction_log", ID: "compact-1", Version: 1,
		Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable,
	}
	coverage := contextfrag.NewSummaryCoverage(ref, []contextfrag.ContextRef{{
		Namespace: "bot_history_message", ID: "row-1", Schema: contextfrag.SchemaContextRef, Durability: contextfrag.RefDurable,
	}})
	shadow := contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "message.000", Message: message, Kind: contextfrag.KindConversationSummary,
		Slot: contextfrag.SlotHistory, CacheClass: contextfrag.CacheNever, Trust: contextfrag.TrustSystem,
		Scope: contextfrag.Scope{CurrentMessageID: "stale"}, Source: "compaction_log", SourceID: "compact-1",
		Collector: "history_records", Budget: contextfrag.BudgetPolicy{MaxChars: 1, Overflow: contextfrag.OverflowDrop},
	})
	shadow.Ref = ref
	shadow.Coverage = &coverage
	shadow.ConflictKey = "stale-policy"
	shadow.Parts = append(shadow.Parts, contextfrag.Part{Type: contextfrag.PartText, Text: "untrusted extra part"})
	scope := contextfrag.Scope{BotID: "bot-1", CurrentMessageID: "current"}
	cfg := agentpkg.RunConfig{
		Messages: []sdk.Message{message}, ContextFrags: []contextfrag.ContextFrag{shadow},
		ContextScope: scope, ContextQueryMaterialized: true, ContextTrimmableMessages: 1,
	}
	cfg.ContextSourceFrags = CollectNonSystemProviderSourceFrags(context.Background(), cfg)

	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if !reflect.DeepEqual(got.Messages, cfg.Messages) {
		t.Fatalf("shadow policy changed provider messages: got %#v want %#v", got.Messages, cfg.Messages)
	}
	if len(got.ContextFrags) != 1 {
		t.Fatalf("provider fragments = %#v, want one history fragment", got.ContextFrags)
	}
	merged := got.ContextFrags[0]
	if merged.Kind != contextfrag.KindConversationSummary || merged.Coverage == nil || merged.Ref.ID != ref.ID {
		t.Fatalf("matching audit metadata was not preserved: %#v", merged)
	}
	if merged.Budget != (contextfrag.BudgetPolicy{}) || merged.ConflictKey != "" || merged.CacheClass != contextfrag.CacheStable ||
		merged.Trust != contextfrag.TrustExternal || merged.Scope.CurrentMessageID != scope.CurrentMessageID || len(merged.Parts) != 1 {
		t.Fatalf("shadow selection/render policy leaked into authoritative fragment: %#v", merged)
	}
}

func TestProviderRunConfigApplierUsesInjectedLoggerShape(t *testing.T) {
	t.Parallel()
	applier := ProviderRunConfigApplier(nil)
	got, err := applier(context.Background(), agentpkg.RunConfig{System: "system", Query: "query"})
	if err != nil {
		t.Fatalf("applier error = %v", err)
	}
	if got.System != "system" || len(got.Messages) != 1 {
		t.Fatalf("got = %#v", got)
	}
}

func TestProviderRunConfigApplierInstallsStepReselectorOnAssembledPayloads(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		out, err := ProviderRunConfigApplier(nil)(context.Background(), fragsFirstFixture())
		if err != nil {
			t.Fatalf("applier error = %v", err)
		}
		if out.ContextStepReselector == nil {
			t.Fatal("successful provider compilation did not install the step reselector")
		}

		prefix := []sdk.Message{sdk.UserMessage("provider-prefix")}
		messages := append(append([]sdk.Message(nil), prefix...),
			sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "old-call", ToolName: "search", Input: map[string]any{"q": "old"},
			}}},
			sdk.ToolMessage(sdk.ToolResultPart{
				ToolCallID: "old-call", ToolName: "search", Result: strings.Repeat("old ", 2048),
			}),
			sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ToolCallPart{
				ToolCallID: "new-call", ToolName: "search", Input: map[string]any{"q": "new"},
			}}},
			sdk.ToolMessage(sdk.ToolResultPart{
				ToolCallID: "new-call", ToolName: "search", Result: "new",
			}),
		)
		result := out.ContextStepReselector(context.Background(), agentpkg.ContextStepSelectionInput{
			InitialMessageCount: len(prefix), Messages: messages, BudgetMaxTokens: 200,
		})
		if result.Dropped != 2 || len(result.Messages) != 4 {
			t.Fatalf("step result = %#v, want the bulky old tool closure dropped", result)
		}
		if !reflect.DeepEqual(result.Messages[0], prefix[0]) {
			t.Fatalf("step prefix = %#v, want %#v", result.Messages[0], prefix[0])
		}
		call, ok := result.Messages[2].Content[0].(sdk.ToolCallPart)
		if !ok || call.ToolCallID != "new-call" {
			t.Fatalf("surviving tool call = %#v, want new-call", result.Messages[2].Content[0])
		}
	})

	t.Run("legacy fallback", func(t *testing.T) {
		duplicate := systemTextFrag("duplicate", "source ignored", contextfrag.KindSystemPrompt, 20)
		out, err := ProviderRunConfigApplier(nil)(context.Background(), agentpkg.RunConfig{
			ContextSourceFrags: []contextfrag.ContextFrag{duplicate, duplicate},
		})
		if err != nil {
			t.Fatalf("fallback error = %v", err)
		}
		if out.ContextStepReselector == nil {
			t.Fatal("legacy fallback is an assembly path and must keep step reselection")
		}
	})

	t.Run("protected budget failure", func(t *testing.T) {
		required := systemTextFrag("system.required", "required", contextfrag.KindSystemPrompt, 20)
		required.RetentionTier = contextfrag.RetentionRequired
		required.TokenEstimate = 930
		current := currentMessageFrag("current", "current")
		current.TokenEstimate = 10

		out, err := ProviderRunConfigApplier(nil)(context.Background(), agentpkg.RunConfig{
			ContextSourceFrags: []contextfrag.ContextFrag{required, current}, ContextBudgetMaxTokens: 400,
		})
		if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
			t.Fatalf("failure error = %v, want ErrProtectedContextOverflow", err)
		}
		if out.ContextStepReselector != nil {
			t.Fatal("protected budget failure installed the step reselector")
		}
	})
}
