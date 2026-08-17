package contextfrag_test

import (
	"encoding/json"
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
