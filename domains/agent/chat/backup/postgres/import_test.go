package postgres

import (
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
)

func TestNewRequiresPool(t *testing.T) {
	if _, err := New(nil, exclusiveBotLock{}); err == nil {
		t.Fatal("New(nil) error = nil")
	}
}

func TestNewRequiresBotLock(t *testing.T) {
	if _, err := New(&pgxpool.Pool{}, nil); err == nil {
		t.Fatal("New(..., nil) error = nil")
	}
}

func TestReconstructLegacyTurns(t *testing.T) {
	sessionID := "old-session"
	messages := []chatbackup.Message{
		{ID: "user-1", SessionID: &sessionID, Role: "user"},
		{ID: "assistant-1", SessionID: &sessionID, Role: "assistant"},
		{ID: "tool-1", SessionID: &sessionID, Role: "tool"},
		{ID: "assistant-2", SessionID: &sessionID, Role: "assistant"},
		{ID: "user-2", SessionID: &sessionID, Role: "user"},
	}

	got := reconstructLegacyTurns(messages)
	wantSeq := []int64{1, 2, 3, 4, 1}
	wantPosition := []int64{1, 1, 1, 1, 2}
	for i := range got {
		if got[i].TurnMessageSeq == nil || *got[i].TurnMessageSeq != wantSeq[i] {
			t.Fatalf("message %d sequence = %v, want %d", i, got[i].TurnMessageSeq, wantSeq[i])
		}
		if got[i].TurnPosition == nil || *got[i].TurnPosition != wantPosition[i] {
			t.Fatalf("message %d position = %v, want %d", i, got[i].TurnPosition, wantPosition[i])
		}
		if !got[i].TurnVisible {
			t.Fatalf("message %d was not made visible", i)
		}
	}
	if *got[0].TurnID != *got[3].TurnID {
		t.Fatal("first round messages received different turn ids")
	}
	if *got[0].TurnID == *got[4].TurnID {
		t.Fatal("separate rounds received the same turn id")
	}
}

func TestReconstructLegacyTurnsPreservesExplicitTurn(t *testing.T) {
	sessionID := "old-session"
	turnID := "old-turn"
	position := int64(7)
	sequence := int64(2)
	messages := []chatbackup.Message{{
		ID:             "assistant",
		SessionID:      &sessionID,
		Role:           "assistant",
		TurnID:         &turnID,
		TurnPosition:   &position,
		TurnMessageSeq: &sequence,
		TurnVisible:    false,
	}}

	got := reconstructLegacyTurns(messages)
	if *got[0].TurnID != turnID || *got[0].TurnPosition != position || *got[0].TurnMessageSeq != sequence {
		t.Fatalf("explicit turn changed: %+v", got[0])
	}
	if got[0].TurnVisible {
		t.Fatal("explicit hidden turn became visible")
	}
}

func TestRebindForkMetadata(t *testing.T) {
	raw := json.RawMessage(`{"forked_from":{"session_id":"old-session","message_id":"old-message","fork_message_id":"old-fork"}}`)
	got := rebindForkMetadata(raw,
		map[string]string{"old-session": "new-session"},
		map[string]string{"old-message": "new-message", "old-fork": "new-fork"},
	)
	var decoded struct {
		ForkedFrom struct {
			SessionID     string `json:"session_id"`
			MessageID     string `json:"message_id"`
			ForkMessageID string `json:"fork_message_id"`
		} `json:"forked_from"`
	}
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatalf("unmarshal rebound metadata: %v", err)
	}
	if decoded.ForkedFrom.SessionID != "new-session" ||
		decoded.ForkedFrom.MessageID != "new-message" ||
		decoded.ForkedFrom.ForkMessageID != "new-fork" {
		t.Fatalf("rebound metadata = %+v", decoded.ForkedFrom)
	}
}

func TestDeterministicIDIsStableAndTyped(t *testing.T) {
	botID := "00000000-0000-0000-0000-000000000001"
	first := deterministicID(botID, "message", "source")
	if second := deterministicID(botID, "message", "source"); second != first {
		t.Fatalf("deterministic id changed: %s != %s", first, second)
	}
	if other := deterministicID(botID, "event", "source"); other == first {
		t.Fatal("different owner record kinds received the same id")
	}
}
