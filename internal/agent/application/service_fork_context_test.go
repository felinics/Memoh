package application

import (
	"testing"

	historyfrag "github.com/memohai/memoh/internal/agent/context/history"
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

func TestReplyableExternalMessageIDsIncludeOnlyVisibleUserHistory(t *testing.T) {
	records := []historyfrag.HistoryRecord{
		{
			ExternalMessageID: "telegram-10",
			Platform:          "telegram",
			ModelMessage: ModelMessage{
				Role:    "user",
				Content: newTextContent(`<message id="telegram-10">first</message>`),
			},
		},
		{
			ExternalMessageID: "telegram-11",
			Platform:          "telegram",
			ModelMessage: ModelMessage{
				Role:    "assistant",
				Content: newTextContent("bot output"),
			},
		},
		{
			ExternalMessageID: "telegram-12",
			Platform:          "telegram",
			ModelMessage: ModelMessage{
				Role:    "user",
				Content: newTextContent(`<message id="telegram-12">retained</message>`),
			},
		},
		{
			ExternalMessageID: "discord-13",
			Platform:          "discord",
			ModelMessage: ModelMessage{
				Role:    "user",
				Content: newTextContent(`<message id="discord-13">other platform</message>`),
			},
		},
	}
	messages := []ModelMessage{
		{Role: "system", Content: newTextContent("runtime context")},
		records[0].ModelMessage,
		records[1].ModelMessage,
		records[2].ModelMessage,
		records[3].ModelMessage,
		{Role: "user", Content: newTextContent("current runtime query")},
	}

	got := replyableExternalMessageIDsForMessages(messages, records, "telegram")
	want := []string{"telegram-10", "telegram-12"}
	if len(got) != len(want) {
		t.Fatalf("replyable IDs = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replyable ID %d = %q, want %q", i, got[i], want[i])
		}
	}
	got = appendUniqueMessageID(got, "telegram-12")
	got = appendUniqueMessageID(got, "telegram-13")
	if len(got) != 3 || got[2] != "telegram-13" {
		t.Fatalf("current message append/dedupe = %#v", got)
	}
}
