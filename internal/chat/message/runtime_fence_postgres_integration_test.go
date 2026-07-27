package message

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/memohai/memoh/internal/db"
	"github.com/memohai/memoh/internal/db/dbtest"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
	"github.com/memohai/memoh/internal/runtimefence"
)

var (
	runtimeFenceMigrationOnce sync.Once
	runtimeFenceMigrationErr  error
)

func TestPostgresRuntimeFenceUnfencedReplacementIsAtomic(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	service := NewService(nil, postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool)))

	user, err := service.Persist(ctx, PersistInput{
		BotID: botID.String(), SessionID: sessionID.String(), Role: "user",
		Content: []byte(`{"role":"user","content":"hello"}`),
	})
	if err != nil {
		t.Fatalf("persist baseline user: %v", err)
	}
	assistant, err := service.Persist(ctx, PersistInput{
		BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant",
		Content: []byte(`{"role":"assistant","content":"original"}`), TurnRequestMessageID: user.ID,
	})
	if err != nil {
		t.Fatalf("persist baseline assistant: %v", err)
	}
	oldTurn, err := service.GetVisibleTurnByMessage(ctx, sessionID.String(), assistant.ID)
	if err != nil {
		t.Fatalf("load baseline turn: %v", err)
	}

	var beforeFailedReplacement int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM bot_history_messages WHERE session_id = $1", sessionID).Scan(&beforeFailedReplacement); err != nil {
		t.Fatalf("count messages before failed replacement: %v", err)
	}
	_, handled, err := service.PersistRound(ctx, []PersistInput{{
		BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant",
		Content: []byte(`{"role":"assistant","content":"must roll back"}`), SkipHistoryTurn: true,
	}}, RoundPersistenceOptions{Replacement: &TurnReplacement{
		OldTurnID: uuid.NewString(), RequestMessageID: user.ID, Reason: "retry",
	}})
	if err == nil || !handled {
		t.Fatalf("invalid local replacement = handled %v, error %v", handled, err)
	}
	var afterFailedReplacement int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM bot_history_messages WHERE session_id = $1", sessionID).Scan(&afterFailedReplacement); err != nil {
		t.Fatalf("count messages after failed replacement: %v", err)
	}
	if afterFailedReplacement != beforeFailedReplacement {
		t.Fatalf("failed local replacement left orphan messages: before=%d after=%d", beforeFailedReplacement, afterFailedReplacement)
	}

	replacement, handled, err := service.PersistRound(ctx, []PersistInput{{
		BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant",
		Content: []byte(`{"role":"assistant","content":"replacement"}`), SkipHistoryTurn: true,
	}}, RoundPersistenceOptions{Replacement: &TurnReplacement{
		OldTurnID: oldTurn.ID, RequestMessageID: user.ID, Reason: "retry",
		SessionMetadata: map[string]any{"forked_from": map[string]any{"fork_message_id": "assistant-parent"}},
	}})
	if err != nil || !handled || len(replacement) != 1 {
		t.Fatalf("persist local replacement = (%d, %v, %v), want (1, true, nil)", len(replacement), handled, err)
	}
	visible, err := service.ListBySession(ctx, sessionID.String())
	if err != nil {
		t.Fatalf("list visible local replacement: %v", err)
	}
	if len(visible) != 2 || visible[0].ID != user.ID || visible[1].ID != replacement[0].ID {
		t.Fatalf("visible messages after local replacement = %#v", visible)
	}
	var forkMessageID string
	if err := pool.QueryRow(ctx, "SELECT metadata->'forked_from'->>'fork_message_id' FROM bot_sessions WHERE id = $1", sessionID).Scan(&forkMessageID); err != nil {
		t.Fatalf("load local replacement metadata: %v", err)
	}
	if forkMessageID != "assistant-parent" {
		t.Fatalf("fork message id = %q, want assistant-parent", forkMessageID)
	}
}

