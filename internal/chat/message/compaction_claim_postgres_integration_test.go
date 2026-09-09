package message

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPostgresReclaimedCompactionCannotComplete(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)

	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          first.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}

	expectedFirst := repeatedCompactionClaim(first.ID, len(messageIDs))
	marked, err = queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          second.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: expectedFirst,
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("stale claim reclamation = %d, %v", marked, err)
	}

	_, err = queries.CompleteCompactionLog(ctx, successfulCompaction(first.ID))
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("reclaimed owner completion = %v, want pgx.ErrNoRows", err)
	}
	if _, err := queries.CompleteCompactionLog(ctx, successfulCompaction(second.ID)); err != nil {
		t.Fatalf("current owner completion: %v", err)
	}
}

func TestPostgresMismatchedClaimArraysDoNotReclaimSource(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          first.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}

	marked, err = queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          second.ID,
		MessageIds:         messageIDs[:1],
		ExpectedCompactIds: repeatedCompactionClaim(first.ID, 2),
	})
	if err != nil || marked != 0 {
		t.Fatalf("mismatched claim = %d, %v, want zero rows", marked, err)
	}
	var status string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM bot_history_message_compacts WHERE id = $1
	`, first.ID).Scan(&status); err != nil {
		t.Fatalf("read first claim status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("first claim status after mismatched input = %q, want pending", status)
	}
}

func TestPostgresReclaimDoesNotOverwriteCompletedSource(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)

	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          first.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}
	if rows, err := queries.ListUncompactedMessagesBySession(ctx, mustTestUUID(t, fixture.sessionID)); err != nil || len(rows) != len(messageIDs) {
		t.Fatalf("stale source selection = %d, %v", len(rows), err)
	}
	if _, err := queries.CompleteCompactionLog(ctx, successfulCompaction(first.ID)); err != nil {
		t.Fatalf("complete selected source: %v", err)
	}

	marked, err = queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          second.ID,
		MessageIds:         messageIDs,
		ExpectedCompactIds: repeatedCompactionClaim(first.ID, len(messageIDs)),
	})
	if err != nil || marked != 0 {
		t.Fatalf("claim after source completion = %d, %v, want 0", marked, err)
	}
}

func TestPostgresReclaimSerializesWithSourceCompletion(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: first.ID, MessageIds: messageIDs, ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}

	completeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin completion: %v", err)
	}
	defer func() { _ = completeTx.Rollback(ctx) }()
	if _, err := dbsqlc.New(completeTx).CompleteCompactionLog(ctx, successfulCompaction(first.ID)); err != nil {
		t.Fatalf("complete source: %v", err)
	}
	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reclamation: %v", err)
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
			CompactID: second.ID, MessageIds: messageIDs, ExpectedCompactIds: repeatedCompactionClaim(first.ID, len(messageIDs)),
		})
		result <- markResult{count: count, err: err}
	}()
	waitForPostgresLock(t, fixture.pool, markPID)
	if err := completeTx.Commit(ctx); err != nil {
		t.Fatalf("commit source completion: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.count != 0 {
			t.Fatalf("reclaim after source completion = %+v, want zero rows", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reclaim did not resume after source completion")
	}
}

func TestPostgresSourceCompletionCannotPassCommittedReclaim(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	queries := dbsqlc.New(fixture.pool)
	first := fixture.createLog(t)
	second := fixture.createLog(t)
	messageIDs := fixtureMessageIDs(t, fixture)
	marked, err := queries.MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: first.ID, MessageIds: messageIDs, ExpectedCompactIds: emptyCompactionClaims(len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("first claim = %d, %v", marked, err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_message_compacts
		SET started_at = now() - INTERVAL '16 minutes'
		WHERE id = $1
	`, first.ID); err != nil {
		t.Fatalf("age first claim: %v", err)
	}

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reclamation: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	marked, err = dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID: second.ID, MessageIds: messageIDs, ExpectedCompactIds: repeatedCompactionClaim(first.ID, len(messageIDs)),
	})
	if err != nil || marked != int64(len(messageIDs)) {
		t.Fatalf("reclaim source = %d, %v", marked, err)
	}
	completeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source completion: %v", err)
	}
	defer func() { _ = completeTx.Rollback(ctx) }()
	completePID := postgresBackendPID(t, completeTx)
	result := make(chan error, 1)
	go func() {
		_, err := dbsqlc.New(completeTx).CompleteCompactionLog(ctx, successfulCompaction(first.ID))
		result <- err
	}()
	waitForPostgresLock(t, fixture.pool, completePID)
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit reclamation: %v", err)
	}
	select {
	case err := <-result:
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("source completion after reclaim = %v, want pgx.ErrNoRows", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source completion did not resume after reclamation")
	}
}

