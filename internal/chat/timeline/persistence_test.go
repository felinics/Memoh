package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type fakeEventQueries struct {
	dbstore.Queries
	nextCursor int64
	created    sqlc.CreateSessionEventParams
}

func (f *fakeEventQueries) NextSessionEventCursor(context.Context) (int64, error) {
	return f.nextCursor, nil
}

func (f *fakeEventQueries) CreateSessionEvent(_ context.Context, arg sqlc.CreateSessionEventParams) (pgtype.UUID, error) {
	f.created = arg
	id, err := dbpkg.ParseUUID("55555555-5555-5555-5555-555555555555")
	if err != nil {
		return pgtype.UUID{}, err
	}
	return id, nil
}

func TestPersistEventStampsCursorIntoPayload(t *testing.T) {
	queries := &fakeEventQueries{nextCursor: 424242}
	store := NewEventStore(slog.New(slog.DiscardHandler), queries)

	event := MessageEvent{
		SessionID:    "66666666-6666-6666-6666-666666666666",
		MessageID:    "m1",
		ReceivedAtMs: 1000,
		TimestampSec: 1,
		Content:      []ContentNode{{Type: "text", Text: "hello"}},
	}

	id, stamped, err := store.PersistEvent(context.Background(),
		"77777777-7777-7777-7777-777777777777",
		"66666666-6666-6666-6666-666666666666",
		event,
	)
	if err != nil {
		t.Fatalf("persist event: %v", err)
	}
	if id == "" {
		t.Fatal("expected persisted event id")
	}
	message, ok := stamped.(MessageEvent)
	if !ok || message.EventCursor != 424242 {
		t.Fatalf("expected stamped cursor 424242, got %+v", stamped)
	}

	var persisted MessageEvent
	if err := json.Unmarshal(queries.created.EventData, &persisted); err != nil {
		t.Fatalf("decode persisted event data: %v", err)
	}
	if persisted.EventCursor != 424242 {
		t.Fatalf("persisted payload cursor = %d, want 424242", persisted.EventCursor)
	}
}

type failingInsertEventQueries struct {
	fakeEventQueries
}

func (*failingInsertEventQueries) CreateSessionEvent(context.Context, sqlc.CreateSessionEventParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, errors.New("insert unavailable")
}

func TestPersistEventReturnsStampedEventOnInsertFailure(t *testing.T) {
	queries := &failingInsertEventQueries{fakeEventQueries{nextCursor: 515151}}
	store := NewEventStore(slog.New(slog.DiscardHandler), queries)

	_, stamped, err := store.PersistEvent(context.Background(),
		"77777777-7777-7777-7777-777777777777",
		"66666666-6666-6666-6666-666666666666",
		MessageEvent{SessionID: "66666666-6666-6666-6666-666666666666", MessageID: "m1", ReceivedAtMs: 1000},
	)
	if err == nil {
		t.Fatal("expected insert failure")
	}
	message, ok := stamped.(MessageEvent)
	if !ok || message.EventCursor != 515151 {
		t.Fatalf("stamped event must survive insert failure, got %+v", stamped)
	}
}

type conflictingEventQueries struct {
	fakeEventQueries
}

func (*conflictingEventQueries) CreateSessionEvent(context.Context, sqlc.CreateSessionEventParams) (pgtype.UUID, error) {
	return pgtype.UUID{}, pgx.ErrNoRows
}

func TestPersistEventReturnsOriginalEventOnDedup(t *testing.T) {
	queries := &conflictingEventQueries{fakeEventQueries{nextCursor: 616161}}
	store := NewEventStore(slog.New(slog.DiscardHandler), queries)

	id, projected, err := store.PersistEvent(context.Background(),
		"77777777-7777-7777-7777-777777777777",
		"66666666-6666-6666-6666-666666666666",
		EditEvent{SessionID: "66666666-6666-6666-6666-666666666666", MessageID: "m1", ReceivedAtMs: 1000},
	)
	if err != nil || id != "" {
		t.Fatalf("dedup must return no id and no error, got id=%q err=%v", id, err)
	}
	edit, ok := projected.(EditEvent)
	if !ok || edit.EventCursor != 0 {
		t.Fatalf("deduplicated delivery must project the original unstamped event, got %+v", projected)
	}
}