func TestPostgresRuntimeFenceRejectsStaleRoundAndReplacement(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	service := NewService(nil, postgresstore.NewQueriesWithPool(pool, queries))

	token1 := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	owner1 := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: token1})
	persisted, handled, err := service.PersistRound(owner1, []PersistInput{
		{BotID: botID.String(), SessionID: sessionID.String(), Role: "user", Content: []byte(`{"role":"user","content":"hello"}`)},
		{BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant", Content: []byte(`{"role":"assistant","content":"owner one"}`)},
	}, RoundPersistenceOptions{})
	if err != nil || !handled || len(persisted) != 2 {
		t.Fatalf("persist owner-one round = (%d, %v, %v), want (2, true, nil)", len(persisted), handled, err)
	}
	oldTurn, err := service.GetVisibleTurnByMessage(ctx, sessionID.String(), persisted[1].ID)
	if err != nil {
		t.Fatalf("load owner-one turn: %v", err)
	}

	token2 := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	owner2 := runtimefence.WithContext(ctx, runtimefence.Fence{BotID: botID.String(), SessionID: sessionID.String(), Token: token2})
	var beforeFailedReplacement int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM bot_history_messages WHERE session_id = $1", sessionID).Scan(&beforeFailedReplacement); err != nil {
		t.Fatalf("count messages before failed replacement: %v", err)
	}
	_, handled, err = service.PersistRound(owner2, []PersistInput{{
		BotID:           botID.String(),
		SessionID:       sessionID.String(),
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"must roll back"}`),
		SkipHistoryTurn: true,
	}}, RoundPersistenceOptions{Replacement: &TurnReplacement{
		OldTurnID:        uuid.NewString(),
		RequestMessageID: persisted[0].ID,
		Reason:           "retry",
	}})
	if err == nil || !handled {
		t.Fatalf("invalid atomic replacement = handled %v, error %v", handled, err)
	}
	var afterFailedReplacement int
	if err := pool.QueryRow(ctx, "SELECT COUNT(*) FROM bot_history_messages WHERE session_id = $1", sessionID).Scan(&afterFailedReplacement); err != nil {
		t.Fatalf("count messages after failed replacement: %v", err)
	}
	if afterFailedReplacement != beforeFailedReplacement {
		t.Fatalf("failed replacement left orphan messages: before=%d after=%d", beforeFailedReplacement, afterFailedReplacement)
	}
	replacement, handled, err := service.PersistRound(owner2, []PersistInput{{
		BotID:           botID.String(),
		SessionID:       sessionID.String(),
		Role:            "assistant",
		Content:         []byte(`{"role":"assistant","content":"owner two"}`),
		SkipHistoryTurn: true,
	}}, RoundPersistenceOptions{Replacement: &TurnReplacement{
		OldTurnID:        oldTurn.ID,
		RequestMessageID: persisted[0].ID,
		Reason:           "retry",
	}})
	if err != nil || !handled || len(replacement) != 1 {
		t.Fatalf("persist owner-two replacement = (%d, %v, %v), want (1, true, nil)", len(replacement), handled, err)
	}

	_, _, err = service.PersistRound(owner1, []PersistInput{{
		BotID: botID.String(), SessionID: sessionID.String(), Role: "assistant",
		Content: []byte(`{"role":"assistant","content":"stale"}`),
	}}, RoundPersistenceOptions{})
	if !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("stale PersistRound error = %v, want ErrStale", err)
	}
	if _, err := service.ReplaceTurn(owner1, sessionID.String(), oldTurn.ID, persisted[0].ID, replacement[0].ID, "retry"); !errors.Is(err, runtimefence.ErrStale) {
		t.Fatalf("stale ReplaceTurn error = %v, want ErrStale", err)
	}

	visible, err := service.ListBySession(ctx, sessionID.String())
	if err != nil {
		t.Fatalf("list visible messages: %v", err)
	}
	if len(visible) != 2 || visible[0].ID != persisted[0].ID || visible[1].ID != replacement[0].ID {
		t.Fatalf("visible messages changed after stale writes: %#v", visible)
	}
}

func TestPostgresRuntimeFenceLockSerializesTokenAdvance(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	token1 := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	token2, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate owner-two fence: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin fence lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := dbsqlc.New(tx).LockSessionRuntimeFence(ctx, dbsqlc.LockSessionRuntimeFenceParams{
		SessionID:           sessionID,
		BotID:               botID,
		RuntimeFencingToken: token1,
	}); err != nil {
		t.Fatalf("lock owner-one fence: %v", err)
	}

	type activateResult struct {
		token int64
		err   error
	}
	resultCh := make(chan activateResult, 1)
	go func() {
		token, err := queries.ActivateSessionRuntimeFence(ctx, dbsqlc.ActivateSessionRuntimeFenceParams{SessionID: sessionID, BotID: botID, RuntimeFencingToken: token2})
		resultCh <- activateResult{token: token, err: err}
	}()
	select {
	case result := <-resultCh:
		t.Fatalf("fence activated while persistence lock was held: %#v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit fence lock transaction: %v", err)
	}
	select {
	case result := <-resultCh:
		if result.err != nil || result.token != token2 {
			t.Fatalf("fence activation after commit = %#v, want token %d", result, token2)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for fence advance after lock release")
	}
}

func TestPostgresRuntimeFenceLockSerializesConcurrentWritersBeforeUpdate(t *testing.T) {
	ctx := context.Background()
	pool := openRuntimeFencePostgresPool(t, ctx)
	botID, sessionID := createRuntimeFenceFixtures(t, ctx, pool)
	queries := dbsqlc.New(pool)
	token := acquireRuntimeFenceToken(t, ctx, queries, botID, sessionID)
	params := dbsqlc.LockSessionRuntimeFenceParams{
		SessionID:           sessionID,
		BotID:               botID,
		RuntimeFencingToken: token,
	}

	tx1, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin first writer: %v", err)
	}
	defer func() { _ = tx1.Rollback(ctx) }()
	if _, err := dbsqlc.New(tx1).LockSessionRuntimeFence(ctx, params); err != nil {
		t.Fatalf("lock first writer fence: %v", err)
	}

	type writerResult struct {
		stage string
		err   error
	}
	backendPID := make(chan int32, 1)
	locked := make(chan writerResult, 1)
	proceed := make(chan struct{})
	done := make(chan writerResult, 1)
	go func() {
		tx2, err := pool.Begin(ctx)
		if err != nil {
			locked <- writerResult{stage: "begin", err: err}
			return
		}
		defer func() { _ = tx2.Rollback(ctx) }()
		var pid int32
		if err := tx2.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			locked <- writerResult{stage: "backend pid", err: err}
			return
		}
		backendPID <- pid
		if _, err := dbsqlc.New(tx2).LockSessionRuntimeFence(ctx, params); err != nil {
			locked <- writerResult{stage: "lock", err: err}
			return
		}
		locked <- writerResult{stage: "locked"}
		<-proceed
		if _, err := tx2.Exec(ctx, "UPDATE bot_sessions SET next_turn_position = next_turn_position + 1 WHERE id = $1", sessionID); err != nil {
			done <- writerResult{stage: "update", err: err}
			return
		}
		done <- writerResult{stage: "commit", err: tx2.Commit(ctx)}
	}()

	var pid int32
	select {
	case pid = <-backendPID:
	case result := <-locked:
		t.Fatalf("second writer failed before fence lock: %#v", result)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out starting second writer")
	}
	var early *writerResult
	blocked := false
	deadline := time.Now().Add(2 * time.Second)
	for !blocked && early == nil && time.Now().Before(deadline) {
		select {
		case result := <-locked:
			early = &result
		default:
			if err := pool.QueryRow(ctx, "SELECT COALESCE(wait_event_type = 'Lock', false) FROM pg_stat_activity WHERE pid = $1", pid).Scan(&blocked); err != nil {
				t.Fatalf("inspect second writer lock wait: %v", err)
			}
			if !blocked {
				time.Sleep(10 * time.Millisecond)
			}
		}
	}
	if early != nil {
		_ = tx1.Rollback(ctx)
		if early.stage == "locked" {
			close(proceed)
			select {
			case <-done:
			case <-time.After(2 * time.Second):
			}
		}
		t.Fatalf("second writer passed fence lock before first writer committed: %#v", *early)
	}
	if !blocked {
		_ = tx1.Rollback(ctx)
		select {
		case result := <-locked:
			if result.stage == "locked" {
				close(proceed)
				select {
				case <-done:
				case <-time.After(2 * time.Second):
				}
			}
		case <-time.After(2 * time.Second):
		}
		t.Fatal("second writer never reached the PostgreSQL fence lock wait")
	}
	if _, err := tx1.Exec(ctx, "UPDATE bot_sessions SET next_turn_position = next_turn_position + 1 WHERE id = $1", sessionID); err != nil {
		t.Fatalf("update first writer: %v", err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatalf("commit first writer: %v", err)
	}
	select {
	case result := <-locked:
		if result.err != nil || result.stage != "locked" {
			t.Fatalf("second writer lock after first commit = %#v", result)
		}
		close(proceed)
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second writer fence lock")
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatalf("second writer %s: %v", result.stage, result.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for second writer commit")
	}
}

func openRuntimeFencePostgresPool(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("postgres runtime fence tests are required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("skip postgres runtime fence test: TEST_POSTGRES_DSN is not set")
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatalf("create required postgres pool: %v", err)
		}
		t.Skipf("skip postgres runtime fence test: cannot connect to database: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatalf("required postgres ping failed: %v", err)
		}
		t.Skipf("skip postgres runtime fence test: database ping failed: %v", err)
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		runtimeFenceMigrationOnce.Do(func() {
			runtimeFenceMigrationErr = dbtest.MigratePostgresUp(dsn)
		})
		if runtimeFenceMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", runtimeFenceMigrationErr)
		}
	}
	return pool
}

func createRuntimeFenceFixtures(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (pgtype.UUID, pgtype.UUID) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	sessionID := uuid.New()
	name := fmt.Sprintf("runtime-fence-test-%s", uuid.NewString())
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true) RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user`, userID, name); err != nil {
		t.Fatalf("create runtime fence user: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)", botID, userID, name); err != nil {
		t.Fatalf("create runtime fence bot: %v", err)
	}
	if _, err := pool.Exec(ctx, "INSERT INTO bot_sessions (id, bot_id, channel_type) VALUES ($1, $2, 'local')", sessionID, botID); err != nil {
		t.Fatalf("create runtime fence session: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(ctx, "DELETE FROM users WHERE id = $1", userID)
	})
	pgBotID, err := dbpkg.ParseUUID(botID.String())
	if err != nil {
		t.Fatalf("parse runtime fence bot id: %v", err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID.String())
	if err != nil {
		t.Fatalf("parse runtime fence session id: %v", err)
	}
	return pgBotID, pgSessionID
}

func acquireRuntimeFenceToken(t *testing.T, ctx context.Context, queries *dbsqlc.Queries, botID, sessionID pgtype.UUID) int64 {
	t.Helper()
	token, err := queries.NextSessionRuntimeFenceToken(ctx)
	if err != nil {
		t.Fatalf("allocate runtime fence: %v", err)
	}
	activated, err := queries.ActivateSessionRuntimeFence(ctx, dbsqlc.ActivateSessionRuntimeFenceParams{SessionID: sessionID, BotID: botID, RuntimeFencingToken: token})
	if err != nil {
		t.Fatalf("activate runtime fence: %v", err)
	}
	if activated != token {
		t.Fatalf("activated runtime fence = %d, want %d", activated, token)
	}
	return token
}
