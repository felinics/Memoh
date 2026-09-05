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
	nextCursor   int64
	created      sqlc.CreateSessionEventParams
	replayRows   []sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow
	replayParams []sqlc.ListSessionEventsBySessionPageBeforeWithinBytesParams
	eventCount   int64
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

func (f *fakeEventQueries) ListSessionEventsBySessionPageBeforeWithinBytes(_ context.Context, arg sqlc.ListSessionEventsBySessionPageBeforeWithinBytesParams) ([]sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow, error) {
	f.replayParams = append(f.replayParams, arg)
	return f.replayRows, nil
}

func (f *fakeEventQueries) CountSessionEvents(context.Context, pgtype.UUID) (int64, error) {
	return f.eventCount, nil
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

type staticReplayArtifacts []CompactionArtifact

func (a staticReplayArtifacts) ActiveCompactionArtifacts(context.Context, string, string) ([]CompactionArtifact, error) {
	return a, nil
}

func TestLoadEventsForReplayPassesFrontierAndRestoresChronologicalOrder(t *testing.T) {
	t.Parallel()
	sessionID := "66666666-6666-6666-6666-666666666666"
	makeRow := func(id string, receivedAt int64, messageID string) sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow {
		event := MessageEvent{SessionID: sessionID, MessageID: messageID, ReceivedAtMs: receivedAt, Content: []ContentNode{{Type: "text", Text: messageID}}}
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		pgID, err := dbpkg.ParseUUID(id)
		if err != nil {
			t.Fatal(err)
		}
		return sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow{
			ID: pgID, EventKind: string(EventMessage), EventData: data,
			ReceivedAtMs: receivedAt, PayloadBytes: int64(len(data)), CumulativeBytes: int64(len(data) * 2),
		}
	}
	queries := &fakeEventQueries{
		eventCount: 3,
		replayRows: []sqlc.ListSessionEventsBySessionPageBeforeWithinBytesRow{
			makeRow("55555555-5555-5555-5555-555555555552", 20, "new"),
			makeRow("55555555-5555-5555-5555-555555555551", 10, "old"),
		},
	}
	store := NewEventStore(slog.New(slog.DiscardHandler), queries)
	store.SetReplayArtifactProvider(staticReplayArtifacts{{
		ID: "artifact", Summary: "covered", CoverageAsOfMs: 15,
		Sources: []CompactionSource{{ExternalMessageID: "covered-message", CreatedAtMs: 5}},
	}})
	events, err := store.LoadEventsForReplay(context.Background(), "77777777-7777-7777-7777-777777777777", sessionID)
	if err != nil {
		t.Fatalf("LoadEventsForReplay() error = %v", err)
	}
	if len(events) != 2 || events[0].(MessageEvent).MessageID != "old" || events[1].(MessageEvent).MessageID != "new" {
		t.Fatalf("events not chronological: %#v", events)
	}
	if len(queries.replayParams) != 1 {
		t.Fatalf("query calls = %d, want 1", len(queries.replayParams))
	}
	var coverage map[string]int64
	if err := json.Unmarshal(queries.replayParams[0].CoveredExternalMessages, &coverage); err != nil {
		t.Fatalf("decode coverage: %v", err)
	}
	if coverage["covered-message"] != 15 {
		t.Fatalf("coverage = %#v", coverage)
	}
}
