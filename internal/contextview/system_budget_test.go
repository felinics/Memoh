package contextview

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func TestComputeContextBudgetPlan(t *testing.T) {
	t.Parallel()

	disabled, err := ComputeContextBudgetPlan(0, 200, 100, 50)
	if err != nil {
		t.Fatalf("disabled plan error = %v", err)
	}
	if disabled != nil {
		t.Fatalf("disabled plan = %#v, want nil", disabled)
	}

	plan, err := ComputeContextBudgetPlan(1000, 200, 100, 50)
	if err != nil {
		t.Fatalf("ComputeContextBudgetPlan() error = %v", err)
	}
	want := &contextfrag.ContextBudgetPlan{
		Estimator:                    contextfrag.ProviderBudgetEstimator,
		EstimatorSafetyFactorPercent: contextfrag.ProviderBudgetSafetyFactorPercent,
		Window:                       1000,
		OutputReserve:                200,
		ToolDefsCost:                 100,
		CurrentRequestCost:           50,
		SystemBudget:                 650,
	}
	if !reflect.DeepEqual(plan, want) {
		t.Fatalf("plan = %#v, want %#v", plan, want)
	}

	unsatisfied, err := ComputeContextBudgetPlan(300, 200, 100, 50)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("reserves error = %v, want ErrBudgetUnsatisfied", err)
	}
	if unsatisfied == nil || unsatisfied.SystemBudget != MinimumSystemBudgetTokens {
		t.Fatalf("unsatisfied plan = %#v, want minimum system budget recorded", unsatisfied)
	}
}

func TestSystemBudgetNoPressurePreservesSelection(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep),
		systemBudgetTestFrag("optional", contextfrag.RetentionOptional, 20, 20, 0, ""),
		historyBudgetTestFrag("history", 30),
	}
	plan := &contextfrag.ContextBudgetPlan{Window: 1000, SystemBudget: 100}
	selector := &FragmentSelector{}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{
		MaxTokens: 1000,
		Plan:      plan,
	})

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if !reflect.DeepEqual(result.Selected, frags) {
		t.Fatalf("selected = %#v, want byte-equivalent passthrough", result.Selected)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped = %#v, want none", result.Dropped)
	}
	if plan.ActualSystemCost != 31 || plan.HistoryBudget != 69 {
		t.Fatalf("plan after selection = %#v, want actual=31 history=69", plan)
	}
	if hasFragID(result.Selected, systemBudgetMarkerID) {
		t.Fatal("no-pressure selection must not add a marker")
	}
}

func TestSystemBudgetDropsOptionalBeforePreferredInDeterministicOrder(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	frags := []contextfrag.ContextFrag{
		required,
		systemBudgetTestFrag("optional-high-drop", contextfrag.RetentionOptional, 100, 10, 2, ""),
		systemBudgetTestFrag("optional-high-render", contextfrag.RetentionOptional, 100, 90, 1, ""),
		systemBudgetTestFrag("optional-lex-b", contextfrag.RetentionOptional, 100, 80, 1, ""),
		systemBudgetTestFrag("optional-lex-a", contextfrag.RetentionOptional, 100, 80, 1, ""),
		systemBudgetTestFrag("preferred", contextfrag.RetentionPreferred, 100, 99, 99, ""),
	}
	wantDropped := []string{
		"optional-high-drop",
		"optional-high-render",
		"optional-lex-a",
		"optional-lex-b",
		"preferred",
	}
	marker := systemBudgetMarkerFrag(wantDropped, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, marker}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{Plan: plan})

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if got := droppedIDsForReason(result.Summary.DropReasons, systemBudgetDropReason); !reflect.DeepEqual(got, wantDropped) {
		t.Fatalf("system-budget drop order = %v, want %v", got, wantDropped)
	}
	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{"required", systemBudgetMarkerID}) {
		t.Fatalf("selected IDs = %v, want required + one marker", got)
	}
	if plan.ActualSystemCost != plan.SystemBudget {
		t.Fatalf("actual system cost = %d, want budget-accounted %d", plan.ActualSystemCost, plan.SystemBudget)
	}
	if plan.HistoryBudget != 0 {
		t.Fatalf("history budget = %d, want nothing left for history", plan.HistoryBudget)
	}
}

