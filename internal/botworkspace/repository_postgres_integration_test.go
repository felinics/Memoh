package botworkspace

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

// TestPostgresClaimByDesiredState runs the claim against a real database. A
// desired state claims only the rows that want it, even when a row of the
// other kind is due first; none claims every due row, as when removals share
// the slots. It all runs in a transaction that is rolled back, on rows due
// long before any real one, so the claims touch nothing else.
func TestPostgresClaimByDesiredState(t *testing.T) {
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		if err := dbtest.MigratePostgresUp(dsn); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	ctx := t.Context()
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })

	owner, removing, creating := uuid.New(), uuid.New(), uuid.New()
	seed := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, username, is_active, metadata) VALUES ($1, $2, true, '{}')`, []any{owner, "claim-pools-" + owner.String()}},
		{`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`, []any{team.DefaultTeamID, owner}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, metadata) VALUES ($1, $2, $3, $4, '{}')`, []any{removing, team.DefaultTeamID, owner, "removing-" + removing.String()[:8]}},
		{`INSERT INTO bots (id, team_id, owner_user_id, name, metadata) VALUES ($1, $2, $3, $4, '{}')`, []any{creating, team.DefaultTeamID, owner, "creating-" + creating.String()[:8]}},
		// A running workspace asked to go away, and a new one to build that
		// is due first.
		{`INSERT INTO bot_workspaces (bot_id, desired_state, desired_generation, observed_state, observed_generation, next_attempt_at)
		  VALUES ($1, 'absent', 2, 'running', 1, '2000-01-02'), ($2, 'present', 1, 'absent', 0, '2000-01-01')`, []any{removing, creating}},
	}
	for _, s := range seed {
		if _, err := tx.Exec(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	repo := NewRepository(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(tx)))
	claim := func(desired string, limit int32) []string {
		t.Helper()
		rows, err := repo.Claim(ctx, "claim-pools", time.Minute, desired, limit)
		if err != nil {
			t.Fatalf("claim %q: %v", desired, err)
		}
		ids := make([]string, 0, len(rows))
		for _, w := range rows {
			ids = append(ids, w.BotID)
			if err := repo.Release(ctx, w.BotID, "claim-pools"); err != nil {
				t.Fatalf("release: %v", err)
			}
		}
		slices.Sort(ids)
		return ids
	}

	if got := claim(DesiredAbsent, 1); !slices.Equal(got, []string{removing.String()}) {
		t.Fatalf("removal claim = %v, want only the removal although the creation is due first", got)
	}
	if _, err := tx.Exec(ctx, `UPDATE bot_workspaces SET next_attempt_at = '1999-12-31' WHERE bot_id = $1`, removing); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if got := claim(DesiredPresent, 1); !slices.Equal(got, []string{creating.String()}) {
		t.Fatalf("provisioning claim = %v, want only the creation although the removal is due first", got)
	}
	both := []string{creating.String(), removing.String()}
	slices.Sort(both)
	if got := claim("", 2); !slices.Equal(got, both) {
		t.Fatalf("shared claim = %v, want both rows", got)
	}
}
