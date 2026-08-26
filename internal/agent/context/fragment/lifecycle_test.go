package contextfrag_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
	"github.com/memohai/memoh/internal/agent/runtime/native"
)

func TestLifecycleHolderKeepsDurableContentLightBudgetAudit(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	if _, ok := holder.Snapshot(); ok {
		t.Fatal("empty holder unexpectedly exposed a snapshot")
	}
	ledger := contextfrag.NewMutationLedger()
	ledger.Record(contextfrag.MutationContextBudgetFailure, "protected_context_overflow")
	plan := contextfrag.ContextBudgetPlan{Window: 1024, SystemBudget: 256, ActualSystemCost: 930}
	holder.SetManifest(contextfrag.Manifest{
		View:               contextfrag.ViewRunConfigPreProvider,
		Counts:             contextfrag.ManifestCounts{Fragments: 2, Messages: 1, TextBytes: 2048},
		Items:              []contextfrag.ManifestItem{{ID: "private-content-marker"}},
		Selection:          &contextfrag.SelectionTrace{Selected: 1, Dropped: 1, DropReasons: map[string]int{"system_budget": 1}},
		SelectionDecisions: []contextfrag.SelectionDecision{{ID: "system.optional", Decision: contextfrag.DecisionDropped, Reason: "system_budget"}},
		BudgetPlan:         &plan,
		Mutations:          ledger,
	})
	holder.SetAssistantMessageID(" assistant-message-1 ")

	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.BudgetPlan == nil || snapshot.BudgetPlan.ActualSystemCost != 930 {
		t.Fatalf("snapshot = %#v, ok = %v", snapshot, ok)
	}
	if snapshot.AssistantMessageID != "assistant-message-1" {
		t.Fatalf("assistant message ID = %q", snapshot.AssistantMessageID)
	}
	if snapshot.Selection.DropReasons["system_budget"] != 1 || len(snapshot.SelectionDecisions) != 1 {
		t.Fatalf("selection audit = %#v / %#v", snapshot.Selection, snapshot.SelectionDecisions)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "private-content-marker") || strings.Contains(string(raw), `"items"`) {
		t.Fatalf("content-light snapshot leaked manifest items: %s", raw)
	}

	ledger.SetFinalInputHash("final-hash")
	refreshed, _ := holder.Snapshot()
	if refreshed.FinalInputHash != "final-hash" {
		t.Fatalf("live final hash = %q", refreshed.FinalInputHash)
	}
	refreshed.Selection.DropReasons["system_budget"] = 99
	again, _ := holder.Snapshot()
	if again.Selection.DropReasons["system_budget"] != 1 {
		t.Fatal("Snapshot exposed mutable holder state")
	}
}

func TestRefreshContextFragUpdatesLifecycleHolder(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	cfg := native.RunConfig{
		System:           "system prompt",
		Messages:         []sdk.Message{sdk.UserMessage("hello")},
		ContextLifecycle: holder,
	}.RefreshContextFrag()

	snapshot, ok := holder.Snapshot()
	if !ok {
		t.Fatal("RefreshContextFrag did not update the lifecycle holder")
	}
	if snapshot.View != cfg.ContextManifest.View || snapshot.Counts != cfg.ContextManifest.Counts {
		t.Fatalf("snapshot = %#v, manifest = %#v", snapshot, cfg.ContextManifest)
	}
}