func TestSystemBudgetDropsGroupedItemsIndividually(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	header := systemBudgetGroupFrag("system.skills.header", "system.skills", "skills header", 10)
	alpha := systemBudgetGroupFrag("system.skill.alpha", "system.skills", "alpha", 100)
	unicode := systemBudgetGroupFrag("system.skill.技能", "system.skills", "技能", 100)
	marker := systemBudgetMarkerFrag([]string{alpha.ID}, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, header, unicode, marker}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(
		[]contextfrag.ContextFrag{required, header, alpha, unicode},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if got := droppedIDsForReason(result.Summary.DropReasons, systemBudgetDropReason); !reflect.DeepEqual(got, []string{alpha.ID}) {
		t.Fatalf("system-budget drops = %v, want only %s", got, alpha.ID)
	}
	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{
		required.ID,
		header.ID,
		unicode.ID,
		systemBudgetMarkerID,
	}) {
		t.Fatalf("selected IDs = %v, want header and remaining skill item", got)
	}
	payload, _ := renderSDKPayload(t, result.Selected, placementFor(result.Selected))
	want := "required\n\nskills header\n技能\n\n" + marker.Parts[0].Text
	if payload.System != want {
		t.Fatalf("rendered system = %q, want %q", payload.System, want)
	}
}

func TestSystemBudgetSweepsGroupHeaderAfterLastItemDrops(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	header := systemBudgetGroupFrag("system.skills.header", "system.skills", "skills header", 10)
	alpha := systemBudgetGroupFrag("system.skill.alpha", "system.skills", "alpha", 100)
	unicode := systemBudgetGroupFrag("system.skill.技能", "system.skills", "技能", 100)
	wantDropped := []string{alpha.ID, unicode.ID, header.ID}
	marker := systemBudgetMarkerFrag(wantDropped, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, marker}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(
		[]contextfrag.ContextFrag{required, header, alpha, unicode},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if got := droppedIDsForReason(result.Summary.DropReasons, systemBudgetDropReason); !reflect.DeepEqual(got, wantDropped) {
		t.Fatalf("system-budget drops = %v, want items followed by swept header %v", got, wantDropped)
	}
	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{required.ID, systemBudgetMarkerID}) {
		t.Fatalf("selected IDs = %v, want required + marker", got)
	}
	if got := result.Selected[1].Parts[0].Text; got != marker.Parts[0].Text {
		t.Fatalf("marker = %q, want exactly dropped IDs in %q", got, marker.Parts[0].Text)
	}
}

func TestSystemBudgetProtectedOverflowReturnsFatalError(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 100, 10, 0, "")
	kept := systemBudgetTestFrag("preferred-keep", contextfrag.RetentionPreferred, 100, 20, 0, contextfrag.OverflowKeep)
	selector := &FragmentSelector{}
	plan := &contextfrag.ContextBudgetPlan{Window: 1000, SystemBudget: 50}
	result := selector.Select(
		[]contextfrag.ContextFrag{required, kept},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Select() error = %v, want ErrProtectedContextOverflow", result.FatalError)
	}
	if len(result.Dropped) != 0 || hasFragID(result.Selected, systemBudgetMarkerID) {
		t.Fatalf("protected overflow selected=%v dropped=%v, want no drop and no marker", fragIDs(result.Selected), fragIDs(result.Dropped))
	}
	if plan.ActualSystemCost != 201 {
		t.Fatalf("actual protected cost = %d, want 201", plan.ActualSystemCost)
	}
}

