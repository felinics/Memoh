package gateway

import (
	"context"
	"sync"
	"testing"
)

type recordingObserver struct {
	mu     sync.Mutex
	events []observedEvent
}

type observedEvent struct {
	BotID  string
	Source ChannelType
	Event  StreamEvent
}

func (r *recordingObserver) OnStreamEvent(_ context.Context, botID string, source ChannelType, event StreamEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, observedEvent{BotID: botID, Source: source, Event: event})
}

func (r *recordingObserver) recorded() []observedEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	cp := make([]observedEvent, len(r.events))
	copy(cp, r.events)
	return cp
}

// stubStream records pushed events.
type stubStream struct {
	mu     sync.Mutex
	events []StreamEvent
	closed bool
}

func (s *stubStream) Push(_ context.Context, event StreamEvent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
	return nil
}

func (s *stubStream) Close(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func TestNewTeeStream_NilObserver(t *testing.T) {
	primary := &stubStream{}
	result := NewTeeStream(primary, nil, "bot1", "telegram")
	if result != primary {
		t.Fatal("expected primary stream returned when observer is nil")
	}
}

func TestTeeStream_Push(t *testing.T) {
	primary := &stubStream{}
	obs := &recordingObserver{}
	stream := NewTeeStream(primary, obs, "bot1", "telegram")

	event := StreamEvent{Type: StreamEventDelta, Delta: "hello"}
	if err := stream.Push(context.Background(), event); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	primary.mu.Lock()
	if len(primary.events) != 1 {
		t.Fatalf("expected 1 primary event, got %d", len(primary.events))
	}
	if primary.events[0].Delta != "hello" {
		t.Fatalf("unexpected primary delta: %s", primary.events[0].Delta)
	}
	primary.mu.Unlock()

	recorded := obs.recorded()
	if len(recorded) != 1 {
		t.Fatalf("expected 1 observed event, got %d", len(recorded))
	}
	if recorded[0].BotID != "bot1" {
		t.Fatalf("unexpected botID: %s", recorded[0].BotID)
	}
	if recorded[0].Source != "telegram" {
		t.Fatalf("unexpected source: %s", recorded[0].Source)
	}
	if recorded[0].Event.Delta != "hello" {
		t.Fatalf("unexpected observed delta: %s", recorded[0].Event.Delta)
	}
}

func TestTeeStream_Close(t *testing.T) {
	primary := &stubStream{}
	obs := &recordingObserver{}
	stream := NewTeeStream(primary, obs, "bot1", "telegram")

	if err := stream.Close(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	primary.mu.Lock()
	if !primary.closed {
		t.Fatal("expected primary stream to be closed")
	}
	primary.mu.Unlock()

	if len(obs.recorded()) != 0 {
		t.Fatal("close should not produce observer events")
	}
}

func TestTeeStream_MultipleEvents(t *testing.T) {
	primary := &stubStream{}
	obs := &recordingObserver{}
	stream := NewTeeStream(primary, obs, "bot1", "feishu")

	events := []StreamEvent{
		{Type: StreamEventStatus, Status: StreamStatusStarted},
		{Type: StreamEventDelta, Delta: "chunk1"},
		{Type: StreamEventDelta, Delta: "chunk2"},
		{Type: StreamEventFinal, Final: &StreamFinalizePayload{Message: Message{Text: "done"}}},
		{Type: StreamEventStatus, Status: StreamStatusCompleted},
	}
	for _, event := range events {
		if err := stream.Push(context.Background(), event); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	primary.mu.Lock()
	if len(primary.events) != len(events) {
		t.Fatalf("expected %d primary events, got %d", len(events), len(primary.events))
	}
	primary.mu.Unlock()

	recorded := obs.recorded()
	if len(recorded) != len(events) {
		t.Fatalf("expected %d observed events, got %d", len(events), len(recorded))
	}
	for i, r := range recorded {
		if r.Source != "feishu" {
			t.Fatalf("event %d: unexpected source %s", i, r.Source)
		}
	}
}
