package application

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
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
