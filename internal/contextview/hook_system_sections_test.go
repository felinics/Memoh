package contextview

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

func TestApplyProviderRunConfigCarriesHookSystemSectionPolicyAndWarnings(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1", SessionID: "session-1"}
	frags := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{
		hookSystemTestFrag(
			"system.prompt",
			"base system",
			contextfrag.RetentionRequired,
			contextfrag.CacheStable,
			contextfrag.TrustSystem,
			20,
			scope,
		),
		hookSystemTestFrag(
			"system.hook.policy.dynamic",
			"dynamic hook",
			contextfrag.RetentionOptional,
			contextfrag.CacheDynamic,
			contextfrag.TrustWorkspace,
			80,
			scope,
		),
		hookSystemTestFrag(
			"system.hook.policy.stable",
			"stable hook",
			contextfrag.RetentionPreferred,
			contextfrag.CacheStable,
			contextfrag.TrustWorkspace,
			80,
			scope,
		),
	})
	warning := contextfrag.ValidationWarning{
		Code:    "hook_system_section_required_clamped",
		Message: "hook system section retention was clamped from required to preferred",
		Ref:     frags[2].Ref,
	}

	got := applyProviderRunConfigOK(context.Background(), nil, agentpkg.RunConfig{
		System:                "base system",
		ContextScope:          scope,
		ContextSourceFrags:    frags,
		ContextSourceWarnings: []contextfrag.ValidationWarning{warning},
	})

	if got.System != "base system\n\ndynamic hook\n\nstable hook" {
		t.Fatalf("system = %q, want built-in followed by declared hook order", got.System)
	}
	dynamic := manifestItemByID(got.ContextManifest.Items, "system.hook.policy.dynamic")
	if dynamic == nil ||
		dynamic.Kind != contextfrag.KindHookContext ||
		dynamic.Role != sdk.MessageRoleSystem ||
		dynamic.Slot != contextfrag.SlotSystem ||
		dynamic.RetentionTier != contextfrag.RetentionOptional ||
		dynamic.CacheClass != contextfrag.CacheDynamic ||
		dynamic.Trust != contextfrag.TrustWorkspace {
		t.Fatalf("dynamic hook manifest item = %#v", dynamic)
	}
	stable := manifestItemByID(got.ContextManifest.Items, "system.hook.policy.stable")
	if stable == nil ||
		stable.RetentionTier != contextfrag.RetentionPreferred ||
		stable.CacheClass != contextfrag.CacheStable ||
		stable.Trust != contextfrag.TrustWorkspace {
		t.Fatalf("stable hook manifest item = %#v", stable)
	}

	workspace := trustBreakdownByTrust(got.ContextManifest.TrustBreakdown, contextfrag.TrustWorkspace)
	if workspace == nil || workspace.Fragments != 2 {
		t.Fatalf("workspace trust breakdown = %#v, want two hook fragments", workspace)
	}
	if !hasValidationWarning(got.ContextManifest.ValidationWarnings, warning) {
		t.Fatalf(
			"validation warnings = %#v, want source warning %#v",
			got.ContextManifest.ValidationWarnings,
			warning,
		)
	}

	placement := StablePrefixPlacer{}.Place(got.ContextFrags, contextfrag.IntentRunConfigPreProvider)
	dynamicIndex := fragIndexByID(got.ContextFrags, "system.hook.policy.dynamic")
	stableIndex := fragIndexByID(got.ContextFrags, "system.hook.policy.stable")
	if dynamicIndex < 0 || placement.FirstVolatileIndex != dynamicIndex || stableIndex <= dynamicIndex {
		t.Fatalf(
			"placement first volatile=%d dynamic=%d stable=%d; declaration order must not be regrouped",
			placement.FirstVolatileIndex,
			dynamicIndex,
			stableIndex,
		)
	}
}

