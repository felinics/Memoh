package handlers

import (
	"context"
	"reflect"
	"testing"

	"github.com/felinics/memoh/internal/agent/turn"
	"github.com/felinics/memoh/internal/channel"
	"github.com/felinics/memoh/internal/channel/adapters/local"
)

// The Web chat, its socket /queue, and its queue API all start a text's turn
// here: sent by the signed-in user, acting for them, answering in the bot's
// own private Web conversation.
func TestLocalTextTurnIsAWebTurn(t *testing.T) {
	cmd, _, err := localTurns{channel: local.WebType}.text(context.Background(), nil, "user-1", "bot-1", "session-1", "  hello  ")
	if err != nil {
		t.Fatal(err)
	}
	want := turn.StartTurnCommand{
		SchemaVersion: 1, Mode: turn.ModeChat,
		BotID: "bot-1", ChatID: "bot-1", ThreadID: "session-1",
		ActorAccountID: "user-1", UserID: "user-1", SourceChannelIdentityID: "user-1",
		Query: "hello", UserVisibleText: "hello",
		ReplyTarget: "bot-1", ConversationType: channel.ConversationTypePrivate,
		CurrentChannel: "web", Channels: []string{"web"},
	}
	if !reflect.DeepEqual(cmd, want) {
		t.Fatalf("text turn = %+v\nwant        %+v", cmd, want)
	}
}
