package contextview

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	agentpkg "github.com/felinics/memoh/internal/agent/runtime/native"
)

const contextbenchFixtureSeed int64 = 0x434f4e54455854

type contextbench16KFixture struct {
	sourceFrags  []contextfrag.ContextFrag
	toolDefs     []contextfrag.ToolDefAccounting
	systemCount  int
	messageCount int
}

func TestApplyProviderRunConfigContextbench16KRenderedEnvelope(t *testing.T) {
	t.Parallel()

	fixture := buildContextbench16KFixture()
	if fixture.systemCount != 71 || fixture.messageCount != 31 || len(fixture.toolDefs) != 12 {
		t.Fatalf("fixture shape = system:%d messages:%d tools:%d, want 71/31/12",
			fixture.systemCount, fixture.messageCount, len(fixture.toolDefs))
	}
	out, err := ProviderRunConfigApplier(nil)(context.Background(), agentpkg.RunConfig{
		ContextSourceFrags:       slices.Clone(fixture.sourceFrags),
		ContextScope:             contextfrag.Scope{BotID: "contextbench"},
		ContextQueryMaterialized: true,
		ContextToolDefs:          slices.Clone(fixture.toolDefs),
		ContextToolDefsResolved:  true,
		ContextBudgetMaxTokens:   16_000,
	})
	if err != nil {
		t.Fatalf("contextbench 16k preflight error = %v, want conservative selection to fit", err)
	}

	inputTokens := testRenderedProviderEnvelopeTokens(out.System, out.Messages, out.ContextToolDefs)
	if out.ContextManifest.BudgetPlan == nil {
		t.Fatal("budget plan missing from manifest")
	}
	reserve := out.ContextManifest.BudgetPlan.OutputReserve
	if envelope := inputTokens + reserve; envelope > out.ContextBudgetMaxTokens {
		t.Fatalf("provider call allowed with rendered envelope %d > window %d (input=%d reserve=%d)",
			envelope, out.ContextBudgetMaxTokens, inputTokens, reserve)
	}
}

func TestProviderSelectorReservesConservativeHistoryTrimNotice(t *testing.T) {
	t.Parallel()

	notice := TrimNoticeFrag(contextfrag.Scope{})
	legacyCost := contextfrag.ResolveFragTokens(notice)
	providerCost := contextfrag.ResolveProviderBudgetFragTokens(notice)
	if legacyCost != 55 || providerCost != 68 || providerCost-legacyCost != 13 {
		t.Fatalf("trim notice costs = legacy:%d provider:%d, want 55/68 (13-token drift)", legacyCost, providerCost)
	}

	frags := []contextfrag.ContextFrag{
		testBudgetMessageFrag("old-1", sdk.UserMessage(strings.Repeat("a", 320)), contextfrag.SlotHistory, 100),
		testBudgetMessageFrag("old-2", sdk.AssistantMessage(strings.Repeat("b", 320)), contextfrag.SlotHistory, 100),
		testBudgetMessageFrag("current", sdk.UserMessage("current"), contextfrag.SlotCurrentUser, 10),
	}
	frags[2].Kind = contextfrag.KindCurrentUserMessage
	frags[2].Trust = contextfrag.TrustUser
	frags[2].Budget.Overflow = contextfrag.OverflowKeep
	plan := &contextfrag.ContextBudgetPlan{SystemBudget: 167}
	selector := &FragmentSelector{}
	result := selector.Select(
		frags,
		selector.ProfileFor(contextfrag.IntentRunConfigPreProvider),
		BudgetEnvelope{Plan: plan, RecentProtectTokens: 0},
	)
	if result.FatalError != nil {
		t.Fatalf("selector error = %v", result.FatalError)
	}
	if !result.TrimNotice || len(result.Dropped) != 2 {
		t.Fatalf("trim result = notice:%v dropped:%d, want conservative notice and both history messages dropped",
			result.TrimNotice, len(result.Dropped))
	}
}

