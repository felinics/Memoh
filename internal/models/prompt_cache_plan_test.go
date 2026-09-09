package models

import (
	"testing"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
)

func anthropicTestModel() *sdk.Model {
	provider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	return provider.ChatModel("claude-test")
}

func TestApplyPromptCacheZeroPlanMatchesLegacy(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{sdk.UserMessage("hello"), sdk.AssistantMessage("hi")}
	tools := []sdk.Tool{{Name: "calc"}}

	legacySystem, legacyMessages, legacyTools := ApplyPromptCache(model, "5m", "system", messages, tools)
	planSystem, planMessages, planTools, _, _ := ApplyPromptCacheWithPlan(model, "5m", contextfrag.CachePlan{}, "system", messages, tools)

	if legacySystem != planSystem || len(legacyMessages) != len(planMessages) || len(legacyTools) != len(planTools) {
		t.Fatal("zero plan must preserve the legacy cache layout")
	}
	for i := range legacyMessages {
		if len(legacyMessages[i].Content) != len(planMessages[i].Content) {
			t.Fatalf("message %d content diverged", i)
		}
	}
}

func TestApplyPromptCachePlanSetsStableMessageBreakpoint(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		sdk.UserMessage("stable summary"),
		sdk.UserMessage("volatile question"),
	}
	plan := contextfrag.CachePlan{StablePrefixHash: "abc", StableMessageCount: 1}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "system", messages, nil)

	// got[0] is the relocated system message; got[1] is the stable message.
	stable, ok := got[1].Content[len(got[1].Content)-1].(sdk.TextPart)
	if !ok || stable.CacheControl == nil {
		t.Fatalf("stable message should carry cache breakpoint: %#v", got[1].Content)
	}
	volatilePart, ok := got[2].Content[len(got[2].Content)-1].(sdk.TextPart)
	if !ok || volatilePart.CacheControl != nil {
		t.Fatalf("volatile message must not carry cache breakpoint: %#v", got[2].Content)
	}
	if actualStableCount != 1 {
		t.Fatalf("actual stable message count = %d, want 1 (breakpoint landed exactly where requested)", actualStableCount)
	}
}

func TestApplyPromptCachePlanOutOfRangeIgnored(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{sdk.UserMessage("only")}
	plan := contextfrag.CachePlan{StableMessageCount: 5}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "", messages, nil)
	part, ok := got[0].Content[0].(sdk.TextPart)
	if !ok || part.CacheControl != nil {
		t.Fatalf("out-of-range plan must be ignored: %#v", got[0].Content)
	}
	if actualStableCount != 5 {
		t.Fatalf("actual stable message count = %d, want unchanged passthrough of 5 since placement was never attempted", actualStableCount)
	}
}

// TestApplyPromptCachePlanSkipsUnsupportedLastPartAndFindsEarlierBreakpoint is
// the P1 RED test: history whose last stable message ends in a
// ToolResultPart (a real, common shape — a tool-result-ending turn) must not
// silently drop the breakpoint just because ToolResultPart carries no
// CacheControl field. The fix scans backward for the nearest earlier message
// that can carry the breakpoint and shrinks the reported stable count to
// match, so the plan never claims more was cached than actually was.
func TestApplyPromptCachePlanSkipsUnsupportedLastPartAndFindsEarlierBreakpoint(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		sdk.UserMessage("stable text message"),
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "search", Result: "ok"}),
		sdk.UserMessage("volatile question"),
	}
	// StableMessageCount=2 marks messages[0:2] stable; messages[1] ends in a
	// ToolResultPart, which cannot carry cache_control.
	plan := contextfrag.CachePlan{StableMessageCount: 2}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "", messages, nil)

	if actualStableCount != 1 {
		t.Fatalf("actual stable message count = %d, want 1 (breakpoint fell back to the earlier text message)", actualStableCount)
	}
	stable, ok := got[0].Content[len(got[0].Content)-1].(sdk.TextPart)
	if !ok || stable.CacheControl == nil {
		t.Fatalf("earlier eligible message should carry the cache breakpoint: %#v", got[0].Content)
	}
	if _, ok := got[1].Content[len(got[1].Content)-1].(sdk.ToolResultPart); !ok {
		t.Fatalf("tool-result message should be left otherwise untouched: %#v", got[1].Content)
	}
}

// TestApplyPromptCachePlanNoEligibleMessageReportsZero covers the case where
// every message in [0, StableMessageCount-1] ends in an unsupported part
// (tool call, tool result): no breakpoint can be placed at all, so the
// honest reported stable count is 0, not the unmet claim.
func TestApplyPromptCachePlanNoEligibleMessageReportsZero(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		{
			Role: sdk.MessageRoleAssistant,
			Content: []sdk.MessagePart{
				sdk.ToolCallPart{ToolCallID: "call-1", ToolName: "search", Input: map[string]any{}},
			},
		},
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "search", Result: "ok"}),
	}
	plan := contextfrag.CachePlan{StableMessageCount: 2}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "", messages, nil)

	if actualStableCount != 0 {
		t.Fatalf("actual stable message count = %d, want 0 (no message in range supports cache_control)", actualStableCount)
	}
	for i, msg := range got {
		for _, part := range msg.Content {
			if tp, ok := part.(sdk.TextPart); ok && tp.CacheControl != nil {
				t.Fatalf("message %d unexpectedly carries a text cache breakpoint", i)
			}
		}
	}
}

