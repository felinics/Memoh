//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/db/postgres/sqlc"
	"github.com/felinics/memoh/internal/team"
)

func TestListTerminalSessionRunsNeedingContextLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	const (
		teamTwo          = "00000000-0000-0000-0000-0000000000f2"
		teamOneBotID     = "00000000-0000-0000-0000-00000000b521"
		teamOneSessionID = "00000000-0000-0000-0000-00000000c521"
		runningSessionID = "00000000-0000-0000-0000-00000000c522"
		waitingSessionID = "00000000-0000-0000-0000-00000000c523"
		teamTwoBotID     = "00000000-0000-0000-0000-00000000b529"
		teamTwoSessionID = "00000000-0000-0000-0000-00000000c529"
		teamTwoRunID     = "00000000-0000-0000-0000-00000000d529"
	)
	if _, err := pool.Exec(ctx, `INSERT INTO teams (id, slug) VALUES ($1, 'lifecycle-reconciliation-team-two')`, teamTwo); err != nil {
		t.Fatalf("seed reconciliation team two: %v", err)
	}
	seedLifecycleReconciliationTeam(
		t,
		ctx,
		pool,
		team.DefaultTeamID,
		"lifecycle-reconciliation-owner-one",
		teamOneBotID,
		teamOneSessionID,
		runningSessionID,
		waitingSessionID,
	)
	seedLifecycleReconciliationTeam(
		t,
		ctx,
		pool,
		teamTwo,
		"lifecycle-reconciliation-owner-two",
		teamTwoBotID,
		teamTwoSessionID,
	)

	if _, err := pool.Exec(ctx, `
INSERT INTO session_runs (
  run_id, team_id, bot_id, session_id, invocation_id, turn_id,
  turn_position, state, input_json, input_fingerprint, fencing_token,
  error_code, error_message, created_at, updated_at
)
SELECT
  fixtures.run_id::uuid,
  $1::uuid,
  $2::uuid,
  fixtures.session_id::uuid,
  fixtures.invocation_id,
  gen_random_uuid(),
  fixtures.turn_position,
  fixtures.state,
  '{}'::jsonb,
  'fp-' || fixtures.invocation_id,
  fixtures.fencing_token,
  fixtures.error_code,
  fixtures.error_message,
  fixtures.terminal_at::timestamptz,
  fixtures.terminal_at::timestamptz
FROM (VALUES
  ('00000000-0000-0000-0000-00000000d520', $4::uuid, 'active-running',   1::bigint, 'running',          10::bigint, NULL::text,              NULL::text,            '2025-12-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d52f', $5::uuid, 'active-waiting',   1::bigint, 'waiting_decision', 10::bigint, NULL::text,              NULL::text,            '2025-12-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d521', $3::uuid, 'stale-completed',  1::bigint, 'completed',        11::bigint, NULL::text,              NULL::text,            '2026-01-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d522', $3::uuid, 'missing-aborted',  2::bigint, 'aborted',          12::bigint, NULL::text,              NULL::text,            '2026-01-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d523', $3::uuid, 'stale-failed',     3::bigint, 'failed',           13::bigint, 'provider.failed'::text, 'provider exploded', '2026-01-02T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d524', $3::uuid, 'missing-lost',     4::bigint, 'lost',             14::bigint, 'runtime.owner_lost',    'owner expired',      '2026-01-03T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d525', $3::uuid, 'aligned-completed',5::bigint, 'completed',        15::bigint, NULL::text,              NULL::text,            '2025-11-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d526', $3::uuid, 'aligned-aborted',  6::bigint, 'aborted',          16::bigint, NULL::text,              NULL::text,            '2025-11-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d527', $3::uuid, 'aligned-failed',   7::bigint, 'failed',           17::bigint, 'runtime.generic'::text, 'generic failure',    '2025-11-01T00:00:00Z'),
  ('00000000-0000-0000-0000-00000000d528', $3::uuid, 'aligned-lost',     8::bigint, 'lost',             18::bigint, 'runtime.owner_lost',    'owner expired',      '2025-11-01T00:00:00Z')
) AS fixtures(
  run_id, session_id, invocation_id, turn_position, state, fencing_token,
  error_code, error_message, terminal_at
)
`, team.DefaultTeamID, teamOneBotID, teamOneSessionID, runningSessionID, waitingSessionID); err != nil {
		t.Fatalf("seed team-one session runs: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO context_lifecycles (run_id, team_id, bot_id, session_id, status, error_code, snapshot)
VALUES
  ('00000000-0000-0000-0000-00000000d521', $1, $2, $3, 'aborted',         NULL,           '{}'),
  ('00000000-0000-0000-0000-00000000d523', $1, $2, $3, 'completed',       NULL,           '{}'),
  ('00000000-0000-0000-0000-00000000d525', $1, $2, $3, 'completed',       NULL,           '{}'),
  ('00000000-0000-0000-0000-00000000d526', $1, $2, $3, 'aborted',         NULL,           '{}'),
  ('00000000-0000-0000-0000-00000000d527', $1, $2, $3, 'failed_provider', 'app.specific', '{}'),
  ('00000000-0000-0000-0000-00000000d528', $1, $2, $3, 'failed_provider', NULL,           '{}')
`, team.DefaultTeamID, teamOneBotID, teamOneSessionID); err != nil {
		t.Fatalf("seed team-one context lifecycles: %v", err)
	}
	if _, err := pool.Exec(ctx, `
INSERT INTO session_runs (
  run_id, team_id, bot_id, session_id, invocation_id, turn_id,
  turn_position, state, input_json, input_fingerprint, fencing_token,
  created_at, updated_at
)
VALUES ($1, $2, $3, $4, 'team-two-missing', gen_random_uuid(), 1,
        'completed', '{}', 'fp-team-two-missing', 29,
        '2025-01-01T00:00:00Z', '2025-01-01T00:00:00Z')
`, teamTwoRunID, teamTwo, teamTwoBotID, teamTwoSessionID); err != nil {
		t.Fatalf("seed team-two terminal run: %v", err)
	}

	rls := rlsConn(t, pool, dsn)
	if _, err := rls.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("bind reconciliation team one: %v", err)
	}
	queries := sqlc.New(rls)
	limited, err := queries.ListTerminalSessionRunsNeedingContextLifecycle(ctx, 2)
	if err != nil {
		t.Fatalf("list limited lifecycle reconciliation candidates: %v", err)
	}
	assertLifecycleReconciliationRunIDs(t, limited, []string{
		"00000000-0000-0000-0000-00000000d521",
		"00000000-0000-0000-0000-00000000d522",
	})
	if limited[0].State != "completed" || limited[0].FencingToken != 11 {
		t.Fatalf("first reconciliation candidate = %#v, want completed token 11", limited[0])
	}
	if limited[1].State != "aborted" || limited[1].FencingToken != 12 {
		t.Fatalf("second reconciliation candidate = %#v, want aborted token 12", limited[1])
	}

	all, err := queries.ListTerminalSessionRunsNeedingContextLifecycle(ctx, 10)
	if err != nil {
		t.Fatalf("list lifecycle reconciliation candidates: %v", err)
	}
	assertLifecycleReconciliationRunIDs(t, all, []string{
		"00000000-0000-0000-0000-00000000d521",
		"00000000-0000-0000-0000-00000000d522",
		"00000000-0000-0000-0000-00000000d523",
		"00000000-0000-0000-0000-00000000d524",
	})
	if all[2].State != "failed" || all[2].FencingToken != 13 ||
		!all[2].ErrorCode.Valid || all[2].ErrorCode.String != "provider.failed" {
		t.Fatalf("failed reconciliation candidate = %#v", all[2])
	}
	if all[3].State != "lost" || all[3].FencingToken != 14 ||
		!all[3].ErrorCode.Valid || all[3].ErrorCode.String != "runtime.owner_lost" {
		t.Fatalf("lost reconciliation candidate = %#v", all[3])
	}

	if _, err := rls.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", teamTwo); err != nil {
		t.Fatalf("bind reconciliation team two: %v", err)
	}
	teamTwoRows, err := queries.ListTerminalSessionRunsNeedingContextLifecycle(ctx, 10)
	if err != nil {
		t.Fatalf("list team-two lifecycle reconciliation candidates: %v", err)
	}
	assertLifecycleReconciliationRunIDs(t, teamTwoRows, []string{teamTwoRunID})
	if teamTwoRows[0].BotID != mustParseLifecycleUUID(t, teamTwoBotID) ||
		teamTwoRows[0].SessionID != mustParseLifecycleUUID(t, teamTwoSessionID) {
		t.Fatalf("team-two reconciliation identity = %#v", teamTwoRows[0])
	}
}

func seedLifecycleReconciliationTeam(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	teamID string,
	username string,
	botID string,
	sessionIDs ...string,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
WITH principal AS (
  INSERT INTO users (username, is_active, metadata)
  VALUES ($1, true, '{}')
  RETURNING id
), membership AS (
  INSERT INTO team_members (team_id, user_id)
  SELECT $2, principal.id FROM principal
  RETURNING user_id
)
INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
SELECT $3, $2, membership.user_id, $1, 'ready', '{}' FROM membership
`, username, teamID, botID); err != nil {
		t.Fatalf("seed lifecycle reconciliation bot %s: %v", botID, err)
	}
	for _, sessionID := range sessionIDs {
		if _, err := pool.Exec(ctx, `
INSERT INTO bot_sessions (id, team_id, bot_id, channel_type, title, metadata)
VALUES ($1, $2, $3, 'local', 'lifecycle reconciliation', '{}')
`, sessionID, teamID, botID); err != nil {
			t.Fatalf("seed lifecycle reconciliation session %s: %v", sessionID, err)
		}
	}
}

func assertLifecycleReconciliationRunIDs(
	t *testing.T,
	rows []sqlc.ListTerminalSessionRunsNeedingContextLifecycleRow,
	want []string,
) {
	t.Helper()
	if len(rows) != len(want) {
		t.Fatalf("reconciliation candidate count = %d, want %d: %#v", len(rows), len(want), rows)
	}
	for i, runID := range want {
		parsed := mustParseLifecycleUUID(t, runID)
		if rows[i].RunID != parsed {
			t.Fatalf("reconciliation candidate %d run = %v, want %v", i, rows[i].RunID, parsed)
		}
	}
}