func TestApplyProviderRunConfigTrimsUnderchargedHistoryBeforeDispatch(t *testing.T) {
	t.Parallel()

	history := testBudgetMessageFrag(
		"history-undercharged",
		sdk.UserMessage(strings.Repeat("x", 4_000)),
		contextfrag.SlotHistory,
		1,
	)
	current := testBudgetMessageFrag("current", sdk.UserMessage("current"), contextfrag.SlotCurrentUser, 3)
	current.Kind = contextfrag.KindCurrentUserMessage
	current.Trust = contextfrag.TrustUser
	current.Budget.Overflow = contextfrag.OverflowKeep

	out, err := ProviderRunConfigApplier(nil)(context.Background(), agentpkg.RunConfig{
		ContextSourceFrags:     []contextfrag.ContextFrag{history, current},
		ContextBudgetMaxTokens: 1_000,
	})
	if err != nil {
		t.Fatalf("preflight error = %v, want undercharged history trimmed by envelope pricing", err)
	}
	for _, message := range out.Messages {
		if strings.Contains(messageText(t, message), "xxxx") {
			t.Fatal("undercharged history reached the provider payload")
		}
	}
	plan := out.ContextManifest.BudgetPlan
	if plan == nil {
		t.Fatal("budget plan missing from manifest")
	}
	if rendered := contextfrag.ProviderEnvelopeTokens(out.System, out.Messages, nil); rendered+plan.OutputReserve > plan.Window {
		t.Fatalf("rendered envelope = %d + reserve %d exceeds window %d", rendered, plan.OutputReserve, plan.Window)
	}
	for _, record := range out.ContextMutations.Records() {
		if record.Kind == contextfrag.MutationContextBudgetFailure {
			t.Fatalf("trimmable history recorded %#v", record)
		}
	}
}

func TestValidateProviderRenderedEnvelopeFailsClosedOnOverflow(t *testing.T) {
	t.Parallel()

	payload := &SDKRenderedPayload{System: "system", Messages: []sdk.Message{sdk.UserMessage(strings.Repeat("x", 4_000))}}
	plan := &contextfrag.ContextBudgetPlan{Window: 1_000, OutputReserve: 250}
	err := validateProviderRenderedEnvelope(payload, nil, plan)
	if !errors.Is(err, contextfrag.ErrBudgetUnsatisfied) {
		t.Fatalf("validateProviderRenderedEnvelope() = %v, want ErrBudgetUnsatisfied", err)
	}
	if want := "rendered_input=1252 output_reserve=250 window=1000"; !strings.Contains(err.Error(), want) {
		t.Fatalf("validateProviderRenderedEnvelope() = %v, want detail %q", err, want)
	}
	if err := validateProviderRenderedEnvelope(payload, nil, &contextfrag.ContextBudgetPlan{Window: 1_502, OutputReserve: 250}); err != nil {
		t.Fatalf("exact fit rejected: %v", err)
	}
}

