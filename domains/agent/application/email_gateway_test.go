package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	agentdomain "github.com/memohai/memoh/domains/agent"
)

type fakeBotOwnerResolver struct {
	owner string
	err   error
	gotID string
}

func (f *fakeBotOwnerResolver) ResolveBotOwner(_ context.Context, botID string) (string, error) {
	f.gotID = botID
	return f.owner, f.err
}

type fakeEmailTurnStarter struct {
	command agentdomain.StartTurnCommand
	handle  agentdomain.RunHandle
	err     error
}

func (f *fakeEmailTurnStarter) StartTurn(_ context.Context, command agentdomain.StartTurnCommand) (agentdomain.RunHandle, error) {
	f.command = command
	return f.handle, f.err
}

type fakeEmailRunHandle struct {
	events   <-chan agentdomain.Event
	errs     <-chan error
	canceled bool
}

func (*fakeEmailRunHandle) RunID() string { return "email-run" }

func (h *fakeEmailRunHandle) Events() <-chan agentdomain.Event { return h.events }

func (h *fakeEmailRunHandle) Errs() <-chan error { return h.errs }

func (*fakeEmailRunHandle) Inject(context.Context, agentdomain.InjectMessage) error { return nil }

func (*fakeEmailRunHandle) AddOutboundAssets([]agentdomain.OutboundAssetRef) {}

func (h *fakeEmailRunHandle) Cancel() { h.canceled = true }

func TestEmailChatGateway_resolveBotOwner(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	lookupErr := errors.New("lookup failed")

	tests := []struct {
		name    string
		owners  BotOwnerResolver
		want    string
		wantErr string
	}{
		{
			name:    "owner lookup error",
			owners:  &fakeBotOwnerResolver{err: lookupErr},
			wantErr: "lookup failed",
		},
		{
			name:    "empty owner",
			owners:  &fakeBotOwnerResolver{owner: "  "},
			wantErr: "bot owner not found",
		},
		{
			name:   "success",
			owners: &fakeBotOwnerResolver{owner: "user-1"},
			want:   "user-1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			g := NewEmailChatGateway(nil, tt.owners, "", slog.New(slog.DiscardHandler))
			got, err := g.resolveBotOwner(ctx, "bot-1")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("resolveBotOwner() error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveBotOwner() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveBotOwner() = %q, want %q", got, tt.want)
			}
			if f, ok := tt.owners.(*fakeBotOwnerResolver); ok && f.gotID != "bot-1" {
				t.Fatalf("ResolveBotOwner botID = %q, want bot-1", f.gotID)
			}
		})
	}
}

func TestEmailChatGatewayTriggerBotChat(t *testing.T) {
	events := make(chan agentdomain.Event)
	close(events)
	errs := make(chan error)
	close(errs)
	handle := &fakeEmailRunHandle{events: events, errs: errs}
	turns := &fakeEmailTurnStarter{handle: handle}
	gateway := NewEmailChatGateway(
		turns,
		&fakeBotOwnerResolver{owner: "owner-1"},
		"email-test-secret",
		slog.New(slog.DiscardHandler),
	)

	if err := gateway.TriggerBotChat(t.Context(), "bot-1", "new email"); err != nil {
		t.Fatalf("TriggerBotChat() error = %v", err)
	}
	if !handle.canceled {
		t.Fatal("TriggerBotChat() did not release the run handle")
	}
	command := turns.command
	if command.TeamID == "" || command.Mode != agentdomain.ModeChat {
		t.Fatalf("StartTurn command team/mode = %q/%q", command.TeamID, command.Mode)
	}
	if command.BotID != "bot-1" || command.ChatID != "bot-1" || command.UserID != "owner-1" {
		t.Fatalf("StartTurn command identities = bot %q, chat %q, user %q", command.BotID, command.ChatID, command.UserID)
	}
	if command.Query != "new email" || command.CurrentChannel != "email" {
		t.Fatalf("StartTurn command content/channel = %q/%q", command.Query, command.CurrentChannel)
	}
	if !strings.HasPrefix(command.Token, "Bearer ") {
		t.Fatalf("StartTurn command token = %q, want bearer token", command.Token)
	}
}

func TestEmailChatGatewayReturnsRunError(t *testing.T) {
	events := make(chan agentdomain.Event)
	close(events)
	errs := make(chan error, 1)
	errs <- errors.New("turn failed")
	close(errs)
	gateway := NewEmailChatGateway(
		&fakeEmailTurnStarter{handle: &fakeEmailRunHandle{events: events, errs: errs}},
		&fakeBotOwnerResolver{owner: "owner-1"},
		"email-test-secret",
		slog.New(slog.DiscardHandler),
	)

	err := gateway.TriggerBotChat(t.Context(), "bot-1", "new email")
	if err == nil || !strings.Contains(err.Error(), "turn failed") {
		t.Fatalf("TriggerBotChat() error = %v, want turn failure", err)
	}
}
