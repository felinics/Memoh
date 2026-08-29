package contextview

import (
	"context"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
	tools "github.com/felinics/memoh/internal/agent/tool"
)

func TestApplyProviderRunConfigGatesUnavailableSkillGuidance(t *testing.T) {
	t.Parallel()

	cfg := capabilityGateFixture()
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)

	if got.System != "base system" {
		t.Fatalf("system = %q, want unavailable skill guidance removed", got.System)
	}
	if strings.Contains(got.System, "[System Notice]") {
		t.Fatalf("capability gating must not render an omission marker: %q", got.System)
	}
	for _, id := range []string{"system.skills.header", "system.skill.alpha"} {
		decision, ok := decisionByID(got.ContextManifest.SelectionDecisions, id)
		if !ok || decision.Decision != contextfrag.DecisionDropped || decision.Reason != capabilityGateDropReason {
			t.Errorf("decision for %q = %#v, want dropped/%s", id, decision, capabilityGateDropReason)
		}
	}
	if got.ContextManifest.Selection == nil ||
		got.ContextManifest.Selection.DropReasons[capabilityGateDropReason] != 2 {
		t.Fatalf("selection = %#v, want two capability-gated drops", got.ContextManifest.Selection)
	}
	records := got.ContextMutations.Records()
	if len(records) != 2 ||
		records[0].Kind != contextfrag.MutationContextBudgetDisabled ||
		records[1].Kind != contextfrag.MutationCapabilityGate ||
		records[1].Detail != "dropped=2" {
		t.Fatalf("mutations = %+v, want disabled-budget audit then count-only capability gate", records)
	}
}

func TestApplyProviderRunConfigKeepsAvailableSkillGuidanceByteIdentical(t *testing.T) {
	t.Parallel()

	cfg := capabilityGateFixture()
	cfg.ContextToolDefs = []contextfrag.ToolDefAccounting{{Name: tools.ToolUseSkill().String()}}
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)

	if got.System != cfg.System {
		t.Fatalf("system = %q, want byte-identical %q", got.System, cfg.System)
	}
	if got.ContextManifest.Selection != nil &&
		got.ContextManifest.Selection.DropReasons[capabilityGateDropReason] != 0 {
		t.Fatalf("selection = %#v, want no capability gate", got.ContextManifest.Selection)
	}
	if records := got.ContextMutations.Records(); len(records) != 1 || records[0].Kind != contextfrag.MutationContextBudgetDisabled {
		t.Fatalf("mutations = %+v, want only missing-window audit", records)
	}
}

func TestApplyProviderRunConfigLeavesUnknownCapabilityRosterUngated(t *testing.T) {
	t.Parallel()

	cfg := capabilityGateFixture()
	cfg.ContextToolDefsResolved = false
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)

	if got.System != cfg.System {
		t.Fatalf("system = %q, want unknown roster to preserve %q", got.System, cfg.System)
	}
	if records := got.ContextMutations.Records(); len(records) != 1 || records[0].Kind != contextfrag.MutationContextBudgetDisabled {
		t.Fatalf("mutations = %+v, want unknown roster ungated plus missing-window audit", records)
	}
}

func TestApplyProviderRunConfigCapabilityGateKeepsHeaderForRemainingToolUsage(t *testing.T) {
	t.Parallel()

	base := capabilitySystemFrag(
		"system.prompt",
		"base system",
		contextfrag.KindSystemPrompt,
		20,
		"",
		contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
	)
	cfg := agentpkg.RunConfig{
		System:             "base system\n\n## Tool usage\n\nZeta usage\n\nAlpha 用法",
		ContextSourceFrags: []contextfrag.ContextFrag{base},
		ContextToolUsage:   "## Tool usage\n\nZeta usage\n\nAlpha 用法",
		ContextToolUsageFrags: []contextfrag.ContextFrag{
			toolUsageTestFrag("system.tool_usage.header", "## Tool usage", "zeta_tool", 0),
			toolUsageTestFrag("system.tool_usage.zeta_tool", "Zeta usage", "zeta_tool", 1),
			toolUsageTestFrag("system.tool_usage.alpha_tool", "Alpha 用法", "alpha_tool", 2),
		},
		ContextToolDefs:         []contextfrag.ToolDefAccounting{{Name: "alpha_tool"}},
		ContextToolDefsResolved: true,
	}

	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	want := "base system\n\n## Tool usage\n\nAlpha 用法"
	if got.System != want {
		t.Fatalf("system = %q, want remaining provider usage %q", got.System, want)
	}
	if !hasFragID(got.ContextFrags, "system.tool_usage.header") ||
		hasFragID(got.ContextFrags, "system.tool_usage.zeta_tool") {
		t.Fatalf("selected fragments = %v, want header plus only available provider", fragIDs(got.ContextFrags))
	}
	if decision, ok := decisionByID(
		got.ContextManifest.SelectionDecisions,
		"system.tool_usage.zeta_tool",
	); !ok || decision.Reason != capabilityGateDropReason {
		t.Fatalf("zeta decision = %#v, want capability-gated", decision)
	}
}

