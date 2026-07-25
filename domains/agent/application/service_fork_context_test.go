package application

import (
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestHistorySourceMessageIDsFollowRetainedDatabaseMessages(t *testing.T) {
	records := []history.HistoryRecord{
		{
			DBMessageID: "00000000-0000-0000-0000-000000000001",
			ModelMessage: agentdomain.ModelMessage{
				Role:    "user",
				Content: agentdomain.NewTextContent("first"),
			},
		},
		{
			DBMessageID: "00000000-0000-0000-0000-000000000002",
			ModelMessage: agentdomain.ModelMessage{
				Role:    "assistant",
				Content: agentdomain.NewTextContent("second"),
			},
		},
	}
	messages := []agentdomain.ModelMessage{
		{Role: "system", Content: agentdomain.NewTextContent("runtime workspace context")},
		records[1].ModelMessage,
		{Role: "user", Content: agentdomain.NewTextContent("current query")},
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
