//go:build integration

package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestContextLifecycleMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	assertContextLifecycleSchema(t, ctx, pool, true)
	stepDown(t, dsn, countMigrationsFrom(t, "0134_context_lifecycles.up.sql"))
	assertContextLifecycleSchema(t, ctx, pool, false)
	stepUp(t, dsn, countMigrationsFrom(t, "0134_context_lifecycles.up.sql"))
	assertContextLifecycleSchema(t, ctx, pool, true)
}

func TestCanonicalInitContainsContextLifecycles(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := resetToEmpty(t)
	applyCanonicalInitOnly(t, dsn)

	assertContextLifecycleSchema(t, ctx, pool, true)
}

func TestContextLifecycleQueriesRoundTripContentLight(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire database connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind default team: %v", err)
	}

	const (
		botID     = "00000000-0000-0000-0000-00000000b501"
		sessionID = "00000000-0000-0000-0000-00000000c501"
		runID     = "00000000-0000-0000-0000-00000000d501"
		secret    = "private prompt text must never be persisted"
	)
	if _, err := conn.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ('context-lifecycle-owner', true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $1, principal.id FROM principal
  RETURNING user_id
), bot AS (
  INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
  SELECT $2, $1, membership.user_id, 'context-lifecycle-bot', 'ready', '{}' FROM membership
  RETURNING id
)
INSERT INTO bot_sessions (id, team_id, bot_id, channel_type, title, metadata)
SELECT $3, $1, bot.id, 'local', 'context lifecycle', '{}' FROM bot
`, team.DefaultTeamID, botID, sessionID); err != nil {
		t.Fatalf("seed context lifecycle owner: %v", err)
	}

	snapshot := map[string]any{
		"version": 1,
		"view":    "run_config_pre_provider",
		"counts": map[string]any{
			"fragments":  1,
			"messages":   1,
			"images":     0,
			"text_bytes": len(secret),
		},
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal lifecycle snapshot: %v", err)
	}
	if strings.Contains(string(snapshotJSON), secret) {
		t.Fatal("content-light lifecycle snapshot contains raw prompt text before persistence")
	}

	parsedRunID := mustParseLifecycleUUID(t, runID)
	parsedBotID := mustParseLifecycleUUID(t, botID)
	parsedSessionID := mustParseLifecycleUUID(t, sessionID)
	queries := sqlc.New(conn)
	created, err := queries.CreateContextLifecycle(ctx, sqlc.CreateContextLifecycleParams{
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Status:    "failed_provider",
		ErrorCode: pgtype.Text{String: "workspace.unreachable", Valid: true},
		Snapshot:  snapshotJSON,
	})
	if err != nil {
		t.Fatalf("create context lifecycle: %v", err)
	}
	if created.RunID != parsedRunID || created.Status != "failed_provider" {
		t.Fatalf("created lifecycle identity = (%v, %q), want (%v, failed_provider)", created.RunID, created.Status, parsedRunID)
	}

	got, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("get context lifecycle: %v", err)
	}
	assertJSONSemanticallyEqual(t, got.Snapshot, snapshotJSON)
	if strings.Contains(string(got.Snapshot), secret) {
		t.Fatal("persisted lifecycle snapshot contains raw prompt text")
	}

	const pausedRunID = "00000000-0000-0000-0000-00000000d502"
	pausedMetadata, err := json.Marshal(map[string]any{"context_lifecycle": snapshot})
	if err != nil {
		t.Fatalf("marshal paused lifecycle metadata: %v", err)
	}
	parsedPausedRunID := mustParseLifecycleUUID(t, pausedRunID)
	var pausedAssistantID pgtype.UUID
	if err := conn.QueryRow(ctx, `
