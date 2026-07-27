package message

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	dbpkg "github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
)

func TestPostgresReplaceTurnRetryHidesSupersededAssistant(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	user, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"hello"}`),
	})
	if err != nil {
		t.Fatalf("persist user: %v", err)
	}
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "assistant",
		Content:   []byte(`{"role":"assistant","content":"old"}`),
	})
	if err != nil {
		t.Fatalf("persist assistant: %v", err)
	}
	oldTurn, err := svc.GetVisibleTurnByMessage(ctx, postgresMessageTestSessionID, assistant.ID)
	if err != nil {
		t.Fatalf("get old turn: %v", err)
	}
	replacement, err := svc.Persist(ctx, PersistInput{
		BotID:           postgresMessageTestBotID,
		SessionID:       postgresMessageTestSessionID,
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"new"}`),
		SkipHistoryTurn: true,
	})
	if err != nil {
		t.Fatalf("persist replacement assistant: %v", err)
	}
	if _, err := svc.ReplaceTurn(ctx, postgresMessageTestSessionID, oldTurn.ID, user.ID, replacement.ID, "retry"); err != nil {
		t.Fatalf("replace turn: %v", err)
	}

	assertPostgresVisibleMessageIDs(t, ctx, svc, user.ID, replacement.ID)
	assertPostgresMessageVisibility(t, ctx, tx, assistant.ID, false, true)
}

func TestPostgresReplaceTurnAdvancesSessionCompactionEpoch(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	user, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"hello"}`),
	})
	if err != nil {
		t.Fatalf("persist user: %v", err)
	}
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "assistant",
		Content:   []byte(`{"role":"assistant","content":"old"}`),
	})
	if err != nil {
		t.Fatalf("persist assistant: %v", err)
	}
	oldTurn, err := svc.GetVisibleTurnByMessage(ctx, postgresMessageTestSessionID, assistant.ID)
	if err != nil {
		t.Fatalf("get old turn: %v", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary)
		VALUES
			('00000000-0000-0000-0000-000000074131', $1, $2, 'ok', 'covers replaced source'),
			('00000000-0000-0000-0000-000000074132', $1, $2, 'ok', 'same session frontier')
	`, postgresMessageTestBotID, postgresMessageTestSessionID); err != nil {
		t.Fatalf("insert compaction artifacts: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bot_history_messages
		SET compact_id = '00000000-0000-0000-0000-000000074131'
		WHERE id = $1
	`, assistant.ID); err != nil {
		t.Fatalf("link assistant to compaction artifact: %v", err)
	}

	replacement, err := svc.Persist(ctx, PersistInput{
		BotID:           postgresMessageTestBotID,
		SessionID:       postgresMessageTestSessionID,
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"new"}`),
		SkipHistoryTurn: true,
	})
	if err != nil {
		t.Fatalf("persist replacement assistant: %v", err)
	}
	if _, err := svc.ReplaceTurn(ctx, postgresMessageTestSessionID, oldTurn.ID, user.ID, replacement.ID, "retry"); err != nil {
		t.Fatalf("replace turn: %v", err)
	}

	var sessionEpoch int64
	if err := tx.QueryRow(ctx, `SELECT compaction_epoch FROM bot_sessions WHERE id = $1`, postgresMessageTestSessionID).Scan(&sessionEpoch); err != nil {
		t.Fatalf("read session compaction epoch: %v", err)
	}
	if sessionEpoch != 1 {
		var compactID *string
		var artifactEpoch *int64
		if err := tx.QueryRow(ctx, `
			SELECT message.compact_id::text, compact.compaction_epoch
			FROM bot_history_messages message
			LEFT JOIN bot_history_message_compacts compact ON compact.id = message.compact_id
			WHERE message.id = $1
		`, assistant.ID).Scan(&compactID, &artifactEpoch); err != nil {
			t.Fatalf("diagnose unchanged compaction epoch: %v", err)
		}
		compactValue := "<nil>"
		if compactID != nil {
			compactValue = *compactID
		}
		artifactValue := int64(-1)
		if artifactEpoch != nil {
			artifactValue = *artifactEpoch
		}
		t.Fatalf("session compaction epoch = %d, want 1 (message compact_id=%s artifact_epoch=%d)", sessionEpoch, compactValue, artifactValue)
	}
	var currentArtifacts int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM bot_history_message_compacts compact
		JOIN bot_sessions session ON session.id = compact.session_id
		WHERE compact.session_id = $1
		  AND compact.compaction_epoch = session.compaction_epoch
	`, postgresMessageTestSessionID).Scan(&currentArtifacts); err != nil {
		t.Fatalf("count current compaction artifacts: %v", err)
	}
	if currentArtifacts != 0 {
		t.Fatalf("current compaction artifacts = %d, want 0 after replacement", currentArtifacts)
	}
}

func TestPostgresReplaceTurnEditHidesSupersededTurn(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	oldUser, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"old prompt"}`),
	})
	if err != nil {
		t.Fatalf("persist old user: %v", err)
	}
	oldAssistant, err := svc.Persist(ctx, PersistInput{
		BotID:     postgresMessageTestBotID,
		SessionID: postgresMessageTestSessionID,
		Role:      "assistant",
		Content:   []byte(`{"role":"assistant","content":"old answer"}`),
	})
	if err != nil {
		t.Fatalf("persist old assistant: %v", err)
	}
	oldTurn, err := svc.GetVisibleTurnByMessage(ctx, postgresMessageTestSessionID, oldAssistant.ID)
	if err != nil {
		t.Fatalf("get old turn: %v", err)
	}
	newUser, err := svc.Persist(ctx, PersistInput{
		BotID:           postgresMessageTestBotID,
		SessionID:       postgresMessageTestSessionID,
		Role:            "user",
		Content:         []byte(`{"role":"user","content":"new prompt"}`),
		SkipHistoryTurn: true,
	})
	if err != nil {
		t.Fatalf("persist new user: %v", err)
	}
	newAssistant, err := svc.Persist(ctx, PersistInput{
		BotID:           postgresMessageTestBotID,
		SessionID:       postgresMessageTestSessionID,
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"new answer"}`),
		SkipHistoryTurn: true,
	})
	if err != nil {
		t.Fatalf("persist new assistant: %v", err)
	}
	if _, err := svc.ReplaceTurn(ctx, postgresMessageTestSessionID, oldTurn.ID, newUser.ID, newAssistant.ID, "edit"); err != nil {
		t.Fatalf("replace turn: %v", err)
	}

	assertPostgresVisibleMessageIDs(t, ctx, svc, newUser.ID, newAssistant.ID)
	assertPostgresMessageVisibility(t, ctx, tx, oldUser.ID, false, true)
	assertPostgresMessageVisibility(t, ctx, tx, oldAssistant.ID, false, true)
}

func TestPostgresRepairSupersededMessageVisibilityMigration(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresMessageTestTx(t, ctx)
	setupPostgresMessageTestFixtures(t, ctx, tx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, turn_superseded_at
		)
		VALUES
			(
				'00000000-0000-0000-0000-000000074111',
				$1, $2, 'assistant', '{"role":"assistant","content":"bad"}'::jsonb,
				'00000000-0000-0000-0000-000000074121', 1, 2, true, now()
			),
			(
				'00000000-0000-0000-0000-000000074112',
				$1, $2, 'assistant', '{"role":"assistant","content":"good"}'::jsonb,
				'00000000-0000-0000-0000-000000074122', 2, 2, true, NULL
			)
	`, postgresMessageTestBotID, postgresMessageTestSessionID); err != nil {
		t.Fatalf("insert repair fixtures: %v", err)
	}

	sql, err := os.ReadFile("../../../db/postgres/migrations/0105_repair_superseded_message_visibility.up.sql")
	if err != nil {
		t.Fatalf("read repair migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("run repair migration: %v", err)
	}

	assertPostgresMessageVisibility(t, ctx, tx, "00000000-0000-0000-0000-000000074111", false, true)
	assertPostgresMessageVisibility(t, ctx, tx, "00000000-0000-0000-0000-000000074112", true, false)
}

