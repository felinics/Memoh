package ledger_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// Rows that reference the bot take FOR KEY SHARE on it in their foreign key
// check. The admission lock must leave that lock grantable: an insert waiting
// on admission while admission waits on a session row the insert already
// key-share locked is a deadlock.
func TestPostgresAdmissionLockLeavesForeignKeyChecksGrantable(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		t.Fatal(err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := dbsqlc.New(tx).LockBotForSessionRunClaim(ctx, pgBotID); err != nil {
		t.Fatalf("lock admission bot: %v", err)
	}

	insertCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := pool.Exec(insertCtx, `
		INSERT INTO bot_session_events (bot_id, session_id, event_kind, event_data, received_at_ms)
		VALUES ($1, $2, 'message', '{}', 0)
	`, botID, sessionID); err != nil {
		t.Fatalf("insert an event of the bot while admission holds its lock: %v", err)
	}
}

// Claim also locks the session row, which rows that reference the session
// key-share lock in their foreign key check.
func TestPostgresClaimLockLeavesForeignKeyChecksGrantable(t *testing.T) {
	ctx := context.Background()
	pool := openLedgerResetPostgres(t, ctx)
	botID, sessionID := createLedgerResetFixture(t, ctx, pool)
	pgBotID, err := dbpkg.ParseUUID(botID)
	if err != nil {
		t.Fatal(err)
	}
	pgSessionID, err := dbpkg.ParseUUID(sessionID)
	if err != nil {
		t.Fatal(err)
	}

	runID := pgtype.UUID{Bytes: uuid.New(), Valid: true}
	admitTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admission: %v", err)
	}
	defer func() { _ = admitTx.Rollback(context.WithoutCancel(ctx)) }()
	admit := dbsqlc.New(admitTx)
	if _, err := admit.LockBotForSessionRunClaim(ctx, pgBotID); err != nil {
		t.Fatalf("lock admission bot: %v", err)
	}
	if _, err := admit.AdmitLockedSessionRun(ctx, dbsqlc.AdmitLockedSessionRunParams{
		RunID:            runID,
		BotID:            pgBotID,
		InvocationID:     uuid.NewString(),
		TurnID:           pgtype.UUID{Bytes: uuid.New(), Valid: true},
		InputJson:        []byte(`{}`),
		InputFingerprint: "fingerprint",
		SessionID:        pgSessionID,
	}); err != nil {
		t.Fatalf("admit run: %v", err)
	}
	if err := admitTx.Commit(ctx); err != nil {
		t.Fatalf("commit admission: %v", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin claim: %v", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	q := dbsqlc.New(tx)
	if _, err := q.LockBotForSessionRunClaim(ctx, pgBotID); err != nil {
		t.Fatalf("lock claim bot: %v", err)
	}
	if _, err := q.ClaimLockedSessionRun(ctx, dbsqlc.ClaimLockedSessionRunParams{
		OwnerID:      pgtype.Text{String: "owner", Valid: true},
		FencingToken: 1,
		RunID:        runID,
		BotID:        pgBotID,
		SessionID:    pgSessionID,
	}); err != nil {
		t.Fatalf("claim run: %v", err)
	}

	insertCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := pool.Exec(insertCtx, `
		INSERT INTO bot_session_events (bot_id, session_id, event_kind, event_data, received_at_ms)
		VALUES ($1, $2, 'message', '{}', 0)
	`, botID, sessionID); err != nil {
		t.Fatalf("insert an event of the session while claim holds its lock: %v", err)
	}
}
