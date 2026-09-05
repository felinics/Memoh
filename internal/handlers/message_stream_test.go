package handlers

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/felinics/memoh/internal/bots"
	messageevent "github.com/felinics/memoh/internal/chat/event"
	session "github.com/felinics/memoh/internal/chat/thread"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	dbstore "github.com/felinics/memoh/internal/db/store"
)

type compactionActivityStub []string

func (s compactionActivityStub) ActiveSessions(string) []string { return s }

func TestCompactionActivityFiltersUnreadableSessions(t *testing.T) {
	t.Parallel()
	cache := newSessionCache(nil, nil)
	cache.rows["mine"] = session.Thread{ID: "mine", BotID: "bot", Type: session.TypeChat, CreatedByUserID: "me"}
	cache.rows["private"] = session.Thread{ID: "private", BotID: "bot", Type: session.TypeChat, CreatedByUserID: "other"}
	cache.rows["internal"] = session.Thread{ID: "internal", BotID: "bot", Type: session.TypeSubagent, CreatedByUserID: "me"}
	h := &MessageHandler{compactionActivity: compactionActivityStub{"mine", "private", "internal", "missing"}}
	ids := h.visibleCompactingSessions(context.Background(), "me", "bot", []string{bots.PermissionChat}, cache)
	if len(ids) != 1 || ids[0] != "mine" {
		t.Fatalf("unexpected visible compactions: %v", ids)
	}
	if ids := h.visibleCompactingSessions(context.Background(), "me", "bot", nil, cache); len(ids) != 0 {
		t.Fatalf("no permission must expose no compactions: %v", ids)
	}
}

// sessionCreateRecorder is a minimal sqlc-shaped fake that records the row a
// CreateSession call returns so we can assert what the service publishes.
type sessionCreateRecorder struct {
	dbstore.Queries
}

func (sessionCreateRecorder) GetBotByID(_ context.Context, id pgtype.UUID) (sqlc.GetBotByIDRow, error) {
	return sqlc.GetBotByIDRow{ID: id, IsActive: true}, nil
}

func (sessionCreateRecorder) CreateSession(_ context.Context, arg sqlc.CreateSessionParams) (sqlc.BotSession, error) {
	return sqlc.BotSession{
		ID:        testUUID("22222222-2222-2222-2222-222222222222"),
		BotID:     arg.BotID,
		Type:      arg.Type,
		Title:     arg.Title,
		Metadata:  arg.Metadata,
		CreatedAt: pgtype.Timestamptz{Time: time.Unix(1700000000, 0).UTC(), Valid: true},
		UpdatedAt: pgtype.Timestamptz{Valid: true},
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

	svc := session.NewService(nil, sessionCreateRecorder{}, hub)

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

	svc := session.NewService(nil, sessionCreateRecorder{}, nil)
	if _, err := svc.Create(context.Background(), session.CreateInput{
		BotID: "11111111-1111-1111-1111-111111111111",
		Type:  session.TypeChat,
	}); err != nil {
		t.Fatalf("Create: %v", err)
	}
}
