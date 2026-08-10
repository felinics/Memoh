package contextview

import (
	"context"
	"testing"

	sdk "github.com/memohai/twilight-ai/sdk"

	contextfrag "github.com/memohai/memoh/internal/agent/context/fragment"
)

func TestHistoryCollectorPreservesMessagesAndSeparatesCurrent(t *testing.T) {
	t.Parallel()

	messages := []sdk.Message{sdk.UserMessage("history"), sdk.AssistantMessage("answer"), sdk.UserMessage("  current \n")}
	current := 2
	cfg := HistoryMessagesConfig{Messages: messages, CurrentUserMessageIndex: &current}
	history, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	currentFrags, err := (&materializedCurrentUserCollector{}).Collect(context.Background(), CollectRequest{Config: cfg})
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 || len(currentFrags) != 1 || currentFrags[0].Slot != contextfrag.SlotCurrentUser {
		t.Fatalf("history/current = %#v / %#v", history, currentFrags)
	}
	msg := contextfrag.FragMessage(currentFrags[0])
	text := msg.Content[0].(sdk.TextPart)
	if text.Text != "  current \n" {
		t.Fatalf("current text = %q", text.Text)
	}
}

func TestHistoryCollectorDoesNotInheritCurrentAttention(t *testing.T) {
	t.Parallel()

	frags, err := (&HistoryMessagesCollector{}).Collect(context.Background(), CollectRequest{
		Scope:  contextfrag.Scope{Attention: []contextfrag.AttentionReason{contextfrag.AttentionDirect}},
		Config: HistoryMessagesConfig{Messages: []sdk.Message{sdk.UserMessage("old")}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags[0].Scope.Attention) != 0 {
		t.Fatalf("history attention = %#v", frags[0].Scope.Attention)
	}
}

func TestMaterializedCurrentIndexFallsBackToLatestUser(t *testing.T) {
	t.Parallel()

	stale := 99
	frags, err := (&materializedCurrentUserCollector{}).Collect(context.Background(), CollectRequest{
		Config: HistoryMessagesConfig{Messages: []sdk.Message{sdk.UserMessage("first"), sdk.AssistantMessage("answer"), sdk.UserMessage("latest")}, CurrentUserMessageIndex: &stale},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 1 || frags[0].ID != "message.002" {
		t.Fatalf("frags = %#v", frags)
	}
}
