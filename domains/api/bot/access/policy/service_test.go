package policy

import (
	"context"
	"errors"
	"testing"

	"github.com/memohai/memoh/domains/api/bot"
)

type botReaderFake struct {
	bot       bot.Bot
	err       error
	gotBotID  string
	readCount int
}

func (f *botReaderFake) GetForAccess(_ context.Context, botID string) (bot.Bot, error) {
	f.gotBotID = botID
	f.readCount++
	return f.bot, f.err
}

func TestResolveUsesAccessOnlyBotRead(t *testing.T) {
	reader := &botReaderFake{}
	service := NewService(nil, reader)

	decision, err := service.Resolve(t.Context(), " bot-id ")
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if decision.BotID != "bot-id" {
		t.Fatalf("Resolve() BotID = %q, want %q", decision.BotID, "bot-id")
	}
	if reader.gotBotID != "bot-id" || reader.readCount != 1 {
		t.Fatalf("access read = (%q, %d calls), want (%q, 1 call)", reader.gotBotID, reader.readCount, "bot-id")
	}
}

func TestBotOwnerUserIDUsesAccessOnlyBotRead(t *testing.T) {
	reader := &botReaderFake{bot: bot.Bot{OwnerUserID: " owner-id "}}
	service := NewService(nil, reader)

	ownerID, err := service.BotOwnerUserID(t.Context(), " bot-id ")
	if err != nil {
		t.Fatalf("BotOwnerUserID() error = %v", err)
	}
	if ownerID != "owner-id" {
		t.Fatalf("BotOwnerUserID() = %q, want %q", ownerID, "owner-id")
	}
	if reader.gotBotID != "bot-id" || reader.readCount != 1 {
		t.Fatalf("access read = (%q, %d calls), want (%q, 1 call)", reader.gotBotID, reader.readCount, "bot-id")
	}
}

func TestResolvePropagatesAccessReadError(t *testing.T) {
	wantErr := errors.New("read bot identity")
	service := NewService(nil, &botReaderFake{err: wantErr})

	_, err := service.Resolve(t.Context(), "bot-id")
	if !errors.Is(err, wantErr) {
		t.Fatalf("Resolve() error = %v, want %v", err, wantErr)
	}
}
