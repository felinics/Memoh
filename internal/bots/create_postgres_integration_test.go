package bots

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/felinics/memoh/internal/botworkspace"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

var (
	botCreateMigrationOnce sync.Once
	botCreateMigrationErr  error
)

func TestPostgresBotCreateMakesOneBotAndOneIntentPerKey(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openBotCreatePostgres(t, ctx)
	ownerID := createBotCreateOwner(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	svc := NewService(nil, store)
	svc.SetWorkspaceIntents(postgresIntents{})

	// Identical creates racing with one key, as a client resending in parallel
	// would send them; a loser is answered the way the endpoint answers it.
	key := uuid.NewString()
	const attempts = 8
	start := make(chan struct{})
	type answer struct {
		botID      string
		generation int64
		err        error
	}
	answers := make(chan answer, attempts)
	var wg sync.WaitGroup
	for range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			req := CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all", RequestKey: key}
			bot, err := svc.Create(ctx, ownerID, req)
			if errors.Is(err, ErrBotNameTaken) {
				var found bool
				if bot, found, err = svc.FindCreated(ctx, ownerID, key); err == nil && !found {
					err = errors.New("lost the race but found no winner")
				}
				if err == nil {
					bot, err = svc.AwaitCreated(ctx, bot, req)
				}
			}
			if err != nil {
				answers <- answer{err: err}
				return
			}
			// Whoever answers sees the winner's bot complete, intent included.
			generation, err := intentGeneration(ctx, pool, bot.ID)
			answers <- answer{botID: bot.ID, generation: generation, err: err}
		}()
	}
	close(start)
	wg.Wait()
	close(answers)

	var botID string
	for a := range answers {
		if a.err != nil {
			t.Fatalf("attempt failed: %v", a.err)
		}
		if botID == "" {
			botID = a.botID
		}
		if a.botID != botID || a.generation != 1 {
			t.Fatalf("answered bot %s at generation %d; want every attempt answered with %s at generation 1", a.botID, a.generation, botID)
		}
	}
	if n := countBotsWithKey(t, ctx, pool, key); n != 1 {
		t.Fatalf("bots made with the key = %d, want 1", n)
	}
	if generation, err := intentGeneration(ctx, pool, botID); err != nil || generation != 1 {
		t.Fatalf("workspace intent generation = %d (%v), want 1", generation, err)
	}
}

func TestPostgresBotCreateThatFailsPartWayLeavesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool := openBotCreatePostgres(t, ctx)
	ownerID := createBotCreateOwner(t, ctx, pool)
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	key := uuid.NewString()

	failing := NewService(nil, store)
	failing.SetWorkspaceIntents(postgresIntents{failAfterWrite: true})
	if _, err := failing.Create(ctx, ownerID, CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all", RequestKey: key}); err == nil {
		t.Fatal("Create succeeded; want the intent failure")
	}
	if n := countBotsWithKey(t, ctx, pool, key); n != 0 {
		t.Fatalf("bots left behind by the failed create = %d, want 0", n)
	}

	// Nothing is left half-made, so the resend simply creates the bot.
	svc := NewService(nil, store)
	svc.SetWorkspaceIntents(postgresIntents{})
	bot, err := svc.Create(ctx, ownerID, CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all", RequestKey: key})
	if err != nil {
		t.Fatalf("resend: %v", err)
	}
	if generation, err := intentGeneration(ctx, pool, bot.ID); err != nil || generation != 1 {
		t.Fatalf("resent bot's intent generation = %d (%v), want 1", generation, err)
	}
}

// postgresIntents records intents the way the botworkspace adapter in
// cmd/internal/core does; failAfterWrite fails the create after the write.
type postgresIntents struct{ failAfterWrite bool }

func (i postgresIntents) RecordPresent(ctx context.Context, q dbstore.Queries, botID, image string) error {
	if _, err := botworkspace.NewRepository(q).Upsert(ctx, botID, botworkspace.DesiredPresent, strings.TrimSpace(image), false); err != nil {
		return err
	}
	if i.failAfterWrite {
		return errors.New("store went away")
	}
	return nil
}

func (postgresIntents) Wake(context.Context) {}

func (postgresIntents) RequestAbsent(context.Context, string, bool) (int64, error) { return 0, nil }

func (postgresIntents) AwaitSettled(context.Context, string, int64) (WorkspaceOutcome, error) {
	return WorkspaceOutcome{}, nil
}

func (postgresIntents) Current(context.Context, string) (WorkspaceOutcome, bool, error) {
	return WorkspaceOutcome{}, false, nil
}

func intentGeneration(ctx context.Context, pool *pgxpool.Pool, botID string) (int64, error) {
	var generation int64
	err := pool.QueryRow(ctx, `SELECT desired_generation FROM bot_workspaces WHERE bot_id = $1`, botID).Scan(&generation)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, errors.New("bot has no workspace intent")
	}
	return generation, err
}

func countBotsWithKey(t *testing.T, ctx context.Context, pool *pgxpool.Pool, key string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM bots WHERE create_request_key = $1`, key).Scan(&n); err != nil {
		t.Fatalf("count bots: %v", err)
	}
	return n
}

func createBotCreateOwner(t *testing.T, ctx context.Context, pool *pgxpool.Pool) string {
	t.Helper()
	userID := uuid.New()
	if _, err := pool.Exec(ctx, `
		WITH created_user AS (
			INSERT INTO users (id, username, is_active, metadata)
			VALUES ($1, $2, true, '{}'::jsonb)
			RETURNING id
		)
		INSERT INTO team_members (team_id, user_id, role)
		SELECT $3, id, 'admin' FROM created_user`,
		userID, "bot-create-"+userID.String(), team.DefaultTeamID,
	); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	// The test's own context is canceled by the time cleanup runs.
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE owner_user_id = $1", userID)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", userID)
	})
	return userID.String()
}

func openBotCreatePostgres(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		if os.Getenv("MEMOH_TEST_POSTGRES_REQUIRED") == "1" {
			t.Fatal("bot create PostgreSQL test is required, but TEST_POSTGRES_DSN is not set")
		}
		t.Skip("set TEST_POSTGRES_DSN to run bot create PostgreSQL integration")
	}
	if os.Getenv("TEST_POSTGRES_BOOTSTRAP_SCHEMA") == "1" {
		botCreateMigrationOnce.Do(func() { botCreateMigrationErr = dbtest.MigratePostgresUp(dsn) })
		if botCreateMigrationErr != nil {
			t.Fatalf("migrate bot create PostgreSQL database: %v", botCreateMigrationErr)
		}
	}
	pool, err := dbpkg.OpenPostgresDSN(ctx, dsn)
	if err != nil {
		t.Fatalf("open bot create PostgreSQL pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
