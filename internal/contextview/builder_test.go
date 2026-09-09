package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestBuildEndToEndPassthrough(t *testing.T) {
	t.Parallel()

	frags := testFrags()
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "static", Frags: frags}),
		PassthroughSelector{},
		IdentityPlacer{},
		NewMapRendererRegistry(NoopRenderer{TargetName: contextfrag.RenderSDKMessages}),
	)

	got, err := builder.Build(context.Background(), BuildInput{
		Scope:   contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"},
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: "static"}},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
	})
	if err != nil {
		t.Fatalf("Build returned error: %v", err)
	}
	if len(got.SourceFrags) != 3 || len(got.Selected) != 3 || got.Manifest.Counts.Fragments != 3 {
		t.Fatalf("unexpected view counts: source=%d selected=%d manifest=%d", len(got.SourceFrags), len(got.Selected), got.Manifest.Counts.Fragments)
	}
	if got.Manifest.View != contextfrag.ViewRunConfigPreProvider {
		t.Fatalf("manifest view = %q", got.Manifest.View)
	}
	if _, ok := got.Rendered[contextfrag.RenderSDKMessages]; !ok {
		t.Fatal("SDK render target missing")
	}
	if got.Trace.SelectionSummary.TotalCollected != 3 || got.Trace.SelectionSummary.TotalSelected != 3 {
		t.Fatalf("selection trace = %#v", got.Trace.SelectionSummary)
	}
}

func TestBuildDryRunSkipsRender(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "static", Frags: testFrags()[:1]}),
		PassthroughSelector{},
		IdentityPlacer{},
		NewMapRendererRegistry(),
	)
	got, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: "static"}},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
		Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Rendered) != 0 || len(got.Trace.RenderSummaries) != 0 {
		t.Fatalf("dry run rendered output: %#v", got.Rendered)
	}
}

