//go:build integration

package settings

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/botagents"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

var (
	defaultAgentMigrationOnce sync.Once
	defaultAgentMigrationErr  error
)

func TestDefaultAgentAssignmentSerializesWithDisableAndDelete(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool := openDefaultAgentPostgres(t, ctx)

	for _, mutation := range []string{"disable", "delete"} {
		mutation := mutation
		t.Run(mutation+" wins the lock", func(t *testing.T) {
			botID, agentID := createDefaultAgentFixture(t, ctx, pool)
			mutationLocked := make(chan struct{})
			releaseMutation := make(chan struct{})
			setterEnteredTx := make(chan struct{})

			mutationStore := newHookedAgentStore(pool, nil, mutationLocked, releaseMutation)
			setterStore := newHookedAgentStore(pool, setterEnteredTx, nil, nil)
			mutationService := botagents.NewService(slog.Default(), mutationStore)
			setterService := newDefaultAgentSettingsService(setterStore)

			mutationResult := make(chan error, 1)
			go func() { mutationResult <- mutateAgent(ctx, mutationService, mutation, botID, agentID) }()
			waitForAgentSignal(t, mutationLocked, "mutation to lock bot")

			setterResult := make(chan error, 1)
			go func() { setterResult <- setDefaultAgent(ctx, setterService, botID, agentID) }()
			waitForAgentSignal(t, setterEnteredTx, "setter to finish its optimistic precheck")
			close(releaseMutation)

			if err := waitForAgentResult(t, mutationResult, "mutation"); err != nil {
				t.Fatalf("%s error = %v", mutation, err)
			}
			if err := waitForAgentResult(t, setterResult, "setter"); !errors.Is(err, botagents.ErrUnavailable) {
				t.Fatalf("setter error = %v, want %v", err, botagents.ErrUnavailable)
			}
			assertDefaultAgentState(t, ctx, pool, botID, agentID, false, false, mutation == "delete")
		})

		t.Run("assignment wins before "+mutation, func(t *testing.T) {
			botID, agentID := createDefaultAgentFixture(t, ctx, pool)
			setterLocked := make(chan struct{})
			releaseSetter := make(chan struct{})
			mutationEnteredTx := make(chan struct{})

			setterStore := newHookedAgentStore(pool, nil, setterLocked, releaseSetter)
			mutationStore := newHookedAgentStore(pool, mutationEnteredTx, nil, nil)
			setterService := newDefaultAgentSettingsService(setterStore)
			mutationService := botagents.NewService(slog.Default(), mutationStore)

			setterResult := make(chan error, 1)
			go func() { setterResult <- setDefaultAgent(ctx, setterService, botID, agentID) }()
			waitForAgentSignal(t, setterLocked, "setter to lock bot")

			mutationResult := make(chan error, 1)
			go func() { mutationResult <- mutateAgent(ctx, mutationService, mutation, botID, agentID) }()
			waitForAgentSignal(t, mutationEnteredTx, "mutation to finish its optimistic read")
			close(releaseSetter)

			if err := waitForAgentResult(t, setterResult, "setter"); err != nil {
				t.Fatalf("setter error = %v", err)
			}
			if err := waitForAgentResult(t, mutationResult, "mutation"); !errors.Is(err, botagents.ErrDefaultInUse) {
				t.Fatalf("%s error = %v, want %v", mutation, err, botagents.ErrDefaultInUse)
			}
			assertDefaultAgentState(t, ctx, pool, botID, agentID, true, true, false)
		})
	}
}

type hookedAgentStore struct {
	dbstore.Queries
	base        *postgresstore.Queries
	enteredTx   chan struct{}
	locked      chan struct{}
	releaseLock <-chan struct{}
	enteredOnce sync.Once
	lockedOnce  sync.Once
}

func newHookedAgentStore(pool *pgxpool.Pool, enteredTx, locked chan struct{}, releaseLock <-chan struct{}) *hookedAgentStore {
	base := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	return &hookedAgentStore{
		Queries:     base,
		base:        base,
		enteredTx:   enteredTx,
		locked:      locked,
		releaseLock: releaseLock,
	}
}

func (*hookedAgentStore) SupportsTransactions() bool { return true }

func (s *hookedAgentStore) InTx(ctx context.Context, fn func(dbstore.Queries) error) error {
	return s.base.InTx(ctx, func(q dbstore.Queries) error {
		if s.enteredTx != nil {
			s.enteredOnce.Do(func() { close(s.enteredTx) })
		}
		return fn(&hookedAgentTxQueries{Queries: q, owner: s})
	})
}

