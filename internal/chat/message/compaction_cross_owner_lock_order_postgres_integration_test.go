package message

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

func TestPostgresCompactionClaimLocksMessagesInIDOrder(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	target := fixture.createLog(t)
	lowMessageID := mustTestUUID(t, fixture.user.ID)
	highMessageID := mustTestUUID(t, fixture.assistant.ID)
	if bytes.Compare(lowMessageID.Bytes[:], highMessageID.Bytes[:]) > 0 {
		lowMessageID, highMessageID = highMessageID, lowMessageID
	}

	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin low message blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_history_messages WHERE id = $1 FOR UPDATE
	`, lowMessageID); err != nil {
		t.Fatalf("lock low message: %v", err)
	}

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin reverse-order source claim: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	markPID := postgresBackendPID(t, markTx)
	type markResult struct {
		count int64
		err   error
	}
	marked := make(chan markResult, 1)
	go func() {
		count, err := dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
			CompactID:          target.ID,
			MessageIds:         []pgtype.UUID{highMessageID, lowMessageID},
			ExpectedCompactIds: emptyCompactionClaims(2),
		})
		marked <- markResult{count: count, err: err}
	}()
	waitForPostgresLock(t, fixture.pool, markPID)

	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin message order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_history_messages WHERE id = $1 FOR UPDATE NOWAIT
	`, highMessageID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("source claim locked high message before low message: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release message order probe: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release low message: %v", err)
	}

	select {
	case got := <-marked:
		if got.err != nil || got.count != 2 {
			t.Fatalf("reverse-order source claim after release = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("reverse-order source claim did not resume after release")
	}
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit reverse-order source claim: %v", err)
	}
}

func TestPostgresCompactionClaimLocksTargetArtifactBeforeMessages(t *testing.T) {
	fixture := setupCommittedCompactionFixture(t)
	ctx := context.Background()
	target := fixture.createLog(t)

	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin target artifact blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_history_message_compacts WHERE id = $1 FOR UPDATE
	`, target.ID); err != nil {
		t.Fatalf("lock target artifact: %v", err)
	}

	markTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin source claim: %v", err)
	}
	defer func() { _ = markTx.Rollback(ctx) }()
	markPID := postgresBackendPID(t, markTx)
	messageUUID := mustTestUUID(t, fixture.assistant.ID)
	type markResult struct {
		count int64
		err   error
	}
	marked := make(chan markResult, 1)
	go func() {
		count, err := dbsqlc.New(markTx).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
			CompactID:          target.ID,
			MessageIds:         []pgtype.UUID{messageUUID},
			ExpectedCompactIds: emptyCompactionClaims(1),
		})
		marked <- markResult{count: count, err: err}
	}()
	waitForPostgresLock(t, fixture.pool, markPID)

	probeTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin message order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_history_messages WHERE id = $1 FOR UPDATE NOWAIT
	`, fixture.assistant.ID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("source claim locked message before target artifact: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release message order probe: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release target artifact: %v", err)
	}

	select {
	case got := <-marked:
		if got.err != nil || got.count != 1 {
			t.Fatalf("source claim after target release = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source claim did not resume after target release")
	}
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit source claim: %v", err)
	}
}

func TestPostgresCrossOwnerClaimLocksOwnerSessionsInIDOrder(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	ctx := context.Background()
	targetArtifactID, staleArtifactID := orderedTestUUIDPair()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (
			id, bot_id, session_id, compaction_epoch
		)
		VALUES ($1, $3, $4, 0), ($2, $3, $5, 0)
	`, targetArtifactID, staleArtifactID, fixture.botID, fixture.highSessionID, fixture.lowSessionID); err != nil {
		t.Fatalf("insert cross-owner claims: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_messages SET compact_id = $1 WHERE id = $2
	`, staleArtifactID, fixture.highMessage.ID); err != nil {
		t.Fatalf("assign stale foreign claim: %v", err)
	}

	blockerTx, err := fixture.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin owner lock blocker: %v", err)
	}
	defer func() { _ = blockerTx.Rollback(ctx) }()
	if _, err := blockerTx.Exec(ctx, `
		SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE
	`, fixture.lowSessionID); err != nil {
		t.Fatalf("lock low owner session: %v", err)
	}
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
	messageUUID := mustTestUUID(t, fixture.highMessage.ID)
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
		t.Fatalf("begin owner order probe: %v", err)
	}
	if _, err := probeTx.Exec(ctx, `
		SELECT id FROM bot_sessions WHERE id = $1 FOR UPDATE NOWAIT
	`, fixture.highSessionID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("cross-owner claim locked high owner before low owner: %v", err)
	}
	if err := probeTx.Rollback(ctx); err != nil {
		t.Fatalf("release owner order probe: %v", err)
	}
	if err := blockerTx.Commit(ctx); err != nil {
		t.Fatalf("release owner lock blocker: %v", err)
	}

	select {
	case got := <-marked:
		if got.err != nil || got.count != 1 {
			t.Fatalf("cross-owner claim after owner release = %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cross-owner claim did not resume after owner release")
	}
	if err := markTx.Commit(ctx); err != nil {
		t.Fatalf("commit cross-owner claim: %v", err)
	}
}

func TestPostgresDeletedSourceDoesNotReclaimForeignArtifact(t *testing.T) {
	fixture := setupOrderedSessionFixture(t)
	ctx := context.Background()
	targetArtifactID, foreignArtifactID := orderedTestUUIDPair()
	if _, err := fixture.pool.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (
			id, bot_id, session_id, compaction_epoch
		)
		VALUES ($1, $3, $4, 0), ($2, $3, $5, 0)
	`, targetArtifactID, foreignArtifactID, fixture.botID, fixture.lowSessionID, fixture.highSessionID); err != nil {
		t.Fatalf("insert cross-owner claims: %v", err)
	}
	if _, err := fixture.pool.Exec(ctx, `
		UPDATE bot_history_messages SET compact_id = $1 WHERE id = $2
	`, foreignArtifactID, fixture.lowMessage.ID); err != nil {
		t.Fatalf("assign foreign claim: %v", err)
	}
	messageUUID := mustTestUUID(t, fixture.lowMessage.ID)
	if err := dbsqlc.New(fixture.pool).DeleteMessagesByIDs(ctx, []pgtype.UUID{messageUUID}); err != nil {
		t.Fatalf("delete claimed source message: %v", err)
	}

	marked, err := dbsqlc.New(fixture.pool).MarkMessagesCompacted(ctx, dbsqlc.MarkMessagesCompactedParams{
		CompactID:          mustTestUUID(t, targetArtifactID),
		MessageIds:         []pgtype.UUID{messageUUID},
		ExpectedCompactIds: []pgtype.UUID{mustTestUUID(t, foreignArtifactID)},
	})
	if err != nil || marked != 0 {
		t.Fatalf("claim deleted source = %d, %v, want zero rows", marked, err)
	}
	var status string
	if err := fixture.pool.QueryRow(ctx, `
		SELECT status FROM bot_history_message_compacts WHERE id = $1
	`, foreignArtifactID).Scan(&status); err != nil {
		t.Fatalf("read foreign claim status: %v", err)
	}
	if status != "pending" {
		t.Fatalf("foreign claim status after stale source CAS = %q, want pending", status)
	}
}