func TestSystemBudgetCostIncludesEveryRenderedFragmentBoundary(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 0, 10, 0, contextfrag.OverflowKeep)
	required.Parts[0].Text = strings.Repeat("x", 1023)
	optional := systemBudgetTestFrag("optional", contextfrag.RetentionOptional, 0, 20, 0, "")
	optional.Parts[0].Text = "four"

	if got := contextfrag.ResolveFragTokens(required) + contextfrag.ResolveFragTokens(optional); got != 256 {
		t.Fatalf("per-fragment cost = %d, want floor-rounded 256", got)
	}
	if got := systemFragCost([]contextfrag.ContextFrag{required, optional}); got != 322 {
		t.Fatalf("rendered system cost = %d, want conservative provider cost 322", got)
	}
}

func TestSystemBudgetCostMatchesRenderedShortSections(t *testing.T) {
	t.Parallel()

	first := systemBudgetTestFrag("first", contextfrag.RetentionRequired, 0, 10, 0, contextfrag.OverflowKeep)
	first.Parts[0].Text = "aaa"
	second := systemBudgetTestFrag("second", contextfrag.RetentionRequired, 0, 20, 0, contextfrag.OverflowKeep)
	second.Parts[0].Text = "bbb"

	rendered := "aaa\n\nbbb"
	const want = 2
	if got := systemFragCost([]contextfrag.ContextFrag{first, second}); got != want {
		t.Fatalf("rendered system cost = %d, want %d for %q", got, want, rendered)
	}
}

func TestSystemBudgetCostUsesRendererPriorityOrderForGroups(t *testing.T) {
	t.Parallel()

	grouped := func(id string) contextfrag.ContextFrag {
		frag := systemBudgetGroupFrag(id, "system.skills", "xxx", 0)
		frag.Priority = 60
		return frag
	}
	ungrouped := func(id string) contextfrag.ContextFrag {
		frag := systemBudgetTestFrag(id, contextfrag.RetentionRequired, 0, 50, 0, contextfrag.OverflowKeep)
		frag.Parts[0].Text = "xxx"
		return frag
	}
	frags := []contextfrag.ContextFrag{
		grouped("system.skills.header"),
		ungrouped("unrelated.1"),
		grouped("system.skill.1"),
		ungrouped("unrelated.2"),
		grouped("system.skill.2"),
		ungrouped("unrelated.3"),
		grouped("system.skill.3"),
		grouped("system.skill.4"),
	}

	renderedFrags := sortSystemFragsByPriority(frags)
	payload, _ := renderSDKPayload(t, renderedFrags, placementFor(renderedFrags))
	const want = 11
	if len(payload.System) != 34 {
		t.Fatalf("fixture rendered bytes = %d, want 34", len(payload.System))
	}
	if got := systemFragCost(frags); got != want {
		t.Fatalf("system cost = %d, want renderer-ordered cost %d for %q", got, want, payload.System)
	}
}

func TestSystemBudgetRecomputesCostAfterEveryDrop(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	optionalFirst := systemBudgetTestFrag("optional-first", contextfrag.RetentionOptional, 100, 20, 2, "")
	optionalSecond := systemBudgetTestFrag("optional-second", contextfrag.RetentionOptional, 100, 20, 1, "")
	marker := systemBudgetMarkerFrag([]string{"optional-first", "optional-second"}, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, marker}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(
		[]contextfrag.ContextFrag{required, optionalFirst, optionalSecond},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if got := droppedIDsForReason(result.Summary.DropReasons, systemBudgetDropReason); !reflect.DeepEqual(got, []string{"optional-first", "optional-second"}) {
		t.Fatalf("system-budget drop order = %v, want both optional sections", got)
	}
	if plan.ActualSystemCost != plan.SystemBudget {
		t.Fatalf("actual system cost = %d, want exact-fit budget %d", plan.ActualSystemCost, plan.SystemBudget)
	}
}

func TestSystemBudgetRequiredMarkerOverflowIsProtected(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	optional := systemBudgetTestFrag("optional", contextfrag.RetentionOptional, 100, 20, 0, "")
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required}),
	}
	selector := &FragmentSelector{}
	result := selector.Select(
		[]contextfrag.ContextFrag{required, optional},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Select() error = %v, want ErrProtectedContextOverflow", result.FatalError)
	}
	if got := fragIDs(result.Dropped); !reflect.DeepEqual(got, []string{"optional"}) {
		t.Fatalf("dropped = %v, want optional section", got)
	}
	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{"required", systemBudgetMarkerID}) {
		t.Fatalf("selected = %v, want required section and required marker", got)
	}
	if plan.ActualSystemCost <= plan.SystemBudget {
		t.Fatalf("actual system cost = %d, want marker overflow above budget %d", plan.ActualSystemCost, plan.SystemBudget)
	}
}

