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
