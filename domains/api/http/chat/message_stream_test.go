package chat

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	messageevent "github.com/memohai/memoh/domains/agent/chat/event"
	session "github.com/memohai/memoh/domains/agent/chat/thread"
)

// sessionCreateRecorder records CreateThread input so tests can assert what
// the service publishes.
type sessionCreateRecorder struct {
	stubThreadStore
}

func (sessionCreateRecorder) InTransaction(_ context.Context, fn func(session.Store) error) error {
	return fn(sessionCreateRecorder{})
}

func (sessionCreateRecorder) GetBotMetadata(context.Context, string) (map[string]any, error) {
	return nil, nil
}

func (sessionCreateRecorder) CreateThread(_ context.Context, record session.CreateRecord) (session.Thread, error) {
	return session.Thread{
		ID:              "22222222-2222-2222-2222-222222222222",
		BotID:           record.BotID,
		Type:            record.Type,
		SessionMode:     record.SessionMode,
		RuntimeType:     record.RuntimeType,
		Title:           record.Title,
		Metadata:        record.Metadata,
		CreatedAt:       time.Unix(1700000000, 0).UTC(),
		CreatedByUserID: record.CreatedByUserID,
	}, nil
}

// TestSessionServicePublishesSessionCreated guards the producer side of the
// EventTypeSessionCreated chain. Without the publish wired here the activity
// stream's consumer case is dead code.
func TestSessionServicePublishesSessionCreated(t *testing.T) {
	t.Parallel()

	hub := messageevent.NewHub()
	botID := "11111111-1111-1111-1111-111111111111"
	sub, cancel := hub.Subscribe(botID, 8)
	defer cancel()

	svc := newThreadServiceForTestWithPublisher(sessionCreateRecorder{}, hub)

	sess, err := svc.Create(context.Background(), session.CreateInput{
		BotID: botID,
		Type:  session.TypeChat,
		Title: "hello",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	select {
	case ev := <-sub.Events:
		if ev.Type != messageevent.EventTypeSessionCreated {
			t.Fatalf("event type = %q, want %q", ev.Type, messageevent.EventTypeSessionCreated)
		}
		if ev.BotID != botID {
			t.Fatalf("event botID = %q, want %q", ev.BotID, botID)
		}
		var payload map[string]any
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		if payload["session_id"] != sess.ID {
			t.Fatalf("payload session_id = %v, want %s", payload["session_id"], sess.ID)
		}
		if payload["type"] != session.TypeChat {
			t.Fatalf("payload type = %v, want %s", payload["type"], session.TypeChat)
		}
		if payload["title"] != "hello" {
			t.Fatalf("payload title = %v, want hello", payload["title"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for session_created event")
	}
}

// TestSessionServiceCreateSucceedsWithoutPublisher confirms the publish is
// best-effort: a nil-publisher service still creates successfully so a hub
// outage never blocks session creation.
func TestSessionServiceCreateSucceedsWithoutPublisher(t *testing.T) {
	t.Parallel()

	svc := newThreadServiceForTest(sessionCreateRecorder{})
	if _, err := svc.Create(context.Background(), session.CreateInput{
		BotID: "11111111-1111-1111-1111-111111111111",
		Type:  session.TypeChat,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}

// TestPayloadSessionIDExtractsTopLevelSnakeCase pins the contract every
// in-tree publisher follows: session_id is lifted to the top of the event
// payload, not nested inside a sub-object. A future publisher that nests it
// will fail this test (and silently lose events to the per-session SSE
// filter).
func TestPayloadSessionIDExtractsTopLevelSnakeCase(t *testing.T) {
	t.Parallel()

	if got := payloadSessionID(map[string]any{"session_id": "s1"}); got != "s1" {
		t.Fatalf("snake_case: got %q, want s1", got)
	}
	if got := payloadSessionID(map[string]any{}); got != "" {
		t.Fatalf("missing: got %q, want empty", got)
	}
	// Realistic BackgroundTask wire shape — `session_id` MUST be at the top
	// (see cmd/agent/app.go's bgManager.SetEventFunc). If the extractor only
	// walked into "task", a producer change would silently drop events.
	bgPayload := map[string]any{
		"event":      "started",
		"session_id": "s-bg",
		"task": map[string]any{
			"task_id":    "task-1",
			"session_id": "s-bg",
			"status":     "started",
		},
	}
	if got := payloadSessionID(bgPayload); got != "s-bg" {
		t.Fatalf("background_task payload: got %q, want s-bg", got)
	}
	// Realistic AgentStream wire shape — same top-level convention (see
	// domains/agent/application/resolver_trigger.go).
	agentPayload := map[string]any{
		"event":      "delta",
		"session_id": "s-agent",
		"stream":     map[string]any{"text": "hello"},
	}
	if got := payloadSessionID(agentPayload); got != "s-agent" {
		t.Fatalf("agent_stream payload: got %q, want s-agent", got)
	}
}