func TestSystemBudgetActualCostShrinksHistoryBudgetAndChargesProtectedHistory(t *testing.T) {
	t.Parallel()

	frags := []contextfrag.ContextFrag{
		systemBudgetTestFrag("system", contextfrag.RetentionRequired, 150, 10, 0, contextfrag.OverflowKeep),
		historyBudgetTestFrag("old", 70),
		historyBudgetTestFrag("new", 40),
	}
	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)

	withoutPlan := selector.Select(frags, profile, BudgetEnvelope{MaxTokens: 200})
	if len(withoutPlan.Dropped) != 0 {
		t.Fatalf("plan-disabled selection dropped %v, want none", fragIDs(withoutPlan.Dropped))
	}

	noticeCost := contextfrag.ResolveProviderBudgetFragTokens(TrimNoticeFrag(contextfrag.Scope{}))
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: 150 + 40 + noticeCost + 1,
	}
	withPlan := selector.Select(frags, profile, BudgetEnvelope{
		MaxTokens:           200,
		RecentProtectTokens: 0,
		Plan:                plan,
	})
	if withPlan.FatalError != nil {
		t.Fatalf("Select() error = %v", withPlan.FatalError)
	}
	if got := fragIDs(withPlan.Dropped); !reflect.DeepEqual(got, []string{"old"}) {
		t.Fatalf("dropped = %v, want old history after protected history and notice consume the allowance", got)
	}
	if plan.HistoryBudget != 40+noticeCost+1 {
		t.Fatalf("history budget = %d, want protected history plus notice and one token %d", plan.HistoryBudget, 40+noticeCost+1)
	}
}

func TestHistoryBudgetProtectedOverflowReturnsFatalError(t *testing.T) {
	t.Parallel()

	protected := historyBudgetTestFrag("latest", 40)
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: 39,
	}
	selector := &FragmentSelector{}
	result := selector.Select(
		[]contextfrag.ContextFrag{protected},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if !errors.Is(result.FatalError, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Select() error = %v, want ErrProtectedContextOverflow", result.FatalError)
	}
	if got := fragIDs(result.Selected); !reflect.DeepEqual(got, []string{"latest"}) {
		t.Fatalf("selected = %v, want protected history retained for audit", got)
	}
	if len(result.Dropped) != 0 {
		t.Fatalf("dropped = %v, want no protected history drop", fragIDs(result.Dropped))
	}
}