func TestHookSystemSectionsDropOptionalBeforePreferredWithNotice(t *testing.T) {
	t.Parallel()

	required := systemBudgetTestFrag(
		"system.prompt",
		contextfrag.RetentionRequired,
		10,
		20,
		0,
		contextfrag.OverflowKeep,
	)
	optional := hookSystemBudgetTestFrag(
		"system.hook.policy.optional",
		contextfrag.RetentionOptional,
		100,
	)
	preferred := hookSystemBudgetTestFrag(
		"system.hook.policy.preferred",
		contextfrag.RetentionPreferred,
		100,
	)
	marker := systemBudgetMarkerFrag([]string{optional.ID}, contextfrag.Scope{})
	plan := &contextfrag.ContextBudgetPlan{
		Window:       1000,
		SystemBudget: systemFragCost([]contextfrag.ContextFrag{required, preferred, marker}),
	}
	selector := &FragmentSelector{}

	result := selector.Select(
		[]contextfrag.ContextFrag{required, optional, preferred},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan},
	)

	if result.FatalError != nil {
		t.Fatalf("Select() error = %v", result.FatalError)
	}
	if got := droppedIDsForReason(result.Summary.DropReasons, systemBudgetDropReason); len(got) != 1 || got[0] != optional.ID {
		t.Fatalf("system-budget drops = %v, want only optional hook section", got)
	}
	if !hasFragID(result.Selected, preferred.ID) {
		t.Fatalf("selected = %v, want preferred hook section retained", fragIDs(result.Selected))
	}
	if !hasFragID(result.Selected, systemBudgetMarkerID) {
		t.Fatalf("selected = %v, want required omission marker", fragIDs(result.Selected))
	}
	payload, _ := renderSDKPayload(t, result.Selected, placementFor(result.Selected))
	if !strings.Contains(payload.System, optional.ID) {
		t.Fatalf("system budget marker = %q, want omitted hook ID", payload.System)
	}
}

func TestHookSystemSectionBudgetTrimsUnicodeByRunes(t *testing.T) {
	t.Parallel()

	frag := hookSystemTestFrag(
		"system.hook.policy.unicode",
		"甲乙丙丁戊己",
		contextfrag.RetentionPreferred,
		contextfrag.CacheDynamic,
		contextfrag.TrustWorkspace,
		80,
		contextfrag.Scope{},
	)
	frag.Budget = contextfrag.BudgetPolicy{
		MaxChars: 4,
		Overflow: contextfrag.OverflowTrim,
	}
	selector := &FragmentSelector{}

	result := selector.Select(
		[]contextfrag.ContextFrag{frag},
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{},
	)

	if result.FatalError != nil || len(result.Selected) != 1 {
		t.Fatalf("Select() error=%v selected=%v", result.FatalError, fragIDs(result.Selected))
	}
	text := result.Selected[0].Parts[0].Text
	if !utf8.ValidString(text) || utf8.RuneCountInString(text) > frag.Budget.MaxChars {
		t.Fatalf("trimmed hook text = %q, want valid UTF-8 within %d runes", text, frag.Budget.MaxChars)
	}
	if len(result.Edited) != 1 || result.EditReasons[frag.ID] != "frag_budget:max_chars" {
		t.Fatalf("edits=%#v reasons=%#v, want max-chars trim audit", result.Edited, result.EditReasons)
	}
}

func TestProviderBudgetAuditCarriesHookSystemSourceWarnings(t *testing.T) {
	t.Parallel()

	warning := contextfrag.ValidationWarning{Code: "hook_warning"}
	got := providerBudgetAuditConfig(
		agentpkg.RunConfig{
			ContextSourceWarnings: []contextfrag.ValidationWarning{warning},
		},
		nil,
		contextfrag.NewMutationLedger(),
		nil,
	)

	if !hasValidationWarning(got.ContextManifest.ValidationWarnings, warning) {
		t.Fatalf("budget-audit warnings = %#v, want %#v", got.ContextManifest.ValidationWarnings, warning)
	}
}

