package application

import (
	"testing"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

func TestHistorySourceMessageIDsFollowRetainedDatabaseMessages(t *testing.T) {
	records := []historyfrag.HistoryRecord{
		{
			DBMessageID: "00000000-0000-0000-0000-000000000001",
			ModelMessage: ModelMessage{
				Role:    "user",
				Content: newTextContent("first"),
			},
		},
		{
			DBMessageID: "00000000-0000-0000-0000-000000000002",
			ModelMessage: ModelMessage{
				Role:    "assistant",
				Content: newTextContent("second"),
			},
		},
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