func TestBuildRejectsUnknownCollectorAndRenderer(t *testing.T) {
	t.Parallel()

	builder := NewBuilder(NewMapCollectorRegistry(), PassthroughSelector{}, IdentityPlacer{}, NewMapRendererRegistry())
	_, err := builder.Build(context.Background(), BuildInput{
		Intent: contextfrag.IntentRunConfigPreProvider, Sources: []SourceSpec{{Name: "missing"}}, Options: BuildOptions{DryRun: true},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown collector "missing"`) {
		t.Fatalf("collector error = %v", err)
	}

	builder = NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "static", Frags: testFrags()[:1]}),
		PassthroughSelector{}, IdentityPlacer{}, NewMapRendererRegistry(),
	)
	_, err = builder.Build(context.Background(), BuildInput{
		Intent: contextfrag.IntentRunConfigPreProvider, Sources: []SourceSpec{{Name: "static"}}, Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
	})
	if err == nil || !strings.Contains(err.Error(), `unknown renderer "sdk_messages"`) {
		t.Fatalf("renderer error = %v", err)
	}
}

func TestBuildNormalizesCollectedContextRefs(t *testing.T) {
	t.Parallel()

	frag := textFrag("system.prompt", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "system prompt")
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "static", Frags: []contextfrag.ContextFrag{frag}}),
		PassthroughSelector{}, IdentityPlacer{}, NewMapRendererRegistry(),
	)
	view, err := builder.Build(context.Background(), BuildInput{
		Intent: contextfrag.IntentRunConfigPreProvider, Sources: []SourceSpec{{Name: "static"}}, Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if view.SourceFrags[0].Ref.ID == "" || view.Selected[0].Ref.ContentHash == "" {
		t.Fatalf("refs were not normalized: %#v", view.Selected[0].Ref)
	}
	if view.Placement.Items[0].Ref.Schema != contextfrag.SchemaContextRef {
		t.Fatalf("placement ref = %#v", view.Placement.Items[0].Ref)
	}
}

type droppingSelector struct{}

func (droppingSelector) ProfileFor(intent contextfrag.Intent) IntentProfile {
	return IntentProfile{Intent: intent}
}

func (droppingSelector) Select(frags []contextfrag.ContextFrag, _ IntentProfile, _ BudgetEnvelope) SelectionResult {
	return SelectionResult{
		Selected: frags[1:],
		Dropped:  frags[:1],
		Summary: SelectionSummary{
			TotalCollected: len(frags), TotalSelected: len(frags) - 1, TotalDropped: 1,
			DropReasons: []DropRecord{{FragID: frags[0].ID, Ref: frags[0].Ref, Reason: "fixture"}},
		},
	}
}

func TestBuildRecordsDroppedFragmentEditTrace(t *testing.T) {
	t.Parallel()

	frags := testFrags()
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: "static", Frags: frags}),
		droppingSelector{}, IdentityPlacer{}, NewMapRendererRegistry(),
	)
	got, err := builder.Build(context.Background(), BuildInput{
		Intent: contextfrag.IntentRunConfigPreProvider, Sources: []SourceSpec{{Name: "static"}}, Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Manifest.EditTrace) != 1 || got.Manifest.EditTrace[0].Op != contextfrag.EditRemove {
		t.Fatalf("edit trace = %#v", got.Manifest.EditTrace)
	}
	if len(got.Manifest.Items) != len(frags)-1 {
		t.Fatalf("manifest items = %d, want %d", len(got.Manifest.Items), len(frags)-1)
	}
	if len(got.Manifest.SelectionDecisions) != len(frags) {
		t.Fatalf("selection decisions = %#v", got.Manifest.SelectionDecisions)
	}
	for i, decision := range got.Manifest.SelectionDecisions {
		if decision.ID != frags[i].ID || decision.Ref.ID == "" {
			t.Fatalf("selection decision %d = %#v", i, decision)
		}
		wantDecision := contextfrag.DecisionSelected
		wantReason := ""
		if i == 0 {
			wantDecision = contextfrag.DecisionDropped
			wantReason = "fixture"
		}
		if decision.Decision != wantDecision || decision.Reason != wantReason {
			t.Fatalf("selection decision %d = %#v, want %q/%q", i, decision, wantDecision, wantReason)
		}
	}
}

func TestSelectionDecisionsMarksChangedSelectionTrimmed(t *testing.T) {
	t.Parallel()

	source := textFrag("same", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "changed")
	selected := textFrag("same", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "change")
	frags := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{source, selected})
	source, selected = frags[0], frags[1]
	if contextfrag.ResolveFragTokens(source) != contextfrag.ResolveFragTokens(selected) {
		t.Fatal("fixture must keep the same token estimate")
	}

	decisions := selectionDecisions([]contextfrag.ContextFrag{source}, SelectionResult{
		Selected: []contextfrag.ContextFrag{selected},
		Summary:  SelectionSummary{TotalCollected: 1, TotalSelected: 1},
	})
	if len(decisions) != 1 || decisions[0].Decision != contextfrag.DecisionTrimmed {
		t.Fatalf("selection decisions = %#v", decisions)
	}
	if decisions[0].Ref.ContentHash != selected.Ref.ContentHash || decisions[0].Ref.ContentHash == source.Ref.ContentHash {
		t.Fatalf("decision ref = %#v, source = %#v, selected = %#v", decisions[0].Ref, source.Ref, selected.Ref)
	}
	if decisions[0].TextBytes != len("change") || decisions[0].TokenEstimate != contextfrag.ResolveFragTokens(selected) {
		t.Fatalf("decision accounting = %#v", decisions[0])
	}
}

func TestBuilderPlacesLateSystemFragmentInRenderedPrefixOrder(t *testing.T) {
	t.Parallel()

	history := messageFrag("history", sdk.UserMessage("hello"))
	workspace := textFrag("workspace", contextfrag.SlotSystem, contextfrag.KindWorkspaceInstruction, sdk.MessageRoleSystem, "workspace")
	workspace.Priority = 50
	toolUsage := textFrag("tool-usage", contextfrag.SlotSystem, contextfrag.KindToolUsage, sdk.MessageRoleSystem, "tools")
	toolUsage.Priority = 45
	registry := NewMapCollectorRegistry(StaticCollector{
		CollectorName: "late-system",
		Frags:         []contextfrag.ContextFrag{workspace, history, toolUsage},
	})
	builder := NewBuilder(registry, PassthroughSelector{}, StablePrefixPlacer{}, NewMapRendererRegistry(&SDKMessagesRenderer{}))

	view, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: "late-system"}},
		Targets: []contextfrag.RenderTarget{contextfrag.RenderSDKMessages},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tool-usage", "workspace", "history"}
	for i, id := range want {
		if view.Selected[i].ID != id || view.Placement.Items[i].FragID != id {
			t.Fatalf("selected = %#v, placement = %#v", view.Selected, view.Placement.Items)
		}
	}
	payload := view.Rendered[contextfrag.RenderSDKMessages].Data.(*SDKRenderedPayload)
	if payload.System != "tools\n\nworkspace" {
		t.Fatalf("system = %q", payload.System)
	}
}

func testFrags() []contextfrag.ContextFrag {
	return []contextfrag.ContextFrag{
		textFrag("system.prompt", contextfrag.SlotSystem, contextfrag.KindSystemPrompt, sdk.MessageRoleSystem, "System guidance"),
		messageFrag("history.001", sdk.UserMessage("previous user turn")),
		textFrag("current.message", contextfrag.SlotCurrentUser, contextfrag.KindCurrentUserMessage, sdk.MessageRoleUser, "current request"),
	}
}

func textFrag(id string, slot contextfrag.Slot, kind contextfrag.Kind, role sdk.MessageRole, text string) contextfrag.ContextFrag {
	cacheClass := contextfrag.CacheStable
	if slot == contextfrag.SlotCurrentUser {
		cacheClass = contextfrag.CacheNever
	}
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID: id, Kind: kind, Role: role, Slot: slot, Text: text, Priority: 10,
		CacheClass: cacheClass, Trust: contextfrag.TrustSystem, Source: "static", Collector: "static",
	})
}

func messageFrag(id string, msg sdk.Message) contextfrag.ContextFrag {
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: id, Message: msg, Kind: contextfrag.KindConversationEvent, Slot: contextfrag.SlotHistory,
		CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustExternal, Source: "static", Collector: "static",
	})
}
