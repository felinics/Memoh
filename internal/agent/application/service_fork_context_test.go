package application

import (
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
	"github.com/felinics/memoh/internal/agent/toolexec"
)

func TestHistorySourceMessageIDsFollowRetainedDatabaseMessages(t *testing.T) {
	records := []historyfrag.HistoryRecord{
		loadedRecord("00000000-0000-0000-0000-000000000001", ModelMessage{Role: "user", Content: newTextContent("first")}),
		loadedRecord("00000000-0000-0000-0000-000000000002", ModelMessage{Role: "assistant", Content: newTextContent("second")}),
	}
	messages := []ModelMessage{
		{Role: "system", Content: newTextContent("runtime workspace context")},
		records[1].ModelMessage,
		{Role: "user", Content: newTextContent("current query")},
	}

	got := historySourceMessageIDsForMessages(messages, records)
	want := []string{"", records[1].DBMessageID, ""}
	if len(got) != len(want) {
		t.Fatalf("source IDs = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("source ID %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// A tool row answering a live call and an orphan one is rewritten to the
// live result; the rewrite still renders that row.
func TestHistorySourceMessageIDsSurviveToolClosureRepair(t *testing.T) {
	call := sdkMessagesToModelMessages([]sdk.Message{{Role: sdk.MessageRoleAssistant, Content: []sdk.MessagePart{
		sdk.ToolCallPart{ToolCallID: "live", ToolName: "exec", Input: toolexec.ArgumentsFromValue(map[string]any{})},
	}}})[0]
	results := sdkMessagesToModelMessages([]sdk.Message{sdk.ToolMessage(
		sdk.ToolResultPart{ToolCallID: "live", ToolName: "exec", Result: toolexec.OutputFromValue("up")},
		sdk.ToolResultPart{ToolCallID: "orphan", ToolName: "exec", Result: toolexec.OutputFromValue("up")},
	)})[0]
	records := []historyfrag.HistoryRecord{loadedRecord("call-row", call), loadedRecord("result-row", results)}

	normalized := normalizeContext([]ModelMessage{records[0].ModelMessage, records[1].ModelMessage})
	got := historySourceMessageIDsForMessages(normalized.models(), records)
	if len(got) != 2 || got[1] != "result-row" || len(toolResultParts(normalized.sent()[1])) != 1 {
		t.Fatalf("source IDs = %v for %+v, want the filtered result to keep result-row", got, normalized.sent())
	}
}
