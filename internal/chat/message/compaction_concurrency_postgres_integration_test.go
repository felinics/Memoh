package message

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/memohai/memoh/internal/db"
	dbsqlc "github.com/memohai/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/memohai/memoh/internal/db/postgres/store"
)

type committedCompactionFixture struct {
	pool      *pgxpool.Pool
	botID     string
	sessionID string
	user      Message
	assistant Message
	turn      HistoryTurn
}

func setupCommittedCompactionFixture(t *testing.T) committedCompactionFixture {
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
	name := "compaction-race-" + uuid.NewString()[:12]
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
	return committedCompactionFixture{
		pool:      pool,
		botID:     botID,
		sessionID: sessionID,
		user:      user,
		assistant: assistant,
		turn:      turn,
	}
}

func (fixture committedCompactionFixture) createLog(t *testing.T) dbsqlc.BotHistoryMessageCompact {
	t.Helper()
	log, err := dbsqlc.New(fixture.pool).CreateCompactionLog(context.Background(), dbsqlc.CreateCompactionLogParams{
		BotID:         mustTestUUID(t, fixture.botID),
		SessionID:     mustTestUUID(t, fixture.sessionID),
		ExpectedEpoch: fixture.epoch(t),
	})
	if err != nil {
		t.Fatalf("create compaction log: %v", err)
	}
	return log
}

func (fixture committedCompactionFixture) epoch(t *testing.T) int64 {
	t.Helper()
	var epoch int64
	if err := fixture.pool.QueryRow(context.Background(), `
		SELECT compaction_epoch FROM bot_sessions WHERE id = $1
	`, fixture.sessionID).Scan(&epoch); err != nil {
		t.Fatalf("read fixture epoch: %v", err)
	}
	return epoch
}

func fixtureMessageIDs(t *testing.T, fixture committedCompactionFixture) []pgtype.UUID {
	t.Helper()
	return []pgtype.UUID{mustTestUUID(t, fixture.user.ID), mustTestUUID(t, fixture.assistant.ID)}
}

func emptyCompactionClaims(count int) []pgtype.UUID {
	return make([]pgtype.UUID, count)
}

func TestPostgresAssetMutationInvalidatesClaimedCompaction(t *testing.T) {
	for _, status := range []string{"pending", "ok"} {
		t.Run(status, func(t *testing.T) {
			fixture := setupCommittedCompactionFixture(t)
			ctx := context.Background()
			queries := dbsqlc.New(fixture.pool)
			log := fixture.createLog(t)
			messageIDs := fixtureMessageIDs(t, fixture)
			marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
				CompactID:          log.ID,
				MessageIds:         messageIDs,
				ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
			})
			if err != nil || marked != int64(len(messageIDs)) {
				t.Fatalf("claim compaction sources = %d, %v", marked, err)
			}
			if status == "ok" {
				if _, err := queries.CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{
					ID:           log.ID,
					Status:       "ok",
					Summary:      "summary",
					MessageCount: 2,
					Coverage:     []byte("[]"),
				}); err != nil {
					t.Fatalf("complete compaction log: %v", err)
				}
			}

			asset := dbsqlc.CreateMessageAssetParams{
				MessageID:   mustTestUUID(t, fixture.assistant.ID),
				Role:        "attachment",
				Ordinal:     0,
				ContentHash: "sha256:late-asset",
				Name:        "late.png",
				Metadata:    []byte("{}"),
			}
			if _, err := queries.CreateMessageAsset(ctx, asset); err != nil {
				t.Fatalf("link late asset: %v", err)
			}
			if got := fixture.epoch(t); got != 1 {
				t.Fatalf("epoch after %s claim asset mutation = %d, want 1", status, got)
			}
			if _, err := queries.CreateMessageAsset(ctx, asset); err != nil {
				t.Fatalf("repeat identical asset link: %v", err)
			}
			if got := fixture.epoch(t); got != 1 {
				t.Fatalf("idempotent asset link advanced epoch to %d", got)
			}
		})
	}
}

func TestPostgresIdempotentAssetUpsertPreservesCurrentCompaction(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	asset := dbsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.assistant.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:stable-asset",
		Name:        "stable.png",
		Metadata:    []byte("{}"),
	}
	if _, err := queries.CreateMessageAsset(ctx, asset); err != nil {
		t.Fatalf("link initial asset: %v", err)
	}
	log := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          log.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("claim compaction sources = %d, %v", marked, err)
	}
	if _, err := queries.CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{
		ID: log.ID, Status: "ok", Summary: "summary", MessageCount: 2, Coverage: []byte("[]"),
	}); err != nil {
		t.Fatalf("complete compaction log: %v", err)
	}
	if _, err := queries.CreateMessageAsset(ctx, asset); err != nil {
		t.Fatalf("repeat identical asset link: %v", err)
	}
	if got := fixture.epoch(t); got != 0 {
		t.Fatalf("identical asset link advanced epoch to %d", got)
	}
	asset.Ordinal = 1
	if _, err := queries.CreateMessageAsset(ctx, asset); err != nil {
		t.Fatalf("change persisted asset shape: %v", err)
	}
	if got := fixture.epoch(t); got != 1 {
		t.Fatalf("changed asset shape left epoch %d, want 1", got)
	}
}

