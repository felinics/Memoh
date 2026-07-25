package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	messagepkg "github.com/memohai/memoh/domains/agent/chat/message"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
)

type forkAnchorMessageService struct {
	recordingMessageService
	visibleFrom []messagepkg.Message
	before      []messagepkg.Message
}

func (s *forkAnchorMessageService) ListVisibleFromBySession(context.Context, string, string) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.visibleFrom...), nil
}

func (s *forkAnchorMessageService) ListBeforeMessageBySession(context.Context, string, string, int32) ([]messagepkg.Message, error) {
	return append([]messagepkg.Message(nil), s.before...), nil
}

func TestReplacePersistedTurnMovesForkAnchorMetadata(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	messages := &forkAnchorMessageService{
		visibleFrom: []messagepkg.Message{
			{ID: "assistant-old", Role: "assistant", CreatedAt: createdAt.Add(-time.Minute)},
		},
		before: []messagepkg.Message{
			{ID: "user-1", Role: "user", CreatedAt: createdAt.Add(-4 * time.Minute)},
			{ID: "assistant-prev", Role: "assistant", CreatedAt: createdAt.Add(-3 * time.Minute)},
			{ID: "user-2", Role: "user", CreatedAt: createdAt.Add(-2 * time.Minute)},
		},
	}
	var updated map[string]any
	resolver := &Service{
		messageService: messages,
		sessionService: &fakeBackgroundSessionService{
			getFn: func(context.Context, string) (session.Thread, error) {
				return session.Thread{
					ID:        "fork-session",
					CreatedAt: createdAt,
					Metadata: map[string]any{
						"forked_from": map[string]any{
							"session_id":       "source-session",
							"message_id":       "source-assistant",
							"fork_message_id":  "assistant-old",
							"source_extra_key": "kept",
						},
					},
				}, nil
			},
			updateMetadataFn: func(_ context.Context, _ string, metadata map[string]any) (session.Thread, error) {
				updated = metadata
				return session.Thread{Metadata: metadata}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.replacePersistedTurn(
		context.Background(),
		ChatRequest{ThreadID: "fork-session", HistoryCutoffBeforeMessageID: "assistant-old"},
		"old-turn",
		"request-2",
		"retry",
		[]messagepkg.Message{{ID: "assistant-new", Role: "assistant"}},
	); err != nil {
		t.Fatalf("replacePersistedTurn() error = %v", err)
	}

	fork, ok := updated["forked_from"].(map[string]any)
	if !ok {
		t.Fatalf("updated fork metadata missing: %#v", updated)
	}
	if got := fork["fork_message_id"]; got != "assistant-prev" {
		t.Fatalf("fork_message_id = %#v, want assistant-prev", got)
	}
	if got := fork["source_extra_key"]; got != "kept" {
		t.Fatalf("source_extra_key = %#v, want kept", got)
	}
}

type recordingEventPublisher struct {
	events []messageevent.Event
}

func (p *recordingEventPublisher) Publish(event messageevent.Event) {
	p.events = append(p.events, event)
}

func TestReplacePersistedTurnPublishesReplacementMessageEvent(t *testing.T) {
	t.Parallel()

	messages := &recordingMessageService{}
	events := &recordingEventPublisher{}
	resolver := &Service{
		messageService: messages,
		eventPublisher: events,
		logger:         slog.Default(),
	}

	err := resolver.replacePersistedTurn(context.Background(), ChatRequest{
		BotID:    "bot-1",
		ThreadID: "session-1",
	}, "old-turn", "user-new", "retry", []messagepkg.Message{
		{ID: "user-new", BotID: "bot-1", SessionID: "session-1", Role: "user"},
		{ID: "assistant-new", BotID: "bot-1", SessionID: "session-1", Role: "assistant", CreatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("replace persisted turn: %v", err)
	}
	if len(events.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(events.events))
	}
	event := events.events[0]
	if event.Type != messageevent.EventTypeMessageCreated {
		t.Fatalf("event type = %q, want %q", event.Type, messageevent.EventTypeMessageCreated)
	}
	if event.BotID != "bot-1" {
		t.Fatalf("event bot id = %q, want bot-1", event.BotID)
	}
	var payload messagepkg.Message
	if err := json.Unmarshal(event.Data, &payload); err != nil {
		t.Fatalf("unmarshal event payload: %v", err)
	}
	if payload.ID != "assistant-new" || payload.SessionID != "session-1" {
		t.Fatalf("payload = %#v, want assistant-new in session-1", payload)
	}
}

func TestReplacePersistedTurnClearsForkAnchorWhenNoInheritedAssistantRemains(t *testing.T) {
	t.Parallel()

	createdAt := time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)
	messages := &forkAnchorMessageService{
		visibleFrom: []messagepkg.Message{
			{ID: "assistant-old", Role: "assistant", CreatedAt: createdAt.Add(-time.Minute)},
		},
		before: []messagepkg.Message{
			{ID: "user-1", Role: "user", CreatedAt: createdAt.Add(-2 * time.Minute)},
		},
	}
	var updated map[string]any
	resolver := &Service{
		messageService: messages,
		sessionService: &fakeBackgroundSessionService{
			getFn: func(context.Context, string) (session.Thread, error) {
				return session.Thread{
					ID:        "fork-session",
					CreatedAt: createdAt,
					Metadata: map[string]any{
						"forked_from": map[string]any{
							"session_id":      "source-session",
							"message_id":      "source-assistant",
							"fork_message_id": "assistant-old",
						},
					},
				}, nil
			},
			updateMetadataFn: func(_ context.Context, _ string, metadata map[string]any) (session.Thread, error) {
				updated = metadata
				return session.Thread{Metadata: metadata}, nil
			},
		},
		logger: slog.New(slog.DiscardHandler),
	}

	if err := resolver.replacePersistedTurn(
		context.Background(),
		ChatRequest{ThreadID: "fork-session", HistoryCutoffBeforeMessageID: "assistant-old"},
		"old-turn",
		"request-1",
		"retry",
		[]messagepkg.Message{{ID: "assistant-new", Role: "assistant"}},
	); err != nil {
		t.Fatalf("replacePersistedTurn() error = %v", err)
	}

	fork, ok := updated["forked_from"].(map[string]any)
	if !ok {
		t.Fatalf("updated fork metadata missing: %#v", updated)
	}
	if _, ok := fork["fork_message_id"]; ok {
		t.Fatalf("fork_message_id was not cleared: %#v", fork)
	}
}
