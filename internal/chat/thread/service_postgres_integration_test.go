package thread

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	message "github.com/memohai/memoh/internal/chat/message"
	dbpkg "github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
)

func TestPostgresForkFromAssistantMessageCopiesVisibleTurns(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)), nil)
	fork, err := svc.ForkFromAssistantMessage(ctx, ForkFromAssistantInput{
		BotID:           postgresSessionTestBotID,
		ThreadID:        postgresSessionTestSessionID,
		MessageID:       postgresSessionTestAssistant2ID,
		Title:           "Forked",
		CreatedByUserID: postgresSessionTestUserID,
	})
	if err != nil {
		t.Fatalf("fork assistant message: %v", err)
	}

	forkedFrom, ok := fork.Metadata["forked_from"].(map[string]any)
	if !ok {
		t.Fatalf("fork metadata missing forked_from: %#v", fork.Metadata)
	}
	if got := forkedFrom["session_id"]; got != postgresSessionTestSessionID {
		t.Fatalf("fork source session_id = %#v, want %s", got, postgresSessionTestSessionID)
	}
	if got := forkedFrom["message_id"]; got != postgresSessionTestAssistant2ID {
		t.Fatalf("fork source message_id = %#v, want %s", got, postgresSessionTestAssistant2ID)
	}
	forkMessageID, _ := forkedFrom["fork_message_id"].(string)
	if forkMessageID == "" || forkMessageID == postgresSessionTestAssistant2ID {
		t.Fatalf("fork_message_id = %#v, want copied assistant message id", forkedFrom["fork_message_id"])
	}

	var nextTurnPosition int64
	if err := tx.QueryRow(ctx, `
		SELECT next_turn_position
		FROM bot_sessions
		WHERE id = $1
	`, fork.ID).Scan(&nextTurnPosition); err != nil {
		t.Fatalf("read fork next_turn_position: %v", err)
	}
	if nextTurnPosition != 3 {
		t.Fatalf("next_turn_position = %d, want 3", nextTurnPosition)
	}

	rows, err := tx.Query(ctx, `
		SELECT role, turn_position, turn_message_seq
		FROM bot_history_messages
		WHERE session_id = $1
		ORDER BY turn_position ASC, turn_message_seq ASC
	`, fork.ID)
	if err != nil {
		t.Fatalf("list fork messages: %v", err)
	}
	var got []string
	for rows.Next() {
		var role string
		var position, seq int64
		if err := rows.Scan(&role, &position, &seq); err != nil {
			t.Fatalf("scan fork message: %v", err)
		}
		got = append(got, fmt.Sprintf("%s:%d:%d", role, position, seq))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fork messages: %v", err)
	}
	rows.Close()
	want := []string{"user:1:1", "assistant:1:2", "user:2:1", "assistant:2:2"}
	if len(got) != len(want) {
		t.Fatalf("fork messages = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fork messages = %#v, want %#v", got, want)
		}
	}

	var anchorRole string
	var anchorPosition, anchorSeq int64
	if err := tx.QueryRow(ctx, `
		SELECT role, turn_position, turn_message_seq
		FROM bot_history_messages
		WHERE id = $1 AND session_id = $2
	`, forkMessageID, fork.ID).Scan(&anchorRole, &anchorPosition, &anchorSeq); err != nil {
		t.Fatalf("read fork anchor message: %v", err)
	}
	if anchorRole != "assistant" || anchorPosition != 2 || anchorSeq != 2 {
		t.Fatalf("fork anchor = %s:%d:%d, want assistant:2:2", anchorRole, anchorPosition, anchorSeq)
	}
	var anchorAssetCount int
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM bot_history_message_assets
		WHERE message_id = $1
		  AND role = 'attachment'
		  AND ordinal = 1
		  AND content_hash = 'sha256:postgres-session-test-asset'
		  AND name = 'fixture.txt'
	`, forkMessageID).Scan(&anchorAssetCount); err != nil {
		t.Fatalf("count fork anchor assets: %v", err)
	}
	if anchorAssetCount != 1 {
		t.Fatalf("fork anchor asset count = %d, want 1", anchorAssetCount)
	}

	messageSvc := message.NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	newUser, err := messageSvc.Persist(ctx, message.PersistInput{
		BotID:     postgresSessionTestBotID,
		SessionID: fork.ID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"third"}`),
	})
	if err != nil {
		t.Fatalf("persist fork follow-up user: %v", err)
	}
	newAssistant, err := messageSvc.Persist(ctx, message.PersistInput{
		BotID:                postgresSessionTestBotID,
		SessionID:            fork.ID,
		Role:                 "assistant",
		TurnRequestMessageID: newUser.ID,
		Content:              []byte(`{"role":"assistant","content":"third reply"}`),
	})
	if err != nil {
		t.Fatalf("persist fork follow-up assistant: %v", err)
	}

	rows, err = tx.Query(ctx, `
		SELECT id, role, turn_position, turn_message_seq
		FROM bot_history_messages
		WHERE id = ANY($1::uuid[])
		ORDER BY turn_message_seq ASC
	`, []string{newUser.ID, newAssistant.ID})
	if err != nil {
		t.Fatalf("list fork follow-up messages: %v", err)
	}
	got = got[:0]
	for rows.Next() {
		var id string
		var role string
		var position, seq int64
		if err := rows.Scan(&id, &role, &position, &seq); err != nil {
			t.Fatalf("scan fork follow-up message: %v", err)
		}
		got = append(got, fmt.Sprintf("%s:%d:%d", role, position, seq))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fork follow-up messages: %v", err)
	}
	rows.Close()
	want = []string{"user:3:1", "assistant:3:2"}
	if len(got) != len(want) {
		t.Fatalf("fork follow-up messages = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fork follow-up messages = %#v, want %#v", got, want)
		}
	}
}

