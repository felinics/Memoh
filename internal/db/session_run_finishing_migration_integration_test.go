//go:build integration

package db_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func TestSessionRunFinishingMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)
	// Later migrations may exist; isolate the 0143 down/up pair this test owns.
	migrateTo(t, dsn, 143)
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("set team context: %v", err)
	}

	userID := uuid.NewString()
	botID := uuid.NewString()
	sessionID := uuid.NewString()
	runID := uuid.NewString()
	name := "finishing-migration-" + uuid.NewString()
	if _, err := conn.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active)
			VALUES ($1, $2, true)
			RETURNING id
		)
		INSERT INTO team_members (user_id, role)
		SELECT id, 'admin' FROM created_user
	`, userID, name); err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := conn.Exec(ctx, `INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)`, botID, userID, name); err != nil {
		t.Fatalf("create bot: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO bot_sessions (id, bot_id, channel_type, runtime_type)
		VALUES ($1, $2, 'local', 'acp_agent')
	`, sessionID, botID); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if _, err := conn.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, owner_since,
			fencing_token, live_generation, proposed_terminal_state, finish_proposed_at
		) VALUES (
			$1, $2, $3, $4, $5, 1,
			'finishing', '{}'::jsonb, $6, 'owner-migration', now(),
			1, 'generation-migration', 'completed', now()
		)
	`, runID, botID, sessionID, uuid.NewString(), uuid.NewString(), "finishing-"+runID); err != nil {
		t.Fatalf("create finishing run: %v", err)
	}

	if _, err := conn.Exec(ctx, `
		INSERT INTO session_runs (
			run_id, bot_id, session_id, invocation_id, turn_id, turn_position,
			state, input_json, input_fingerprint, owner_id, owner_since,
			fencing_token, live_generation
		) VALUES ($1, $2, $3, $4, $5, 2, 'running', '{}'::jsonb, $6, 'other-owner', now(), 2, 'other-generation')
	`, uuid.NewString(), botID, sessionID, uuid.NewString(), uuid.NewString(), "blocked-by-finishing-"+runID); sqlState(err) != "23505" {
		t.Fatalf("parallel active run error = %v, want unique violation", err)
	}

	stepDown(t, dsn, 1)
	var state string
	if err := conn.QueryRow(ctx, `SELECT state FROM session_runs WHERE run_id = $1`, runID).Scan(&state); err != nil {
		t.Fatalf("load downgraded run: %v", err)
	}
	if state != "completed" {
		t.Fatalf("downgraded run state = %q, want completed", state)
	}
	var proposalColumnExists bool
	if err := conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'session_runs'
			  AND column_name = 'proposed_terminal_state'
		)
	`).Scan(&proposalColumnExists); err != nil {
		t.Fatalf("inspect downgraded columns: %v", err)
	}
	if proposalColumnExists {
		t.Fatal("proposal column remained after downgrade")
	}
	if predicate := sessionRunIndexPredicate(t, ctx, conn, "session_runs_single_active"); strings.Contains(predicate, "finishing") {
		t.Fatalf("downgraded active index still includes finishing: %s", predicate)
	}

	stepUp(t, dsn, 1)
	if predicate := sessionRunIndexPredicate(t, ctx, conn, "session_runs_single_active"); !strings.Contains(predicate, "finishing") {
		t.Fatalf("upgraded active index excludes finishing: %s", predicate)
	}
	if predicate := sessionRunIndexPredicate(t, ctx, conn, "idx_session_runs_recovery"); !strings.Contains(predicate, "finishing") {
		t.Fatalf("upgraded recovery index excludes finishing: %s", predicate)
	}
	migrateUpAll(t, dsn)
}

func sessionRunIndexPredicate(t *testing.T, ctx context.Context, conn *pgxpool.Conn, name string) string {
	t.Helper()
	var predicate string
	if err := conn.QueryRow(ctx, `
		SELECT pg_get_expr(i.indpred, i.indrelid)
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'public' AND c.relname = $1
	`, name).Scan(&predicate); err != nil {
		t.Fatalf("inspect index %s: %v", name, err)
	}
	return predicate
}
