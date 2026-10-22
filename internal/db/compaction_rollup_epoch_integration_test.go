package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
)

// The segment-merge rollup completion must advance the session's Context
// Epoch in the SAME statement that supersedes the parents (Context Epoch
// spec, rules 3+6): a fenced-out attempt changes nothing, and a committed
// merge leaves both the supersession and the epoch record behind.
func TestCompleteCompactionRollupAdvancesContextEpochAtomically(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect postgres: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	bindTeamQueryFixture(t, ctx, tx)

	userID := uuid.New()
	if _, err := tx.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active) VALUES ($1, $2, true) RETURNING id
		)
		INSERT INTO team_members (user_id, role) SELECT id, 'admin' FROM created_user
	`, userID, "rollup-epoch-"+uuid.NewString()); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	botID := uuid.New()
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)`, botID, userID, "rollup-epoch-bot"); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	sessionID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, title, metadata)
		VALUES ($1, $2, 'local', 'merge', '{"context_epoch":{"epoch":3,"anchor_created_at_ms":1000,"last_reason":"budget"}}'::jsonb)
	`, sessionID, botID); err != nil {
		t.Fatalf("insert session: %v", err)
	}

	coverage := func(id string, ms int64) []byte {
		raw, err := json.Marshal([]map[string]any{{
			"ref": map[string]any{
				"namespace": "test", "id": id, "schema": "msg",
				"hash_algo": "sha256", "hash_scope": "source_payload", "content_hash": id,
			},
			"created_at_ms": ms,
		}})
		if err != nil {
			t.Fatalf("marshal coverage: %v", err)
		}
		return raw
	}
	parentIDs := make([]pgtype.UUID, 0, 8)
	for i := 0; i < 8; i++ {
		parentID := uuid.New()
		parentIDs = append(parentIDs, pgtype.UUID{Bytes: parentID, Valid: true})
		if _, err := tx.Exec(ctx, `
			INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status, summary, coverage, anchor_start_ms, anchor_end_ms)
			VALUES ($1, $2, $3, 'ok', $4, $5, $6, $6)
		`, parentID, botID, sessionID, "segment", coverage(uuid.NewString(), int64(i+1)*1000), int64(i+1)*1000); err != nil {
			t.Fatalf("insert parent %d: %v", i, err)
		}
	}
	mergeID := uuid.New()
	if _, err := tx.Exec(ctx, `
		INSERT INTO bot_history_message_compacts (id, bot_id, session_id, status)
		VALUES ($1, $2, $3, 'pending')
	`, mergeID, botID, sessionID); err != nil {
		t.Fatalf("insert merge log: %v", err)
	}

	epochState := []byte(`{"epoch":4,"anchor_created_at_ms":8000,"last_reason":"segment-merge"}`)
	q := sqlc.New(tx)

	// A stale epoch attempt (not strictly ahead of the stored 3) must no-op
	// the ENTIRE statement: no supersession, no completion, no epoch write.
	staleParams := sqlc.CompleteCompactionRollupParams{
		ID: pgtype.UUID{Bytes: mergeID, Valid: true}, Status: "ok", Summary: "merged",
		Parents: parentIDs, Coverage: coverage(uuid.NewString(), 8000),
		AnchorStartMs: 1000, AnchorEndMs: 8000, Level: 1,
		ContextEpochState: epochState, ContextEpoch: 3, ContextEpochAnchorMs: 8000,
	}
	if _, err := q.CompleteCompactionRollup(ctx, staleParams); err == nil {
		t.Fatal("stale epoch merge must be fenced out")
	}
	var superseded int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE superseded_by IS NOT NULL`).Scan(&superseded); err != nil {
		t.Fatalf("count superseded: %v", err)
	}
	if superseded != 0 {
		t.Fatalf("stale merge superseded %d parents", superseded)
	}
	var epoch int64
	if err := tx.QueryRow(ctx, `SELECT (metadata->'context_epoch'->>'epoch')::bigint FROM bot_sessions WHERE id = $1`, sessionID).Scan(&epoch); err != nil {
		t.Fatalf("read epoch: %v", err)
	}
	if epoch != 3 {
		t.Fatalf("stale merge moved the epoch to %d", epoch)
	}

	// The real merge: epoch 3→4 with the anchor at the merged coverage end,
	// committed together with the supersession.
	okParams := staleParams
	okParams.ContextEpoch = 4
	row, err := q.CompleteCompactionRollup(ctx, okParams)
	if err != nil {
		t.Fatalf("merge completion: %v", err)
	}
	if row.Status != "ok" || row.Summary != "merged" {
		t.Fatalf("merge row = %+v", row)
	}
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM bot_history_message_compacts WHERE superseded_by = $1`, mergeID).Scan(&superseded); err != nil {
		t.Fatalf("count superseded: %v", err)
	}
	if superseded != 8 {
		t.Fatalf("superseded = %d, want 8", superseded)
	}
	var state struct {
		Epoch             int64  `json:"epoch"`
		AnchorCreatedAtMs int64  `json:"anchor_created_at_ms"`
		LastReason        string `json:"last_reason"`
	}
	var raw string
	if err := tx.QueryRow(ctx, `SELECT (metadata->'context_epoch')::text FROM bot_sessions WHERE id = $1`, sessionID).Scan(&raw); err != nil {
		t.Fatalf("read epoch state: %v", err)
	}
	if err := json.Unmarshal([]byte(raw), &state); err != nil {
		t.Fatalf("decode epoch state: %v", err)
	}
	if state.Epoch != 4 || state.AnchorCreatedAtMs != 8000 || state.LastReason != "segment-merge" {
		t.Fatalf("epoch state = %+v, want (4, 8000, segment-merge)", state)
	}
}