func TestApplyProviderRunConfigRebindsHookWarningAfterTrim(t *testing.T) {
	t.Parallel()

	scope := contextfrag.Scope{BotID: "bot-1"}
	base := hookSystemTestFrag(
		"system.prompt",
		"base",
		contextfrag.RetentionRequired,
		contextfrag.CacheStable,
		contextfrag.TrustSystem,
		20,
		scope,
	)
	hook := hookSystemTestFrag(
		"system.hook.policy.trimmed",
		"甲乙丙丁戊己",
		contextfrag.RetentionPreferred,
		contextfrag.CacheDynamic,
		contextfrag.TrustWorkspace,
		80,
		scope,
	)
	hook.Budget.MaxChars = 4
	frags := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{base, hook})
	sourceRef := frags[1].Ref
	got := applyProviderRunConfigOK(context.Background(), nil, agentpkg.RunConfig{
		System:             "base",
		ContextScope:       scope,
		ContextSourceFrags: frags,
		ContextSourceWarnings: []contextfrag.ValidationWarning{{
			Code: "hook_system_section_required_clamped",
			Ref:  sourceRef,
		}},
	})

	item := manifestItemByID(got.ContextManifest.Items, hook.ID)
	if item == nil || item.Ref.ContentHash == sourceRef.ContentHash {
		t.Fatalf("trimmed item = %#v, want recomputed content hash distinct from %#v", item, sourceRef)
	}
	for _, warning := range got.ContextManifest.ValidationWarnings {
		if warning.Code != "hook_system_section_required_clamped" {
			continue
		}
		if warning.Ref.ID != item.Ref.ID || warning.Ref.ContentHash != item.Ref.ContentHash {
			t.Fatalf("trim warning ref = %#v, want final item ref %#v", warning.Ref, item.Ref)
		}
		return
	}
	t.Fatalf("validation warnings = %#v, want clamp warning", got.ContextManifest.ValidationWarnings)
}

func hookSystemTestFrag(
	id, text string,
	retention contextfrag.RetentionTier,
	cache contextfrag.CacheClass,
	trust contextfrag.TrustLevel,
	priority int,
	scope contextfrag.Scope,
) contextfrag.ContextFrag {
	kind := contextfrag.KindHookContext
	source := "hook_system_section"
	collector := "hook_system_sections"
	if id == "system.prompt" {
		kind = contextfrag.KindSystemPrompt
		source = contextfrag.SourceRunConfig
		collector = sourceFragsCollectorName
	}
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:            id,
		Kind:          kind,
		Role:          sdk.MessageRoleSystem,
		Slot:          contextfrag.SlotSystem,
		Text:          text,
		Priority:      priority,
		RetentionTier: retention,
		CacheClass:    cache,
		Trust:         trust,
		Scope:         scope,
		Source:        source,
		Collector:     collector,
		Render:        contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		Budget: contextfrag.BudgetPolicy{
			MaxChars: 8 * 1024,
			Overflow: contextfrag.OverflowTrim,
		},
	})
}

func hookSystemBudgetTestFrag(
	id string,
	retention contextfrag.RetentionTier,
	tokens int,
) contextfrag.ContextFrag {
	frag := systemBudgetTestFrag(id, retention, tokens, 80, 0, contextfrag.OverflowTrim)
	frag.Kind = contextfrag.KindHookContext
	frag.CacheClass = contextfrag.CacheDynamic
	frag.Trust = contextfrag.TrustWorkspace
	frag.Provenance.Source = "hook_system_section"
	frag.Provenance.Collector = "hook_system_sections"
	return frag
}

func trustBreakdownByTrust(
	breakdown []contextfrag.TrustBreakdown,
	trust contextfrag.TrustLevel,
) *contextfrag.TrustBreakdown {
	for i := range breakdown {
		if breakdown[i].Trust == trust {
			return &breakdown[i]
		}
	}
	return nil
}

func hasValidationWarning(
	warnings []contextfrag.ValidationWarning,
	want contextfrag.ValidationWarning,
) bool {
	for _, warning := range warnings {
		if warning.Code == want.Code &&
			warning.Message == want.Message &&
			warning.Ref.ID == want.Ref.ID &&
			warning.Ref.ContentHash == want.Ref.ContentHash {
			return true
		}
	}
	return false
}

func fragIndexByID(frags []contextfrag.ContextFrag, id string) int {
	for i, frag := range frags {
		if frag.ID == id {
			return i
		}
	}
	return -1
}
