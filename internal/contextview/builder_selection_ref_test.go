package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestSelectionDecisionsUsesContextRefWhenIDsCollide(t *testing.T) {
	first := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "same-id", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: "winner", Priority: 10,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustSystem,
		Source: "selection-test", Collector: "selection-test", ConflictKey: "same-conflict",
	})
	second := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "same-id", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: "loser", Priority: 10,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustWorkspace,
		Source: "selection-test", Collector: "selection-test", ConflictKey: "same-conflict",
	})
	sources := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{first, second})
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "selection-test", Frags: sources}),
		&FragmentSelector{},
		IdentityPlacer{},
		NewMapRendererRegistry(),
	)
	view, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: "selection-test"}},
		Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Selected) != 1 || view.Selected[0].Ref.ContentHash != sources[0].Ref.ContentHash {
		t.Fatalf("selector winner = %#v, want first ContextRef", view.Selected)
	}
	if len(view.Manifest.SelectionDecisions) != 2 {
		t.Fatalf("selection decisions = %#v, want two source decisions", view.Manifest.SelectionDecisions)
	}
	firstDecision := view.Manifest.SelectionDecisions[0]
	secondDecision := view.Manifest.SelectionDecisions[1]
	if firstDecision.Decision != contextfrag.DecisionSelected || firstDecision.Ref.ContentHash != sources[0].Ref.ContentHash {
		t.Fatalf("winner decision = %#v, want selected first ContextRef", firstDecision)
	}
	if secondDecision.Decision != contextfrag.DecisionDropped || secondDecision.Ref.ContentHash != sources[1].Ref.ContentHash {
		t.Fatalf("loser decision = %#v, want dropped second ContextRef", secondDecision)
	}
}

func TestSelectionDecisionsAttributeTrimmedThenDroppedFragByID(t *testing.T) {
	required := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "required", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: "required", Priority: 10,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustSystem,
		Source: "selection-test", Collector: "selection-test",
		RetentionTier: contextfrag.RetentionRequired,
	})
	oversized := contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: "oversized-optional", Kind: contextfrag.KindSystemPrompt, Role: sdk.MessageRoleSystem,
		Slot: contextfrag.SlotSystem, Text: strings.Repeat("budget pressure ", 200), Priority: 9,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustSystem,
		Source: "selection-test", Collector: "selection-test",
		RetentionTier: contextfrag.RetentionOptional,
	})
	oversized.Budget = contextfrag.BudgetPolicy{MaxTokens: 500, Overflow: contextfrag.OverflowTrim}
	sources := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{required, oversized})
	marker := systemBudgetMarkerFrag([]string{oversized.ID}, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{sources[0], marker}),
	}
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "selection-test", Frags: sources}),
		&FragmentSelector{},
		IdentityPlacer{},
		NewMapRendererRegistry(),
	)
	view, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: "selection-test"}},
		Budget:  BudgetEnvelope{Plan: plan},
		Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	var dropped *contextfrag.SelectionDecision
	for i := range view.Manifest.SelectionDecisions {
		if view.Manifest.SelectionDecisions[i].ID == oversized.ID {
			dropped = &view.Manifest.SelectionDecisions[i]
			break
		}
	}
	if dropped == nil {
		t.Fatalf("selection decisions = %#v, want an entry for %s", view.Manifest.SelectionDecisions, oversized.ID)
	}
	if dropped.Decision != contextfrag.DecisionDropped || dropped.Reason != systemBudgetDropReason {
		t.Fatalf("decision for %s = %#v, want dropped/%s", oversized.ID, dropped, systemBudgetDropReason)
	}
	if dropped.Ref.ContentHash != sources[1].Ref.ContentHash {
		t.Fatalf("dropped ref hash = %q, want source hash %q", dropped.Ref.ContentHash, sources[1].Ref.ContentHash)
	}
}