func TestLifecycleSnapshotIncludesAttemptAudit(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	ledger.SetModelInfo("claude-x", "anthropic-messages")
	ledger.SetLoopSelectionMode(contextfrag.LoopSelectionSuffixOnly)
	ledger.AppendStepSnapshot(contextfrag.StepSnapshot{StepIndex: 0, PostPrepareInputHash: "step-hash-0"})
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{
		StepIndex:        0,
		CacheReadTokens:  11,
		CacheWriteTokens: 7,
	})
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider, Mutations: ledger})

	snapshot, ok := holder.Snapshot()
	if !ok {
		t.Fatal("snapshot should be available")
	}
	if snapshot.Model != "claude-x" || snapshot.ClientType != "anthropic-messages" {
		t.Fatalf("snapshot model/client_type = (%q, %q)", snapshot.Model, snapshot.ClientType)
	}
	if snapshot.LoopSelectionMode != contextfrag.LoopSelectionSuffixOnly {
		t.Fatalf("snapshot loop selection mode = %q", snapshot.LoopSelectionMode)
	}
	if len(snapshot.Steps) != 1 || snapshot.Steps[0].PostPrepareInputHash != "step-hash-0" {
		t.Fatalf("snapshot steps = %#v", snapshot.Steps)
	}
	if snapshot.CacheReadTokens != 11 || snapshot.CacheWriteTokens != 7 || len(snapshot.CacheUsage) != 1 {
		t.Fatalf("snapshot cache usage = %#v, read/write = %d/%d", snapshot.CacheUsage, snapshot.CacheReadTokens, snapshot.CacheWriteTokens)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	for _, want := range []string{
		`"model":"claude-x"`, `"client_type":"anthropic-messages"`,
		`"loop_selection_mode":"suffix_only"`, `"steps":`, "step-hash-0",
		`"step_index":0`, `"cache_read_tokens":11`, `"cache_write_tokens":7`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("snapshot JSON missing %q: %s", want, raw)
		}
	}
}

func TestLifecycleHolderSnapshotOwnsStepDropReasons(t *testing.T) {
	t.Parallel()

	ledger := contextfrag.NewMutationLedger()
	ledger.AppendStepSnapshot(contextfrag.StepSnapshot{
		StepIndex:   0,
		DropReasons: map[string]int{"budget": 1},
	})
	holder := contextfrag.NewLifecycleHolder()
	holder.SetManifest(contextfrag.Manifest{View: contextfrag.ViewRunConfigPreProvider, Mutations: ledger})

	first, ok := holder.Snapshot()
	if !ok || len(first.Steps) != 1 {
		t.Fatalf("snapshot = %#v, ok = %v", first, ok)
	}
	first.Steps[0].DropReasons["budget"] = 2

	second, _ := holder.Snapshot()
	if second.Steps[0].DropReasons["budget"] != 1 {
		t.Fatalf("snapshot exposed mutable step audit: %#v", second.Steps[0].DropReasons)
	}
}