func TestPostgresAssetLinkBeforeClaimSerializesIntoSourceSnapshot(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	log := fixture.createLog(t)

	assetTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin asset transaction: %v", err)
	}
	defer func() { _ = assetTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(assetTx).CreateMessageAsset(ctx, dbsqlc.CreateMessageAssetParams{
		MessageID:   mustTestUUID(t, fixture.assistant.ID),
		Role:        "attachment",
		Ordinal:     0,
		ContentHash: "sha256:before-claim",
		Name:        "before.png",
		Metadata:    []byte("{}"),
	}); err != nil {
		t.Fatalf("link asset before claim: %v", err)
	}

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mark transaction: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	markPID := postgresBackendPID(t, markTx)
	type markResult struct {
		count int64
		err   error
	}
	result := make(chan markResult, 1)
	go func() {
		count, err := dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
			CompactID:          log.ID,
			MessageIds:         []pgtype.UUID{mustTestUUID(t, fixture.assistant.ID)},
			ExpectedCompactIds: emptyCompactionClaims(1),
		})
		result <- markResult{count: count, err: err}
	}()
	waitForPostgresLock(t, fixture.pool, markPID)
	if err := assetTx.Commit(ctx); err != nil {
		t.Fatalf("commit asset link: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.count != 1 {
			t.Fatalf("claim after asset commit = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not resume after asset commit")
	}
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit claim: %v", err)
	}
	if got := fixture.epoch(t); got != 0 {
		t.Fatalf("pre-claim asset link advanced epoch to %d", got)
	}
	assets, err := dbsqlc.New(fixture.pool).ListMessageAssets(ctx, mustTestUUID(t, fixture.assistant.ID))
	if err != nil || len(assets) != 1 {
		t.Fatalf("claimed source assets = %#v, %v", assets, err)
	}
}

func TestPostgresCompactionClaimCannotOverwriteLiveAttempt(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	claims := emptyCompactionClaims(len(messageIDs))

	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: first.ID, MessageIds: messageIDs, ExpectedCompactIds: claims,
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	marked, err = queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: second.ID, MessageIds: messageIDs, ExpectedCompactIds: claims,
	})
	if err != nil || marked != 0 {
		t.Fatalf("competing claim = %d, %v, want 0", marked, err)
	}
	rows, err := queries.ListUncompactedMessagesBySession(ctx, mustTestUUID(t, fixture.sessionID))
	if err != nil || len(rows) != 0 {
		t.Fatalf("fresh pending lease exposed %d rows: %v", len(rows), err)
	}

	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}
	rows, err = queries.ListUncompactedMessagesBySession(ctx, mustTestUUID(t, fixture.sessionID))
	if err != nil || len(rows) != len(messageIDs) {
		t.Fatalf("stale pending lease exposed %d rows: %v", len(rows), err)
	}
	expectedFirst := []pgtype.UUID{first.ID, first.ID}
	marked, err = queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: second.ID, MessageIds: messageIDs, ExpectedCompactIds: expectedFirst,
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("stale claim reclamation = %d, %v", marked, err)
	}
}

func TestPostgresReplaceTurnSeesConcurrentCompactionClaim(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
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
	log := fixture.createLog(t)

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mark transaction: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	marked, err := dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          log.ID,
		MessageIds:         []pgtype.UUID{mustTestUUID(t, fixture.assistant.ID)},
		ExpectedCompactIds: emptyCompactionClaims(1),
	})
	if err != nil || marked != 1 {
		t.Fatalf("prepare concurrent claim = %d, %v", marked, err)
	}

	replaceTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin replace transaction: %v", err)
	}
	defer func() { _ = replaceTx.Rollback(ctx) }()
	replacePID := postgresBackendPID(t, replaceTx)
	type replaceResult struct {
		err error
	}
	replaced := make(chan replaceResult, 1)
	go func() {
		_, err := dbsqlc.New(replaceTx).ReplaceHistoryTurn(ctx, dbsqlc.ReplaceHistoryTurnParams{
			OldTurnID:          mustTestUUID(t, fixture.turn.ID),
			SessionID:          mustTestUUID(t, fixture.sessionID),
			RequestMessageID:   mustTestUUID(t, fixture.user.ID),
			AssistantMessageID: mustTestUUID(t, replacement.ID),
			SupersededAt:       pgtype.Timestamptz{Time: time.Now().UTC(), Valid: true},
			SupersededReason:   pgtype.Text{String: "retry", Valid: true},
		})
		replaced <- replaceResult{err: err}
	}()
	waitForPostgresLock(t, fixture.pool, replacePID)
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit concurrent claim: %v", err)
	}
	select {
	case got := <-replaced:
		if got.err != nil {
			t.Fatalf("replace after claim commit: %v", got.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("replace did not resume after claim commit")
	}
	if err := replaceTx.Commit(ctx); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	if got := fixture.epoch(t); got != 1 {
		t.Fatalf("replace after concurrent claim left epoch %d, want 1", got)
	}
	_, err = dbsqlc.New(fixture.pool).CompleteCompactionLog(ctx, dbsqlc.CompleteCompactionLogParams{
		ID:           log.ID,
		Status:       "ok",
		Summary:      "stale summary",
		MessageCount: 1,
		Coverage:     []byte("[]"),
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("stale completion after replacement = %v, want pgx.ErrNoRows", err)
	}
}
