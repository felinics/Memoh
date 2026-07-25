package application

import (
	"reflect"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
	"github.com/memohai/memoh/domains/agent/chat/context/fragment"
	"github.com/memohai/memoh/domains/agent/chat/context/history"
)

func TestStripToolMessagesWhenCompactionSummaryIsActive(t *testing.T) {
	messages := []agentdomain.ModelMessage{
		{Role: "user", Content: agentdomain.NewTextContent("question")},
		{Role: "tool", Content: agentdomain.NewTextContent("large tool output")},
	}

	t.Run("raw history keeps tool messages", func(t *testing.T) {
		records := []history.HistoryRecord{{
			Kind:       fragment.KindConversationEvent,
			SourceKind: history.SourceDBMessage,
		}}
		got := stripToolMessagesWhenCompactionSummaryIsActive(messages, records)
		if !reflect.DeepEqual(got, messages) {
			t.Fatalf("raw history messages = %#v, want %#v", got, messages)
		}
	})

	t.Run("active summary strips tool messages", func(t *testing.T) {
		records := []history.HistoryRecord{{
			Kind:       fragment.KindConversationSummary,
			SourceKind: history.SourceCompactionLog,
			Lifecycle:  history.LifecycleActiveSummary,
		}}
		got := stripToolMessagesWhenCompactionSummaryIsActive(messages, records)
		if len(got) != 1 || got[0].Role != "user" {
			t.Fatalf("summarized history messages = %#v, want only user message", got)
		}
	})
}
