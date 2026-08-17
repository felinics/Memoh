package contextview

import (
	"context"
	"reflect"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	agentpkg "github.com/memohai/memoh/internal/agent/runtime/native"
)

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
