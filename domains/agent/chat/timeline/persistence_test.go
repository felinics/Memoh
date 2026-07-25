package timeline

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
)

// fakeEventStore serves the narrow Store port EventStore depends on. Only the
// cursor allocation and the insert are exercised here; the remaining methods
// satisfy the interface.
type fakeEventStore struct {
	nextCursor int64
	created    EventRecord
	createErr  error
	createdID  string
}

func (f *fakeEventStore) NextEventCursor(context.Context) (int64, error) {
	return f.nextCursor, nil
}

func (f *fakeEventStore) CreateEvent(_ context.Context, record EventRecord) (string, error) {
	f.created = record
	if f.createErr != nil {
		return "", f.createErr
	}
	return f.createdID, nil
}

func (*fakeEventStore) ListEvents(context.Context, string) ([]StoredEvent, error) { return nil, nil }
func (*fakeEventStore) CountEvents(context.Context, string) (int64, error)        { return 0, nil }

func (*fakeEventStore) GetDiscussCursor(context.Context, string, string) (DiscussCursorPosition, error) {
	return DiscussCursorPosition{}, nil
}

func (*fakeEventStore) UpsertDiscussCursor(context.Context, DiscussCursorRecord) error { return nil }

const (
	persistTestBotID     = "77777777-7777-7777-7777-777777777777"
	persistTestSessionID = "66666666-6666-6666-6666-666666666666"
)

func TestPersistEventStampsCursorIntoPayload(t *testing.T) {
	store := &fakeEventStore{nextCursor: 424242, createdID: "55555555-5555-5555-5555-555555555555"}
	events := NewEventStore(slog.New(slog.DiscardHandler), store)

	event := MessageEvent{
		SessionID:    persistTestSessionID,
		MessageID:    "m1",
		ReceivedAtMs: 1000,
		TimestampSec: 1,
		Content:      []ContentNode{{Type: "text", Text: "hello"}},
	}

	id, stamped, err := events.PersistEvent(context.Background(), persistTestBotID, persistTestSessionID, event)
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
	if err := json.Unmarshal(store.created.Data, &persisted); err != nil {
		t.Fatalf("decode persisted event data: %v", err)
	}
	if persisted.EventCursor != 424242 {
		t.Fatalf("persisted payload cursor = %d, want 424242", persisted.EventCursor)
	}
}

func TestPersistEventReturnsStampedEventOnInsertFailure(t *testing.T) {
	store := &fakeEventStore{nextCursor: 515151, createErr: errors.New("insert unavailable")}
	events := NewEventStore(slog.New(slog.DiscardHandler), store)

	_, stamped, err := events.PersistEvent(context.Background(), persistTestBotID, persistTestSessionID,
		MessageEvent{SessionID: persistTestSessionID, MessageID: "m1", ReceivedAtMs: 1000},
	)
	if err == nil {
		t.Fatal("expected insert failure")
	}
	message, ok := stamped.(MessageEvent)
	if !ok || message.EventCursor != 515151 {
		t.Fatalf("stamped event must survive insert failure, got %+v", stamped)
	}
}

func TestPersistEventReturnsOriginalEventOnDedup(t *testing.T) {
	// An empty id is how the store reports ON CONFLICT DO NOTHING: the freshly
	// allocated cursor was never stored, so the caller must keep projecting the
	// original event.
	store := &fakeEventStore{nextCursor: 616161, createdID: ""}
	events := NewEventStore(slog.New(slog.DiscardHandler), store)

	id, projected, err := events.PersistEvent(context.Background(), persistTestBotID, persistTestSessionID,
		EditEvent{SessionID: persistTestSessionID, MessageID: "m1", ReceivedAtMs: 1000},
	)
	if err != nil || id != "" {
		t.Fatalf("dedup must return no id and no error, got id=%q err=%v", id, err)
	}
	edit, ok := projected.(EditEvent)
	if !ok || edit.EventCursor != 0 {
		t.Fatalf("deduplicated delivery must project the original unstamped event, got %+v", projected)
	}
}
