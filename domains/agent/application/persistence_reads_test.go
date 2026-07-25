package application

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/memohai/memoh/domains/api/bot"
	"github.com/memohai/memoh/domains/iam/account"
)

type botReaderFake struct {
	bot bot.Bot
	err error
}

func (f botReaderFake) GetForAccess(context.Context, string) (bot.Bot, error) {
	return f.bot, f.err
}

type accountReaderFake struct {
	accounts map[string]account.Account
}

func (f accountReaderFake) Get(_ context.Context, id string) (account.Account, error) {
	record, ok := f.accounts[id]
	if !ok {
		return account.Account{}, errors.New("account not found")
	}
	return record, nil
}

type channelIdentityReaderFake struct {
	identities map[string]ChannelIdentity
}

func (f channelIdentityReaderFake) GetByID(_ context.Context, id string) (ChannelIdentity, error) {
	identity, ok := f.identities[id]
	if !ok {
		return ChannelIdentity{}, errors.New("channel identity not found")
	}
	return identity, nil
}

func TestLoadBotRuntimeInfoUsesBotReader(t *testing.T) {
	service := &Service{
		bots: botReaderFake{bot: bot.Bot{
			ID:          "bot-1",
			Name:        "helper",
			DisplayName: "Helper",
			Timezone:    "Asia/Tokyo",
			Metadata: map[string]any{
				"features": map[string]any{
					"loop_detection": map[string]any{"enabled": true},
				},
			},
		}},
		logger: slog.New(slog.DiscardHandler),
	}

	info, loopDetection := service.loadBotRuntimeInfo(context.Background(), "bot-1")
	if info.ID != "bot-1" || info.Name != "helper" || info.DisplayName != "Helper" || info.Timezone != "Asia/Tokyo" {
		t.Fatalf("bot info = %#v", info)
	}
	if !loopDetection {
		t.Fatal("loop detection should be enabled")
	}
}

func TestIdentityReadersPreserveFallbacksAndExistence(t *testing.T) {
	service := &Service{
		channelIdentities: channelIdentityReaderFake{identities: map[string]ChannelIdentity{
			"identity-1": {ID: "identity-1", DisplayName: "Ada"},
		}},
		accounts: accountReaderFake{accounts: map[string]account.Account{
			"user-1": {ID: "user-1"},
		}},
	}

	if got := service.resolveDisplayName(context.Background(), ChatRequest{SourceChannelIdentityID: "identity-1"}); got != "Ada" {
		t.Fatalf("display name = %q, want Ada", got)
	}
	if got := service.resolveDisplayName(context.Background(), ChatRequest{SourceChannelIdentityID: "missing"}); got != "User" {
		t.Fatalf("missing display name = %q, want User", got)
	}
	if !service.isExistingChannelIdentityID(context.Background(), "identity-1") ||
		service.isExistingChannelIdentityID(context.Background(), "missing") {
		t.Fatal("channel identity existence result is incorrect")
	}
	if !service.isExistingUserID(context.Background(), "user-1") ||
		service.isExistingUserID(context.Background(), "missing") {
		t.Fatal("user existence result is incorrect")
	}
}

func TestResolveTimezonePrefersBotProfile(t *testing.T) {
	service := &Service{
		bots: botReaderFake{bot: bot.Bot{Timezone: "Asia/Tokyo"}},
		accounts: accountReaderFake{accounts: map[string]account.Account{
			"user-1": {ID: "user-1", Timezone: "Europe/Berlin"},
		}},
		clockLocation: time.UTC,
	}

	name, location := service.resolveTimezone(context.Background(), "bot-1", "user-1")
	if name != "Asia/Tokyo" || location.String() != "Asia/Tokyo" {
		t.Fatalf("timezone = (%q, %q), want Asia/Tokyo", name, location)
	}
}
