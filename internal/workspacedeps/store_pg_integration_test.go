//go:build integration

package workspacedeps

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

var (
	depsMigrationOnce sync.Once
	depsMigrationErr  error
)

// openDependencyPostgres mirrors the other Postgres integration harnesses:
// TEST_POSTGRES_DSN selects the database, TEST_POSTGRES_BOOTSTRAP_SCHEMA=1
// runs the embedded migration chain first, and the pool binds the default
// team GUC exactly like production.
func openDependencyPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		depsMigrationOnce.Do(func() { depsMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if depsMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", depsMigrationErr)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// createDependencyBot seeds a user, its team membership, and a bot the
// installations can reference. Deleting the bot cascades to the records.
func createDependencyBot(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	uid := uuid.New()
	bid := uuid.New()
	name := "workspace-deps-it-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		), created_member AS (
			INSERT INTO team_members (team_id, user_id, role)
			SELECT $3, id, 'admin' FROM created_user
			RETURNING user_id
		)
		INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
		SELECT $4, $3, user_id, $2, 'ready', '{}'::jsonb
		FROM created_member`,
		uid, name, team.DefaultTeamID, bid,
	); err != nil {
		t.Fatalf("create dependency fixture: %v", err)
	}
	// The test context is already cancelled when cleanup runs.
	t.Cleanup(func() { //nolint:contextcheck // cleanup outlives the test context
		_, _ = pool.Exec(context.Background(), "DELETE FROM bots WHERE id = $1", bid)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", uid)
	})
	return bid.String()
}

func newIntegrationStore(pool *pgxpool.Pool) Store {
	return NewPostgresStore(postgresstore.NewQueries(dbsqlc.New(pool)))
}

func TestPostgresStoreLifecycle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	botID := createDependencyBot(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "codex"}

	// Upsert creates the intent row.
	created, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey:  key,
		Source:           InstallationSourceManaged,
		Status:           StatusInstalling,
		InstalledVersion: "",
		ManifestDigest:   "sha256:one",
	})
	if err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if created.ID == "" || created.BotID != botID || created.Status != StatusInstalling || created.Source != InstallationSourceManaged {
		t.Fatalf("created = %+v", created)
	}
	if created.LastCheckedAt != nil || created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
		t.Fatalf("defaults not applied: %+v", created)
	}

	// Get returns the same row.
	got, err := store.Get(ctx, key)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.ID != created.ID || got.ManifestDigest != "sha256:one" {
		t.Fatalf("get = %+v, want %+v", got, created)
	}

	// A second Upsert on the same key replaces the intent columns only.
	again, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey:  key,
		Source:           InstallationSourceImage,
		Status:           StatusInstalled,
		InstalledVersion: "0.151.0",
		ManifestDigest:   "sha256:two",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	if again.ID != created.ID {
		t.Fatalf("upsert must keep the row id: %q vs %q", again.ID, created.ID)
	}
	if again.Source != InstallationSourceImage || again.Status != StatusInstalled || again.InstalledVersion != "0.151.0" || again.ManifestDigest != "sha256:two" {
		t.Fatalf("intent columns not replaced: %+v", again)
	}

	// SetStatus writes status and last_error.
	failed, err := store.SetStatus(ctx, key, StatusFailed, "exec exited 1")
	if err != nil {
		t.Fatalf("set status: %v", err)
	}
	if failed.Status != StatusFailed || failed.LastError != "exec exited 1" {
		t.Fatalf("set status = %+v", failed)
	}

	// UpdateObserved: nil fields leave columns untouched, pointers write.
	checked := time.Now().UTC().Truncate(time.Microsecond)
	latest := "0.152.0"
	observed, err := store.UpdateObserved(ctx, key, ObservedUpdate{
		LatestVersion: &latest,
		LastCheckedAt: &checked,
	})
	if err != nil {
		t.Fatalf("update observed: %v", err)
	}
	if observed.LatestVersion != latest || observed.LastCheckedAt == nil || !observed.LastCheckedAt.Equal(checked) {
		t.Fatalf("observed columns not written: %+v", observed)
	}
	if observed.Source != InstallationSourceImage || observed.InstalledVersion != "0.151.0" ||
		observed.LastError != "exec exited 1" || observed.ManifestDigest != "sha256:two" || observed.Status != StatusFailed {
		t.Fatalf("nil fields must leave columns untouched: %+v", observed)
	}
	empty := ""
	cleared, err := store.UpdateObserved(ctx, key, ObservedUpdate{LastError: &empty})
	if err != nil {
		t.Fatalf("clear last_error: %v", err)
	}
	if cleared.LastError != "" || cleared.LatestVersion != latest {
		t.Fatalf("pointer to empty string must clear only last_error: %+v", cleared)
	}

	// Listing by target, bot, and status all see the row.
	second := InstallationKey{BotID: botID, WorkspaceTargetID: "remote-1", DependencyID: "claude-code"}
	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: second, Source: InstallationSourceManaged, Status: StatusMissing}); err != nil {
		t.Fatalf("upsert second: %v", err)
	}
	forTarget, err := store.ListForTarget(ctx, botID, "native")
	if err != nil {
		t.Fatalf("list for target: %v", err)
	}
	if len(forTarget) != 1 || forTarget[0].DependencyID != "codex" {
		t.Fatalf("list for target = %+v", forTarget)
	}
	forBot, err := store.ListForBot(ctx, botID)
	if err != nil {
		t.Fatalf("list for bot: %v", err)
	}
	if len(forBot) != 2 || forBot[0].WorkspaceTargetID != "native" || forBot[1].WorkspaceTargetID != "remote-1" {
		t.Fatalf("list for bot = %+v", forBot)
	}
	missing, err := store.ListByStatus(ctx, StatusMissing)
	if err != nil {
		t.Fatalf("list by status: %v", err)
	}
	if !containsKey(missing, second) {
		t.Fatalf("list by status missing = %+v, want %+v", missing, second)
	}

	// Delete removes the row; a second delete and a Get report not found.
	if err := store.Delete(ctx, key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := store.Delete(ctx, key); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("second delete error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.Get(ctx, key); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("get after delete error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.SetStatus(ctx, key, StatusFailed, ""); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("set status after delete error = %v, want ErrInstallationNotFound", err)
	}
	if _, err := store.UpdateObserved(ctx, key, ObservedUpdate{}); !errors.Is(err, ErrInstallationNotFound) {
		t.Fatalf("update observed after delete error = %v, want ErrInstallationNotFound", err)
	}
}

func TestPostgresStoreStaleOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	botID := createDependencyBot(t, ctx, pool)
	store := newIntegrationStore(pool)

	stuck := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "codex"}
	fresh := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "claude-code"}
	done := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "hermes"}
	for _, in := range []UpsertInstallation{
		{InstallationKey: stuck, Source: InstallationSourceManaged, Status: StatusUpdating},
		{InstallationKey: fresh, Source: InstallationSourceManaged, Status: StatusInstalling},
		{InstallationKey: done, Source: InstallationSourceManaged, Status: StatusInstalled},
	} {
		if _, err := store.Upsert(ctx, in); err != nil {
			t.Fatalf("upsert %s: %v", in.DependencyID, err)
		}
	}
	// Age the stuck record and the finished one; only in-progress rows count.
	if _, err := pool.Exec(ctx, `
		UPDATE bot_dependency_installations
		SET updated_at = now() - interval '1 hour'
		WHERE bot_id = $1 AND dependency_id IN ('codex', 'hermes')`, botID); err != nil {
		t.Fatalf("age records: %v", err)
	}

	stale, err := store.ListStaleOperations(ctx, 30*time.Minute)
	if err != nil {
		t.Fatalf("list stale: %v", err)
	}
	if !containsKey(stale, stuck) {
		t.Fatalf("stale = %+v, want %+v", stale, stuck)
	}
	if containsKey(stale, fresh) || containsKey(stale, done) {
		t.Fatalf("stale must only hold aged in-progress rows: %+v", stale)
	}

	notYet, err := store.ListStaleOperations(ctx, 2*time.Hour)
	if err != nil {
		t.Fatalf("list stale (2h): %v", err)
	}
	if containsKey(notYet, stuck) {
		t.Fatalf("a 1h-old record is not stale at a 2h threshold: %+v", notYet)
	}

	// The reaper's transition: stale -> failed refreshes updated_at.
	reaped, err := store.SetStatus(ctx, stuck, StatusFailed, "operation timed out")
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped.Status != StatusFailed || reaped.Status.InProgress() {
		t.Fatalf("reaped = %+v", reaped)
	}
	after, err := store.ListStaleOperations(ctx, 0)
	if err != nil {
		t.Fatalf("list stale (0): %v", err)
	}
	if containsKey(after, stuck) {
		t.Fatalf("failed rows are never stale operations: %+v", after)
	}
}

func TestPostgresStoreRejectsUnknownStatusAndSource(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDependencyPostgres(t, ctx)
	botID := createDependencyBot(t, ctx, pool)
	store := newIntegrationStore(pool)
	key := InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: "codex"}

	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: InstallationSourceManaged, Status: Status("bogus")}); err == nil {
		t.Fatal("status CHECK must reject unknown values")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{InstallationKey: key, Source: "toolkit", Status: StatusInstalled}); err == nil {
		t.Fatal("source CHECK must reject unknown values")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey: InstallationKey{BotID: botID, WorkspaceTargetID: "native", DependencyID: ""},
		Source:          InstallationSourceManaged,
		Status:          StatusInstalled,
	}); err == nil {
		t.Fatal("dependency_id CHECK must reject the empty string")
	}
	if _, err := store.Upsert(ctx, UpsertInstallation{
		InstallationKey: InstallationKey{BotID: uuid.NewString(), WorkspaceTargetID: "native", DependencyID: "codex"},
		Source:          InstallationSourceManaged,
		Status:          StatusInstalled,
	}); err == nil {
		t.Fatal("bots foreign key must reject an unknown bot")
	}
}

func containsKey(items []Installation, key InstallationKey) bool {
	for _, item := range items {
		if item.BotID == key.BotID && item.WorkspaceTargetID == key.WorkspaceTargetID && item.DependencyID == key.DependencyID {
			return true
		}
	}
	return false
}
