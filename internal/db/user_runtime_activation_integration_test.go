//go:build integration

package db_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	memohdb "github.com/felinics/memoh/internal/db"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

func TestUserRuntimeCredentialLifecyclePostgresPath(t *testing.T) {
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
	if _, err := conn.Exec(ctx, "INSERT INTO users (id, username) VALUES ($1, $2)", userID, "runtime-lifecycle"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO team_members (user_id) VALUES ($1)", userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	store := postgresstore.NewWithQueries(dbsqlc.New(conn))
	expiredKey := "mrk_" + strings.Repeat("1", 64)
	expired, err := store.CreateUserRuntime(ctx, dbstore.CreateUserRuntimeInput{
		UserID: userID, Name: "Abandoned", APIToken: expiredKey,
	})
	if err != nil {
		t.Fatalf("create pending Runtime: %v", err)
	}
	if !expired.ActivatedAt.IsZero() || !expired.PendingExpiresAt.After(time.Now()) {
		t.Fatalf("new Runtime lifecycle = activated %v, pending expiry %v", expired.ActivatedAt, expired.PendingExpiresAt)
	}
	if _, err := store.GetUserRuntimeByAPIToken(ctx, expiredKey); err != nil {
		t.Fatalf("authenticate fresh pending Runtime: %v", err)
	}
	if _, err := conn.Exec(ctx, "UPDATE user_runtimes SET pending_expires_at = now() - INTERVAL '1 second' WHERE id = $1", expired.ID); err != nil {
		t.Fatalf("expire pending Runtime: %v", err)
	}
	if _, err := store.GetUserRuntimeByAPIToken(ctx, expiredKey); !errors.Is(err, memohdb.ErrNotFound) {
		t.Fatalf("authenticate expired pending Runtime error = %v, want not found", err)
	}
	if err := store.ExpirePendingUserRuntimes(ctx, userID); err != nil {
		t.Fatalf("clean expired pending Runtimes: %v", err)
	}
	var revoked bool
	if err := conn.QueryRow(ctx, "SELECT revoked_at IS NOT NULL FROM user_runtimes WHERE id = $1", expired.ID).Scan(&revoked); err != nil {
		t.Fatalf("read expired Runtime: %v", err)
	}
	if !revoked {
		t.Fatal("expired pending Runtime was not revoked")
	}

	activeKey := "mrk_" + strings.Repeat("2", 64)
	pending, err := store.CreateUserRuntime(ctx, dbstore.CreateUserRuntimeInput{
		UserID: userID, Name: "Connected", APIToken: activeKey,
	})
	if err != nil {
		t.Fatalf("create second pending Runtime: %v", err)
	}
	active, err := store.ActivateUserRuntime(ctx, pending.ID, activeKey)
	if err != nil {
		t.Fatalf("activate Runtime: %v", err)
	}
	if active.ActivatedAt.IsZero() || !active.PendingExpiresAt.IsZero() {
		t.Fatalf("activated Runtime lifecycle = activated %v, pending expiry %v", active.ActivatedAt, active.PendingExpiresAt)
	}
	if _, err := store.GetUserRuntimeByAPIToken(ctx, activeKey); err != nil {
		t.Fatalf("authenticate activated Runtime: %v", err)
	}
	reconnected, err := store.ActivateUserRuntime(ctx, pending.ID, activeKey)
	if err != nil {
		t.Fatalf("reactivate reusable Runtime: %v", err)
	}
	if !reconnected.ActivatedAt.Equal(active.ActivatedAt) || !reconnected.PendingExpiresAt.IsZero() {
		t.Fatalf("reconnected Runtime lifecycle = activated %v, pending expiry %v", reconnected.ActivatedAt, reconnected.PendingExpiresAt)
	}
}

func TestUserRuntimeActivationMigrationPreservesExistingCredentials(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)
	stepDown(t, dsn, countMigrationsFrom(t, "0133_user_runtime_activation_lifecycle.up.sql"))

	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire connection: %v", err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, "SELECT set_config('memoh.team_id', $1, false)", team.DefaultTeamID); err != nil {
		t.Fatalf("set team context: %v", err)
	}

	userID := uuid.NewString()
	runtimeID := uuid.NewString()
	if _, err := conn.Exec(ctx, "INSERT INTO users (id, username) VALUES ($1, $2)", userID, "runtime-upgrade"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO team_members (user_id) VALUES ($1)", userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO user_runtimes (id, user_id, name, api_token)
VALUES ($1, $2, 'Existing', $3)`, runtimeID, userID, "mrk_"+strings.Repeat("3", 64)); err != nil {
		t.Fatalf("insert pre-0133 Runtime: %v", err)
	}

	stepUp(t, dsn, 1)
	var activatedAt, createdAt time.Time
	var pendingExpiresAt *time.Time
	if err := conn.QueryRow(ctx, `
SELECT activated_at, pending_expires_at, created_at
FROM user_runtimes
WHERE id = $1`, runtimeID).Scan(&activatedAt, &pendingExpiresAt, &createdAt); err != nil {
		t.Fatalf("read migrated Runtime: %v", err)
	}
	if !activatedAt.Equal(createdAt) || pendingExpiresAt != nil {
		t.Fatalf("migrated lifecycle = activated %v, pending %v, created %v", activatedAt, pendingExpiresAt, createdAt)
	}
}

func TestRemoteMountDefaultMigrationPreservesExistingChoices(t *testing.T) {
	ctx := context.Background()
	dsn := teamMigrationDSN(t)
	pool := freshMigratedDB(t)
	stepDown(t, dsn, countMigrationsFrom(t, "0132_remote_mount_default_allow.up.sql"))

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
	existingRuntimeID := uuid.NewString()
	if _, err := conn.Exec(ctx, "INSERT INTO users (id, username) VALUES ($1, $2)", userID, "mount-default-upgrade"); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO team_members (user_id) VALUES ($1)", userID); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO bots (id, owner_user_id, name) VALUES ($1, $2, $3)", botID, userID, "mount-default-upgrade"); err != nil {
		t.Fatalf("insert bot: %v", err)
	}
	if _, err := conn.Exec(ctx, `
INSERT INTO user_runtimes (id, user_id, name, api_token)
VALUES ($1, $2, 'Existing', $3)`, existingRuntimeID, userID, "mrk_"+strings.Repeat("4", 64)); err != nil {
		t.Fatalf("insert existing Runtime: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO bot_remote_runtime_bindings (bot_id, runtime_id) VALUES ($1, $2)", botID, existingRuntimeID); err != nil {
		t.Fatalf("insert existing mount: %v", err)
	}

	stepUp(t, dsn, 1)
	assertRemoteMountModes(t, ctx, conn, existingRuntimeID, "ask", "ask")

	newRuntimeID := uuid.NewString()
	if _, err := conn.Exec(ctx, `
INSERT INTO user_runtimes (id, user_id, name, api_token)
VALUES ($1, $2, 'New', $3)`, newRuntimeID, userID, "mrk_"+strings.Repeat("5", 64)); err != nil {
		t.Fatalf("insert new Runtime: %v", err)
	}
	if _, err := conn.Exec(ctx, "INSERT INTO bot_remote_runtime_bindings (bot_id, runtime_id) VALUES ($1, $2)", botID, newRuntimeID); err != nil {
		t.Fatalf("insert new mount: %v", err)
	}
	assertRemoteMountModes(t, ctx, conn, newRuntimeID, "allow", "allow")
}

func assertRemoteMountModes(t *testing.T, ctx context.Context, conn *pgxpool.Conn, runtimeID, wantWrite, wantExec string) {
	t.Helper()
	var writeMode, execMode string
	if err := conn.QueryRow(ctx, `
SELECT tool_approval_config->'write'->>'mode', tool_approval_config->'exec'->>'mode'
FROM bot_remote_runtime_bindings
WHERE runtime_id = $1`, runtimeID).Scan(&writeMode, &execMode); err != nil {
		t.Fatalf("read mount approval modes: %v", err)
	}
	if writeMode != wantWrite || execMode != wantExec {
		t.Fatalf("mount approval modes = write %q exec %q, want write %q exec %q", writeMode, execMode, wantWrite, wantExec)
	}
}