type hookedAgentTxQueries struct {
	dbstore.Queries
	owner *hookedAgentStore
}

func (q *hookedAgentTxQueries) LockBotForAgentMutation(ctx context.Context, botID pgtype.UUID) (pgtype.UUID, error) {
	lockedID, err := q.Queries.LockBotForAgentMutation(ctx, botID)
	if err != nil {
		return pgtype.UUID{}, err
	}
	if q.owner.locked != nil {
		q.owner.lockedOnce.Do(func() { close(q.owner.locked) })
	}
	if q.owner.releaseLock != nil {
		select {
		case <-q.owner.releaseLock:
		case <-ctx.Done():
			return pgtype.UUID{}, ctx.Err()
		}
	}
	return lockedID, nil
}

func newDefaultAgentSettingsService(store dbstore.Queries) *Service {
	agents := botagents.NewService(slog.Default(), store)
	service := NewService(slog.Default(), store, nil, nil)
	service.SetBotAgents(agents)
	return service
}

func setDefaultAgent(ctx context.Context, service *Service, botID, agentID string) error {
	_, err := service.UpsertBot(ctx, botID, UpsertRequest{DefaultBotAgentID: &agentID})
	return err
}

func mutateAgent(ctx context.Context, service *botagents.Service, mutation, botID, agentID string) error {
	if mutation == "delete" {
		return service.Delete(ctx, botID, agentID)
	}
	enabled := false
	_, err := service.Update(ctx, botID, agentID, botagents.UpdateRequest{Enabled: &enabled})
	return err
}

func createDefaultAgentFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (string, string) {
	t.Helper()
	userID := uuid.New()
	botID := uuid.New()
	agentID := uuid.New()
	name := "default-agent-race-" + uuid.NewString()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		), created_member AS (
			INSERT INTO team_members (team_id, user_id, role)
			SELECT $3, id, 'admin' FROM created_user
			RETURNING user_id
		), created_bot AS (
			INSERT INTO bots (id, team_id, owner_user_id, name, status, metadata)
			SELECT $4, $3, user_id, $2, 'ready',
				'{"acp":{"agents":{"codex":{"enabled":true,"setup_mode":"self"}}}}'::jsonb
			FROM created_member
			RETURNING id
		)
		INSERT INTO bot_agents (id, team_id, bot_id, name, runtime, enabled, metadata)
		SELECT $5, $3, id, 'Codex', 'acp', true, '{"provider":"codex"}'::jsonb
		FROM created_bot`,
		userID, name, team.DefaultTeamID, botID, agentID,
	); err != nil {
		t.Fatalf("create default Agent fixture: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), "DELETE FROM bots WHERE id = $1", botID)
		_, _ = pool.Exec(context.Background(), "DELETE FROM users WHERE id = $1", userID)
	})
	return botID.String(), agentID.String()
}

func assertDefaultAgentState(t *testing.T, ctx context.Context, pool *pgxpool.Pool, botID, agentID string, pointsToAgent, enabled, deleted bool) {
	t.Helper()
	var gotPointsToAgent, gotEnabled, gotDeleted bool
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(b.default_bot_agent_id = a.id, false), a.enabled, a.deleted_at IS NOT NULL
		FROM bots b
		JOIN bot_agents a ON a.bot_id = b.id
		WHERE b.id = $1 AND a.id = $2`, botID, agentID,
	).Scan(&gotPointsToAgent, &gotEnabled, &gotDeleted); err != nil {
		t.Fatalf("read default Agent state: %v", err)
	}
	if gotPointsToAgent != pointsToAgent || gotEnabled != enabled || gotDeleted != deleted {
		t.Fatalf("state = (default=%t enabled=%t deleted=%t), want (%t %t %t)",
			gotPointsToAgent, gotEnabled, gotDeleted, pointsToAgent, enabled, deleted)
	}
}

func waitForAgentSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForAgentResult(t *testing.T, result <-chan error, description string) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func openDefaultAgentPostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not set")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		defaultAgentMigrationOnce.Do(func() { defaultAgentMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if defaultAgentMigrationErr != nil {
			t.Fatalf("migrate PostgreSQL test database: %v", defaultAgentMigrationErr)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping PostgreSQL: %v", err)
	}
	return pool
}
