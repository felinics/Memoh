package models

import (
	"reflect"
	"testing"

	anthropicmessages "github.com/felinics/twilight/provider/anthropic/messages"
	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func anthropicTestModel() *sdk.Model {
	provider := anthropicmessages.New(anthropicmessages.WithAPIKey("test"))
	return provider.ChatModel("claude-test")
}

func TestApplyPromptCacheZeroPlanMatchesLegacy(t *testing.T) {
	t.Parallel()

	model := anthropicTestModel()
	messages := []sdk.Message{sdk.UserMessage("hello"), sdk.AssistantMessage("hi")}
	tools := []toolexec.Tool{{Name: "calc"}}

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

// A stable span that ends in a tool result, the common shape of a turn that
// called tools, takes the breakpoint on that result.
func TestApplyPromptCachePlanMarksToolResultEndingSpan(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{
		sdk.UserMessage("stable text message"),
		sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "search", Result: toolexec.OutputFromValue("ok")}),
		sdk.UserMessage("volatile question"),
	}
	plan := contextfrag.CachePlan{StableMessageCount: 2}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(anthropicTestModel(), "5m", plan, "", messages, nil)

	if actualStableCount != 2 {
		t.Fatalf("actual stable message count = %d, want 2", actualStableCount)
	}
	if result, ok := got[1].Content[0].(sdk.ToolResultPart); !ok || result.CacheControl == nil {
		t.Fatalf("tool result should carry the breakpoint: %#v", got[1].Content)
	}
}

// A thinking block cannot be marked, so a stable span ending in one falls
// back to the nearest earlier message that can, and reports only what that
// breakpoint covers; with none, it reports zero rather than an unmet claim.
func TestApplyPromptCachePlanFallsBackPastUnmarkableParts(t *testing.T) {
	t.Parallel()

	thinking := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{sdk.ReasoningPart{Text: "thinking"}}}
	plan := contextfrag.CachePlan{StableMessageCount: 2}

	_, got, _, _, actualStableCount := ApplyPromptCacheWithPlan(anthropicTestModel(), "5m", plan, "",
		[]sdk.Message{sdk.UserMessage("stable text message"), thinking, sdk.UserMessage("volatile")}, nil)
	if actualStableCount != 1 {
		t.Fatalf("actual stable message count = %d, want 1", actualStableCount)
	}
	if text, ok := got[0].Content[0].(sdk.TextPart); !ok || text.CacheControl == nil {
		t.Fatalf("earlier text message should carry the breakpoint: %#v", got[0].Content)
	}

	_, _, _, _, actualStableCount = ApplyPromptCacheWithPlan(anthropicTestModel(), "5m", plan, "",
		[]sdk.Message{thinking, thinking}, nil)
	if actualStableCount != 0 {
		t.Fatalf("actual stable message count = %d, want 0", actualStableCount)
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

// Each call marks the last message the next call sends again, never the
// background summary each call moves; with four breakpoints in place the
// mid-span one yields; the caller's request stays unmarked.
func TestWithRollingCacheBreakpoint(t *testing.T) {
	t.Parallel()

	cc := &sdk.CacheControl{Type: "ephemeral"}
	markedText := func(text string) sdk.Message {
		return sdk.Message{Role: sdk.MessageRoleUser, Content: []sdk.MessagePart{sdk.TextPart{Text: text, CacheControl: cc}}}
	}
	system := markedText("system")
	system.Role = sdk.MessageRoleSystem
	req := sdk.Request{
		Messages: []sdk.Message{
			system,
			markedText("mid"),
			markedText("stable"),
			sdk.UserMessage("question"),
			sdk.ToolMessage(sdk.ToolResultPart{ToolCallID: "call-1", ToolName: "search", Result: toolexec.OutputFromValue("ok")}),
			sdk.UserMessage(contextfrag.BackgroundSummaryMessagePrefix + "- build (running)"),
		},
		Tools: []sdk.ToolDefinition{{Name: "search", CacheControl: cc}},
	}

	got := WithRollingCacheBreakpoint(req, cc)
	marked := make([]bool, len(got.Messages))
	for i, msg := range got.Messages {
		marked[i] = messageHasCacheControl(msg)
	}
	if want := []bool{true, false, true, false, true, false}; !reflect.DeepEqual(marked, want) {
		t.Fatalf("marked messages = %v, want %v", marked, want)
	}
	if messageHasCacheControl(req.Messages[4]) || !messageHasCacheControl(req.Messages[1]) {
		t.Fatal("the caller's request was modified")
	}
	if again := WithRollingCacheBreakpoint(got, cc); !reflect.DeepEqual(again, got) {
		t.Fatal("a request whose end is marked was marked again")
	}
	if same := WithRollingCacheBreakpoint(req, nil); !reflect.DeepEqual(same, req) {
		t.Fatal("a vendor without breakpoints got one")
	}
}
