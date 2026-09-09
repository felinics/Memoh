package inbound

import (
	"context"
	"errors"
	"testing"

	"github.com/felinics/memoh/internal/chat/timeline"
)

type stampingEventStore struct {
	cursor int64
	err    error
	id     string
}

func (s stampingEventStore) PersistEvent(_ context.Context, _, _ string, event timeline.CanonicalEvent) (string, timeline.CanonicalEvent, error) {
	msg, ok := event.(timeline.MessageEvent)
	if !ok {
		return "", event, errors.New("unexpected event type")
	}
	msg.EventCursor = s.cursor
	return s.id, msg, s.err
}

func inboundTestEvent() timeline.MessageEvent {
	return timeline.MessageEvent{
		SessionID:    "s1",
		MessageID:    "m1",
		ReceivedAtMs: 1000,
		TimestampSec: 1,
		Content:      []timeline.ContentNode{{Type: "text", Text: "hello"}},
		Conversation: timeline.ConversationMeta{Channel: "telegram", ConversationType: "group"},
	}
}

func TestPersistAndProjectEventProjectsStampedCursor(t *testing.T) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	store := stampingEventStore{cursor: 4242, id: "event-1"}

	id, rc := persistAndProjectEvent(context.Background(), store, pipeline, nil, "bot-1", "s1", inboundTestEvent())

	if id != "event-1" {
		t.Fatalf("event id = %q, want event-1", id)
	}
	if len(rc) != 1 || rc[0].LastEventCursor != 4242 {
		t.Fatalf("projected segment must carry the stamped cursor, got %+v", rc)
	}
}

func TestPersistAndProjectEventProjectsStampedCursorOnPersistFailure(t *testing.T) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})
	store := stampingEventStore{cursor: 4243, err: errors.New("insert unavailable"), id: "ignored"}

	id, rc := persistAndProjectEvent(context.Background(), store, pipeline, nil, "bot-1", "s1", inboundTestEvent())

	if id != "" {
		t.Fatalf("failed persist must not report an event id, got %q", id)
	}
	if len(rc) != 1 || rc[0].LastEventCursor != 4243 {
		t.Fatalf("projection must still use the store's event, got %+v", rc)
	}
}

func TestPersistAndProjectEventWithoutStore(t *testing.T) {
	pipeline := timeline.NewPipeline(timeline.RenderParams{})

	id, rc := persistAndProjectEvent(context.Background(), nil, pipeline, nil, "bot-1", "s1", inboundTestEvent())

	if id != "" || len(rc) != 1 || rc[0].LastEventCursor != 0 {
		t.Fatalf("storeless projection must still render the event, got id=%q rc=%+v", id, rc)
	}
}
