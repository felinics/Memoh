package application

import (
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestDedupePersistedCurrentUserMessageRemovesCurrentInboundFromHistory(t *testing.T) {
	t.Parallel()

	hist := []history.HistoryRecord{
		historyRecord("row-1", agentdomain.ModelMessage{
			Role:    "user",
			Content: agentdomain.NewTextContent("---\nmessage-id: qq-msg-1\nchannel: qq\n---\nhello"),
		}, func(record *history.HistoryRecord) {
			record.ExternalMessageID = "qq-msg-1"
			record.Platform = "qq"
			record.SenderChannelIdentityID = "channel-identity-1"
		}),
		historyRecord("row-2", agentdomain.ModelMessage{
			Role:    "assistant",
			Content: agentdomain.NewTextContent("ok"),
		}, nil),
	}

	got := dedupePersistedCurrentUserMessage(hist, ChatRequest{
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
