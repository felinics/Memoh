package application

import (
	"testing"

	historyfrag "github.com/felinics/memoh/internal/agent/context/history"
)

func TestDedupePersistedCurrentUserMessageRemovesCurrentInboundFromHistory(t *testing.T) {
	t.Parallel()

	history := []historyfrag.HistoryRecord{
		historyRecord("row-1", ModelMessage{
			Role:    "user",
			Content: newTextContent("---\nmessage-id: qq-msg-1\nchannel: qq\n---\nhello"),
		}, func(record *historyfrag.HistoryRecord) {
			record.ExternalMessageID = "qq-msg-1"
			record.Platform = "qq"
			record.SenderChannelIdentityID = "channel-identity-1"
		}),
		historyRecord("row-2", ModelMessage{
			Role:    "assistant",
			Content: newTextContent("ok"),
		}, nil),
	}

	got := dedupePersistedCurrentUserMessage(history, ChatRequest{
		UserMessagePersisted:    true,
		RouteID:                 "route-1",
		ExternalMessageID:       "qq-msg-1",
		CurrentChannel:          "qq",
		SourceChannelIdentityID: "channel-identity-1",
	})

	if len(got) != 1 {
		t.Fatalf("expected 1 message after dedupe, got %d", len(got))
	}
	if got[0].ModelMessage.Role != "assistant" {
		t.Fatalf("unexpected remaining role: %s", got[0].ModelMessage.Role)
	}
}