func buildContextbench16KFixture() contextbench16KFixture {
	rng := rand.New(rand.NewSource(contextbenchFixtureSeed)) //nolint:gosec // fixed seed is the regression contract
	sections := []agentpkg.SystemSection{
		contextbenchSection("system.prompt.intro", contextfrag.KindSystemPrompt, 10, contextfrag.RetentionRequired, 0, contextbenchSeededText(rng, 900, false)),
		contextbenchSection("system.bot_identity", contextfrag.KindBotIdentity, 20, contextfrag.RetentionPreferred, 0, contextbenchSeededText(rng, 420, true)),
		contextbenchSection("system.prompt.body", contextfrag.KindSystemPrompt, 30, contextfrag.RetentionRequired, 0, contextbenchSeededText(rng, 2_600, false)),
		contextbenchSection("system.prompt.tail", contextfrag.KindSystemPrompt, 50, contextfrag.RetentionRequired, 0, contextbenchSeededText(rng, 1_400, false)),
	}
	toolRender := contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown, GroupID: "system.tool_usage", GroupJoiner: "\n\n"}
	sections = append(sections, agentpkg.SystemSection{
		ID: "system.tool_usage.header", Kind: contextfrag.KindToolUsage, Priority: 45,
		RetentionTier: contextfrag.RetentionPreferred, Text: "## Tool usage", Render: toolRender,
	})
	for i := range 10 {
		sections = append(sections, agentpkg.SystemSection{
			ID: fmt.Sprintf("system.tool_usage.provider-%02d", i), Kind: contextfrag.KindToolUsage, Priority: 45,
			RetentionTier: contextfrag.RetentionPreferred, DropPriority: contextfrag.DropPriority(rng.Intn(3)),
			Text: contextbenchSeededText(rng, 260+rng.Intn(641), i%3 == 0), Render: toolRender,
		})
	}
	identityRender := contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown, GroupID: "system.platform_identity", GroupJoiner: "\n"}
	sections = append(sections, agentpkg.SystemSection{
		ID: "system.platform_identity.header", Kind: contextfrag.KindPlatformIdentity, Priority: 60,
		RetentionTier: contextfrag.RetentionPreferred, Text: "## Connected identities", Render: identityRender,
	})
	for i := range 8 {
		sections = append(sections, agentpkg.SystemSection{
			ID: fmt.Sprintf("system.platform_identity.identity-%02d", i), Kind: contextfrag.KindPlatformIdentity, Priority: 60,
			RetentionTier: contextfrag.RetentionPreferred, DropPriority: contextfrag.DropPriority(rng.Intn(3)),
			Text: contextbenchSeededText(rng, 180+rng.Intn(421), i%3 == 1), Render: identityRender,
		})
	}
	skillRender := contextfrag.RenderPolicy{Format: contextfrag.RenderMarkdown, GroupID: "system.skills", GroupJoiner: "\n"}
	sections = append(sections, agentpkg.SystemSection{
		ID: "system.skills.header", Kind: contextfrag.KindSkillsCatalog, Priority: 65,
		RetentionTier: contextfrag.RetentionOptional, RequiredCapability: "use_skill",
		Text: "## Available skills (40)", Render: skillRender,
	})
	for i := range 40 {
		sections = append(sections, agentpkg.SystemSection{
			ID: fmt.Sprintf("system.skill.skill-%02d", i), Kind: contextfrag.KindSkillsCatalog, Priority: 65,
			RetentionTier: contextfrag.RetentionOptional, DropPriority: contextfrag.DropPriority(rng.Intn(5)),
			RequiredCapability: "use_skill", Text: contextbenchSeededText(rng, 50+rng.Intn(1_951), i < 12 || i%7 == 0),
			Render: skillRender,
		})
	}
	for i := range 6 {
		sections = append(sections, agentpkg.SystemSection{
			ID: fmt.Sprintf("system.workspace_file.file-%02d", i), Kind: contextfrag.KindWorkspaceInstruction, Priority: 70,
			RetentionTier: contextfrag.RetentionPreferred, DropPriority: contextfrag.DropPriority(rng.Intn(4)),
			Text: contextbenchSeededText(rng, 1_024+rng.Intn(19*1_024+1), i%2 == 0),
		})
	}

	systemFrags := agentpkg.SystemSectionFrags(sections, contextfrag.Scope{BotID: "contextbench"})
	messageFrags := make([]contextfrag.ContextFrag, 0, 31)
	for i := range 30 {
		text := contextbenchSeededText(rng, 180+rng.Intn(921), i%5 == 0)
		message := sdk.UserMessage(text)
		trust := contextfrag.TrustExternal
		if i%2 == 1 {
			message = sdk.AssistantMessage(text)
			trust = contextfrag.TrustWorkspace
		}
		messageFrags = append(messageFrags, contextbenchMessageFrag(
			fmt.Sprintf("message.%03d", i), message, contextfrag.KindConversationEvent, contextfrag.SlotHistory, trust, i,
		))
	}
	current := contextbenchMessageFrag(
		"message.current",
		sdk.UserMessage("Compare the retained context precisely and explain any omitted material."),
		contextfrag.KindCurrentUserMessage,
		contextfrag.SlotCurrentUser,
		contextfrag.TrustUser,
		30,
	)
	current.CacheClass = contextfrag.CacheNever
	current.Budget.Overflow = contextfrag.OverflowKeep
	messageFrags = append(messageFrags, current)

	tools := contextbenchTools()
	toolDefs := make([]contextfrag.ToolDefAccounting, 0, len(tools))
	for _, tool := range tools {
		toolDefs = append(toolDefs, contextfrag.ToolDefAccountingFor("contextbench", tool))
	}
	return contextbench16KFixture{
		sourceFrags: append(slices.Clone(systemFrags), messageFrags...),
		toolDefs:    toolDefs, systemCount: len(systemFrags), messageCount: len(messageFrags),
	}
}