func TestBuilderReturnsBudgetErrorWithContentLightAudit(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag("required", contextfrag.RetentionRequired, 10, 10, 0, contextfrag.OverflowKeep)
	optional := systemBudgetTestFrag("optional", contextfrag.RetentionOptional, 100, 20, 0, "")
	marker := systemBudgetMarkerFrag([]string{"optional"}, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, marker}),
	}
	builder := NewBuilder(
		NewMapCollectorRegistry(StaticCollector{CollectorName: sourceFragsCollectorName, Frags: []contextfrag.ContextFrag{required, optional}}),
		&FragmentSelector{},
		IdentityPlacer{},
		nil,
	)
	view, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: sourceFragsCollectorName}},
		Budget:  BudgetEnvelope{Plan: plan},
		Options: BuildOptions{DryRun: true},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if view.Manifest.BudgetPlan == nil || view.Manifest.BudgetPlan.ActualSystemCost != plan.SystemBudget {
		t.Fatalf("manifest plan = %#v, want actual system cost %d", view.Manifest.BudgetPlan, plan.SystemBudget)
	}
	optionalDecision, ok := decisionByID(view.Manifest.SelectionDecisions, "optional")
	if !ok || optionalDecision.Decision != contextfrag.DecisionDropped || optionalDecision.Reason != systemBudgetDropReason {
		t.Fatalf("optional decision = %#v, want dropped/system_budget", optionalDecision)
	}
	markerDecision, ok := decisionByID(view.Manifest.SelectionDecisions, systemBudgetMarkerID)
	if !ok || markerDecision.Decision != contextfrag.DecisionSelected {
		t.Fatalf("marker decision = %#v, want selected", markerDecision)
	}

	overflowPlan := &contextfrag.ContextBudgetPlan{Window: 1000, SystemBudget: 1}
	overflowView, err := builder.Build(context.Background(), BuildInput{
		Intent:  contextfrag.IntentRunConfigPreProvider,
		Sources: []SourceSpec{{Name: sourceFragsCollectorName}},
		Budget:  BudgetEnvelope{Plan: overflowPlan},
		Options: BuildOptions{DryRun: true},
	})
	if !errors.Is(err, contextfrag.ErrProtectedContextOverflow) {
		t.Fatalf("Build() error = %v, want ErrProtectedContextOverflow", err)
	}
	if overflowView == nil || overflowView.Manifest.BudgetPlan == nil {
		t.Fatalf("overflow view = %#v, want partial audit view", overflowView)
	}
}

func systemBudgetTestFrag(
	id string,
	tier contextfrag.RetentionTier,
	tokens, priority int,
	dropPriority contextfrag.DropPriority,
	overflow contextfrag.OverflowAction,
) contextfrag.ContextFrag {
	return contextfrag.ContextFrag{
		ID:            id,
		Kind:          contextfrag.KindSystemPrompt,
		Role:          sdk.MessageRoleSystem,
		Slot:          contextfrag.SlotSystem,
		Priority:      priority,
		RetentionTier: tier,
		DropPriority:  dropPriority,
		CacheClass:    contextfrag.CacheStable,
		Trust:         contextfrag.TrustSystem,
		Budget:        contextfrag.BudgetPolicy{Overflow: overflow},
		TokenEstimate: tokens,
		Parts:         []contextfrag.Part{{Type: contextfrag.PartText, Text: id}},
	}
}

func systemBudgetGroupFrag(id, groupID, text string, tokens int) contextfrag.ContextFrag {
	frag := systemBudgetTestFrag(id, contextfrag.RetentionOptional, tokens, 65, 0, "")
	frag.Parts[0].Text = text
	frag.Render = contextfrag.RenderPolicy{
		Format:      contextfrag.RenderMarkdown,
		GroupID:     groupID,
		GroupJoiner: "\n",
	}
	return frag
}

func historyBudgetTestFrag(id string, tokens int) contextfrag.ContextFrag {
	msg := sdk.UserMessage(id)
	return contextfrag.ContextFrag{
		ID:            id,
		Kind:          contextfrag.KindConversationEvent,
		Role:          sdk.MessageRoleUser,
		Slot:          contextfrag.SlotHistory,
		TokenEstimate: tokens,
		Parts:         []contextfrag.Part{{Type: contextfrag.PartSDKMessage, Message: &msg, SDKMessage: &msg}},
	}
}

func droppedIDsForReason(records []DropRecord, reason string) []string {
	var ids []string
	for _, record := range records {
		if record.Reason == reason {
			ids = append(ids, record.FragID)
		}
	}
	return ids
}
