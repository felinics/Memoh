package backup

import (
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	chatbackup "github.com/memohai/memoh/domains/agent/chat/backup"
	"github.com/memohai/memoh/internal/db"
)

func TestPostgresImportPreservesHistorySemanticsAtomically(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip postgres integration test: TEST_POSTGRES_DSN is not set")
	}
	ctx := t.Context()
	pool, err := db.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip postgres integration test: cannot connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skip postgres integration test: database ping failed: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	name := "chat-backup-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO iam.users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO iam.team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create fixture user: %v", err)
	}
	if _, err := pool.Exec(ctx,
		"INSERT INTO api.bots (id, owner_user_id, name) VALUES ($1, $2, $3)",
		botID, userID, name,
	); err != nil {
		t.Fatalf("create fixture bot: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM api.bots WHERE id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM iam.users WHERE id = $1", userID)
	})

	store, err := New(pool, exclusiveBotLock{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	oldSessionID := uuid.NewString()
	oldRequestID := uuid.NewString()
	oldAssistantID := uuid.NewString()
	oldReplacementID := uuid.NewString()
	oldTurnID := uuid.NewString()
	oldReplacementTurnID := uuid.NewString()
	position := int64(1)
	requestSeq := int64(1)
	assistantSeq := int64(2)
	supersededAt := time.Now().UTC().Truncate(time.Microsecond)
	reason := "retry"
	request := chatbackup.ImportRequest{
		BotID:       botID,
		ActorUserID: userID,
		Sessions: []chatbackup.Session{{
			ID:              oldSessionID,
			BotID:           uuid.NewString(),
			Type:            "chat",
			SessionMode:     "chat",
			RuntimeType:     "model",
			RuntimeMetadata: json.RawMessage(`{}`),
			Metadata: json.RawMessage(fmt.Sprintf(
				`{"forked_from":{"session_id":"%s","message_id":"%s"}}`,
				oldSessionID, oldAssistantID,
			)),
		}},
		Messages: []chatbackup.Message{
			{
				ID: oldRequestID, SessionID: &oldSessionID, Role: "user",
				Content: json.RawMessage(`{"text":"request"}`), Metadata: json.RawMessage(`{}`),
				SessionMode: "chat", RuntimeType: "model", TurnID: &oldTurnID,
				TurnPosition: &position, TurnMessageSeq: &requestSeq, TurnVisible: false,
				TurnSupersededByTurnID: &oldReplacementTurnID,
				TurnSupersededAt:       &supersededAt,
				TurnSupersededReason:   &reason,
			},
			{
				ID: oldAssistantID, SessionID: &oldSessionID, Role: "assistant",
				Content: json.RawMessage(`{"text":"old"}`), Metadata: json.RawMessage(`{"model":"m"}`),
				SessionMode: "chat", RuntimeType: "model", TurnID: &oldTurnID,
				TurnPosition: &position, TurnMessageSeq: &assistantSeq, TurnVisible: false,
				TurnSupersededByTurnID: &oldReplacementTurnID,
				TurnSupersededAt:       &supersededAt,
				TurnSupersededReason:   &reason,
			},
			{
				ID: oldReplacementID, SessionID: &oldSessionID, Role: "assistant",
				Content: json.RawMessage(`{"text":"new"}`), Metadata: json.RawMessage(`{}`),
				SessionMode: "chat", RuntimeType: "model", TurnID: &oldReplacementTurnID,
				TurnPosition: &position, TurnMessageSeq: &assistantSeq, TurnVisible: true,
			},
		},
		Assets: []chatbackup.Asset{{
			RelID: uuid.NewString(), MessageID: oldAssistantID, Role: "attachment",
			Ordinal: 0, ContentHash: "sha256:test", Name: "file.txt",
			Metadata: json.RawMessage(`{"kind":"text"}`),
		}},
	}

	result, err := store.Import(ctx, request)
	if err != nil {
		t.Fatalf("Import() error = %v", err)
	}
	var (
		gotPosition      int64
		gotSequence      int64
		gotVisible       bool
		gotSupersededBy  string
		gotReason        string
		gotAssetMetadata []byte
		gotSessionMeta   []byte
	)
	if err := pool.QueryRow(ctx, `
		SELECT turn_position, turn_message_seq, turn_visible,
		       turn_superseded_by_turn_id::text, turn_superseded_reason
		FROM agent.bot_history_messages
		WHERE id = $1
	`, result.MessageIDs[oldAssistantID]).Scan(
		&gotPosition, &gotSequence, &gotVisible, &gotSupersededBy, &gotReason,
	); err != nil {
		t.Fatalf("read imported message: %v", err)
	}
	if gotPosition != position || gotSequence != assistantSeq || gotVisible {
		t.Fatalf("turn state = (%d,%d,%t)", gotPosition, gotSequence, gotVisible)
	}
	if gotSupersededBy != deterministicID(botID, "turn", oldReplacementTurnID) || gotReason != reason {
		t.Fatalf("supersession = (%s,%s)", gotSupersededBy, gotReason)
	}
	if err := pool.QueryRow(ctx,
		"SELECT metadata FROM agent.bot_history_message_assets WHERE message_id = $1",
		result.MessageIDs[oldAssistantID],
	).Scan(&gotAssetMetadata); err != nil {
		t.Fatalf("read imported asset: %v", err)
	}
	if string(gotAssetMetadata) != `{"kind": "text"}` && string(gotAssetMetadata) != `{"kind":"text"}` {
		t.Fatalf("asset metadata = %s", gotAssetMetadata)
	}
	if err := pool.QueryRow(ctx,
		"SELECT metadata FROM agent.bot_sessions WHERE id = $1",
		result.SessionIDs[oldSessionID],
	).Scan(&gotSessionMeta); err != nil {
		t.Fatalf("read imported session metadata: %v", err)
	}
	if !containsJSONValue(gotSessionMeta, "message_id", result.MessageIDs[oldAssistantID]) {
		t.Fatalf("session metadata was not rebound: %s", gotSessionMeta)
	}

	badSessionID := uuid.NewString()
	_, err = store.Import(ctx, chatbackup.ImportRequest{
		BotID:       botID,
		ActorUserID: userID,
		Sessions: []chatbackup.Session{{
			ID: badSessionID, Type: "chat", SessionMode: "chat", RuntimeType: "model",
			RuntimeMetadata: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`),
		}},
		Messages: []chatbackup.Message{{
			ID: uuid.NewString(), SessionID: &badSessionID, Role: "invalid",
			Content: json.RawMessage(`{}`), Metadata: json.RawMessage(`{}`),
			SessionMode: "chat", RuntimeType: "model",
		}},
	})
	if err == nil {
		t.Fatal("invalid import error = nil")
	}
	var count int
	if err := pool.QueryRow(ctx,
		"SELECT count(*) FROM agent.bot_sessions WHERE id = $1",
		deterministicID(botID, "session", badSessionID),
	).Scan(&count); err != nil {
		t.Fatalf("count rolled back session: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled back session count = %d, want 0", count)
	}
}

func containsJSONValue(raw []byte, key, want string) bool {
	var value map[string]any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	fork, _ := value["forked_from"].(map[string]any)
	got, _ := fork[key].(string)
	return got == want
}
