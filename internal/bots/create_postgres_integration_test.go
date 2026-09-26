package bots

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/felinics/memoh/internal/botworkspace"
	dbpkg "github.com/felinics/memoh/internal/db"
	"github.com/felinics/memoh/internal/db/dbtest"
	dbsqlc "github.com/felinics/memoh/internal/db/postgres/sqlc"
	postgresstore "github.com/felinics/memoh/internal/db/postgres/store"
	dbstore "github.com/felinics/memoh/internal/db/store"
	"github.com/felinics/memoh/internal/team"
)

func TestPostgresBotCreate(t *testing.T) {
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
	owner := uuid.New()
	if _, err := pool.Exec(ctx, `WITH u AS (INSERT INTO users (id, username, is_active, metadata) VALUES ($1, $2, true, '{}') RETURNING id)
		INSERT INTO team_members (team_id, user_id, role) SELECT $3, id, 'admin' FROM u`, owner, "bot-create-"+owner.String(), team.DefaultTeamID); err != nil {
		t.Fatalf("create owner: %v", err)
	}
	cleanupCtx := context.WithoutCancel(ctx)
	t.Cleanup(func() {
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM bots WHERE owner_user_id = $1", owner)
		_, _ = pool.Exec(cleanupCtx, "DELETE FROM users WHERE id = $1", owner)
	})
	store := postgresstore.NewQueriesWithPool(pool, dbsqlc.New(pool))
	newService := func(intents postgresIntents) *Service {
		svc := NewService(nil, store)
		svc.SetWorkspaceIntents(intents)
		return svc
	}
	count := func(query string, args ...any) (n int64) {
		if err := pool.QueryRow(ctx, query, args...).Scan(&n); err != nil {
			t.Errorf("%s: %v", query, err)
		}
		return n
	}
	generation := func(botID string) int64 {
		return count("SELECT COALESCE(max(desired_generation), 0) FROM bot_workspaces WHERE bot_id = $1", botID)
	}

	t.Run("concurrent creates with one key make one bot and one intent", func(t *testing.T) {
		svc := newService(postgresIntents{})
		req := CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all", RequestKey: uuid.NewString()}
		ids := make(chan string, 8)
		var wg sync.WaitGroup
		for range 8 {
			wg.Go(func() {
				bot, err := svc.Create(ctx, owner.String(), req)
				if errors.Is(err, ErrBotNameTaken) { // lost the race: answered as the endpoint answers it
					if bot, _, err = svc.FindCreated(ctx, owner.String(), req.RequestKey); err == nil {
						bot, err = svc.AwaitCreated(ctx, bot, req)
					}
				}
				if err != nil {
					t.Error(err)
					return
				}
				if g := generation(bot.ID); g != 1 {
					t.Errorf("answered with bot %s at intent generation %d, want 1", bot.ID, g)
				}
				ids <- bot.ID
			})
		}
		wg.Wait()
		close(ids)
		first := <-ids
		for id := range ids {
			if id != first {
				t.Errorf("answered with bots %s and %s, want one", first, id)
			}
		}
		if n := count("SELECT count(*) FROM bots WHERE create_request_key = $1", req.RequestKey); n != 1 {
			t.Errorf("bots made with the key = %d, want 1", n)
		}
	})

	t.Run("a create that fails part-way leaves nothing", func(t *testing.T) {
		req := CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all", RequestKey: uuid.NewString()}
		if _, err := newService(postgresIntents{fail: true}).Create(ctx, owner.String(), req); err == nil {
			t.Fatal("Create succeeded, want the intent failure")
		}
		if n := count("SELECT count(*) FROM bots WHERE create_request_key = $1", req.RequestKey); n != 0 {
			t.Errorf("bots left behind = %d, want 0", n)
		}
		bot, err := newService(postgresIntents{}).Create(ctx, owner.String(), req)
		if err != nil || generation(bot.ID) != 1 {
			t.Errorf("resend: %v, intent generation %d; want a new bot at generation 1", err, generation(bot.ID))
		}
	})

	t.Run("creates without a key never replay", func(t *testing.T) {
		svc := newService(postgresIntents{})
		before := count("SELECT count(*) FROM bots WHERE owner_user_id = $1 AND create_request_key IS NULL", owner)
		for range 2 {
			if _, err := svc.Create(ctx, owner.String(), CreateBotRequest{DisplayName: "小猫", AclPreset: "allow_all"}); err != nil {
				t.Fatal(err)
			}
		}
		if n := count("SELECT count(*) FROM bots WHERE owner_user_id = $1 AND create_request_key IS NULL", owner) - before; n != 2 {
			t.Errorf("keyless creates made %d bots, want 2", n)
		}
	})
}

// postgresIntents writes intents as the botworkspace adapter in
// cmd/internal/core does; fail fails the create after the write.
type postgresIntents struct {
	WorkspaceIntents // Create uses only RecordPresent and Wake
	fail             bool
}

func (i postgresIntents) RecordPresent(ctx context.Context, q dbstore.Queries, botID, image string) error {
	if _, err := botworkspace.NewRepository(q).Upsert(ctx, botID, botworkspace.DesiredPresent, image, false); err != nil || !i.fail {
		return err
	}
	return errors.New("store went away")
}

func (postgresIntents) Wake(context.Context) {}