func TestLifecycleHolderKeepsRichContentLightSnapshot(t *testing.T) {
	t.Parallel()

	breakdown := []contextfrag.KindBreakdown{{
		Kind: contextfrag.KindConversationEvent, Fragments: 2, TokenEstimate: 96,
	}}
	trustBreakdown := []contextfrag.TrustBreakdown{{
		Trust: contextfrag.TrustExternal, Fragments: 2, TokenEstimate: 96,
	}}
	toolDefs := []contextfrag.ToolDefAccounting{{
		Provider: "mcp", Name: "jira_search", Bytes: 400, TokenEstimate: 100,
	}}
	decisions := []contextfrag.SelectionDecision{{
		ID: "history.message-1", Decision: contextfrag.DecisionSelected, TokenEstimate: 48,
	}}
	budgetPlan := &contextfrag.ContextBudgetPlan{
		Estimator:                    contextfrag.ProviderBudgetEstimator,
		EstimatorSafetyFactorPercent: contextfrag.ProviderBudgetSafetyFactorPercent,
		Window:                       8192,
		OutputReserve:                1024,
		ToolDefsCost:                 100,
		CurrentRequestCost:           32,
		SystemBudget:                 2048,
		ActualSystemCost:             1536,
		HistoryBudget:                4988,
	}
	cachePlan := &contextfrag.CachePlan{
		StablePrefixHash:          "stable-prefix",
		StableMessageCount:        3,
		StablePrefixTokenEstimate: 1800,
		MidStableMessageCount:     2,
	}
	ledger := contextfrag.NewMutationLedger()
	ledger.SetModelInfo("claude-x", "anthropic-messages")
	ledger.SetLoopSelectionMode(contextfrag.LoopSelectionSuffixOnly)

	holder := contextfrag.NewLifecycleHolder()
	holder.SetAssistantMessageID(" assistant-1 ")
	holder.SetManifest(contextfrag.Manifest{
		View:               contextfrag.ViewRunConfigPreProvider,
		Counts:             contextfrag.ManifestCounts{Fragments: 2, Messages: 2, TokenEstimate: 96},
		Breakdown:          breakdown,
		TrustBreakdown:     trustBreakdown,
		ToolDefs:           toolDefs,
		SelectionDecisions: decisions,
		BudgetPlan:         budgetPlan,
		CachePlan:          cachePlan,
		Mutations:          ledger,
		Items:              []contextfrag.ManifestItem{{ID: "private-prompt-sentinel"}},
	})

	breakdown[0].TokenEstimate = -1
	trustBreakdown[0].TokenEstimate = -1
	toolDefs[0].Name = "mutated-tool"
	decisions[0].ID = "mutated-decision"
	budgetPlan.Estimator = "mutated-estimator"
	cachePlan.StablePrefixHash = "mutated-prefix"

	ledger.Record(contextfrag.MutationMidTaskPrune, "pruned=1")
	ledger.RecordCacheUsage(contextfrag.CacheUsageRecord{StepIndex: 0, CacheReadTokens: 11, CacheWriteTokens: 7})
	ledger.AppendStepSnapshot(contextfrag.StepSnapshot{
		StepIndex: 0, PostPrepareInputHash: "step-hash", DropReasons: map[string]int{"budget": 1},
	})

	snapshot, ok := holder.Snapshot()
	if !ok {
		t.Fatal("snapshot should be available")
	}
	if snapshot.AssistantMessageID != "assistant-1" {
		t.Fatalf("assistant message ID = %q", snapshot.AssistantMessageID)
	}
	if snapshot.Breakdown[0].TokenEstimate != 96 || snapshot.TrustBreakdown[0].TokenEstimate != 96 || snapshot.ToolDefs[0].Name != "jira_search" {
		t.Fatalf("composition audit retained caller aliases: %#v / %#v / %#v", snapshot.Breakdown, snapshot.TrustBreakdown, snapshot.ToolDefs)
	}
	if snapshot.SelectionDecisions[0].ID != "history.message-1" {
		t.Fatalf("selection decisions retained caller alias: %#v", snapshot.SelectionDecisions)
	}
	if snapshot.BudgetPlan == nil || snapshot.BudgetPlan.Estimator != contextfrag.ProviderBudgetEstimator ||
		snapshot.BudgetPlan.EstimatorSafetyFactorPercent != contextfrag.ProviderBudgetSafetyFactorPercent {
		t.Fatalf("budget estimator metadata = %#v", snapshot.BudgetPlan)
	}
	if snapshot.StablePrefixHash != "stable-prefix" || snapshot.StableMessageCount != 3 || snapshot.StablePrefixTokenEstimate != 1800 {
		t.Fatalf("flattened cache plan = (%q, %d, %d)", snapshot.StablePrefixHash, snapshot.StableMessageCount, snapshot.StablePrefixTokenEstimate)
	}
	if snapshot.CacheReadTokens != 11 || snapshot.CacheWriteTokens != 7 || len(snapshot.CacheUsage) != 1 {
		t.Fatalf("live cache audit = %#v, totals = %d/%d", snapshot.CacheUsage, snapshot.CacheReadTokens, snapshot.CacheWriteTokens)
	}
	if snapshot.Model != "claude-x" || snapshot.ClientType != "anthropic-messages" || snapshot.LoopSelectionMode != contextfrag.LoopSelectionSuffixOnly {
		t.Fatalf("live provider audit = (%q, %q, %q)", snapshot.Model, snapshot.ClientType, snapshot.LoopSelectionMode)
	}
	if len(snapshot.Steps) != 1 || snapshot.Steps[0].DropReasons["budget"] != 1 {
		t.Fatalf("live step audit = %#v", snapshot.Steps)
	}
	snapshot.Steps[0].DropReasons["budget"] = 99
	again, _ := holder.Snapshot()
	if again.Steps[0].DropReasons["budget"] != 1 {
		t.Fatalf("snapshot exposed mutable step audit: %#v", again.Steps)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-prompt-sentinel", `"items"`, `"cache_plan"`, `"mid_stable_message_count"`} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("content-light snapshot leaked %q: %s", forbidden, raw)
		}
	}
	for _, want := range []string{`"stable_prefix_hash":"stable-prefix"`, `"stable_message_count":3`, `"stable_prefix_token_estimate":1800`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("snapshot JSON missing %q: %s", want, raw)
		}
	}

	emptyReasons := map[string]int{}
	emptyHolder := contextfrag.NewLifecycleHolder()
	emptyHolder.SetManifest(contextfrag.Manifest{Selection: &contextfrag.SelectionTrace{DropReasons: emptyReasons}})
	emptyReasons["added-after-set"] = 1
	emptySnapshot, ok := emptyHolder.Snapshot()
	if !ok {
		t.Fatal("empty-map snapshot should be available")
	}
	if _, exists := emptySnapshot.Selection.DropReasons["added-after-set"]; exists {
		t.Fatalf("snapshot retained aliased empty map: %#v", emptySnapshot.Selection.DropReasons)
	}
}