INSERT INTO bot_history_messages (bot_id, session_id, role, content, metadata, run_id, created_at)
VALUES ($1, $2, 'assistant', '{}'::jsonb, $3, $4, '2026-01-01T00:00:00Z')
RETURNING id
`, botID, sessionID, pausedMetadata, pausedRunID).Scan(&pausedAssistantID); err != nil {
		t.Fatalf("seed paused assistant lifecycle: %v", err)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO bot_history_messages (bot_id, session_id, role, content, metadata, run_id, created_at)
VALUES ($1, $2, 'assistant', '{}'::jsonb, '{"other":"metadata"}'::jsonb,
        '00000000-0000-0000-0000-00000000d504', '2026-01-02T00:00:00Z')
`, botID, sessionID); err != nil {
		t.Fatalf("seed newer unrelated assistant metadata: %v", err)
	}
	pausedAssistant, err := queries.GetLatestAssistantContextLifecycleByRunID(ctx, parsedPausedRunID)
	if err != nil {
		t.Fatalf("get paused assistant lifecycle: %v", err)
	}
	if pausedAssistant.ID != pausedAssistantID {
		t.Fatalf("paused assistant ID = %v, want %v", pausedAssistant.ID, pausedAssistantID)
	}
	assertJSONSemanticallyEqual(t, pausedAssistant.Metadata, pausedMetadata)
	pausedRaw, err := queries.GetLatestAssistantContextLifecycleMetadataByRunID(ctx, parsedPausedRunID)
	if err != nil {
		t.Fatalf("get paused assistant lifecycle metadata: %v", err)
	}
	assertJSONSemanticallyEqual(t, pausedRaw, pausedMetadata)
	legacyRecent, err := queries.ListRecentAssistantMessagesBySession(ctx, sqlc.ListRecentAssistantMessagesBySessionParams{
		SessionID: parsedSessionID,
		MaxCount:  1,
	})
	if err != nil {
		t.Fatalf("list legacy assistant lifecycles: %v", err)
	}
	if len(legacyRecent) != 1 || legacyRecent[0].RunID != parsedPausedRunID {
		t.Fatalf("legacy assistant lifecycles = %#v, want run association %s", legacyRecent, pausedRunID)
	}

	recent, err := queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: parsedSessionID,
		MaxCount:  10,
	})
	if err != nil {
		t.Fatalf("list context lifecycles: %v", err)
	}
	if len(recent) != 1 || recent[0].RunID != parsedRunID || recent[0].Status != "failed_provider" ||
		!recent[0].ErrorCode.Valid || recent[0].ErrorCode.String != "workspace.unreachable" {
		t.Fatalf("recent context lifecycles = %#v, want one failed_provider row for %s", recent, runID)
	}

	if _, err := queries.CreateContextLifecycle(ctx, sqlc.CreateContextLifecycleParams{
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Status:    "completed",
		Snapshot:  []byte(`{}`),
	}); sqlState(err) != "23505" {
		t.Fatalf("duplicate run lifecycle SQLSTATE = %q, want 23505", sqlState(err))
	}

	replacementSnapshot := []byte(`{"version":999}`)
	aborted, err := queries.UpsertAbortedContextLifecycle(ctx, sqlc.UpsertAbortedContextLifecycleParams{
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Snapshot:  replacementSnapshot,
	})
	if err != nil {
		t.Fatalf("upsert existing aborted context lifecycle: %v", err)
	}
	if aborted.Status != "aborted" || aborted.ErrorCode.Valid {
		t.Fatalf("aborted lifecycle terminal = (%q, %#v), want aborted with no error code", aborted.Status, aborted.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, aborted.Snapshot, created.Snapshot)
	if aborted.CreatedAt != created.CreatedAt {
		t.Fatalf("aborted lifecycle changed created_at = %#v, want %#v", aborted.CreatedAt, created.CreatedAt)
	}

	const abortedRunID = "00000000-0000-0000-0000-00000000d503"
	parsedAbortedRunID := mustParseLifecycleUUID(t, abortedRunID)
	insertedAborted, err := queries.UpsertAbortedContextLifecycle(ctx, sqlc.UpsertAbortedContextLifecycleParams{
		RunID:     parsedAbortedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Snapshot:  replacementSnapshot,
	})
	if err != nil {
		t.Fatalf("insert aborted context lifecycle: %v", err)
	}
	if insertedAborted.Status != "aborted" || insertedAborted.ErrorCode.Valid {
		t.Fatalf("inserted aborted lifecycle = %#v", insertedAborted)
	}
	assertJSONSemanticallyEqual(t, insertedAborted.Snapshot, replacementSnapshot)

	authoritativeSnapshot := []byte(`{"version":1000}`)
	convergedAborted, err := queries.UpdateAbortedContextLifecycleSnapshot(ctx, sqlc.UpdateAbortedContextLifecycleSnapshotParams{
		Snapshot:  authoritativeSnapshot,
		RunID:     parsedAbortedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
	})
	if err != nil {
		t.Fatalf("replace recovered aborted snapshot: %v", err)
	}
	if convergedAborted.Status != "aborted" || convergedAborted.ErrorCode.Valid {
		t.Fatalf("converged aborted lifecycle = %#v", convergedAborted)
	}
	assertJSONSemanticallyEqual(t, convergedAborted.Snapshot, authoritativeSnapshot)
	if convergedAborted.CreatedAt != insertedAborted.CreatedAt {
		t.Fatalf("authoritative snapshot update changed created_at = %#v, want %#v", convergedAborted.CreatedAt, insertedAborted.CreatedAt)
	}

	if _, err := conn.Exec(ctx, `
UPDATE context_lifecycles
SET created_at = CASE run_id
  WHEN $1 THEN '2026-01-01T00:00:00Z'::timestamptz
  WHEN $2 THEN '2026-01-02T00:00:00Z'::timestamptz
END
WHERE run_id IN ($1, $2)
`, parsedRunID, parsedAbortedRunID); err != nil {
		t.Fatalf("set lifecycle ordering fixtures: %v", err)
	}
	limitedRecent, err := queries.ListRecentContextLifecyclesBySession(ctx, sqlc.ListRecentContextLifecyclesBySessionParams{
		SessionID: parsedSessionID,
		MaxCount:  1,
	})
	if err != nil {
		t.Fatalf("list limited context lifecycles: %v", err)
	}
	if len(limitedRecent) != 1 || limitedRecent[0].RunID != parsedAbortedRunID {
		t.Fatalf("limited context lifecycles = %#v, want newest run %s", limitedRecent, abortedRunID)
	}

	const teamTwo = "00000000-0000-0000-0000-0000000000f2"
	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'context-lifecycle-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed second team: %v", err)
	}
	rls := rlsConn(t, pool, dsn)
	if _, err := rls.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", teamTwo); err != nil {
		t.Fatalf("bind second team: %v", err)
	}
	var visible int
	if err := rls.QueryRow(ctx, "SELECT count(*) FROM context_lifecycles").Scan(&visible); err != nil {
		t.Fatalf("count second-team context lifecycles: %v", err)
	}
	if visible != 0 {
		t.Fatalf("second team saw %d context lifecycle rows, want 0", visible)
	}
	_, crossTeamErr := rls.Exec(ctx, `
INSERT INTO context_lifecycles (team_id, run_id, bot_id, session_id, status, snapshot)
VALUES ($1, gen_random_uuid(), $2, $3, 'completed', '{}'::jsonb)
`, team.DefaultTeamID, botID, sessionID)
	if sqlState(crossTeamErr) != "42501" {
		t.Fatalf("cross-team lifecycle insert SQLSTATE = %q, want 42501", sqlState(crossTeamErr))
	}
}