func TestApplyProviderRunConfigLeavesLegacySystemUngated(t *testing.T) {
	t.Parallel()

	cfg := capabilityGateFixture()
	cfg.ContextSourceFrags = nil
	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if got.System != cfg.System {
		t.Fatalf("legacy system = %q, want unchanged %q", got.System, cfg.System)
	}
}

func TestApplyProviderRunConfigFallbackCannotRestoreGatedGuidance(t *testing.T) {
	t.Parallel()

	cfg := capabilityGateFixture()
	cfg.Messages = []sdk.Message{sdk.UserMessage("legacy message")}
	currentUserIndex := 0
	cfg.ContextCurrentUserMessageIndex = &currentUserIndex
	cfg.ContextQueryMaterialized = true
	cfg.ContextHookText = "legacy append_context"
	legacyHooks, err := (&HookContextCollector{}).Collect(context.Background(), CollectRequest{
		Config: HookContextConfig{Text: "legacy append_context"},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceHook := contextfrag.NormalizeContextRefs([]contextfrag.ContextFrag{hookSystemTestFrag(
		"system.hook.policy",
		"workspace hook system section",
		contextfrag.RetentionPreferred,
		contextfrag.CacheDynamic,
		contextfrag.TrustWorkspace,
		80,
		contextfrag.Scope{},
	)})[0]
	cfg.ContextSourceWarnings = []contextfrag.ValidationWarning{{
		Code: "hook_system_section_required_clamped",
		Ref:  workspaceHook.Ref,
	}}
	cfg.ContextSourceFrags = append(
		cfg.ContextSourceFrags,
		legacyHooks[0],
		workspaceHook,
		contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID:        "duplicate",
			Message:   sdk.UserMessage("first"),
			Kind:      contextfrag.KindConversationEvent,
			Slot:      contextfrag.SlotHistory,
			Source:    contextfrag.SourceRunConfig,
			Collector: sourceFragsCollectorName,
		}),
		contextfrag.MessageFrag(contextfrag.MessageFragInput{
			ID:        "duplicate",
			Message:   sdk.AssistantMessage("second"),
			Kind:      contextfrag.KindConversationEvent,
			Slot:      contextfrag.SlotHistory,
			Source:    contextfrag.SourceRunConfig,
			Collector: sourceFragsCollectorName,
		}),
	)

	got := ApplyProviderRunConfig(context.Background(), nil, cfg)
	if got.System != "base system" {
		t.Fatalf("fallback system = %q, want capability-filtered base without hook context", got.System)
	}
	if len(got.Messages) != 2 || messageText(t, got.Messages[0]) != "legacy append_context" ||
		messageText(t, got.Messages[1]) != "legacy message" {
		t.Fatalf("fallback messages = %#v, want user hook before current", got.Messages)
	}
	records := got.ContextMutations.Records()
	if len(records) != 3 ||
		records[0].Kind != contextfrag.MutationContextBudgetDisabled ||
		records[1].Kind != contextfrag.MutationCapabilityGate ||
		records[2].Kind != contextfrag.MutationContextViewFallback {
		t.Fatalf("fallback mutations = %+v, want disabled budget, capability gate, then context-view fallback", records)
	}
	if got.ContextManifest.Selection == nil ||
		got.ContextManifest.Selection.DropReasons[capabilityGateDropReason] != 2 {
		t.Fatalf("fallback selection = %#v, want two capability-gated drops", got.ContextManifest.Selection)
	}
	for _, id := range []string{"system.skills.header", "system.skill.alpha"} {
		decision, ok := decisionByID(got.ContextManifest.SelectionDecisions, id)
		if !ok || decision.Decision != contextfrag.DecisionDropped || decision.Reason != capabilityGateDropReason {
			t.Errorf("fallback decision for %q = %#v, want dropped/%s", id, decision, capabilityGateDropReason)
		}
	}
	if decision, ok := decisionByID(got.ContextManifest.SelectionDecisions, "duplicate"); ok {
		t.Fatalf("fallback recorded unsent source history as selected: %#v", decision)
	}
	if got.ContextManifest.Selection.Selected != len(got.ContextFrags) {
		t.Fatalf(
			"fallback selected count = %d, want actual rendered fragment count %d",
			got.ContextManifest.Selection.Selected,
			len(got.ContextFrags),
		)
	}
	if !hasValidationWarning(got.ContextManifest.ValidationWarnings, cfg.ContextSourceWarnings[0]) {
		t.Fatalf(
			"fallback validation warnings = %#v, want source warning %#v",
			got.ContextManifest.ValidationWarnings,
			cfg.ContextSourceWarnings[0],
		)
	}
}