// TestApplyPromptCacheWithPlanReportsSystemPromotion asserts the 4th return
// value truthfully reports whether this call prepended a system message,
// rather than callers having to guess from messages[0].Role afterwards.
func TestApplyPromptCacheWithPlanReportsSystemPromotion(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{sdk.UserMessage("hello")}

	t.Run("anthropic with system and caching on", func(t *testing.T) {
		t.Parallel()
		_, _, _, gotPrepended, _ := ApplyPromptCacheWithPlan(anthropicTestModel(), "5m", contextfrag.CachePlan{}, "system prompt", messages, nil)
		if !gotPrepended {
			t.Fatal("expected systemPrepended=true when Anthropic caching promotes a non-empty system string")
		}
	})

	t.Run("anthropic with empty system", func(t *testing.T) {
		t.Parallel()
		_, _, _, gotPrepended, _ := ApplyPromptCacheWithPlan(anthropicTestModel(), "5m", contextfrag.CachePlan{}, "", messages, nil)
		if gotPrepended {
			t.Fatal("expected systemPrepended=false when system is empty, so nothing was promoted")
		}
	})

	t.Run("anthropic with caching off", func(t *testing.T) {
		t.Parallel()
		_, _, _, gotPrepended, _ := ApplyPromptCacheWithPlan(anthropicTestModel(), PromptCacheTTLOff, contextfrag.CachePlan{}, "system prompt", messages, nil)
		if gotPrepended {
			t.Fatal("expected systemPrepended=false when the cache ttl is off")
		}
	})

	t.Run("non-anthropic model", func(t *testing.T) {
		t.Parallel()
		_, _, _, gotPrepended, _ := ApplyPromptCacheWithPlan(newOpenAITestModel(t), "5m", contextfrag.CachePlan{}, "system prompt", messages, nil)
		if gotPrepended {
			t.Fatal("expected systemPrepended=false for non-Anthropic models")
		}
	})
}

func TestApplyPromptCachePlanSkipsBreakpointBelowMinPrefix(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		sdk.UserMessage("tiny stable"),
		sdk.UserMessage("volatile"),
	}
	plan := contextfrag.CachePlan{
		StableMessageCount:        1,
		StablePrefixTokenEstimate: MinCacheablePrefixTokens - 1,
	}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "system", messages, nil)
	stable, ok := got[1].Content[len(got[1].Content)-1].(sdk.TextPart)
	if !ok || stable.CacheControl != nil {
		t.Fatalf("below-minimum prefix must not carry a message breakpoint: %#v", got[1].Content)
	}
	if actualStableCount != 0 {
		t.Fatalf("actual stable message count = %d, want honest 0 when the breakpoint was withheld", actualStableCount)
	}
}

func TestApplyPromptCachePlanKeepsBreakpointWithoutEstimate(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		sdk.UserMessage("stable"),
		sdk.UserMessage("volatile"),
	}
	plan := contextfrag.CachePlan{StableMessageCount: 1}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "system", messages, nil)
	stable, ok := got[1].Content[len(got[1].Content)-1].(sdk.TextPart)
	if !ok || stable.CacheControl == nil {
		t.Fatalf("plans without an estimate must keep today's placement: %#v", got[1].Content)
	}
	if actualStableCount != 1 {
		t.Fatalf("actual stable message count = %d, want 1", actualStableCount)
	}
}

func TestApplyPromptCachePlanPlacesMidSpanBreakpoint(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{
		sdk.UserMessage("stable one"),
		sdk.AssistantMessage("stable two"),
		sdk.UserMessage("stable three"),
		sdk.AssistantMessage("stable four"),
		sdk.UserMessage("volatile"),
	}
	plan := contextfrag.CachePlan{
		StableMessageCount:        4,
		MidStableMessageCount:     2,
		StablePrefixTokenEstimate: 5000,
	}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(model, "5m", plan, "system", messages, nil)
	mid, ok := got[2].Content[len(got[2].Content)-1].(sdk.TextPart)
	if !ok || mid.CacheControl == nil {
		t.Fatalf("mid-span message should carry a breakpoint: %#v", got[2].Content)
	}
	tail, ok := got[4].Content[len(got[4].Content)-1].(sdk.TextPart)
	if !ok || tail.CacheControl == nil {
		t.Fatalf("tail stable message should carry a breakpoint: %#v", got[4].Content)
	}
	volatilePart, ok := got[5].Content[len(got[5].Content)-1].(sdk.TextPart)
	if !ok || volatilePart.CacheControl != nil {
		t.Fatalf("volatile message must stay undecorated: %#v", got[5].Content)
	}
	if actualStableCount != 4 {
		t.Fatalf("actual stable message count = %d, want 4", actualStableCount)
	}
}
