//go:build integration

package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestListRecentContextLifecyclesBySessionKeysetCursor(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire database connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind default team: %v", err)
	}

	const (
		botID     = "00000000-0000-0000-0000-00000000b502"
		sessionID = "00000000-0000-0000-0000-00000000c502"
	)
	if _, err := conn.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ('context-lifecycle-cursor-owner', true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $1, principal.id FROM principal
  RETURNING user_id
), bot AS (
  INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
  SELECT $2, $1, membership.user_id, 'context-lifecycle-cursor-bot', 'ready', '{}' FROM membership
  RETURNING id
)
INSERT INTO bot_sessions (id, team_id, bot_id, channel_type, title, metadata)
SELECT $3, $1, bot.id, 'local', 'context lifecycle cursor', '{}' FROM bot
`, team.DefaultTeamID, botID, sessionID); err != nil {
		t.Fatalf("seed session: %v", err)
	}

	queries := sqlc.New(conn)
	parsedSessionID := mustParseLifecycleUUID(t, sessionID)
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	runIDs := make([]pgtype.UUID, 0, 4)
	for i := range 4 {
		runID := mustParseLifecycleUUID(t, fmt.Sprintf("00000000-0000-0000-0000-00000000d5%02d", i))
		runIDs = append(runIDs, runID)
		snapshot, err := json.Marshal(contextfrag.LifecycleSnapshot{
			Version: contextfrag.LifecycleSnapshotVersion,
			Counts:  contextfrag.ManifestCounts{Fragments: i + 1},
		})
		if err != nil {
			t.Fatalf("marshal snapshot: %v", err)
		}
		if _, err := queries.CreateContextLifecycle(ctx, sqlc.CreateContextLifecycleParams{
			RunID:     runID,
			BotID:     mustParseLifecycleUUID(t, botID),
			SessionID: parsedSessionID,
			Status:    "completed",
			Snapshot:  snapshot,
		}); err != nil {
			t.Fatalf("create lifecycle %d: %v", i, err)
		}
		// Two runs share a created_at so the run_id tie-break is exercised.
		createdAt := base.Add(time.Duration(i/2) * time.Minute)
		if _, err := conn.Exec(ctx, `UPDATE context_lifecycles SET created_at = $1 WHERE run_id = $2`, createdAt, runID); err != nil {
			t.Fatalf("set created_at %d: %v", i, err)
		}
	}

	first, err := queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: parsedSessionID,
		MaxCount:  2,
	})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(first) != 2 || first[0].RunID != runIDs[3] || first[1].RunID != runIDs[2] {
		t.Fatalf("first page = %#v, want newest two by (created_at, run_id) desc", first)
	}

	second, err := queries.ListRecentContextLifecyclesBySessionBefore(ctx, sqlc.ListRecentContextLifecyclesBySessionBeforeParams{
		SessionID:       parsedSessionID,
		MaxCount:        2,
		BeforeCreatedAt: first[1].CreatedAt,
		BeforeRunID:     first[1].RunID,
	})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if len(second) != 2 || second[0].RunID != runIDs[1] || second[1].RunID != runIDs[0] {
		t.Fatalf("second page = %#v, want the two older runs", second)
	}

	third, err := queries.ListRecentContextLifecyclesBySessionBefore(ctx, sqlc.ListRecentContextLifecyclesBySessionBeforeParams{
		SessionID:       parsedSessionID,
		MaxCount:        2,
		BeforeCreatedAt: second[1].CreatedAt,
		BeforeRunID:     second[1].RunID,
	})
	if err != nil {
		t.Fatalf("list third page: %v", err)
	}
	if len(third) != 0 {
		t.Fatalf("third page = %#v, want empty", third)
	}
}