func contextbenchSection(
	id string,
	kind contextfrag.Kind,
	priority int,
	retention contextfrag.RetentionTier,
	drop contextfrag.DropPriority,
	text string,
) agentpkg.SystemSection {
	return agentpkg.SystemSection{
		ID: id, Kind: kind, Priority: priority, RetentionTier: retention, DropPriority: drop, Text: text,
	}
}

func contextbenchMessageFrag(
	id string,
	message sdk.Message,
	kind contextfrag.Kind,
	slot contextfrag.Slot,
	trust contextfrag.TrustLevel,
	index int,
) contextfrag.ContextFrag {
	frag := contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: id, Message: message, Kind: kind, Slot: slot, Priority: contextfrag.PriorityForMessage(message),
		CacheClass: contextfrag.CacheStable, Trust: trust, Scope: contextfrag.Scope{BotID: "contextbench"},
		Source: "contextbench", Collector: "contextbench", Index: index,
	})
	frag.TokenEstimate = contextfrag.ResolveProviderBudgetFragTokens(frag)
	return frag
}

func testBudgetMessageFrag(
	id string,
	message sdk.Message,
	slot contextfrag.Slot,
	tokens int,
) contextfrag.ContextFrag {
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: id, Message: message, Kind: contextfrag.KindConversationEvent, Slot: slot,
		TokenEstimate: tokens, CacheClass: contextfrag.CacheStable, Trust: contextfrag.TrustWorkspace,
	})
}

func contextbenchTools() []sdk.Tool {
	names := []string{
		"use_skill", "read", "write", "apply_patch", "exec", "search_memory", "web_search",
		"browser_action", "computer_action", "ask_user", "spawn_agent", "read_media",
	}
	tools := make([]sdk.Tool, 0, len(names))
	for i, name := range names {
		tools = append(tools, sdk.Tool{
			Name: name, Description: "Contextbench tool " + name + " with bounded, production-shaped usage guidance.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string", "description": "request text"},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100 + i},
				},
				"required": []string{"query"},
			},
		})
	}
	return tools
}

func contextbenchSeededText(rng *rand.Rand, size int, multilingual bool) string {
	pattern := "context orchestration fixture data "
	if multilingual {
		pattern = "上下文编排🙂稳定性数据 "
	}
	var out strings.Builder
	out.Grow(size)
	for out.Len()+len(pattern) <= size {
		out.WriteString(pattern)
	}
	const alphabet = "abcdefghijklmnopqrstuvwxyz"
	for out.Len() < size {
		out.WriteByte(alphabet[rng.Intn(len(alphabet))])
	}
	return out.String()
}

func testRenderedProviderEnvelopeTokens(
	system string,
	messages []sdk.Message,
	toolDefs []contextfrag.ToolDefAccounting,
) int {
	total := contextfrag.ProviderBudgetTokensFromBytes(len(system))
	for _, message := range messages {
		frag := contextfrag.MessageFrag(contextfrag.MessageFragInput{Message: message})
		total += contextfrag.ResolveProviderBudgetFragTokens(frag)
	}
	for _, definition := range toolDefs {
		total += max(definition.TokenEstimate, contextfrag.ProviderBudgetTokensFromBytes(definition.Bytes))
	}
	return total
}