func TestPostgresCreateSubagentStoresForkContextAsHiddenHistory(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)
	legacySessionID := "00000000-0000-0000-0000-000000075122"
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, type, title, parent_session_id, metadata)
		VALUES ($1, $2, 'subagent', 'Legacy worker', $3, '{"agent_id":"legacy-worker"}'::jsonb)
	`, legacySessionID, postgresSessionTestBotID, postgresSessionTestSessionID); err != nil {
		t.Fatalf("insert configless legacy subagent: %v", err)
	}

	providerID := "00000000-0000-0000-0000-000000075120"
	modelID := "00000000-0000-0000-0000-000000075121"
	name := fmt.Sprintf("postgres-subagent-test-%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `
		INSERT INTO providers (id, name, client_type)
		VALUES ($1, $2, 'openai-completions')
	`, providerID, name); err != nil {
		t.Fatalf("insert subagent provider: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO models (id, model_id, provider_id, type)
		VALUES ($1, 'subagent-test-model', $2, 'chat')
	`, modelID, providerID); err != nil {
		t.Fatalf("insert subagent model: %v", err)
	}

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)), nil)
	child, config, err := svc.CreateSubagent(ctx, CreateSubagentInput{
		Thread: CreateInput{
			BotID:           postgresSessionTestBotID,
			ParentThreadID:  postgresSessionTestSessionID,
			CreatedByUserID: postgresSessionTestUserID,
			Title:           "Forked worker",
			Metadata:        map[string]any{"agent_id": "worker"},
		},
		ModelUUID:    modelID,
		ModelID:      "subagent-test-model",
		ProviderName: name,
		Forked:       true,
		ForkContext: []SubagentForkContextMessage{
			{
				SourceMessageID: postgresSessionTestAssistant2ID,
				Role:            "assistant",
				Message:         []byte(`{"role":"assistant","content":[{"type":"text","text":"second reply"}]}`),
			},
			{
				Role:    "system",
				Message: []byte(`{"role":"system","content":[{"type":"text","text":"runtime workspace context"}]}`),
			},
		},
	})
	if err != nil {
		t.Fatalf("create forked subagent: %v", err)
	}
	if !config.Forked || config.ThreadID != child.ID {
		t.Fatalf("unexpected subagent config: %+v", config)
	}
	listed, err := svc.ListSubagentsByParent(ctx, postgresSessionTestSessionID)
	if err != nil {
		t.Fatalf("list managed subagents: %v", err)
	}
	if len(listed) != 1 || listed[0].ID != child.ID {
		t.Fatalf("configless legacy subagent was not ignored: %+v", listed)
	}

	contextMessages, err := svc.ListSubagentForkContext(ctx, child.ID)
	if err != nil {
		t.Fatalf("list subagent fork context: %v", err)
	}
	if len(contextMessages) != 2 || contextMessages[0].Role != "assistant" || contextMessages[1].Role != "system" {
		t.Fatalf("unexpected fork context: %+v", contextMessages)
	}

	messageSvc := message.NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)))
	visible, err := messageSvc.ListBySession(ctx, child.ID)
	if err != nil {
		t.Fatalf("list visible child history: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("fork context leaked into visible history: %+v", visible)
	}

	var hiddenCount, copiedAssetCount int
	var nextTurnPosition int64
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM bot_history_messages
		WHERE session_id = $1
		  AND turn_visible = false
		  AND session_mode = 'subagent'
		  AND metadata->>'context_scope' = 'subagent_fork'
	`, child.ID).Scan(&hiddenCount); err != nil {
		t.Fatalf("count hidden fork context: %v", err)
	}
	if hiddenCount != 2 {
		t.Fatalf("hidden fork context count = %d, want 2", hiddenCount)
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*)
		FROM bot_history_message_assets asset
		JOIN bot_history_messages message ON message.id = asset.message_id
		WHERE message.session_id = $1
		  AND message.metadata->>'source_message_id' = $2
		  AND asset.content_hash = 'sha256:postgres-session-test-asset'
	`, child.ID, postgresSessionTestAssistant2ID).Scan(&copiedAssetCount); err != nil {
		t.Fatalf("count copied fork assets: %v", err)
	}
	if copiedAssetCount != 1 {
		t.Fatalf("copied fork asset count = %d, want 1", copiedAssetCount)
	}
	if err := tx.QueryRow(ctx, `SELECT next_turn_position FROM bot_sessions WHERE id = $1`, child.ID).Scan(&nextTurnPosition); err != nil {
		t.Fatalf("read child next turn position: %v", err)
	}
	if nextTurnPosition != 3 {
		t.Fatalf("next_turn_position = %d, want 3", nextTurnPosition)
	}

	firstVisible, err := messageSvc.Persist(ctx, message.PersistInput{
		BotID:       postgresSessionTestBotID,
		SessionID:   child.ID,
		Role:        "user",
		Content:     []byte(`{"role":"user","content":"child task"}`),
		SessionMode: TypeSubagent,
	})
	if err != nil {
		t.Fatalf("persist first visible child message: %v", err)
	}
	var visiblePosition int64
	if err := tx.QueryRow(ctx, `SELECT turn_position FROM bot_history_messages WHERE id = $1`, firstVisible.ID).Scan(&visiblePosition); err != nil {
		t.Fatalf("read first visible child position: %v", err)
	}
	if visiblePosition != 3 {
		t.Fatalf("first visible child position = %d, want 3", visiblePosition)
	}
}