const (
	postgresMessageTestUserID    = "00000000-0000-0000-0000-000000074101"
	postgresMessageTestBotID     = "00000000-0000-0000-0000-000000074102"
	postgresMessageTestSessionID = "00000000-0000-0000-0000-000000074103"
)

func beginPostgresMessageTestTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip postgres integration test: TEST_POSTGRES_DSN is not set")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect to configured postgres integration database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping configured postgres integration database: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return tx
}

func setupPostgresMessageTestFixtures(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	name := fmt.Sprintf("postgres-message-test-%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true) RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, postgresMessageTestUserID, name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name)
		VALUES ($1, $2, $3)
	`, postgresMessageTestBotID, postgresMessageTestUserID, name); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type)
		VALUES ($1, $2, 'local')
	`, postgresMessageTestSessionID, postgresMessageTestBotID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
}

func assertPostgresVisibleMessageIDs(t *testing.T, ctx context.Context, svc *DBService, want ...string) {
	t.Helper()
	messages, err := svc.ListBySession(ctx, postgresMessageTestSessionID)
	if err != nil {
		t.Fatalf("list session messages: %v", err)
	}
	got := make([]string, 0, len(messages))
	for _, msg := range messages {
		got = append(got, msg.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("visible message ids = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("visible message ids = %#v, want %#v", got, want)
		}
	}
}

func assertPostgresMessageVisibility(t *testing.T, ctx context.Context, tx pgx.Tx, messageID string, wantVisible bool, wantSuperseded bool) {
	t.Helper()
	pgMessageID, err := dbpkg.ParseUUID(messageID)
	if err != nil {
		t.Fatalf("parse message id: %v", err)
	}
	var visible bool
	var superseded bool
	if err := tx.QueryRow(ctx, `
		SELECT turn_visible, turn_superseded_at IS NOT NULL
		FROM bot_history_messages
		WHERE id = $1
	`, pgMessageID).Scan(&visible, &superseded); err != nil {
		t.Fatalf("read message visibility: %v", err)
	}
	if visible != wantVisible || superseded != wantSuperseded {
		t.Fatalf("message %s visible/superseded = %v/%v, want %v/%v", messageID, visible, superseded, wantVisible, wantSuperseded)
	}
}