func TestPostgresCompactionClaimLocksSessionBeforeMessages(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	log := fixture.createLog(t)
	sessionTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin session lock: %v", err)
	}
	defer func() { _ = sessionTx.Rollback(ctx) }()
	if _, err := sessionTx.Exec(ctx, `SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE`, fixture.sessionID); err != nil {
		t.Fatalf("lock compaction session: %v", err)
	}
	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source claim: %v", err)
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
	if err := sessionTx.Commit(ctx); err != nil {
		t.Fatalf("commit session lock: %v", err)
	}
	select {
	case got := <-result:
		if got.err != nil || got.count != 1 {
			t.Fatalf("claim after session lock = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("claim did not resume after session lock")
	}
}

func TestPostgresCrossOwnerClaimLocksArtifactsInIDOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	ctx := context.Background()
	targetArtifactID, staleArtifactID := orderedTestUUIDPair()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (
			id, bot_id, session_id, compaction_epoch
		)
		VALUES ($1, $3, $4, 0), ($2, $3, $5, 0)
	`, targetArtifactID, staleArtifactID, fixture.botID, fixture.lowSessionID, fixture.highSessionID); err != nil {
		t.Fatalf("insert cross-owner claims: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_messages SET compact_id = $1 WHERE id = $2
	`, staleArtifactID, fixture.lowMessage.ID); err != nil {
		t.Fatalf("assign stale foreign claim: %v", err)
	}

	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin target artifact blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_history_message_compacts WHERE id = $1 FOR UPDATE
	`, targetArtifactID); err != nil {
		t.Fatalf("lock target artifact: %v", err)
	}

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin cross-owner claim: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	markPID := postgresBackendPID(t, markTx)
	targetUUID := mustTestUUID(t, targetArtifactID)
	staleUUID := mustTestUUID(t, staleArtifactID)
	messageUUID := mustTestUUID(t, fixture.lowMessage.ID)
	type markResult struct {
		count int64
		err   error
	}
	marked := make(chan markResult, 1)
	go func() {
		count, err := dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
			CompactID:          targetUUID,
			MessageIds:         []pgtype.UUID{messageUUID},
			ExpectedCompactIds: []pgtype.UUID{staleUUID},
		})
		marked <- markResult{count: count, err: err}
	}()
	waitForPostgresLock(t, fixture.pool, markPID)

	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin artifact order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_history_message_compacts WHERE id = $1 FOR UPDATE NOWAIT
	`, staleArtifactID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("cross-owner claim locked high artifact before low target: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release artifact order probe: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release target artifact: %v", err)
	}

	select {
	case got := <-marked:
		if got.err != nil || got.count != 1 {
			t.Fatalf("cross-owner claim after target release = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cross-owner claim did not resume after target release")
	}
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit cross-owner claim: %v", err)
	}
	var foreignStatus string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM bot_history_message_compacts WHERE id = $1
	`, staleArtifactID).Scan(&foreignStatus); err != nil {
		t.Fatalf("read foreign claim status: %v", err)
	}
	if foreignStatus != "pending" {
		t.Fatalf("foreign claim status = %q, want pending", foreignStatus)
	}
	var currentClaim pgtype.UUID
	if err := fixture.pool.QueryRow(ctx, `
		SELECT compact_id FROM bot_history_messages WHERE id = $1
	`, fixture.lowMessage.ID).Scan(&currentClaim); err != nil {
		t.Fatalf("read current message claim: %v", err)
	}
	if currentClaim != targetUUID {
		t.Fatalf("current message claim = %v, want %v", currentClaim, targetUUID)
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

func repeatedCompactionClaim(id pgtype.UUID, count int) []pgtype.UUID {
	claims := make([]pgtype.UUID, count)
	for i := range claims {
		claims[i] = id
	}
	return claims
}

func successfulCompaction(id pgtype.UUID) dbsqlc.CompleteCompactionLogParams {
	return dbsqlc.CompleteCompactionLogParams{
		ID:           id,
		Status:       "ok",
		Summary:      "summary",
		MessageCount: 2,
		Coverage:     []byte("[]"),
	}
}