func TestPostgresForkFromAssistantMessageRejectsInvalidTargetWithoutSideEffects(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(tx)), nil)
	beforeSessions := countPostgresSessionTestRows(t, ctx, tx, "bot_sessions")
	beforeMessages := countPostgresSessionTestRows(t, ctx, tx, "bot_history_messages")

	for _, messageID := range []string{postgresSessionTestUser1ID, postgresSessionTestHiddenAssistantID} {
		_, err := svc.ForkFromAssistantMessage(ctx, ForkFromAssistantInput{
			BotID:     postgresSessionTestBotID,
			ThreadID:  postgresSessionTestSessionID,
			MessageID: messageID,
			Title:     "Should Not Exist",
		})
		if !errors.Is(err, ErrForkSourceNotReply) {
			t.Fatalf("fork invalid message %s error = %v, want ErrForkSourceNotReply", messageID, err)
		}

		if got := countPostgresSessionTestRows(t, ctx, tx, "bot_sessions"); got != beforeSessions {
			t.Fatalf("bot_sessions count after invalid fork = %d, want %d", got, beforeSessions)
		}
		if got := countPostgresSessionTestRows(t, ctx, tx, "bot_history_messages"); got != beforeMessages {
			t.Fatalf("bot_history_messages count after invalid fork = %d, want %d", got, beforeMessages)
		}
	}
}

