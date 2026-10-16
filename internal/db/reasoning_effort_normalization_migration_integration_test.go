//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

// 0165 rewrites the two retired spellings of reasoning off onto the token that
// survives, across the three columns that hold a user's effort choice. The
// mapping depends on data outside the column — "adaptive" on an always-on model
// means off, because the model cannot be turned off at all — so the migration is
// driven by a subquery over models.config. This test pins all three branches,
// plus the RLS posture the migration has to suspend and restore to reach them.
func TestReasoningEffortNormalizationMigration(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	// Roll back below 0165 so the legacy values can be inserted at all: the
	// migration itself is what removes them.
	migrateTo(t, dsn, 164)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("set team context: %v", err)
	}

	userID := uuid.NewString()
	username := "reasoning-normalize-" + uuid.NewString()
	if _, err := conn.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, username); err != nil {
		t.Fatalf("create user: %v", err)
	}

	// The always-on model the adaptive branch keys off, and a plain provider to
	// hang it on. A second model without the always mode covers the other half.
	providerID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO providers (id, name, client_type)
		VALUES ($1, $2, 'openai-completions')
	`, providerID, "normalize-provider-"+uuid.NewString()); err != nil {
		t.Fatalf("create provider: %v", err)
	}
	alwaysModelID := uuid.NewString()
	plainModelID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO models (id, provider_id, model_id, name, type, enable, config, team_id)
		VALUES ($1, $2, $3, 'Always', 'chat', true, '{"thinking_mode":"always"}'::jsonb, $4),
		       ($5, $2, $6, 'Plain',  'chat', true, '{"thinking_mode":"toggle"}'::jsonb, $4)
	`, alwaysModelID, providerID, "always-"+uuid.NewString(), team.DefaultTeamID,
		plainModelID, "plain-"+uuid.NewString()); err != nil {
		t.Fatalf("create models: %v", err)
	}

	insertBot := func(name, effort, modelID string) string {
		t.Helper()
		botID := uuid.NewString()
		var model any
		if modelID != "" {
			model = modelID
		}
		if _, err := conn.Exec(ctx, `
			INSERT INTO bots (id, team_id, owner_user_id, name, reasoning_effort, chat_model_id)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, botID, team.DefaultTeamID, userID, name, effort, model); err != nil {
			t.Fatalf("create bot %s: %v", name, err)
		}
		return botID
	}

	noneBot := insertBot("normalize-none-"+uuid.NewString(), "none", "")
	alwaysBot := insertBot("normalize-adaptive-always-"+uuid.NewString(), "adaptive", alwaysModelID)
	plainBot := insertBot("normalize-adaptive-plain-"+uuid.NewString(), "adaptive", plainModelID)
	orphanBot := insertBot("normalize-adaptive-nomodel-"+uuid.NewString(), "adaptive", "")

	sessionID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, preferred_reasoning_effort)
		VALUES ($1, $2, 'none')
	`, sessionID, noneBot); err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionID2 := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, preferred_reasoning_effort)
		VALUES ($1, $2, 'adaptive')
	`, sessionID2, noneBot); err != nil {
		t.Fatalf("create session: %v", err)
	}

	scheduleID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
		INSERT INTO schedule (id, team_id, bot_id, name, description, pattern, command, reasoning_effort)
		VALUES ($1, $2, $3, $4, '', '* * * * *', 'x', 'none')
	`, scheduleID, team.DefaultTeamID, noneBot, "normalize-"+uuid.NewString()); err != nil {
		t.Fatalf("create schedule: %v", err)
	}

	migrateUpAll(t, dsn)

	assertEffort := func(label, query, id, want string) {
		t.Helper()
		var got string
		if err := conn.QueryRow(ctx, query, id).Scan(&got); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		if got != want {
			t.Errorf("%s = %q, want %q", label, got, want)
		}
	}
	assertEffort("bots.none", `SELECT reasoning_effort FROM bots WHERE id = $1`, noneBot, "disable")
	assertEffort("bots.adaptive on always-on model", `SELECT reasoning_effort FROM bots WHERE id = $1`, alwaysBot, "disable")
	assertEffort("bots.adaptive on a turn-off-able model", `SELECT reasoning_effort FROM bots WHERE id = $1`, plainBot, "medium")
	assertEffort("bots.adaptive with no model", `SELECT reasoning_effort FROM bots WHERE id = $1`, orphanBot, "medium")
	assertEffort("bot_sessions.none", `SELECT preferred_reasoning_effort FROM bot_sessions WHERE id = $1`, sessionID, "disable")
	assertEffort("bot_sessions.adaptive", `SELECT preferred_reasoning_effort FROM bot_sessions WHERE id = $1`, sessionID2, "medium")
	assertEffort("schedule.none", `SELECT reasoning_effort FROM schedule WHERE id = $1`, scheduleID, "disable")

	// The migration suspends FORCE RLS on every table it reads or writes and must
	// put it back. A table left without the policy would silently stop isolating
	// tenants.
	assertForcedRLS(t, ctx, pool, "bots", "bot_sessions", "schedule", "models")

	// The down migration deliberately keeps the data (the up is lossy), so it has
	// to be a clean no-op that leaves the schema posture alone.
	stepDown(t, dsn, 1)
	assertEffort("bots.none after down", `SELECT reasoning_effort FROM bots WHERE id = $1`, noneBot, "disable")
	assertForcedRLS(t, ctx, pool, "bots", "bot_sessions", "schedule", "models")
	stepUp(t, dsn, 1)
}

// assertForcedRLS fails when any named table is not both RLS-enabled and
// RLS-forced, which is the tenant isolation boundary the migration brackets.
func assertForcedRLS(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tables ...string) {
	t.Helper()
	for _, table := range tables {
		var enabled, forced bool
		if err := pool.QueryRow(ctx, `
			SELECT relrowsecurity, relforcerowsecurity
			  FROM pg_class
			 WHERE relname = $1 AND relnamespace = 'public'::regnamespace
		`, table).Scan(&enabled, &forced); err != nil {
			t.Fatalf("read RLS state for %s: %v", table, err)
		}
		if !enabled || !forced {
			t.Errorf("%s: rls enabled=%v forced=%v, want both true", table, enabled, forced)
		}
	}
}
