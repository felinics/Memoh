//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/team"
)

func TestBotWorkdirRemoteBindingRestrictMigrationRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
	dsn := teamMigrationDSN(t)

	assertBotWorkdirRemoteBindingDeleteAction(t, ctx, pool, "r", false)
	stepDown(t, dsn, countMigrationsFrom(t, "0142_bot_workdirs_remote_binding_restrict.up.sql"))
	assertBotWorkdirRemoteBindingDeleteAction(t, ctx, pool, "c", false)
	stepUp(t, dsn, countMigrationsFrom(t, "0142_bot_workdirs_remote_binding_restrict.up.sql"))
	assertBotWorkdirRemoteBindingDeleteAction(t, ctx, pool, "r", false)
}

func TestReferencedRemoteBindingCannotBeDeleted(t *testing.T) {
	ctx := context.Background()
	pool := freshMigratedDB(t)
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
	runtimeID := uuid.NewString()
	targetID := uuid.NewString()
	workdirID := uuid.NewString()
	sessionID := uuid.NewString()
	seeds := []struct {
		name  string
		query string
		args  []any
	}{
		{"user", "INSERT INTO users (id, username) VALUES ($1, $2)", []any{userID, "guard-" + userID[:8]}},
		{"membership", "INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')", []any{team.DefaultTeamID, userID}},
		{"bot", "INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)", []any{botID, userID, "guard-bot-" + botID[:8]}},
		{"runtime", "INSERT INTO user_runtimes (id, user_id, name, api_token) VALUES ($1, $2, 'Guard Runtime', $3)", []any{runtimeID, userID, "guard-token-" + runtimeID}},
		{"target", "INSERT INTO bot_remote_runtime_bindings (id, bot_id, runtime_id) VALUES ($1, $2, $3)", []any{targetID, botID, runtimeID}},
		{"workdir", `INSERT INTO bot_workdirs (id, bot_id, name, target_kind, remote_binding_id, path)
VALUES ($1, $2, 'Pinned Folder', 'remote', $3, '/tmp/pinned')`, []any{workdirID, botID, targetID}},
		{"session", "INSERT INTO bot_sessions (id, bot_id, workdir_id) VALUES ($1, $2, $3)", []any{sessionID, botID, workdirID}},
	}
	for _, seed := range seeds {
		if _, err := conn.Exec(ctx, seed.query, seed.args...); err != nil {
			t.Fatalf("seed referenced target %s: %v", seed.name, err)
		}
	}

	_, err = conn.Exec(ctx, `
INSERT INTO bot_workdirs (id, bot_id, name, target_kind, remote_binding_id, path)
VALUES ($1, $2, 'Invalid Folder', 'remote', $3, '/tmp/invalid')`,
		uuid.NewString(), botID, uuid.NewString(),
	)
	assertRemoteBindingForeignKeyViolation(t, err)

	_, err = conn.Exec(ctx, "DELETE FROM bot_remote_runtime_bindings WHERE id = $1", targetID)
	assertRemoteBindingRestrictViolation(t, err)
	if _, err := conn.Exec(ctx, "UPDATE bot_workdirs SET archived_at = now() WHERE id = $1", workdirID); err != nil {
		t.Fatalf("archive workdir: %v", err)
	}
	_, err = conn.Exec(ctx, "DELETE FROM bot_remote_runtime_bindings WHERE id = $1", targetID)
	assertRemoteBindingRestrictViolation(t, err)

	var pinnedWorkdirID string
	if err := conn.QueryRow(ctx, "SELECT workdir_id::text FROM bot_sessions WHERE id = $1", sessionID).Scan(&pinnedWorkdirID); err != nil {
		t.Fatalf("read pinned session: %v", err)
	}
	if pinnedWorkdirID != workdirID {
		t.Fatalf("session workdir = %q, want %q", pinnedWorkdirID, workdirID)
	}

	unreferencedRuntimeID := uuid.NewString()
	unreferencedTargetID := uuid.NewString()
	if _, err := conn.Exec(ctx,
		"INSERT INTO user_runtimes (id, user_id, name, api_token) VALUES ($1, $2, 'Unused Runtime', $3)",
		unreferencedRuntimeID, userID, "guard-token-"+unreferencedRuntimeID,
	); err != nil {
		t.Fatalf("seed unreferenced runtime: %v", err)
	}
	if _, err := conn.Exec(ctx,
		"INSERT INTO bot_remote_runtime_bindings (id, bot_id, runtime_id) VALUES ($1, $2, $3)",
		unreferencedTargetID, botID, unreferencedRuntimeID,
	); err != nil {
		t.Fatalf("seed unreferenced target: %v", err)
	}
	result, err := conn.Exec(ctx, "DELETE FROM bot_remote_runtime_bindings WHERE id = $1", unreferencedTargetID)
	if err != nil {
		t.Fatalf("delete unreferenced target: %v", err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("deleted rows = %d, want 1", result.RowsAffected())
	}
}

func assertRemoteBindingForeignKeyViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("insert invalid remote binding error = %v, want PostgreSQL error", err)
	}
	if pgErr.Code != "23503" || pgErr.ConstraintName != "bot_workdirs_remote_binding_fkey" {
		t.Fatalf("insert invalid remote binding = SQLSTATE %q constraint %q, want 23503 bot_workdirs_remote_binding_fkey", pgErr.Code, pgErr.ConstraintName)
	}
}

func assertRemoteBindingRestrictViolation(t *testing.T, err error) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("delete referenced target error = %v, want PostgreSQL error", err)
	}
	if pgErr.Code != "23001" || pgErr.ConstraintName != "bot_workdirs_remote_binding_fkey" {
		t.Fatalf("delete referenced target = SQLSTATE %q constraint %q, want 23001 bot_workdirs_remote_binding_fkey", pgErr.Code, pgErr.ConstraintName)
	}
}

func assertBotWorkdirRemoteBindingDeleteAction(t *testing.T, ctx context.Context, pool *pgxpool.Pool, want string, wantValidated bool) {
	t.Helper()
	var deleteAction string
	var validated bool
	if err := pool.QueryRow(ctx, `
SELECT confdeltype::text, convalidated
FROM pg_constraint
WHERE conrelid = 'public.bot_workdirs'::regclass
  AND conname = 'bot_workdirs_remote_binding_fkey'
`).Scan(&deleteAction, &validated); err != nil {
		t.Fatalf("inspect bot_workdirs remote binding constraint: %v", err)
	}
	if deleteAction != want || validated != wantValidated {
		t.Fatalf("remote binding constraint = delete action %q, validated %t; want %q, %t", deleteAction, validated, want, wantValidated)
	}
}