func TestPostgresRepairIncompleteForkSessionsMigration(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	const (
		pureResidualID      = "00000000-0000-0000-0000-000000075301"
		withFollowUpID      = "00000000-0000-0000-0000-000000075302"
		validForkID         = "00000000-0000-0000-0000-000000075303"
		pureResidualMessage = "00000000-0000-0000-0000-000000075311"
		followUpCopyMessage = "00000000-0000-0000-0000-000000075312"
		followUpNewMessage  = "00000000-0000-0000-0000-000000075313"
		validForkMessage    = "00000000-0000-0000-0000-000000075314"
		followUpCopySecond  = "00000000-0000-0000-0000-000000075315"
		followUpNewReply    = "00000000-0000-0000-0000-000000075316"
	)
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata, created_at, updated_at)
		VALUES
			(
				$2, $1, 'local', 'bad fork',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
				now(), now()
			),
			(
				$3, $1, 'local', 'bad fork with follow-up',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
				now(), now()
			),
			(
				$4, $1, 'local', 'valid fork',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114","fork_message_id":"00000000-0000-0000-0000-000000075314"}}'::jsonb,
				now(), now()
			)
	`, postgresSessionTestBotID, pureResidualID, withFollowUpID, validForkID); err != nil {
		t.Fatalf("insert repair fork sessions: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, created_at
		)
		VALUES
			(
				$3, $1, $2, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075321', 1, 2, true,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $2)
			),
			(
				$5, $1, $4, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075322', 1, 2, true,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $4)
			),
			(
				$9, $1, $4, 'assistant', '{"role":"assistant","content":"copied second"}'::jsonb,
				'00000000-0000-0000-0000-000000075325', 2, 2, true,
				(SELECT created_at - interval '30 seconds' FROM bot_sessions WHERE id = $4)
			),
			(
				$6, $1, $4, 'user', '{"role":"user","content":"follow-up"}'::jsonb,
				'00000000-0000-0000-0000-000000075323', 1, 1, true,
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $4)
			),
			(
				$10, $1, $4, 'assistant', '{"role":"assistant","content":"follow-up reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075323', 1, 2, true,
				(SELECT created_at + interval '2 minutes' FROM bot_sessions WHERE id = $4)
			),
			(
				$8, $1, $7, 'assistant', '{"role":"assistant","content":"valid copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075324', 1, 2, true,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $7)
			)
	`, postgresSessionTestBotID, pureResidualID, pureResidualMessage, withFollowUpID, followUpCopyMessage, followUpNewMessage, validForkID, validForkMessage, followUpCopySecond, followUpNewReply); err != nil {
		t.Fatalf("insert repair fork messages: %v", err)
	}

	sql, err := os.ReadFile("../../../db/postgres/migrations/0105_repair_superseded_message_visibility.up.sql")
	if err != nil {
		t.Fatalf("read fork repair migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("run fork repair migration: %v", err)
	}

	assertPostgresSessionDeleted(t, ctx, tx, pureResidualID, true)
	assertPostgresSessionDeleted(t, ctx, tx, withFollowUpID, false)
	assertPostgresSessionDeleted(t, ctx, tx, validForkID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpCopyMessage, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpCopySecond, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpNewMessage, 3)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpNewReply, 3)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, withFollowUpID, 4)
}

func TestPostgresRepairIncompleteForkRetriedCopiedTurnMigration(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	const (
		sessionID            = "00000000-0000-0000-0000-000000075401"
		copiedUser1          = "00000000-0000-0000-0000-000000075411"
		copiedAssistant1     = "00000000-0000-0000-0000-000000075412"
		reusedUser2          = "00000000-0000-0000-0000-000000075413"
		supersededAssistant2 = "00000000-0000-0000-0000-000000075414"
		replacementAssistant = "00000000-0000-0000-0000-000000075415"
	)
	// Broken fork left by the old fork query, then retried with the old
	// ReplaceHistoryTurn: the replacement turn reused the copied user message
	// (which keeps its pre-fork created_at), got position 1 from the broken
	// allocator, and the superseded copied assistant stayed visible.
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata, next_turn_position, created_at, updated_at)
		VALUES (
			$2, $1, 'local', 'retried broken fork',
			'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
			2, now(), now()
		)
	`, postgresSessionTestBotID, sessionID); err != nil {
		t.Fatalf("insert retried fork session: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, turn_superseded_at, created_at
		)
		VALUES
			(
				$3, $1, $2, 'user', '{"role":"user","content":"copied first"}'::jsonb,
				'00000000-0000-0000-0000-000000075421', 1, 1, true, NULL,
				(SELECT created_at - interval '4 minutes' FROM bot_sessions WHERE id = $2)
			),
			(
				$4, $1, $2, 'assistant', '{"role":"assistant","content":"copied first reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075421', 1, 2, true, NULL,
				(SELECT created_at - interval '4 minutes' FROM bot_sessions WHERE id = $2)
			),
			(
				$5, $1, $2, 'user', '{"role":"user","content":"copied second"}'::jsonb,
				'00000000-0000-0000-0000-000000075423', 1, 1, true, NULL,
				(SELECT created_at - interval '2 minutes' FROM bot_sessions WHERE id = $2)
			),
			(
				$6, $1, $2, 'assistant', '{"role":"assistant","content":"superseded reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075422', 2, 2, true,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $2),
				(SELECT created_at - interval '2 minutes' FROM bot_sessions WHERE id = $2)
			),
			(
				$7, $1, $2, 'assistant', '{"role":"assistant","content":"retry reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075423', 1, 2, true, NULL,
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $2)
			)
	`, postgresSessionTestBotID, sessionID, copiedUser1, copiedAssistant1, reusedUser2, supersededAssistant2, replacementAssistant); err != nil {
		t.Fatalf("insert retried fork messages: %v", err)
	}

	sql, err := os.ReadFile("../../../db/postgres/migrations/0105_repair_superseded_message_visibility.up.sql")
	if err != nil {
		t.Fatalf("read fork repair migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("run fork repair migration: %v", err)
	}

	assertPostgresSessionDeleted(t, ctx, tx, sessionID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, copiedUser1, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, copiedAssistant1, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, supersededAssistant2, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, reusedUser2, 3)
	assertPostgresMessageTurnPosition(t, ctx, tx, replacementAssistant, 3)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, sessionID, 4)
	assertPostgresSessionRepairMarker(t, ctx, tx, sessionID, "0105_incomplete_fork_turn_positions", false)

	var supersededVisible bool
	if err := tx.QueryRow(ctx, `
		SELECT turn_visible
		FROM bot_history_messages
		WHERE id = $1
	`, supersededAssistant2).Scan(&supersededVisible); err != nil {
		t.Fatalf("read superseded assistant visibility: %v", err)
	}
	if supersededVisible {
		t.Fatalf("superseded copied assistant is still visible after repair")
	}

	// Re-running the repair must be a no-op for the already repaired session.
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("re-run fork repair migration: %v", err)
	}
	assertPostgresMessageTurnPosition(t, ctx, tx, reusedUser2, 3)
	assertPostgresMessageTurnPosition(t, ctx, tx, replacementAssistant, 3)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, sessionID, 4)
}