func capabilityGateFixture() agentpkg.RunConfig {
	capability := tools.ToolUseSkill().String()
	render := contextfrag.RenderPolicy{
		Format:      contextfrag.RenderMarkdown,
		GroupID:     "system.skills",
		GroupJoiner: "\n",
	}
	frags := []contextfrag.ContextFrag{
		capabilitySystemFrag(
			"system.prompt",
			"base system",
			contextfrag.KindSystemPrompt,
			20,
			"",
			contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown},
		),
		capabilitySystemFrag(
			"system.skills.header",
			"## Skills\n\n1 skill(s) available:",
			contextfrag.KindSkillsCatalog,
			40,
			capability,
			render,
		),
		capabilitySystemFrag(
			"system.skill.alpha",
			"- **alpha**: Alpha 用法",
			contextfrag.KindSkillsCatalog,
			40,
			capability,
			render,
		),
	}
	return agentpkg.RunConfig{
		System:                  contextfrag.Render(frags).System,
		ContextSourceFrags:      frags,
		ContextToolDefsResolved: true,
	}
}

func capabilitySystemFrag(
	id, text string,
	kind contextfrag.Kind,
	priority int,
	requiredCapability string,
	render contextfrag.RenderPolicy,
) contextfrag.ContextFrag {
	return contextfrag.TextFrag(contextfrag.TextFragInput{
		ID:                 id,
		Kind:               kind,
		Role:               sdk.MessageRoleSystem,
		Slot:               contextfrag.SlotSystem,
		Text:               text,
		Priority:           priority,
		RetentionTier:      contextfrag.RetentionPreferred,
		RequiredCapability: requiredCapability,
		CacheClass:         contextfrag.CacheStable,
		Trust:              contextfrag.TrustSystem,
		Source:             contextfrag.SourceRunConfig,
		Collector:          sourceFragsCollectorName,
		Render:             render,
	})
}

func decisionByID(
	decisions []contextfrag.SelectionDecision,
	id string,
) (contextfrag.SelectionDecision, bool) {
	for _, decision := range decisions {
		if decision.ID == id {
			return decision, true
		}
	}
	return contextfrag.SelectionDecision{}, false
}

func hasFragID(frags []contextfrag.ContextFrag, id string) bool {
	for _, frag := range frags {
		if frag.ID == id {
			return true
		}
	}
	return false
}

func fragIDs(frags []contextfrag.ContextFrag) []string {
	ids := make([]string, 0, len(frags))
	for _, frag := range frags {
		ids = append(ids, frag.ID)
	}
	return ids
}

func messageText(t *testing.T, message sdk.Message) string {
	t.Helper()
	if len(message.Content) != 1 {
		t.Fatalf("message content = %#v", message.Content)
	}
	part, ok := message.Content[0].(sdk.TextPart)
	if !ok {
		t.Fatalf("message part = %#v, want text", message.Content[0])
	}
	return part.Text
}
