package postgres

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/channel"
	channelsqlc "github.com/memohai/memoh/domains/channel/internal/postgres/sqlc"
)

const (
	projectionBotID   = "11111111-1111-1111-1111-111111111111"
	projectionRouteID = "22222222-2222-2222-2222-222222222222"
)

type conversationProjectionQueriesFake struct {
	fn func(context.Context, channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error)
}

func (f *conversationProjectionQueriesFake) ListConversationProjections(ctx context.Context, input channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error) {
	return f.fn(ctx, input)
}

func TestListConversationProjectionsMapsOwnerRows(t *testing.T) {
	store := &Store{projections: &conversationProjectionQueriesFake{
		fn: func(_ context.Context, input channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error) {
			if uuidString(input.BotID) != projectionBotID {
				t.Fatalf("bot id = %q", uuidString(input.BotID))
			}
			if len(input.RouteIds) != 1 || uuidString(input.RouteIds[0]) != projectionRouteID {
				t.Fatalf("route ids = %#v", input.RouteIds)
			}
			if !input.ChannelType.Valid || input.ChannelType.String != "slack" {
				t.Fatalf("channel type = %#v", input.ChannelType)
			}
			return []channelsqlc.ListConversationProjectionsRow{{
				RouteID: projectionUUID(projectionRouteID), Channel: " slack ", ConversationType: " group ",
				ConversationID: " C1 ", ThreadID: " T1 ", ConversationName: " Memoh ",
				ConversationAvatarUrl: " https://example.test/avatar.png ",
			}}, nil
		},
	}}

	items, err := store.ListConversationProjections(t.Context(), channel.ConversationProjectionRequest{
		BotID: projectionBotID, RouteIDs: []string{projectionRouteID}, ChannelType: " slack ",
	})
	if err != nil {
		t.Fatalf("ListConversationProjections() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("ListConversationProjections() = %#v", items)
	}
	got := items[0]
	if got.RouteID != projectionRouteID || got.Channel != "slack" || got.ConversationType != "group" ||
		got.ConversationID != "C1" || got.ThreadID != "T1" || got.ConversationName != "Memoh" ||
		got.ConversationAvatarURL != "https://example.test/avatar.png" {
		t.Fatalf("projection = %#v", got)
	}
}

func TestListConversationProjectionsEmptyRouteIDsSkipsQuery(t *testing.T) {
	store := &Store{projections: &conversationProjectionQueriesFake{
		fn: func(context.Context, channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error) {
			t.Fatal("query should not run")
			return nil, nil
		},
	}}

	items, err := store.ListConversationProjections(t.Context(), channel.ConversationProjectionRequest{BotID: projectionBotID})
	if err != nil || len(items) != 0 || items == nil {
		t.Fatalf("ListConversationProjections() = (%#v, %v)", items, err)
	}
}

func TestListConversationProjectionsRejectsInvalidRouteID(t *testing.T) {
	store := &Store{projections: &conversationProjectionQueriesFake{
		fn: func(context.Context, channelsqlc.ListConversationProjectionsParams) ([]channelsqlc.ListConversationProjectionsRow, error) {
			t.Fatal("query should not run")
			return nil, nil
		},
	}}

	_, err := store.ListConversationProjections(t.Context(), channel.ConversationProjectionRequest{
		BotID: projectionBotID, RouteIDs: []string{"not-a-uuid"},
	})
	if err == nil {
		t.Fatal("expected invalid route id error")
	}
}

func projectionUUID(value string) pgtype.UUID {
	return pgtype.UUID{Bytes: uuid.MustParse(value), Valid: true}
}