func TestPostgresRepairIncompleteForkTurnlessFollowUpMigration(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	const (
		sessionID       = "00000000-0000-0000-0000-000000075501"
		copiedAssistant = "00000000-0000-0000-0000-000000075511"
		turnlessMessage = "00000000-0000-0000-0000-000000075512"
	)
	// Broken fork the user continued, but the follow-up message never got a
	// turn (e.g. the agent crashed before turn creation). There is nothing to
	// renumber, yet the allocator must still move past the copied prefix.
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata, created_at, updated_at)
		VALUES (
			$2, $1, 'local', 'turnless follow-up fork',
			'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
			now(), now()
		)
	`, postgresSessionTestBotID, sessionID); err != nil {
		t.Fatalf("insert turnless fork session: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, created_at
		)
		VALUES
			(
				$3, $1, $2, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075521', 1, 2, true,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $2)
			),
			(
				$4, $1, $2, 'user', '{"role":"user","content":"follow-up"}'::jsonb,
				NULL, NULL, NULL, false,
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $2)
			)
	`, postgresSessionTestBotID, sessionID, copiedAssistant, turnlessMessage); err != nil {
		t.Fatalf("insert turnless fork messages: %v", err)
	}

	sql, err := os.ReadFile("../../../db/postgres/migrations/0105_repair_superseded_message_visibility.up.sql")
	if err != nil {
		t.Fatalf("read fork repair migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("run fork repair migration: %v", err)
	}

	assertPostgresSessionDeleted(t, ctx, tx, sessionID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, copiedAssistant, 1)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, sessionID, 2)
	assertPostgresSessionRepairMarker(t, ctx, tx, sessionID, "0105_incomplete_fork_turn_positions", false)

	var turnless bool
	if err := tx.QueryRow(ctx, `
		SELECT turn_id IS NULL AND turn_position IS NULL
		FROM bot_history_messages
		WHERE id = $1
	`, turnlessMessage).Scan(&turnless); err != nil {
		t.Fatalf("read turnless follow-up message: %v", err)
	}
	if !turnless {
		t.Fatalf("turnless follow-up message gained turn state during repair")
	}
}