func TestUpsertTerminalContextLifecycleConvergesByRunIdentity(t *testing.T) {
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
		botID          = "00000000-0000-0000-0000-00000000b511"
		sessionID      = "00000000-0000-0000-0000-00000000c511"
		otherSessionID = "00000000-0000-0000-0000-00000000c512"
		runID          = "00000000-0000-0000-0000-00000000d511"
	)
	if _, err := conn.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ('terminal-lifecycle-owner', true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $1, principal.id FROM principal
  RETURNING user_id
), bot AS (
  INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
  SELECT $2, $1, membership.user_id, 'terminal-lifecycle-bot', 'ready', '{}' FROM membership
  RETURNING id
)
INSERT INTO bot_sessions (id, team_id, bot_id, channel_type, title, metadata)
SELECT sessions.session_id, $1, bot.id, 'local', 'terminal lifecycle', '{}'
FROM bot
CROSS JOIN unnest(ARRAY[$3::uuid, $4::uuid]) AS sessions(session_id)
`, team.DefaultTeamID, botID, sessionID, otherSessionID); err != nil {
		t.Fatalf("seed terminal lifecycle identity: %v", err)
	}

	queries := sqlc.New(conn)
	parsedRunID := mustParseLifecycleUUID(t, runID)
	parsedBotID := mustParseLifecycleUUID(t, botID)
	parsedSessionID := mustParseLifecycleUUID(t, sessionID)
	parsedOtherSessionID := mustParseLifecycleUUID(t, otherSessionID)
	initialSnapshot := []byte(`{"version":1,"source":"initial"}`)
	created, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "completed",
		Snapshot:        initialSnapshot,
		ReplaceSnapshot: true,
	})
	if err != nil {
		t.Fatalf("insert terminal context lifecycle: %v", err)
	}
	if created.Status != "completed" || created.ErrorCode.Valid {
		t.Fatalf("created terminal lifecycle = (%q, %#v), want completed with no error", created.Status, created.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, created.Snapshot, initialSnapshot)

	authoritativeSnapshot := []byte(`{"version":2,"source":"terminal-candidate"}`)
	failed, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "failed_provider",
		ErrorCode:       pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:        authoritativeSnapshot,
		ReplaceSnapshot: true,
	})
	if err != nil {
		t.Fatalf("replace terminal context lifecycle: %v", err)
	}
	if failed.Status != "failed_provider" || !failed.ErrorCode.Valid || failed.ErrorCode.String != "runtime.generic" {
		t.Fatalf("replaced terminal lifecycle = (%q, %#v), want failed_provider/runtime.generic", failed.Status, failed.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, failed.Snapshot, authoritativeSnapshot)
	if failed.CreatedAt != created.CreatedAt {
		t.Fatalf("terminal upsert changed created_at = %#v, want %#v", failed.CreatedAt, created.CreatedAt)
	}

	errorOnlySnapshot := []byte(`{"version":0,"source":"error-only-repair"}`)
	errorOnlyRepair, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "provider.timeout", Valid: true},
		Snapshot:         errorOnlySnapshot,
		ReplaceSnapshot:  false,
		ReplaceErrorCode: true,
	})
	if err != nil {
		t.Fatalf("apply error-only lifecycle repair: %v", err)
	}
	if !errorOnlyRepair.ErrorCode.Valid || errorOnlyRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("error-only repair code = %#v, want provider.timeout", errorOnlyRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, errorOnlyRepair.Snapshot, authoritativeSnapshot)

	staleRepair, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "failed_provider",
		ErrorCode:       pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:        []byte(`{"version":0,"source":"stale-repair"}`),
		ReplaceSnapshot: false,
	})
	if err != nil {
		t.Fatalf("apply stale same-status lifecycle repair: %v", err)
	}
	if staleRepair.Status != "failed_provider" || !staleRepair.ErrorCode.Valid || staleRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("stale repair lifecycle = (%q, %#v), want richer provider.timeout code", staleRepair.Status, staleRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, staleRepair.Snapshot, authoritativeSnapshot)

	recoveredMetadataSnapshot := []byte(`{"version":2,"source":"recovered-assistant-metadata"}`)
	recoveredMetadataRepair, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:         recoveredMetadataSnapshot,
		ReplaceSnapshot:  true,
		ReplaceErrorCode: false,
	})
	if err != nil {
		t.Fatalf("apply recovered-metadata lifecycle repair: %v", err)
	}
	if !recoveredMetadataRepair.ErrorCode.Valid || recoveredMetadataRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("recovered-metadata repair code = %#v, want richer provider.timeout code", recoveredMetadataRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, recoveredMetadataRepair.Snapshot, recoveredMetadataSnapshot)
	authoritativeSnapshot = recoveredMetadataSnapshot

	reclassifiedSnapshot := []byte(`{"version":3,"source":"authoritative-reclassification"}`)
	reclassified, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "provider.reclassified", Valid: true},
		Snapshot:         reclassifiedSnapshot,
		ReplaceSnapshot:  true,
		ReplaceErrorCode: true,
	})
	if err != nil {
		t.Fatalf("replace same-status lifecycle authoritatively: %v", err)
	}
	if !reclassified.ErrorCode.Valid || reclassified.ErrorCode.String != "provider.reclassified" {
		t.Fatalf("authoritative lifecycle code = %#v, want provider.reclassified", reclassified.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, reclassified.Snapshot, reclassifiedSnapshot)
	authoritativeSnapshot = reclassifiedSnapshot

	staleSnapshot := []byte(`{"version":0,"source":"stale"}`)
	preserveArgs := sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "aborted",
		Snapshot:        staleSnapshot,
		ReplaceSnapshot: false,
	}
	preserved, err := queries.UpsertTerminalContextLifecycle(ctx, preserveArgs)
	if err != nil {
		t.Fatalf("preserve terminal context lifecycle snapshot: %v", err)
	}
	if preserved.Status != "aborted" || preserved.ErrorCode.Valid {
		t.Fatalf("preserved terminal lifecycle = (%q, %#v), want aborted with no error", preserved.Status, preserved.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, preserved.Snapshot, authoritativeSnapshot)
	if preserved.CreatedAt != created.CreatedAt {
		t.Fatalf("snapshot-preserving upsert changed created_at = %#v, want %#v", preserved.CreatedAt, created.CreatedAt)
	}

	idempotent, err := queries.UpsertTerminalContextLifecycle(ctx, preserveArgs)
	if err != nil {
		t.Fatalf("repeat terminal context lifecycle upsert: %v", err)
	}
	if !reflect.DeepEqual(idempotent, preserved) {
		t.Fatalf("idempotent terminal upsert = %#v, want %#v", idempotent, preserved)
	}

	_, err = queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedOtherSessionID,
		Status:          "completed",
		Snapshot:        []byte(`{"version":3,"source":"wrong-session"}`),
		ReplaceSnapshot: true,
	})
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("cross-session terminal upsert error = %v, want pgx.ErrNoRows", err)
	}
	unchanged, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("reload terminal context lifecycle after rejected identity: %v", err)
	}
	if !reflect.DeepEqual(unchanged, preserved) {
		t.Fatalf("rejected cross-session upsert changed lifecycle: got %#v, want %#v", unchanged, preserved)
	}
}

func assertContextLifecycleSchema(t *testing.T, ctx context.Context, database interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, want bool) {
	t.Helper()
	var exists bool
	if err := database.QueryRow(ctx, "SELECT to_regclass('public.context_lifecycles') IS NOT NULL").Scan(&exists); err != nil {
		t.Fatalf("inspect context lifecycle table: %v", err)
	}
	if exists != want {
		t.Fatalf("context_lifecycles exists = %t, want %t", exists, want)
	}
	if !want {
		return
	}
	var (
		indexExists    bool
		rlsEnabled     bool
		rlsForced      bool
		statusValues   string
		sessionRunFKs  int
		tenantFKs      int
		tenantKeyFound bool
	)
	if err := database.QueryRow(ctx, `
