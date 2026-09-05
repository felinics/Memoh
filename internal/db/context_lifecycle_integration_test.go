//go:build integration

package db_test

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	contextfrag "github.com/felinics/memoh/internal/agent/context/fragment"
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

	contentHash := fmt.Sprintf("%x", sha256.Sum256([]byte(secret)))
	snapshot := contextfrag.LifecycleSnapshot{
		Version: contextfrag.LifecycleSnapshotVersion,
		View:    contextfrag.ViewRunConfigPreProvider,
		Counts: contextfrag.ManifestCounts{
			Fragments:     1,
			Messages:      1,
			TextBytes:     len(secret),
			TokenEstimate: 9,
		},
		SelectionDecisions: []contextfrag.SelectionDecision{{
			ID: "system-policy",
			Ref: contextfrag.ContextRef{
				Namespace:   "native-system",
				ID:          "policy",
				HashAlgo:    "sha256",
				ContentHash: contentHash,
				Schema:      "context-frag/v1",
			},
			Slot:          contextfrag.SlotSystem,
			Source:        "embedded-template",
			Decision:      contextfrag.DecisionSelected,
			TokenEstimate: 9,
			TextBytes:     len(secret),
			CacheClass:    contextfrag.CacheStable,
			RetentionTier: contextfrag.RetentionRequired,
		}},
		BudgetPlan: &contextfrag.ContextBudgetPlan{
			Estimator:                    contextfrag.ProviderBudgetEstimator,
			EstimatorSafetyFactorPercent: contextfrag.ProviderBudgetSafetyFactorPercent,
			Window:                       32_768,
			OutputReserve:                4_096,
			SystemBudget:                 8_192,
			ActualSystemCost:             9,
			HistoryBudget:                20_471,
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
	if _, err := queries.CreateContextLifecycle(ctx, sqlc.CreateContextLifecycleParams{
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Status:    "failed_budget",
		ErrorCode: pgtype.Text{String: "context.budget_unsatisfied", Valid: true},
		Snapshot:  snapshotJSON,
	}); err != nil {
		t.Fatalf("create context lifecycle: %v", err)
	}

	got, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("get context lifecycle: %v", err)
	}
	if got.RunID != parsedRunID || got.Status != "failed_budget" {
		t.Fatalf("created lifecycle identity = (%v, %q), want (%v, failed_budget)", got.RunID, got.Status, parsedRunID)
	}
	// The summary column never carries the per-fragment audit; that lives in
	// its own column so summary readers stay bounded however long the session.
	if strings.Contains(string(got.Snapshot), "selection_decisions") {
		t.Fatalf("summary column carries selection_decisions: %s", got.Snapshot)
	}
	var roundTripped contextfrag.LifecycleSnapshot
	if err := json.Unmarshal(got.Snapshot, &roundTripped); err != nil {
		t.Fatalf("unmarshal lifecycle snapshot: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, snapshot.Summary()) {
		t.Fatalf("round-tripped lifecycle summary = %#v, want %#v", roundTripped, snapshot.Summary())
	}
	audit, err := queries.GetContextLifecycleSelectionDecisionsByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("get selection decisions: %v", err)
	}
	var decisions []contextfrag.SelectionDecision
	if err := json.Unmarshal(audit, &decisions); err != nil {
		t.Fatalf("unmarshal selection decisions: %v", err)
	}
	if !reflect.DeepEqual(decisions, snapshot.SelectionDecisions) {
		t.Fatalf("persisted selection decisions = %#v, want %#v", decisions, snapshot.SelectionDecisions)
	}
	latest, err := queries.GetLatestContextLifecycleBySession(ctx, parsedSessionID)
	if err != nil {
		t.Fatalf("get latest context lifecycle summary: %v", err)
	}
	assertJSONSemanticallyEqual(t, latest, got.Snapshot)
	if strings.Contains(string(got.Snapshot), secret) || strings.Contains(string(audit), secret) {
		t.Fatal("persisted lifecycle snapshot contains raw prompt text")
	}

	const pausedRunID = "00000000-0000-0000-0000-00000000d502"
	pausedMetadata, err := json.Marshal(map[string]any{contextfrag.MetadataContextLifecycleKey: snapshot})
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
	pausedSnapshot, ok := contextfrag.LifecycleSnapshotFromMetadata(pausedRaw)
	if !ok || !reflect.DeepEqual(pausedSnapshot, snapshot) {
		t.Fatalf("paused lifecycle snapshot = %#v, %t; want %#v", pausedSnapshot, ok, snapshot)
	}
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
	if len(recent) != 1 || recent[0].RunID != parsedRunID || recent[0].Status != "failed_budget" ||
		!recent[0].ErrorCode.Valid || recent[0].ErrorCode.String != "context.budget_unsatisfied" {
		t.Fatalf("recent context lifecycles = %#v, want one failed_budget row for %s", recent, runID)
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
	if _, err := queries.UpsertAbortedContextLifecycle(ctx, sqlc.UpsertAbortedContextLifecycleParams{
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Snapshot:  replacementSnapshot,
	}); err != nil {
		t.Fatalf("upsert existing aborted context lifecycle: %v", err)
	}
	aborted, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read aborted context lifecycle: %v", err)
	}
	if aborted.Status != "aborted" || aborted.ErrorCode.Valid {
		t.Fatalf("aborted lifecycle terminal = (%q, %#v), want aborted with no error code", aborted.Status, aborted.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, aborted.Snapshot, got.Snapshot)
	assertJSONSemanticallyEqual(t, mustSelectionDecisions(t, ctx, queries, parsedRunID), audit)
	if aborted.CreatedAt != got.CreatedAt {
		t.Fatalf("aborted lifecycle changed created_at = %#v, want %#v", aborted.CreatedAt, got.CreatedAt)
	}

	const abortedRunID = "00000000-0000-0000-0000-00000000d503"
	parsedAbortedRunID := mustParseLifecycleUUID(t, abortedRunID)
	if _, err := queries.UpsertAbortedContextLifecycle(ctx, sqlc.UpsertAbortedContextLifecycleParams{
		RunID:     parsedAbortedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Snapshot:  replacementSnapshot,
	}); err != nil {
		t.Fatalf("insert aborted context lifecycle: %v", err)
	}
	insertedAborted, err := queries.GetContextLifecycleByRunID(ctx, parsedAbortedRunID)
	if err != nil {
		t.Fatalf("read inserted aborted context lifecycle: %v", err)
	}
	if insertedAborted.Status != "aborted" || insertedAborted.ErrorCode.Valid {
		t.Fatalf("inserted aborted lifecycle = %#v", insertedAborted)
	}
	assertJSONSemanticallyEqual(t, insertedAborted.Snapshot, replacementSnapshot)

	authoritativeSnapshot := []byte(`{"version":1000}`)
	if _, err := queries.UpdateAbortedContextLifecycleSnapshot(ctx, sqlc.UpdateAbortedContextLifecycleSnapshotParams{
		Snapshot:  authoritativeSnapshot,
		RunID:     parsedAbortedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
	}); err != nil {
		t.Fatalf("replace recovered aborted snapshot: %v", err)
	}
	convergedAborted, err := queries.GetContextLifecycleByRunID(ctx, parsedAbortedRunID)
	if err != nil {
		t.Fatalf("read converged aborted context lifecycle: %v", err)
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

	// The era probe reports metadata rows that never materialized into the
	// run-keyed table (the paused run above), and clears once a lifecycle row
	// exists for the same run.
	unmaterialized, err := queries.HasUnmaterializedContextLifecycleMetadataBySession(ctx, parsedSessionID)
	if err != nil {
		t.Fatalf("probe unmaterialized lifecycles: %v", err)
	}
	if !unmaterialized {
		t.Fatal("probe = false, want true while paused metadata has no lifecycle row")
	}
	if _, err := queries.CreateContextLifecycle(ctx, sqlc.CreateContextLifecycleParams{
		RunID:     parsedPausedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
		Status:    "aborted",
		Snapshot:  got.Snapshot,
	}); err != nil {
		t.Fatalf("materialize paused lifecycle row: %v", err)
	}
	unmaterialized, err = queries.HasUnmaterializedContextLifecycleMetadataBySession(ctx, parsedSessionID)
	if err != nil {
		t.Fatalf("probe after materialization: %v", err)
	}
	if unmaterialized {
		t.Fatal("probe = true, want false once every metadata run has a lifecycle row")
	}

	// Summary-only rewrites, which is what every read-back-and-write path now
	// produces, must never erase the audit column.
	if _, err := queries.UpdateAbortedContextLifecycleSnapshot(ctx, sqlc.UpdateAbortedContextLifecycleSnapshotParams{
		Snapshot:  []byte(`{"version":2,"source":"summary-only"}`),
		RunID:     parsedRunID,
		BotID:     parsedBotID,
		SessionID: parsedSessionID,
	}); err != nil {
		t.Fatalf("rewrite aborted summary: %v", err)
	}
	rewritten, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read rewritten aborted lifecycle: %v", err)
	}
	assertJSONSemanticallyEqual(t, rewritten.Snapshot, []byte(`{"version":2,"source":"summary-only"}`))
	assertJSONSemanticallyEqual(t, mustSelectionDecisions(t, ctx, queries, parsedRunID), audit)
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "completed",
		Snapshot:        []byte(`{"version":2,"source":"terminal-summary-only"}`),
		ReplaceSnapshot: true,
	}); err != nil {
		t.Fatalf("replace terminal summary: %v", err)
	}
	replaced, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read replaced terminal lifecycle: %v", err)
	}
	assertJSONSemanticallyEqual(t, mustSelectionDecisions(t, ctx, queries, parsedRunID), audit)
	if strings.Contains(string(replaced.Snapshot), "selection_decisions") || !strings.Contains(string(replaced.Snapshot), "terminal-summary-only") {
		t.Fatalf("replaced summary = %s, want the new summary without the audit", replaced.Snapshot)
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
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "completed",
		Snapshot:        initialSnapshot,
		ReplaceSnapshot: true,
	}); err != nil {
		t.Fatalf("insert terminal context lifecycle: %v", err)
	}
	created, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if created.Status != "completed" || created.ErrorCode.Valid {
		t.Fatalf("created terminal lifecycle = (%q, %#v), want completed with no error", created.Status, created.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, created.Snapshot, initialSnapshot)

	authoritativeSnapshot := []byte(`{"version":2,"source":"terminal-candidate"}`)
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "failed_provider",
		ErrorCode:       pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:        authoritativeSnapshot,
		ReplaceSnapshot: true,
	}); err != nil {
		t.Fatalf("replace terminal context lifecycle: %v", err)
	}
	failed, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if failed.Status != "failed_provider" || !failed.ErrorCode.Valid || failed.ErrorCode.String != "runtime.generic" {
		t.Fatalf("replaced terminal lifecycle = (%q, %#v), want failed_provider/runtime.generic", failed.Status, failed.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, failed.Snapshot, authoritativeSnapshot)
	if failed.CreatedAt != created.CreatedAt {
		t.Fatalf("terminal upsert changed created_at = %#v, want %#v", failed.CreatedAt, created.CreatedAt)
	}

	errorOnlySnapshot := []byte(`{"version":0,"source":"error-only-repair"}`)
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "provider.timeout", Valid: true},
		Snapshot:         errorOnlySnapshot,
		ReplaceSnapshot:  false,
		ReplaceErrorCode: true,
	}); err != nil {
		t.Fatalf("apply error-only lifecycle repair: %v", err)
	}
	errorOnlyRepair, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if !errorOnlyRepair.ErrorCode.Valid || errorOnlyRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("error-only repair code = %#v, want provider.timeout", errorOnlyRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, errorOnlyRepair.Snapshot, authoritativeSnapshot)

	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:           parsedRunID,
		BotID:           parsedBotID,
		SessionID:       parsedSessionID,
		Status:          "failed_provider",
		ErrorCode:       pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:        []byte(`{"version":0,"source":"stale-repair"}`),
		ReplaceSnapshot: false,
	}); err != nil {
		t.Fatalf("apply stale same-status lifecycle repair: %v", err)
	}
	staleRepair, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if staleRepair.Status != "failed_provider" || !staleRepair.ErrorCode.Valid || staleRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("stale repair lifecycle = (%q, %#v), want richer provider.timeout code", staleRepair.Status, staleRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, staleRepair.Snapshot, authoritativeSnapshot)

	recoveredMetadataSnapshot := []byte(`{"version":2,"source":"recovered-assistant-metadata"}`)
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "runtime.generic", Valid: true},
		Snapshot:         recoveredMetadataSnapshot,
		ReplaceSnapshot:  true,
		ReplaceErrorCode: false,
	}); err != nil {
		t.Fatalf("apply recovered-metadata lifecycle repair: %v", err)
	}
	recoveredMetadataRepair, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if !recoveredMetadataRepair.ErrorCode.Valid || recoveredMetadataRepair.ErrorCode.String != "provider.timeout" {
		t.Fatalf("recovered-metadata repair code = %#v, want richer provider.timeout code", recoveredMetadataRepair.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, recoveredMetadataRepair.Snapshot, recoveredMetadataSnapshot)

	reclassifiedSnapshot := []byte(`{"version":3,"source":"authoritative-reclassification"}`)
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, sqlc.UpsertTerminalContextLifecycleParams{
		RunID:            parsedRunID,
		BotID:            parsedBotID,
		SessionID:        parsedSessionID,
		Status:           "failed_provider",
		ErrorCode:        pgtype.Text{String: "provider.reclassified", Valid: true},
		Snapshot:         reclassifiedSnapshot,
		ReplaceSnapshot:  true,
		ReplaceErrorCode: true,
	}); err != nil {
		t.Fatalf("replace same-status lifecycle authoritatively: %v", err)
	}
	reclassified, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
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
	if _, err := queries.UpsertTerminalContextLifecycle(ctx, preserveArgs); err != nil {
		t.Fatalf("preserve terminal context lifecycle snapshot: %v", err)
	}
	preserved, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
	}
	if preserved.Status != "aborted" || preserved.ErrorCode.Valid {
		t.Fatalf("preserved terminal lifecycle = (%q, %#v), want aborted with no error", preserved.Status, preserved.ErrorCode)
	}
	assertJSONSemanticallyEqual(t, preserved.Snapshot, authoritativeSnapshot)
	if preserved.CreatedAt != created.CreatedAt {
		t.Fatalf("snapshot-preserving upsert changed created_at = %#v, want %#v", preserved.CreatedAt, created.CreatedAt)
	}

	if _, err := queries.UpsertTerminalContextLifecycle(ctx, preserveArgs); err != nil {
		t.Fatalf("repeat terminal context lifecycle upsert: %v", err)
	}
	idempotent, err := queries.GetContextLifecycleByRunID(ctx, parsedRunID)
	if err != nil {
		t.Fatalf("read terminal lifecycle after upsert: %v", err)
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
}, want bool,
) {
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

func TestContextLifecycleSelectionDecisionsMigrationSplitsAndRollsUpExistingRows(t *testing.T) {
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
		botID     = "00000000-0000-0000-0000-00000000b521"
		sessionID = "00000000-0000-0000-0000-00000000c521"
		runID     = "00000000-0000-0000-0000-00000000d521"
	)
	if _, err := conn.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ('lifecycle-split-owner', true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $1, principal.id FROM principal
  RETURNING user_id
), bot AS (
  INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
  SELECT $2, $1, membership.user_id, 'lifecycle-split-bot', 'ready', '{}' FROM membership
  RETURNING id
)
INSERT INTO bot_sessions (id, team_id, bot_id, channel_type, title, metadata)
SELECT $3, $1, bot.id, 'local', 'lifecycle split', '{}' FROM bot
`, team.DefaultTeamID, botID, sessionID); err != nil {
		t.Fatalf("seed lifecycle split owner: %v", err)
	}

	// Roll back only 0146 so a row can be written in the pre-split shape.
	stepDown(t, dsn, 1)
	legacySnapshot := `{
  "version": 2,
  "counts": {"fragments": 4, "token_estimate": 400},
  "selection": {"selected": 1, "dropped": 3, "drop_reasons": {"history_budget": 2, "unknown": 1}},
  "selection_decisions": [
    {"id": "message.001", "decision": "selected", "token_estimate": 100},
    {"id": "message.002", "decision": "dropped", "reason": "history_budget", "token_estimate": 120},
    {"id": "message.003", "decision": "dropped", "reason": " history_budget ", "token_estimate": 80},
    {"id": "message.004", "decision": "dropped", "token_estimate": 5},
    {"id": "message.005", "decision": "trimmed", "reason": "output_limit", "token_estimate": 60}
  ]
}`
	if _, err := conn.Exec(ctx, `
INSERT INTO context_lifecycles (run_id, team_id, bot_id, session_id, status, snapshot)
VALUES ($1, $2, $3, $4, 'completed', $5::jsonb)
`, runID, team.DefaultTeamID, botID, sessionID, legacySnapshot); err != nil {
		t.Fatalf("seed pre-split lifecycle row: %v", err)
	}

	stepUp(t, dsn, 1)
	var (
		embedded  bool
		decisions int
		selection []byte
	)
	if err := conn.QueryRow(ctx, `
SELECT snapshot ? 'selection_decisions', jsonb_array_length(selection_decisions), snapshot -> 'selection'
FROM context_lifecycles WHERE run_id = $1
`, runID).Scan(&embedded, &decisions, &selection); err != nil {
		t.Fatalf("inspect migrated lifecycle row: %v", err)
	}
	if embedded || decisions != 5 {
		t.Fatalf("migrated row embedded=%t decisions=%d, want the audit moved to its own column", embedded, decisions)
	}
	var trace contextfrag.SelectionTrace
	if err := json.Unmarshal(selection, &trace); err != nil {
		t.Fatalf("decode migrated selection trace: %v", err)
	}
	wantTokens := map[string]int{"history_budget": 200, "unknown": 5}
	if trace.Trimmed != 1 || !reflect.DeepEqual(trace.DropReasonTokens, wantTokens) || trace.Dropped != 3 || trace.Selected != 1 {
		t.Fatalf("migrated selection trace = %+v, want trimmed=1 tokens=%v with counts kept", trace, wantTokens)
	}

	queries := sqlc.New(conn)
	latest, err := queries.GetLatestContextLifecycleBySession(ctx, mustParseLifecycleUUID(t, sessionID))
	if err != nil {
		t.Fatalf("get latest lifecycle summary: %v", err)
	}
	if strings.Contains(string(latest), "selection_decisions") || !strings.Contains(string(latest), "drop_reason_tokens") {
		t.Fatalf("latest summary = %s, want rolled-up trace without the audit", latest)
	}

	// Rolling back folds the audit into the snapshot again and removes the rollup.
	stepDown(t, dsn, 1)
	var folded []byte
	if err := conn.QueryRow(ctx, `SELECT snapshot FROM context_lifecycles WHERE run_id = $1`, runID).Scan(&folded); err != nil {
		t.Fatalf("inspect rolled-back lifecycle row: %v", err)
	}
	assertJSONSemanticallyEqual(t, folded, []byte(legacySnapshot))
	stepUp(t, dsn, 1)
}

func mustSelectionDecisions(t *testing.T, ctx context.Context, queries *sqlc.Queries, runID pgtype.UUID) []byte {
	t.Helper()
	audit, err := queries.GetContextLifecycleSelectionDecisionsByRunID(ctx, runID)
	if err != nil {
		t.Fatalf("get selection decisions for %v: %v", runID, err)
	}
	return audit
}