func TestLifecycleHolderPreservesBoundedMemoryRecallTrace(t *testing.T) {
	t.Parallel()

	const maxRefs = 32
	refs := make([]string, 0, maxRefs+4)
	refs = append(refs, "", " memory-0 ", "memory-0")
	for i := 1; i <= maxRefs+1; i++ {
		refs = append(refs, "memory-"+strconv.Itoa(i))
	}
	trace := contextfrag.MemoryRecallTrace{
		ProviderID:     "provider-1",
		MemoryVersion:  "version-7",
		CacheState:     "stale",
		RetrievalMode:  "graph",
		FallbackReason: "timeout",
		Query: contextfrag.MemoryRecallQueryTrace{
			Source: "current_plus_recent_user_messages", RecentMessages: 4, Truncated: true,
		},
		Result: contextfrag.MemoryRecallResultTrace{
			Count: 40, Refs: refs, ContextBytes: 1800,
		},
	}
	holder := contextfrag.NewLifecycleHolder()
	holder.SetMemoryRecall(trace)
	refs[1] = "mutated-after-set"
	holder.SetAssistantMessageID(" assistant-1 ")
	holder.SetManifest(contextfrag.Manifest{Counts: contextfrag.ManifestCounts{Fragments: 1}})
	holder.SetManifest(contextfrag.Manifest{Counts: contextfrag.ManifestCounts{Fragments: 2}})

	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.MemoryRecall == nil {
		t.Fatalf("snapshot = %#v ok=%v, want memory recall trace", snapshot, ok)
	}
	got := snapshot.MemoryRecall
	if got.ProviderID != "provider-1" || got.MemoryVersion != "version-7" || got.CacheState != "stale" {
		t.Fatalf("memory recall trace = %#v", got)
	}
	if got.Result.Count != 40 || len(got.Result.Refs) != maxRefs {
		t.Fatalf("result trace = %#v, want full count and %d bounded refs", got.Result, maxRefs)
	}
	if got.Result.Refs[0] != "memory-0" || got.Result.Refs[1] != "memory-1" {
		t.Fatalf("refs were not normalized and copied: %#v", got.Result.Refs)
	}
	if snapshot.AssistantMessageID != "assistant-1" || snapshot.Counts.Fragments != 2 {
		t.Fatalf("SetManifest lost durable associations: %#v", snapshot)
	}
	got.Result.Refs[0] = "mutated-after-snapshot"
	again, _ := holder.Snapshot()
	if again.MemoryRecall.Result.Refs[0] != "memory-0" {
		t.Fatalf("snapshot refs alias holder state: %#v", again.MemoryRecall.Result.Refs)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"raw query sentinel", "raw memory body sentinel"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("lifecycle JSON leaked %q: %s", forbidden, raw)
		}
	}
}

