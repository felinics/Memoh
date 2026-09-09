package contextview

import (
	"strings"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	userinput "github.com/felinics/memoh/internal/agent/decision/input"
)

func historyMessageFrag(id string, msg sdk.Message) contextfrag.ContextFrag {
	return contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: id, Message: msg, Kind: contextfrag.KindConversationEvent, Slot: contextfrag.SlotHistory,
		Scope: contextfrag.Scope{BotID: "bot-1"}, Source: "run_config_fields", Collector: "history_messages",
	})
}

func toolExchangeFixture() []contextfrag.ContextFrag {
	return []contextfrag.ContextFrag{
		historyMessageFrag("h0", sdk.UserMessage("question")),
		historyMessageFrag("h1", assistantToolCallMessage("call-1", "web_search", "let me look")),
		historyMessageFrag("h2", toolResultMessage("call-1", "web_search", "bulky result")),
		historyMessageFrag("h3", assistantToolCallMessage("ask-1", userinput.ToolNameAskUser, "")),
		historyMessageFrag("h4", toolResultMessage("ask-1", userinput.ToolNameAskUser, "user picked B")),
		historyMessageFrag("h5", sdk.AssistantMessage("final answer")),
	}
}

func TestToolExchangePolicyStripsBulkyExchangesAndKeepsAskUser(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	result := selector.Select(toolExchangeFixture(), selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{ToolExchange: &contextfrag.ToolExchangePolicy{}})
	ids := make(map[string]bool)
	for _, frag := range result.Selected {
		ids[frag.ID] = true
		if frag.ID == "h1" {
			for _, part := range contextfrag.FragMessage(frag).Content {
				if call, ok := part.(sdk.ToolCallPart); ok && !strings.EqualFold(call.ToolName, userinput.ToolNameAskUser) {
					t.Fatalf("tool call survived: %#v", call)
				}
			}
		}
	}
	if ids["h2"] || !ids["h3"] || !ids["h4"] || !ids["h0"] || !ids["h5"] {
		t.Fatalf("selected ids = %#v", ids)
	}
	if len(result.Edited) == 0 || len(result.Dropped) != 1 || result.Summary.DropReasons[0].Reason != toolExchangeDropReason {
		t.Fatalf("result = %#v", result)
	}
}

func TestToolExchangePolicyThresholdAndNilPreserveEverything(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	profile := selector.ProfileFor(contextfrag.IntentRunConfigPreProvider)
	for _, budget := range []BudgetEnvelope{{}, {ToolExchange: &contextfrag.ToolExchangePolicy{MinMessages: 10}}} {
		result := selector.Select(toolExchangeFixture(), profile, budget)
		if len(result.Selected) != 6 || len(result.Dropped) != 0 || len(result.Edited) != 0 {
			t.Fatalf("budget = %#v, result = %#v", budget, result)
		}
	}
}

// continuationTailFixture models a tool-approval continuation: the history
// replays an earlier finished turn and then the turn still being answered,
// whose parked step (reasoning + text + exec call) already has its result. No
// current user message exists because the continuation carries no new query.
func continuationTailFixture() []contextfrag.ContextFrag {
	parked := sdk.Message{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
		sdk.ReasoningPart{Format: sdk.ReasoningFormatOpenAIChat, Text: "need to touch the file"},
		sdk.TextPart{Text: "creating it now"},
		sdk.ToolCallPart{ToolCallID: "exec-1", ToolName: "exec", Input: map[string]any{"command": "touch /tmp/x"}},
	}}
	return []contextfrag.ContextFrag{
		historyMessageFrag("h0", sdk.UserMessage("earlier question")),
		historyMessageFrag("h1", assistantToolCallMessage("call-1", "web_search", "let me look")),
		historyMessageFrag("h2", toolResultMessage("call-1", "web_search", "bulky result")),
		historyMessageFrag("h3", sdk.AssistantMessage("earlier answer")),
		historyMessageFrag("h4", sdk.UserMessage("create a file in /tmp")),
		historyMessageFrag("h5", parked),
		historyMessageFrag("h6", toolResultMessage("exec-1", "exec", "created")),
	}
}

func selectedByID(result SelectionResult) map[string]contextfrag.ContextFrag {
	out := make(map[string]contextfrag.ContextFrag, len(result.Selected))
	for _, frag := range result.Selected {
		out[frag.ID] = frag
	}
	return out
}

func TestToolExchangePolicyKeepsUnfinishedTurnTailOnContinuation(t *testing.T) {
	t.Parallel()
	selector := &FragmentSelector{}
	result := selector.Select(continuationTailFixture(), selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{ToolExchange: &contextfrag.ToolExchangePolicy{}})
	selected := selectedByID(result)
	if _, ok := selected["h2"]; ok {
		t.Fatalf("earlier tool result survived: %#v", selected["h2"])
	}
	for _, part := range contextfrag.FragMessage(selected["h1"]).Content {
		if _, ok := part.(sdk.ToolCallPart); ok {
			t.Fatalf("earlier tool call survived: %#v", part)
		}
	}
	parked, ok := selected["h5"]
	if !ok {
		t.Fatalf("parked step dropped: %#v", result.Summary.DropReasons)
	}
	var hasCall, hasReasoning bool
	for _, part := range contextfrag.FragMessage(parked).Content {
		switch part.(type) {
		case sdk.ToolCallPart:
			hasCall = true
		case sdk.ReasoningPart:
			hasReasoning = true
		}
	}
	if !hasCall || !hasReasoning {
		t.Fatalf("parked step lost its tool call or reasoning: %#v", contextfrag.FragMessage(parked).Content)
	}
	if _, ok := selected["h6"]; !ok {
		t.Fatalf("parked step result dropped: %#v", result.Summary.DropReasons)
	}
}

func TestToolExchangePolicyStripsPreviousTurnWhenCurrentUserPresent(t *testing.T) {
	t.Parallel()
	frags := continuationTailFixture()
	current := sdk.UserMessage("and now a new question")
	frags = append(frags, contextfrag.MessageFrag(contextfrag.MessageFragInput{
		ID: "c0", Message: current, Kind: contextfrag.KindCurrentUserMessage, Slot: contextfrag.SlotCurrentUser,
		Scope: contextfrag.Scope{BotID: "bot-1"}, Source: "run_config_fields", Collector: "materialized_current_user",
	}))
	selector := &FragmentSelector{}
	result := selector.Select(frags, selector.ProfileFor(contextfrag.IntentRunConfigPreProvider), BudgetEnvelope{ToolExchange: &contextfrag.ToolExchangePolicy{}})
	selected := selectedByID(result)
	if _, ok := selected["h6"]; ok {
		t.Fatalf("previous turn tool result survived with a current user message present")
	}
	for _, part := range contextfrag.FragMessage(selected["h5"]).Content {
		switch part.(type) {
		case sdk.ToolCallPart, sdk.ReasoningPart:
			t.Fatalf("previous turn kept tool exchange part: %#v", part)
		}
	}
	if _, ok := selected["c0"]; !ok {
		t.Fatalf("current user message dropped")
	}
}
