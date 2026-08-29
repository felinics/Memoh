package application

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/felinics/memoh/internal/apperror"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	messagepkg "github.com/felinics/memoh/internal/chat/message"
	session "github.com/felinics/memoh/internal/chat/thread"
)

type forkAnchorMessageService struct {
	recordingMessageService
	visibleFrom []messagepkg.Message
	before      []messagepkg.Message
}

type replacementOperationMessageService struct {
	recordingMessageService
	latest      messagepkg.HistoryTurn
	turnByMsg   messagepkg.HistoryTurn
	turnByMsgID string
}

func (s *replacementOperationMessageService) GetLatestVisibleTurnBySession(context.Context, string) (messagepkg.HistoryTurn, error) {
	return s.latest, nil
}

func (s *replacementOperationMessageService) GetVisibleTurnByMessage(_ context.Context, _ string, messageID string) (messagepkg.HistoryTurn, error) {
	s.turnByMsgID = messageID
	return s.turnByMsg, nil
}

// The deprecated message-id spelling is resolved to a round at the boundary, so
// a client shipped before the turn-id contract keeps working after an upgrade.
func TestResolveTurnIDForMessageMapsTheLegacySpelling(t *testing.T) {
	messages := &replacementOperationMessageService{
		turnByMsg: messagepkg.HistoryTurn{ID: " turn-old "},
	}
	service := &Service{messageService: messages}

	got, err := service.ResolveTurnIDForMessage(context.Background(), "session-1", " assistant-result ")
	if err != nil {
		t.Fatalf("resolve turn id: %v", err)
	}
	if got != "turn-old" {
		t.Fatalf("turn id = %q, want turn-old", got)
	}
	if messages.turnByMsgID != "assistant-result" {
		t.Fatalf("looked up message %q, want assistant-result", messages.turnByMsgID)
	}

	if _, err := service.ResolveTurnIDForMessage(context.Background(), "session-1", "  "); err == nil {
		t.Fatal("resolve without a message id: want error")
	}
}

func TestResolveTurnIDForMessageRejectsAMessageWithoutAVisibleTurn(t *testing.T) {
	service := &Service{messageService: &replacementOperationMessageService{}}

	if _, err := service.ResolveTurnIDForMessage(context.Background(), "session-1", "assistant-result"); err == nil {
		t.Fatal("resolve a message with no visible turn: want error")
	}
}

func TestPrepareReplacementOperationUsesPersistedTurnBoundary(t *testing.T) {
	t.Run("retry begins at the turn's own assistant anchor", func(t *testing.T) {
		messages := &replacementOperationMessageService{
			latest: messagepkg.HistoryTurn{
				ID:                 "turn-old",
				RequestMessageID:   "user-request",
				AssistantMessageID: "assistant-first",
			},
		}
		service := &Service{messageService: messages}

		got, err := service.PrepareRetryLatestTurnOperation(context.Background(), "session-1", "turn-old")
		if err != nil {
			t.Fatalf("prepare retry operation: %v", err)
		}
		if got != "assistant-first" {
			t.Fatalf("replace from message = %q, want assistant-first", got)
		}
	})

	t.Run("edit begins at persisted request message", func(t *testing.T) {
		messages := &replacementOperationMessageService{
			latest: messagepkg.HistoryTurn{
				ID:               "turn-old",
				RequestMessageID: "user-request",
			},
		}
		service := &Service{messageService: messages}

		got, err := service.PrepareEditLatestTurnOperation(context.Background(), "session-1", "turn-old")
		if err != nil {
			t.Fatalf("prepare edit operation: %v", err)
		}
		if got != "user-request" {
			t.Fatalf("replace from message = %q, want user-request", got)
		}
	})

	// The turn id is the whole validation: a client that names an older round
	// is refused without ever naming a stored message.
	t.Run("a turn that is no longer the latest is refused", func(t *testing.T) {
		messages := &replacementOperationMessageService{
			latest: messagepkg.HistoryTurn{
				ID:                 "turn-new",
				RequestMessageID:   "user-request",
				AssistantMessageID: "assistant-first",
			},
		}
		service := &Service{messageService: messages}

		if _, err := service.PrepareRetryLatestTurnOperation(context.Background(), "session-1", "turn-old"); err == nil {
			t.Fatal("prepare retry operation on a superseded turn: want error")
		}
		if _, err := service.PrepareEditLatestTurnOperation(context.Background(), "session-1", "turn-old"); err == nil {
			t.Fatal("prepare edit operation on a superseded turn: want error")
		}
	})

	t.Run("an unnamed turn is refused before any lookup", func(t *testing.T) {
		service := &Service{messageService: &replacementOperationMessageService{}}

		if _, err := service.PrepareRetryLatestTurnOperation(context.Background(), "session-1", "  "); err == nil {
			t.Fatal("prepare retry operation without a turn id: want error")
		}
	})

	t.Run("retry rejects a turn without its canonical assistant anchor", func(t *testing.T) {
		messages := &replacementOperationMessageService{
			latest: messagepkg.HistoryTurn{
				ID:               "turn-old",
				RequestMessageID: "user-request",
			},
		}
		service := &Service{messageService: messages}

		_, err := service.PrepareRetryLatestTurnOperation(context.Background(), "session-1", "turn-old")
		if got := apperror.CodeOf(err); got != apperror.CodeSessionHistoryInconsistent {
			t.Fatalf("error code = %q, want %q", got, apperror.CodeSessionHistoryInconsistent)
		}
	})
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
		ChatRequest{ThreadID: "fork-session", TurnID: "turn-new", TurnPosition: int64Pointer(2), HistoryCutoffBeforeMessageID: "assistant-old"},
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
		BotID:        "bot-1",
		ThreadID:     "session-1",
		TurnID:       "turn-new",
		TurnPosition: int64Pointer(2),
	}, "old-turn", "user-new", "retry", []messagepkg.Message{
		{ID: "user-new", BotID: "bot-1", SessionID: "session-1", Role: "user"},
		{ID: "assistant-new", BotID: "bot-1", SessionID: "session-1", Role: "assistant", CreatedAt: time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC)},
	})
	if err != nil {
		t.Fatalf("replace persisted turn: %v", err)
	}
	if messages.replacementTurnID != "turn-new" || messages.replacementTurnPosition == nil || *messages.replacementTurnPosition != 2 {
		t.Fatalf("replacement identity = (%q, %v), want (turn-new, 2)", messages.replacementTurnID, messages.replacementTurnPosition)
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
		ChatRequest{ThreadID: "fork-session", TurnID: "turn-new", TurnPosition: int64Pointer(2), HistoryCutoffBeforeMessageID: "assistant-old"},
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

func int64Pointer(value int64) *int64 {
	return &value
}
