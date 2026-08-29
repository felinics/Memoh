package application

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

// Capping replayed reasoning rewrites an assistant message's content, and
// provenance is resolved by matching that content against durable rows. Without
// recognising the capped shape the row is silently lost, which drops fork
// context sources and context accounting for exactly the turns the cap touches.
func TestHistorySourceMessageIDsSurviveReasoningCap(t *testing.T) {
	durable := modelMessageFromSDKParts(sdk.MessageRoleAssistant, []sdk.MessagePart{
		reasoningPart("thinking", "SIG"),
		sdk.TextPart{Text: "answer"},
	}, nil)

	records := []historyfrag.HistoryRecord{
		{DBMessageID: "msg-1", ModelMessage: durable},
	}

	capped := dropReasoning(durable)
	if string(capped.Content) == string(durable.Content) {
		t.Fatalf("precondition: dropReasoning did not rewrite the content")
	}

	ids := historySourceMessageIDsForMessages([]ModelMessage{capped}, records)
	if len(ids) != 1 || ids[0] != "msg-1" {
		t.Fatalf("source ids for the capped message: got %v, want [msg-1]", ids)
	}
}

// A reasoning-only turn is projected to text rather than emptied, so its
// provenance has to survive that projection too.
func TestHistorySourceMessageIDsSurviveReasoningOnlyProjection(t *testing.T) {
	durable := modelMessageFromSDKParts(sdk.MessageRoleAssistant, []sdk.MessagePart{
		reasoningPart("only thinking", "SIG"),
	}, nil)

	records := []historyfrag.HistoryRecord{
		{DBMessageID: "msg-2", ModelMessage: durable},
	}

	projected := dropReasoning(durable)
	ids := historySourceMessageIDsForMessages([]ModelMessage{projected}, records)
	if len(ids) != 1 || ids[0] != "msg-2" {
		t.Fatalf("source ids for the projected message: got %v, want [msg-2]", ids)
	}
}