func TestLifecycleHolderSnapshotAvailableWithMemoryRecallOnly(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	holder.SetMemoryRecall(contextfrag.MemoryRecallTrace{ProviderID: "provider-1", CacheState: "miss"})

	snapshot, ok := holder.Snapshot()
	if !ok || snapshot.Version != contextfrag.LifecycleSnapshotVersion || snapshot.MemoryRecall == nil || snapshot.MemoryRecall.ProviderID != "provider-1" {
		t.Fatalf("snapshot = %#v ok=%v, want versioned memory-only lifecycle", snapshot, ok)
	}
}

func TestLifecycleHolderSnapshotIsContentLight(t *testing.T) {
	t.Parallel()

	holder := contextfrag.NewLifecycleHolder()
	if _, ok := holder.Snapshot(); ok {
		t.Fatal("empty holder unexpectedly exposed a snapshot")
	}
	holder.SetManifest(contextfrag.Manifest{
		View: contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{
			Fragments: 4,
			Messages:  2,
			Images:    1,
			TextBytes: 512,
		},
		Items: []contextfrag.ManifestItem{{ID: "private-content-marker"}},
	})
	holder.SetAssistantMessageID(" assistant-message-1 ")

	snapshot, ok := holder.Snapshot()
	if !ok {
		t.Fatal("expected snapshot after SetManifest")
	}
	if snapshot.Version != contextfrag.LifecycleSnapshotVersion || snapshot.View != contextfrag.ViewRunConfigPreProvider {
		t.Fatalf("snapshot identity = (%d, %q), want (1, %q)", snapshot.Version, snapshot.View, contextfrag.ViewRunConfigPreProvider)
	}
	if snapshot.Counts != (contextfrag.ManifestCounts{Fragments: 4, Messages: 2, Images: 1, TextBytes: 512}) {
		t.Fatalf("snapshot counts = %#v", snapshot.Counts)
	}
	if snapshot.AssistantMessageID != "assistant-message-1" {
		t.Fatalf("assistant message ID = %q, want trimmed association", snapshot.AssistantMessageID)
	}

	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(raw), "private-content-marker") || strings.Contains(string(raw), `"items"`) {
		t.Fatalf("content-light snapshot leaked manifest items: %s", raw)
	}
}

func TestDecodeLifecycleSnapshotMapsVersionOneCachePlan(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
		"version": 1,
		"view": "run_config_pre_provider",
		"counts": {"fragments": 2, "messages": 1},
		"cache_plan": {
			"stable_prefix_hash": "hash-v1",
			"stable_message_count": 3,
			"mid_stable_message_count": 1,
			"stable_prefix_token_estimate": 512
		}
	}`)
	snapshot, err := contextfrag.DecodeLifecycleSnapshot(raw)
	if err != nil {
		t.Fatalf("DecodeLifecycleSnapshot() error = %v", err)
	}
	if snapshot.Version != contextfrag.LifecycleSnapshotVersion {
		t.Fatalf("version = %d, want normalized %d", snapshot.Version, contextfrag.LifecycleSnapshotVersion)
	}
	if snapshot.StablePrefixHash != "hash-v1" ||
		snapshot.StableMessageCount != 3 ||
		snapshot.StablePrefixTokenEstimate != 512 {
		t.Fatalf("snapshot = %#v, want version-1 cache_plan mapped onto flattened fields", snapshot)
	}
}
