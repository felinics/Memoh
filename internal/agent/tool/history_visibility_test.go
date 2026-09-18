package tools

import (
	"context"
	"testing"

	sdk "github.com/felinics/twilight/sdk"

	session "github.com/felinics/memoh/internal/chat/thread"
)

func TestHistoryGetMessagesRejectsDeletedCurrentSession(t *testing.T) {
	t.Parallel()
	reader := &fakeHistoryMessageReader{}
	provider := NewHistoryProvider(nil, fakeHistorySessionLister{}, reader, nil)
	available, err := provider.Tools(context.Background(), SessionContext{BotID: "bot-1", SessionID: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range available {
		if tool.Name != ToolGetMessages().String() {
			continue
		}
		_, err = tool.Execute(&sdk.ToolExecContext{Context: context.Background()}, map[string]any{"message_id": "old-message"})
		if err == nil || reader.exactMessageID != "" {
			t.Fatalf("deleted session read: error=%v, message=%q", err, reader.exactMessageID)
		}
		return
	}
	t.Fatal("get_messages tool missing")
}

func TestHistoryVisibilityScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		current  SessionContext
		threads  []session.Thread
		noLister bool
		want     []string
	}{
		{
			name:    "external route excludes creator private history",
			current: SessionContext{BotID: "bot-1", SessionID: "group", UserID: "group-member"},
			threads: []session.Thread{
				{ID: "group", RouteID: "route-group", CreatedByUserID: "creator"},
				{ID: "previous-group", RouteID: "route-group", CreatedByUserID: "other-member"},
				{ID: "creator-web", CreatedByUserID: "creator"},
				{ID: "creator-dm", RouteID: "route-private", CreatedByUserID: "creator"},
				{ID: "group-internal", RouteID: "route-group", Visibility: session.VisibilityInternal},
			},
			want: []string{"group", "previous-group"},
		},
		{
			name:    "web owner excludes internal sessions",
			current: SessionContext{BotID: "bot-1", SessionID: "web", UserID: "other-user"},
			threads: []session.Thread{
				{ID: "web", CreatedByUserID: "owner"},
				{ID: "owner-chat", CreatedByUserID: "owner"},
				{ID: "owner-internal", CreatedByUserID: "owner", Visibility: session.VisibilityInternal},
				{ID: "other-chat", CreatedByUserID: "other-user"},
			},
			want: []string{"web", "owner-chat"},
		},
		{
			name:    "active internal session can read itself",
			current: SessionContext{BotID: "bot-1", SessionID: "internal"},
			threads: []session.Thread{
				{ID: "internal", Visibility: session.VisibilityInternal},
				{ID: "other-internal", Visibility: session.VisibilityInternal},
			},
			want: []string{"internal"},
		},
		{
			name:    "missing current session denies user fallback",
			current: SessionContext{BotID: "bot-1", SessionID: "deleted", UserID: "owner"},
			threads: []session.Thread{{ID: "owner-chat", CreatedByUserID: "owner"}},
		},
		{
			name:    "missing current identity denies user fallback",
			current: SessionContext{BotID: "bot-1", UserID: "owner"},
			threads: []session.Thread{{ID: "owner-chat", CreatedByUserID: "owner"}},
		},
		{
			name:     "legacy without lister keeps current only",
			current:  SessionContext{BotID: "bot-1", SessionID: "current", UserID: "owner"},
			noLister: true,
			want:     []string{"current"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var lister SessionLister = fakeHistorySessionLister{sessions: tt.threads}
			if tt.noLister {
				lister = nil
			}
			visible, allowed, err := visibleHistorySessions(context.Background(), lister, tt.current)
			if err != nil {
				t.Fatal(err)
			}
			if len(allowed) != len(tt.want) {
				t.Fatalf("allowed = %v, want %v", allowed, tt.want)
			}
			for _, id := range tt.want {
				if !historySessionVisible(allowed, id) {
					t.Errorf("session %q is not allowed", id)
				}
			}
			if !tt.noLister && len(visible) != len(tt.want) {
				t.Fatalf("listed %d sessions, want %d", len(visible), len(tt.want))
			}
		})
	}
}
