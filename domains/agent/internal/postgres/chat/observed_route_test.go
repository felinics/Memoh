package chat

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/memohai/memoh/domains/agent/chat/message"
	agentsqlc "github.com/memohai/memoh/domains/agent/internal/postgres/sqlc"
	"github.com/memohai/memoh/internal/db"
)

type observedRouteQueriesFake struct {
	params agentsqlc.ListObservedRoutesParams
	rows   []agentsqlc.ListObservedRoutesRow
	err    error
	calls  int
}

func (f *observedRouteQueriesFake) ListObservedRoutes(_ context.Context, params agentsqlc.ListObservedRoutesParams) ([]agentsqlc.ListObservedRoutesRow, error) {
	f.calls++
	f.params = params
	return f.rows, f.err
}

func TestObservedRouteReaderListsAgentProjection(t *testing.T) {
	t.Parallel()

	const (
		botID      = "00000000-0000-0000-0000-000000000101"
		identityID = "00000000-0000-0000-0000-000000000102"
		routeID    = "00000000-0000-0000-0000-000000000103"
	)
	observedAt := time.Date(2026, time.July, 25, 1, 2, 3, 0, time.UTC)
	fake := &observedRouteQueriesFake{rows: []agentsqlc.ListObservedRoutesRow{{
		RouteID: observedRouteUUID(t, routeID),
		LastObservedAt: pgtype.Timestamptz{
			Time:  observedAt,
			Valid: true,
		},
	}}}
	reader := newObservedRouteReader(fake)

	items, err := reader.ListObservedRoutes(t.Context(), message.ObservedRouteQuery{
		BotID: botID, ChannelIdentityID: identityID,
	})
	if err != nil {
		t.Fatalf("ListObservedRoutes() error = %v", err)
	}
	if fake.calls != 1 {
		t.Fatalf("ListObservedRoutes() calls = %d, want 1", fake.calls)
	}
	if got := db.UUIDString(fake.params.BotID); got != botID {
		t.Errorf("bot ID = %q, want %q", got, botID)
	}
	if got := db.UUIDString(fake.params.ChannelIdentityID); got != identityID {
		t.Errorf("channel identity ID = %q, want %q", got, identityID)
	}
	if len(items) != 1 || items[0].RouteID != routeID || !items[0].LastObservedAt.Equal(observedAt) {
		t.Fatalf("ListObservedRoutes() = %#v, want route %s observed at %s", items, routeID, observedAt)
	}
}

func TestObservedRouteReaderAllowsAnySender(t *testing.T) {
	t.Parallel()

	fake := &observedRouteQueriesFake{}
	reader := newObservedRouteReader(fake)
	_, err := reader.ListObservedRoutes(t.Context(), message.ObservedRouteQuery{
		BotID: "00000000-0000-0000-0000-000000000101",
	})
	if err != nil {
		t.Fatalf("ListObservedRoutes() error = %v", err)
	}
	if fake.params.ChannelIdentityID.Valid {
		t.Fatal("channel identity ID is valid, want SQL NULL")
	}
}

func TestObservedRouteReaderRejectsInvalidIDs(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		query message.ObservedRouteQuery
	}{
		{name: "bot", query: message.ObservedRouteQuery{BotID: "not-a-uuid"}},
		{name: "identity", query: message.ObservedRouteQuery{
			BotID: "00000000-0000-0000-0000-000000000101", ChannelIdentityID: "not-a-uuid",
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fake := &observedRouteQueriesFake{}
			_, err := newObservedRouteReader(fake).ListObservedRoutes(t.Context(), test.query)
			if err == nil {
				t.Fatal("ListObservedRoutes() error = nil, want invalid UUID error")
			}
			if fake.calls != 0 {
				t.Fatalf("ListObservedRoutes() calls = %d, want 0", fake.calls)
			}
		})
	}
}

func TestObservedRouteReaderReturnsQueryError(t *testing.T) {
	t.Parallel()

	want := errors.New("query observed routes")
	fake := &observedRouteQueriesFake{err: want}
	_, err := newObservedRouteReader(fake).ListObservedRoutes(t.Context(), message.ObservedRouteQuery{
		BotID: "00000000-0000-0000-0000-000000000101",
	})
	if !errors.Is(err, want) {
		t.Fatalf("ListObservedRoutes() error = %v, want %v", err, want)
	}
}

func observedRouteUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	id, err := db.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return id
}
