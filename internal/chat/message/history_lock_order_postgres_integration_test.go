package message

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
)

func TestPostgresAssetLinkAndHistoryReplacementShareLockOrder(t *testing.T) {
	fixture := setupCommittedTurnFixture(t)
	ctx := context.Background()
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(fixture.pool)))
	replacement, err := svc.Persist(ctx, PersistInput{
		BotID:           fixture.botID,
		SessionID:       fixture.sessionID,
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"replacement"}`),
		SkipHistoryTurn: true,
	})
	if err != nil {
		t.Fatalf("persist replacement assistant: %v", err)
	}

	assetTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin asset transaction: %v", err)
	}
	defer func() { _ = assetTx.Rollback(ctx) }()
	if _, err := assetTx.Exec(ctx, `
		SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE
	`, fixture.sessionID); err != nil {
		t.Fatalf("lock asset owner session: %v", err)
	}

	replaceTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin replacement: %v", err)
	}
	defer func() { _ = replaceTx.Rollback(ctx) }()
	replacePID := postgresBackendPID(t, replaceTx)
	replaced := make(chan error, 1)
	go func() {
		_, err := dbsqlc.New(replaceTx).ReplaceHistoryTurn(ctx, dbsqlc.ReplaceHistoryTurnParams{
			OldTurnID:           mustTestUUID(t, fixture.turn.ID),
			SessionID:           mustTestUUID(t, fixture.sessionID),
			ReplacementTurnID:   mustTestUUID(t, uuid.NewString()),
			ReplacementPosition: fixture.turn.Position + 1,
			RequestMessageID:    mustTestUUID(t, fixture.user.ID),
			AssistantMessageID:  mustTestUUID(t, replacement.ID),
			SupersededAt:        pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			SupersededReason:    pgtype.Text{String: "retry", Valid: true},
		})
		replaced <- err
	}()
	waitForPostgresLock(t, fixture.pool, replacePID)

	if _, err := dbsqlc.New(assetTx).CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.assistant.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:lock-order",
		Name:        "lock-order.png",
		Metadata:    []byte("{}"),
	}); err != nil {
		t.Fatalf("link asset while replacement waits for session: %v", err)
	}
	if err := assetTx.Commit(ctx); err != nil {
		t.Fatalf("commit asset link: %v", err)
	}
	select {
	case err := <-replaced:
		if err != nil {
			t.Fatalf("replace after asset commit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replacement did not resume after asset commit")
	}
}

func TestPostgresAssetLinkAndHistoryMutationsShareLockOrder(t *testing.T) {
	t.Run("supersede", func(t *testing.T) {
		fixture := setupCommittedTurnFixture(t)
		params := dbsqlc.SupersedeHistoryTurnParams{
			SupersededByTurnID: mustTestUUID(t, uuid.NewString()),
			SupersededAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			SupersededReason:   pgtype.Text{String: "retry", Valid: true},
			OldTurnID:          mustTestUUID(t, fixture.turn.ID),
			SessionID:          mustTestUUID(t, fixture.sessionID),
		}
		assertAssetMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
			_, err := queries.SupersedeHistoryTurn(ctx, params)
			return err
		})
	})
	t.Run("hide", func(t *testing.T) {
		fixture := setupCommittedTurnFixture(t)
		turnID := mustTestUUID(t, fixture.turn.ID)
		assertAssetMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
			return queries.HideMessagesByHistoryTurn(ctx, turnID)
		})
	})
	t.Run("delete", func(t *testing.T) {
		fixture := setupCommittedTurnFixture(t)
		messageIDs := []pgtype.UUID{mustTestUUID(t, fixture.assistant.ID)}
		assertAssetMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
			return queries.DeleteMessagesByIDs(ctx, messageIDs)
		})
	})
}

func TestPostgresBotClearAndBatchDeleteShareSessionLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertBotMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.ClearHistoryByBot(ctx, botID)
	})
}

func TestPostgresBotRuntimeClearAndBatchDeleteShareSessionLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertBotMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.ClearBotRuntimeData(ctx, botID)
	})
}

func TestPostgresBotDeleteAndBatchDeleteShareSessionLockOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	botID := mustTestUUID(t, fixture.botID)
	assertBotMutationLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.DeleteBotByID(ctx, botID)
	})
}

func TestPostgresSessionClearLocksCompactionLogsInIDOrder(t *testing.T) {
	fixture := setupCommittedTurnFixture(t)
	sessionID := mustTestUUID(t, fixture.sessionID)
	assertCompactionLogLockOrder(t, fixture, func(ctx context.Context, queries *dbsqlc.Queries) error {
		return queries.ClearHistoryBySession(ctx, sessionID)
	})
}

func assertCompactionLogLockOrder(
	t *testing.T,
	fixture committedTurnFixture,
	mutate func(context.Context, *dbsqlc.Queries) error,
) {
	t.Helper()
	ctx := context.Background()
	lowArtifactID, highArtifactID := orderedTestUUIDPair()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (id, bot_id, session_id)
		VALUES ($1, $3, $4), ($2, $3, $4)
	`, highArtifactID, lowArtifactID, fixture.botID, fixture.sessionID); err != nil {
		t.Fatalf("insert reverse-order artifacts: %v", err)
	}
	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin artifact blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_history_message_compacts WHERE id = $1 FOR UPDATE
	`, lowArtifactID); err != nil {
		t.Fatalf("lock low artifact: %v", err)
	}
	mutationTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin compaction mutation: %v", err)
	}
	defer func() { _ = mutationTx.Rollback(ctx) }()
	mutationPID := postgresBackendPID(t, mutationTx)
	mutated := make(chan error, 1)
	go func() {
		mutated <- mutate(ctx, dbsqlc.New(mutationTx))
	}()
	waitForPostgresLock(t, fixture.pool, mutationPID)
	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin artifact order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_history_message_compacts WHERE id = $1 FOR UPDATE NOWAIT
	`, highArtifactID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("compaction mutation locked high artifact before low artifact: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release artifact order probe: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release low artifact: %v", err)
	}
	select {
	case err := <-mutated:
		if err != nil {
			t.Fatalf("compaction mutation after low artifact release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("compaction mutation did not resume after low artifact release")
	}
}

type orderedSessionFixture struct {
	committedTurnFixture
	highSessionID string
	lowSessionID  string
	highMessage   Message
	lowMessage    Message
}

func setupOrderedSessionFixture(t *testing.T) orderedSessionFixture {
	t.Helper()
	fixture := setupCommittedTurnFixture(t)
	ctx := context.Background()
	if _, err := fixture.pool.Exec(ctx, `DELETE FROM bot_sessions WHERE id = $1`, fixture.sessionID); err != nil {
		t.Fatalf("remove original fixture session: %v", err)
	}
	lowSessionID, highSessionID := orderedTestUUIDPair()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type)
		VALUES ($1, $3, 'local'), ($2, $3, 'local')
	`, highSessionID, lowSessionID, fixture.botID); err != nil {
		t.Fatalf("insert ordered sessions: %v", err)
	}
	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(fixture.pool)))
	highMessage, err := svc.Persist(ctx, PersistInput{
		BotID: fixture.botID, SessionID: highSessionID, Role: "user", Content: []byte(`{"role":"user","content":"high"}`),
	})
	if err != nil {
		t.Fatalf("persist high-session message: %v", err)
	}
	lowMessage, err := svc.Persist(ctx, PersistInput{
		BotID: fixture.botID, SessionID: lowSessionID, Role: "user", Content: []byte(`{"role":"user","content":"low"}`),
	})
	if err != nil {
		t.Fatalf("persist low-session message: %v", err)
	}
	return orderedSessionFixture{
		committedTurnFixture: fixture,
		highSessionID:        highSessionID,
		lowSessionID:         lowSessionID,
		highMessage:          highMessage,
		lowMessage:           lowMessage,
	}
}

func orderedTestUUIDPair() (string, string) {
	low := uuid.New()
	high := low
	low[0] = 0
	high[0] = 0xff
	return low.String(), high.String()
}

func assertBotMutationLockOrder(
	t *testing.T,
	fixture orderedSessionFixture,
	mutate func(context.Context, *dbsqlc.Queries) error,
) {
	t.Helper()
	ctx := context.Background()
	deleteTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin batch delete: %v", err)
	}
	defer func() { _ = deleteTx.Rollback(ctx) }()
	if _, err := deleteTx.Exec(ctx, `SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE`, fixture.lowSessionID); err != nil {
		t.Fatalf("lock low session: %v", err)
	}
	mutationTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin bot mutation: %v", err)
	}
	defer func() { _ = mutationTx.Rollback(ctx) }()
	mutationPID := postgresBackendPID(t, mutationTx)
	mutated := make(chan error, 1)
	go func() {
		mutated <- mutate(ctx, dbsqlc.New(mutationTx))
	}()
	waitForPostgresLock(t, fixture.pool, mutationPID)
	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin session order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE NOWAIT`, fixture.highSessionID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("bot mutation locked high session before low session: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_history_messages
		WHERE id = ANY($1::uuid[])
		FOR UPDATE NOWAIT
	`, []pgtype.UUID{mustTestUUID(t, fixture.lowMessage.ID), mustTestUUID(t, fixture.highMessage.ID)}); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("bot mutation locked messages before low session: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release session order probe: %v", err)
	}
	if err := dbsqlc.New(deleteTx).DeleteMessagesByIDs(ctx, []pgtype.UUID{
		mustTestUUID(t, fixture.lowMessage.ID),
		mustTestUUID(t, fixture.highMessage.ID),
	}); err != nil {
		t.Fatalf("delete messages while bot mutation waits: %v", err)
	}
	if err := deleteTx.Commit(ctx); err != nil {
		t.Fatalf("commit batch delete: %v", err)
	}
	select {
	case err := <-mutated:
		if err != nil {
			t.Fatalf("bot mutation after batch delete: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bot mutation did not resume after batch delete")
	}
	if err := mutationTx.Commit(ctx); err != nil {
		t.Fatalf("commit bot mutation: %v", err)
	}
}

func assertAssetMutationLockOrder(
	t *testing.T,
	fixture committedTurnFixture,
	mutate func(context.Context, *dbsqlc.Queries) error,
) {
	t.Helper()
	ctx := context.Background()
	assetTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin asset transaction: %v", err)
	}
	defer func() { _ = assetTx.Rollback(ctx) }()
	if _, err := assetTx.Exec(ctx, `
		SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE
	`, fixture.sessionID); err != nil {
		t.Fatalf("lock asset owner session: %v", err)
	}

	mutationTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin history mutation: %v", err)
	}
	defer func() { _ = mutationTx.Rollback(ctx) }()
	mutationPID := postgresBackendPID(t, mutationTx)
	mutated := make(chan error, 1)
	go func() {
		mutated <- mutate(ctx, dbsqlc.New(mutationTx))
	}()
	waitForPostgresLock(t, fixture.pool, mutationPID)
	if _, err := dbsqlc.New(assetTx).CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.assistant.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:lock-order",
		Name:        "lock-order.png",
		Metadata:    []byte("{}"),
	}); err != nil {
		t.Fatalf("link asset while history mutation waits for session: %v", err)
	}
	if err := assetTx.Commit(ctx); err != nil {
		t.Fatalf("commit asset link: %v", err)
	}
	select {
	case err := <-mutated:
		if err != nil {
			t.Fatalf("history mutation after asset commit: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("history mutation did not resume after asset commit")
	}
	if err := mutationTx.Commit(ctx); err != nil {
		t.Fatalf("commit history mutation: %v", err)
	}
}

// committedTurnFixture is one committed turn of a fresh bot and session,
// written through a pool so that concurrent transactions see it.
type committedTurnFixture struct {
	pool      *pgxpool.Pool
	botID     string
	sessionID string
	user      Message
	assistant Message
	turn      HistoryTurn
}

func setupCommittedTurnFixture(t *testing.T) committedTurnFixture {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping postgres: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	sessionID := uuid.NewString()
	name := "history-lock-order-" + uuid.NewString()[:12]
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true) RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("insert fixture user: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bots (id, owner_user_id, name)
		VALUES ($1, $2, $3)
	`, botID, userID, name); err != nil {
		t.Fatalf("insert fixture bot: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type)
		VALUES ($1, $2, 'local')
	`, sessionID, botID); err != nil {
		t.Fatalf("insert fixture session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})

	svc := NewService(nil, postgresstore.NewQueries(dbsqlc.New(pool)))
	user, err := svc.Persist(ctx, PersistInput{
		BotID:     botID,
		SessionID: sessionID,
		Role:      "user",
		Content:   []byte(`{"role":"user","content":"question"}`),
	})
	if err != nil {
		t.Fatalf("persist fixture user: %v", err)
	}
	assistant, err := svc.Persist(ctx, PersistInput{
		BotID:     botID,
		SessionID: sessionID,
		Role:      "assistant",
		Content:   []byte(`{"role":"assistant","content":"answer"}`),
	})
	if err != nil {
		t.Fatalf("persist fixture assistant: %v", err)
	}
	turn, err := svc.GetVisibleTurnByMessage(ctx, sessionID, assistant.ID)
	if err != nil {
		t.Fatalf("load fixture turn: %v", err)
	}
	return committedTurnFixture{
		pool:      pool,
		botID:     botID,
		sessionID: sessionID,
		user:      user,
		assistant: assistant,
		turn:      turn,
	}
}

func postgresBackendPID(t *testing.T, tx pgx.Tx) int32 {
	t.Helper()
	var pid int32
	if err := tx.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid); err != nil {
		t.Fatalf("read postgres backend pid: %v", err)
	}
	return pid
}

func waitForPostgresLock(t *testing.T, pool *pgxpool.Pool, pid int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		var waiting bool
		err := pool.QueryRow(context.Background(), `
			SELECT COALESCE(wait_event_type = 'Lock', false)
			FROM pg_stat_activity
			WHERE pid = $1
		`, pid).Scan(&waiting)
		if err != nil {
			t.Fatalf("read postgres lock state: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("postgres backend %d did not wait on a lock", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
