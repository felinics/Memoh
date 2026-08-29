package contextfrag_test

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/runtime/native"
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