func TestPostgresRepairSkipsConsistentIncompleteForkMigration(t *testing.T) {
	ctx := context.Background()
	tx := beginPostgresSessionTestTx(t, ctx)
	setupPostgresSessionForkFixtures(t, ctx, tx)

	const (
		sessionID         = "00000000-0000-0000-0000-000000075601"
		prefixAssistant   = "00000000-0000-0000-0000-000000075611"
		followUpUser      = "00000000-0000-0000-0000-000000075612"
		followUpAssistant = "00000000-0000-0000-0000-000000075613"

		retriedSessionID  = "00000000-0000-0000-0000-000000075602"
		retriedPrefixMsg  = "00000000-0000-0000-0000-000000075614"
		retriedHiddenMsg  = "00000000-0000-0000-0000-000000075615"
		retriedReusedUser = "00000000-0000-0000-0000-000000075616"
		retriedNewReply   = "00000000-0000-0000-0000-000000075617"

		hiddenCollisionSessionID = "00000000-0000-0000-0000-000000075604"
		hiddenCollisionPrefix    = "00000000-0000-0000-0000-000000075618"
		hiddenCollisionHidden    = "00000000-0000-0000-0000-000000075619"
		hiddenCollisionUser      = "00000000-0000-0000-0000-00000007561a"
		hiddenCollisionVisible   = "00000000-0000-0000-0000-00000007561b"

		arrayMetadataSessionID = "00000000-0000-0000-0000-000000075603"
	)
	// Four sessions that match the missing-fork_message_id filter but must
	// not be repaired:
	// 1. positions and allocator already consistent
	// 2. healthy fork whose post-fork turn was retried with the NEW
	//    ReplaceHistoryTurn: the hidden superseded turn keeps its position and
	//    the replacement reuses the user message, so message-time ordering
	//    disagrees with positions — still consistent, must stay untouched
	// 3. a hidden superseded turn shares a position with a visible turn: the
	//    collision only exists among hidden rows, which never reach visible
	//    history or turn allocation, so it must not count as damage
	// 4. forked_from is a jsonb array, not an object (the ? operator matches
	//    array elements, so a shape guard is required)
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata, next_turn_position, created_at, updated_at)
		VALUES
			(
				$2, $1, 'local', 'consistent fork',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
				3, now(), now()
			),
			(
				$3, $1, 'local', 'retried consistent fork',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
				4, now(), now()
			),
			(
				$4, $1, 'local', 'array metadata',
				'{"forked_from":["session_id","message_id"]}'::jsonb,
				1, now(), now()
			),
			(
				$5, $1, 'local', 'hidden collision fork',
				'{"forked_from":{"session_id":"00000000-0000-0000-0000-000000075103","message_id":"00000000-0000-0000-0000-000000075114"}}'::jsonb,
				3, now(), now()
			)
	`, postgresSessionTestBotID, sessionID, retriedSessionID, arrayMetadataSessionID, hiddenCollisionSessionID); err != nil {
		t.Fatalf("insert consistent fork sessions: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, turn_superseded_at, created_at
		)
		VALUES
			(
				$3, $1, $2, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075621', 1, 2, true, NULL,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $2)
			),
			(
				$4, $1, $2, 'user', '{"role":"user","content":"follow-up"}'::jsonb,
				'00000000-0000-0000-0000-000000075622', 2, 1, true, NULL,
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $2)
			),
			(
				$5, $1, $2, 'assistant', '{"role":"assistant","content":"follow-up reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075622', 2, 2, true, NULL,
				(SELECT created_at + interval '2 minutes' FROM bot_sessions WHERE id = $2)
			),
			(
				$7, $1, $6, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075623', 1, 2, true, NULL,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $6)
			),
			(
				$8, $1, $6, 'assistant', '{"role":"assistant","content":"superseded reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075624', 2, 2, false,
				(SELECT created_at + interval '3 minutes' FROM bot_sessions WHERE id = $6),
				(SELECT created_at + interval '2 minutes' FROM bot_sessions WHERE id = $6)
			),
			(
				$9, $1, $6, 'user', '{"role":"user","content":"reused"}'::jsonb,
				'00000000-0000-0000-0000-000000075625', 3, 1, true, NULL,
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $6)
			),
			(
				$10, $1, $6, 'assistant', '{"role":"assistant","content":"retry reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075625', 3, 2, true, NULL,
				(SELECT created_at + interval '4 minutes' FROM bot_sessions WHERE id = $6)
			),
			(
				$12, $1, $11, 'assistant', '{"role":"assistant","content":"copied"}'::jsonb,
				'00000000-0000-0000-0000-000000075626', 1, 2, true, NULL,
				(SELECT created_at - interval '1 minute' FROM bot_sessions WHERE id = $11)
			),
			(
				$13, $1, $11, 'assistant', '{"role":"assistant","content":"superseded reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075627', 2, 2, false,
				(SELECT created_at + interval '3 minutes' FROM bot_sessions WHERE id = $11),
				(SELECT created_at + interval '1 minute' FROM bot_sessions WHERE id = $11)
			),
			(
				$14, $1, $11, 'user', '{"role":"user","content":"replacement"}'::jsonb,
				'00000000-0000-0000-0000-000000075628', 2, 1, true, NULL,
				(SELECT created_at + interval '2 minutes' FROM bot_sessions WHERE id = $11)
			),
			(
				$15, $1, $11, 'assistant', '{"role":"assistant","content":"replacement reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075628', 2, 2, true, NULL,
				(SELECT created_at + interval '3 minutes' FROM bot_sessions WHERE id = $11)
			)
	`, postgresSessionTestBotID, sessionID, prefixAssistant, followUpUser, followUpAssistant,
		retriedSessionID, retriedPrefixMsg, retriedHiddenMsg, retriedReusedUser, retriedNewReply,
		hiddenCollisionSessionID, hiddenCollisionPrefix, hiddenCollisionHidden, hiddenCollisionUser, hiddenCollisionVisible); err != nil {
		t.Fatalf("insert consistent fork messages: %v", err)
	}

	sql, err := os.ReadFile("../../../db/postgres/migrations/0105_repair_superseded_message_visibility.up.sql")
	if err != nil {
		t.Fatalf("read fork repair migration: %v", err)
	}
	if _, err := tx.Exec(ctx, string(sql)); err != nil {
		t.Fatalf("run fork repair migration: %v", err)
	}

	assertPostgresSessionDeleted(t, ctx, tx, sessionID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, prefixAssistant, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpUser, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, followUpAssistant, 2)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, sessionID, 3)
	assertPostgresSessionRepairMarker(t, ctx, tx, sessionID, "0105_incomplete_fork_turn_positions", false)

	assertPostgresSessionDeleted(t, ctx, tx, retriedSessionID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, retriedPrefixMsg, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, retriedHiddenMsg, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, retriedReusedUser, 3)
	assertPostgresMessageTurnPosition(t, ctx, tx, retriedNewReply, 3)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, retriedSessionID, 4)
	assertPostgresSessionRepairMarker(t, ctx, tx, retriedSessionID, "0105_incomplete_fork_turn_positions", false)

	assertPostgresSessionDeleted(t, ctx, tx, arrayMetadataSessionID, false)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, arrayMetadataSessionID, 1)
	assertPostgresSessionRepairMarker(t, ctx, tx, arrayMetadataSessionID, "0105_incomplete_fork_session", false)
	assertPostgresSessionRepairMarker(t, ctx, tx, arrayMetadataSessionID, "0105_incomplete_fork_turn_positions", false)

	assertPostgresSessionDeleted(t, ctx, tx, hiddenCollisionSessionID, false)
	assertPostgresMessageTurnPosition(t, ctx, tx, hiddenCollisionPrefix, 1)
	assertPostgresMessageTurnPosition(t, ctx, tx, hiddenCollisionHidden, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, hiddenCollisionUser, 2)
	assertPostgresMessageTurnPosition(t, ctx, tx, hiddenCollisionVisible, 2)
	assertPostgresSessionNextTurnPosition(t, ctx, tx, hiddenCollisionSessionID, 3)
	assertPostgresSessionRepairMarker(t, ctx, tx, hiddenCollisionSessionID, "0105_incomplete_fork_turn_positions", false)
}

