package botworkspace

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	"github.com/felinics/memoh/internal/team"
)

// claimTx opens a transaction on TEST_POSTGRES_DSN that is rolled back when
// the test ends, adds a user who may own bots in the default team, and binds
// the repository to the transaction. Rows the tests add are due long before
// any real one, so their claims touch nothing else.
func claimTx(t *testing.T) (context.Context, pgx.Tx, Repository, uuid.UUID) {
	t.Helper()
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

	owner := uuid.New()
	for _, s := range []struct {
		query string
		args  []any
	}{
		{`INSERT INTO users (id, username, is_active, metadata) VALUES ($1, $2, true, '{}')`, []any{owner, "claim-test-" + owner.String()}},
		{`INSERT INTO team_members (team_id, user_id, role) VALUES ($1, $2, 'admin')`, []any{team.DefaultTeamID, owner}},
	} {
		if _, err := tx.Exec(ctx, s.query, s.args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	return ctx, tx, NewRepository(postgresstore.NewQueriesWithPool(pool, dbsqlc.New(tx))), owner
}

func addBot(ctx context.Context, t *testing.T, tx pgx.Tx, owner, id uuid.UUID, name string) {
	t.Helper()
	if _, err := tx.Exec(ctx, `INSERT INTO bots (id, team_id, owner_user_id, name, metadata) VALUES ($1, $2, $3, $4, '{}')`,
		id, team.DefaultTeamID, owner, name+"-"+id.String()[:8]); err != nil {
		t.Fatalf("seed bot: %v", err)
	}
}

// TestPostgresClaimByDesiredState runs the claim against a real database. A
// desired state claims only the rows that want it, even when a row of the
// other kind is due first; none claims every due row, as when removals share
// the slots.
func TestPostgresClaimByDesiredState(t *testing.T) {
	ctx, tx, repo, owner := claimTx(t)
	removing, creating := uuid.New(), uuid.New()
	addBot(ctx, t, tx, owner, removing, "removing")
	addBot(ctx, t, tx, owner, creating, "creating")
	// A running workspace asked to go away, and a new one to build that is
	// due first.
	if _, err := tx.Exec(ctx, `INSERT INTO bot_workspaces (bot_id, desired_state, desired_generation, observed_state, observed_generation, next_attempt_at)
	  VALUES ($1, 'absent', 2, 'running', 1, '2000-01-02'), ($2, 'present', 1, 'absent', 0, '2000-01-01')`, removing, creating); err != nil {
		t.Fatalf("seed: %v", err)
	}
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

// TestPostgresClaimHonorsTheLimit claims under the plan that made the claim
// overrun its limit: a nested loop semi join with no materialized inner side,
// which runs the subquery again for every row it scans, each run past the rows
// the statement already locked. The claim leases as many rows as it asked for.
func TestPostgresClaimHonorsTheLimit(t *testing.T) {
	ctx, tx, repo, owner := claimTx(t)
	ids := make([]uuid.UUID, 5)
	for i := range ids {
		ids[i] = uuid.New()
		addBot(ctx, t, tx, owner, ids[i], "due")
		if _, err := tx.Exec(ctx, `INSERT INTO bot_workspaces (bot_id, desired_state, desired_generation, observed_state, observed_generation, next_attempt_at)
		  VALUES ($1, 'present', 1, 'absent', 0, $2)`, ids[i], time.Date(2000, 1, 1+i, 0, 0, 0, 0, time.UTC)); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	for _, setting := range []string{"enable_hashjoin", "enable_mergejoin", "enable_material", "enable_hashagg", "enable_sort"} {
		if _, err := tx.Exec(ctx, "SET LOCAL "+setting+" = off"); err != nil {
			t.Fatalf("%s: %v", setting, err)
		}
	}

	rows, err := repo.Claim(ctx, "claim-limit", time.Minute, "", 2)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	leased := 0
	for _, id := range ids {
		w, err := repo.Get(ctx, id.String())
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		if w.LeaseOwner == "claim-limit" {
			leased++
		}
	}
	if len(rows) != 2 || leased != 2 {
		t.Fatalf("claim with limit 2 returned %d rows and leased %d of 5 due rows", len(rows), leased)
	}
}