SELECT
  to_regclass('public.idx_context_lifecycles_session_recent') IS NOT NULL,
  c.relrowsecurity,
  c.relforcerowsecurity,
  pg_get_constraintdef(status_con.oid),
  (SELECT count(*) FROM pg_constraint con
    WHERE con.conrelid = 'public.context_lifecycles'::regclass
      AND con.contype = 'f'
      AND con.confrelid = 'public.session_runs'::regclass),
  (SELECT count(*) FROM pg_constraint con
    WHERE con.conrelid = 'public.context_lifecycles'::regclass
      AND con.contype = 'f'
      AND con.confrelid IN ('public.bots'::regclass, 'public.bot_sessions'::regclass)),
  EXISTS (
    SELECT 1 FROM pg_constraint con
    WHERE con.conrelid = 'public.context_lifecycles'::regclass
      AND con.contype = 'u'
      AND pg_get_constraintdef(con.oid) = 'UNIQUE (team_id, run_id)'
  )
FROM pg_class c
JOIN pg_constraint status_con
  ON status_con.conrelid = c.oid
 AND status_con.conname = 'context_lifecycles_status_check'
WHERE c.oid = 'public.context_lifecycles'::regclass
`).Scan(&indexExists, &rlsEnabled, &rlsForced, &statusValues, &sessionRunFKs, &tenantFKs, &tenantKeyFound); err != nil {
		t.Fatalf("inspect context lifecycle schema: %v", err)
	}
	if !indexExists || !rlsEnabled || !rlsForced || sessionRunFKs != 0 || tenantFKs != 2 || !tenantKeyFound {
		t.Fatalf(
			"context lifecycle schema = index:%t rls:%t force:%t session_run_fks:%d tenant_fks:%d tenant_key:%t",
			indexExists, rlsEnabled, rlsForced, sessionRunFKs, tenantFKs, tenantKeyFound,
		)
	}
	for _, status := range []string{"completed", "failed_budget", "failed_provider", "fallback", "aborted"} {
		if !strings.Contains(statusValues, status) {
			t.Fatalf("context lifecycle status CHECK %q is missing %q", statusValues, status)
		}
	}
}

func mustParseLifecycleUUID(t *testing.T, value string) pgtype.UUID {
	t.Helper()
	parsed, err := dbpkg.ParseUUID(value)
	if err != nil {
		t.Fatalf("parse UUID %q: %v", value, err)
	}
	return parsed
}

func assertJSONSemanticallyEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("unmarshal got JSON %q: %v", got, err)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("unmarshal want JSON %q: %v", want, err)
	}
	if !reflect.DeepEqual(gotValue, wantValue) {
		t.Fatalf("JSON mismatch: got %#v, want %#v", gotValue, wantValue)
	}
}