const (
	postgresSessionTestUserID            = "00000000-0000-0000-0000-000000075101"
	postgresSessionTestBotID             = "00000000-0000-0000-0000-000000075102"
	postgresSessionTestSessionID         = "00000000-0000-0000-0000-000000075103"
	postgresSessionTestUser1ID           = "00000000-0000-0000-0000-000000075111"
	postgresSessionTestAssistant1ID      = "00000000-0000-0000-0000-000000075112"
	postgresSessionTestUser2ID           = "00000000-0000-0000-0000-000000075113"
	postgresSessionTestAssistant2ID      = "00000000-0000-0000-0000-000000075114"
	postgresSessionTestHiddenAssistantID = "00000000-0000-0000-0000-000000075115"
)

func beginPostgresSessionTestTx(t *testing.T, ctx context.Context) pgx.Tx {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("skip postgres integration test: TEST_POSTGRES_DSN is not set")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Skipf("skip postgres integration test: cannot connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Skipf("skip postgres integration test: database ping failed: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return tx
}

func setupPostgresSessionForkFixtures(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	name := fmt.Sprintf("postgres-session-test-%d", time.Now().UnixNano())
	if _, err := tx.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, postgresSessionTestUserID, name); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name)
		VALUES ($1, $2, $3)
	`, postgresSessionTestBotID, postgresSessionTestUserID, name); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata)
		VALUES ($1, $2, 'local', 'Source', '{"source":"fixture"}'::jsonb)
	`, postgresSessionTestSessionID, postgresSessionTestBotID); err != nil {
		t.Fatalf("insert session: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_messages (
			id, bot_id, session_id, role, content,
			turn_id, turn_position, turn_message_seq, turn_visible, turn_superseded_at, created_at
		)
		VALUES
			(
				$3, $1, $2, 'user', '{"role":"user","content":"first"}'::jsonb,
				'00000000-0000-0000-0000-000000075201', 1, 1, true, NULL, now() - interval '4 minutes'
			),
			(
				$4, $1, $2, 'assistant', '{"role":"assistant","content":"first reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075201', 1, 2, true, NULL, now() - interval '3 minutes'
			),
			(
				$5, $1, $2, 'user', '{"role":"user","content":"second"}'::jsonb,
				'00000000-0000-0000-0000-000000075202', 2, 1, true, NULL, now() - interval '2 minutes'
			),
			(
				$6, $1, $2, 'assistant', '{"role":"assistant","content":"second reply"}'::jsonb,
				'00000000-0000-0000-0000-000000075202', 2, 2, true, NULL, now() - interval '1 minutes'
			),
			(
				$7, $1, $2, 'assistant', '{"role":"assistant","content":"hidden"}'::jsonb,
				'00000000-0000-0000-0000-000000075203', 3, 2, false, now(), now()
			)
	`, postgresSessionTestBotID, postgresSessionTestSessionID, postgresSessionTestUser1ID, postgresSessionTestAssistant1ID, postgresSessionTestUser2ID, postgresSessionTestAssistant2ID, postgresSessionTestHiddenAssistantID); err != nil {
		t.Fatalf("insert messages: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_message_assets (message_id, role, ordinal, content_hash, name, metadata)
		VALUES ($1, 'attachment', 1, 'sha256:postgres-session-test-asset', 'fixture.txt', '{"source":"fixture"}'::jsonb)
	`, postgresSessionTestAssistant2ID); err != nil {
		t.Fatalf("insert message asset: %v", err)
	}
}

func countPostgresSessionTestRows(t *testing.T, ctx context.Context, tx pgx.Tx, table string) int {
	t.Helper()
	if table != "bot_sessions" && table != "bot_history_messages" {
		t.Fatalf("unexpected test table %q", table)
	}
	query := fmt.Sprintf("SELECT count(*) FROM %s WHERE bot_id = $1", table)
	var count int
	if err := tx.QueryRow(ctx, query, postgresSessionTestBotID).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func assertPostgresSessionDeleted(t *testing.T, ctx context.Context, tx pgx.Tx, sessionID string, wantDeleted bool) {
	t.Helper()
	var deleted bool
	if err := tx.QueryRow(ctx, `
		SELECT deleted_at IS NOT NULL
		FROM bot_sessions
		WHERE id = $1
	`, sessionID).Scan(&deleted); err != nil {
		t.Fatalf("read session deleted state: %v", err)
	}
	if deleted != wantDeleted {
		t.Fatalf("session %s deleted = %v, want %v", sessionID, deleted, wantDeleted)
	}
}

func assertPostgresMessageTurnPosition(t *testing.T, ctx context.Context, tx pgx.Tx, messageID string, want int64) {
	t.Helper()
	var got int64
	if err := tx.QueryRow(ctx, `
		SELECT turn_position
		FROM bot_history_messages
		WHERE id = $1
	`, messageID).Scan(&got); err != nil {
		t.Fatalf("read message %s turn position: %v", messageID, err)
	}
	if got != want {
		t.Fatalf("message %s turn_position = %d, want %d", messageID, got, want)
	}
}

func assertPostgresSessionNextTurnPosition(t *testing.T, ctx context.Context, tx pgx.Tx, sessionID string, want int64) {
	t.Helper()
	var got int64
	if err := tx.QueryRow(ctx, `
		SELECT next_turn_position
		FROM bot_sessions
		WHERE id = $1
	`, sessionID).Scan(&got); err != nil {
		t.Fatalf("read session %s next turn position: %v", sessionID, err)
	}
	if got != want {
		t.Fatalf("session %s next_turn_position = %d, want %d", sessionID, got, want)
	}
}

func assertPostgresSessionRepairMarker(t *testing.T, ctx context.Context, tx pgx.Tx, sessionID string, marker string, want bool) {
	t.Helper()
	var got bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE((metadata->'repair'->>$2)::boolean, false)
		FROM bot_sessions
		WHERE id = $1
	`, sessionID, marker).Scan(&got); err != nil {
		t.Fatalf("read session %s repair marker: %v", sessionID, err)
	}
	if got != want {
		t.Fatalf("session %s repair marker %s = %v, want %v", sessionID, marker, got, want)
	}
}
